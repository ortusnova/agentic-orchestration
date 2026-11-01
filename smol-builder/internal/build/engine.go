package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/runner"
)

// engine drives a container engine CLI. podman and docker differ only in
// how they test for an image and in their save flags.
type engine struct {
	name string
	run  runner.Runner
}

// detectEngine picks podman, else docker, and requires smolvm.
func detectEngine(lookPath func(string) (string, error)) (string, error) {
	name := ""
	for _, candidate := range []string{"podman", "docker"} {
		if _, err := lookPath(candidate); err == nil {
			name = candidate
			break
		}
	}
	if name == "" {
		return "", errors.New("no container engine: neither podman nor docker is on PATH")
	}
	if _, err := lookPath("smolvm"); err != nil {
		return "", errors.New("smolvm is not on PATH")
	}
	return name, nil
}

// exists reports whether tag is an image the engine already holds.
func (e engine) exists(ctx context.Context, tag string) (bool, error) {
	var err error
	if e.name == "podman" {
		_, err = e.run.Output(ctx, e.name, "image", "exists", tag)
	} else {
		_, err = e.run.Output(ctx, e.name, "image", "inspect", tag)
	}
	switch {
	case err == nil:
		return true, nil
	case runner.ExitCode(err) == 1:
		return false, nil
	default:
		return false, err
	}
}

func (e engine) build(ctx context.Context, tag, base, dockerfile, context string) error {
	args := []string{"build", "-t", tag}
	if base != "" {
		args = append(args, "--build-arg", "BASE="+base)
	}
	args = append(args, "-f", dockerfile, context)
	return e.run.Run(ctx, e.name, args...)
}

func (e engine) save(ctx context.Context, tag, archive string) error {
	if e.name == "podman" {
		return e.run.Run(ctx, e.name, "save", "--format", "docker-archive", "-o", archive, tag)
	}
	return e.run.Run(ctx, e.name, "save", "-o", archive, tag)
}

// imageConfig is the part of an image's configuration a pack must keep.
type imageConfig struct {
	Env        []string
	WorkingDir string
}

func (e engine) config(ctx context.Context, tag string) (imageConfig, error) {
	out, err := e.run.Output(ctx, e.name, "image", "inspect", tag)
	if err != nil {
		return imageConfig{}, err
	}
	var images []struct {
		Config imageConfig
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return imageConfig{}, fmt.Errorf("%s image inspect %s: %w", e.name, tag, err)
	}
	if len(images) != 1 {
		return imageConfig{}, fmt.Errorf("%s image inspect %s: got %d images, want 1", e.name, tag, len(images))
	}
	return images[0].Config, nil
}
