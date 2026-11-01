// Package build turns a repository's stack definition into a smolvm pack.
package build

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/runner"
	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/stack"
	"github.com/ortusnova/agentic-orchestration/smol-builder/layers"
)

// Builder machines are named with BuilderPrefix and carry BuilderLabel, so a
// leftover one can be told apart from every other machine.
const (
	BuilderPrefix = "smol-builder-"
	BuilderLabel  = "smol-builder=1"
)

// packSuffix is what smolvm pack create appends to its output stem for the
// pack itself. The stem path receives a launcher executable.
const packSuffix = ".smolmachine"

// cleanupTimeout bounds deleting the builder machine once the build's
// context is done.
const cleanupTimeout = 2 * time.Minute

// Options selects what to build and where to write it.
type Options struct {
	// Repo is the repository root holding smol-builder.toml.
	Repo string
	// Output is the pack's path.
	Output string
	// LayersDir is the layer library. Empty means the shipped library.
	LayersDir string
}

// Report describes a finished build.
type Report struct {
	// Layers is the applied layer list, in order: base, library layers,
	// then "project" when there is a project layer.
	Layers []string `json:"layers"`
	// Image is the last chain step's tag.
	Image string `json:"image"`
	// Reused lists the steps that already existed in the engine.
	Reused     []string `json:"reused"`
	Pack       string   `json:"pack"`
	PackSHA256 string   `json:"pack_sha256"`
	PackBytes  int64    `json:"pack_bytes"`
	Timings    Timings  `json:"timings"`
}

// Timings are wall-clock seconds per build phase.
type Timings struct {
	// Layers holds each chain step's build time; a reused step takes 0.
	Layers map[string]float64 `json:"layers"`
	// Save is exporting the image to an archive.
	Save float64 `json:"save"`
	// Builder is creating, starting, and stopping the builder machine.
	Builder float64 `json:"builder"`
	// Pack is smolvm pack create.
	Pack  float64 `json:"pack"`
	Total float64 `json:"total"`
}

// Builder builds packs. The zero value is not usable; see New.
type Builder struct {
	Run      runner.Runner
	LookPath func(string) (string, error)
	// Log receives diagnostics.
	Log io.Writer
	// TempDir holds the extracted library and the image archive. Empty
	// means the system default.
	TempDir string
	// Library is the shipped layer library, used when Options.LayersDir is
	// empty.
	Library fs.FS
	// Host tells which builds still run, to find the builder machines and
	// temporary directories that interrupted builds left behind.
	Host Host
}

// New returns a Builder that runs host programs and logs to log.
func New(log io.Writer) *Builder {
	return &Builder{
		Run:      runner.Exec{Log: log},
		LookPath: exec.LookPath,
		Log:      log,
		Library:  layers.FS,
		Host:     systemHost{},
	}
}

func (b *Builder) logf(format string, args ...any) {
	fmt.Fprintf(b.Log, "smol-builder: "+format+"\n", args...)
}

// Build builds the pack described by opts.Repo into opts.Output. On failure
// the file at opts.Output is left as it was, and no machine it created
// remains.
func (b *Builder) Build(ctx context.Context, opts Options) (*Report, error) {
	start := time.Now()

	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return nil, err
	}
	outDir := filepath.Dir(output)
	if info, err := os.Stat(outDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("output directory %s does not exist", outDir)
	}

	// Directories an interrupted build left: its work directory, and its
	// pack directory beside this output.
	packPattern := "." + filepath.Base(output) + ".*.tmp"
	b.sweepDirs(b.TempDir, "smol-builder-*")
	b.sweepDirs(outDir, packPattern)

	work, err := b.ownedTempDir(b.TempDir, "smol-builder-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(work); err != nil {
			b.logf("removing %s: %v", work, err)
		}
	}()

	steps, err := b.resolve(work, opts)
	if err != nil {
		return nil, err
	}
	engineName, err := detectEngine(b.LookPath)
	if err != nil {
		return nil, err
	}
	eng := engine{name: engineName, run: b.Run}

	report := &Report{Reused: []string{}, Timings: Timings{Layers: map[string]float64{}}}
	prev := ""
	for _, step := range steps {
		report.Layers = append(report.Layers, step.Name)
		secs, reused, err := b.buildStep(ctx, eng, step, prev)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", step.Name, err)
		}
		if reused {
			report.Reused = append(report.Reused, step.Name)
		}
		report.Timings.Layers[step.Name] = secs
		prev = step.Tag
	}
	last := steps[len(steps)-1]
	report.Image = last.Tag

	t := time.Now()
	archive := filepath.Join(work, "image.tar")
	b.logf("saving %s", last.Tag)
	if err := eng.save(ctx, last.Tag, archive); err != nil {
		return nil, fmt.Errorf("saving %s: %w", last.Tag, err)
	}
	cfg, err := eng.config(ctx, last.Tag)
	if err != nil {
		return nil, err
	}
	report.Timings.Save = seconds(t)

	suffix, err := randomHex(4)
	if err != nil {
		return nil, err
	}
	// smolvm writes the pack and a launcher; both go in a directory beside
	// the output, so the pack can be renamed onto it.
	packDir, err := b.ownedTempDir(outDir, packPattern)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(packDir); err != nil {
			b.logf("removing %s: %v", packDir, err)
		}
	}()
	stem := filepath.Join(packDir, "pack")

	machine := BuilderPrefix + last.Hash[:12] + "-" + suffix
	if err := b.pack(ctx, machine, archive, cfg, stem, &report.Timings); err != nil {
		return nil, err
	}

	sum, size, err := hashFile(stem + packSuffix)
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	if err := os.Rename(stem+packSuffix, output); err != nil {
		return nil, err
	}
	report.Pack = output
	report.PackSHA256 = sum
	report.PackBytes = size
	report.Timings.Total = seconds(start)
	return report, nil
}

// resolve opens the layer library, extracting the shipped one into work when
// opts names none, and resolves the repository's stack into a chain.
func (b *Builder) resolve(work string, opts Options) ([]stack.Step, error) {
	dir := opts.LayersDir
	if dir == "" {
		dir = filepath.Join(work, "layers")
		if err := os.Mkdir(dir, 0o750); err != nil {
			return nil, err
		}
		if err := stack.ExtractLibrary(b.Library, dir); err != nil {
			return nil, err
		}
	}
	lib, err := stack.OpenLibrary(dir)
	if err != nil {
		return nil, err
	}
	def, err := stack.Load(opts.Repo, lib)
	if err != nil {
		return nil, err
	}
	return stack.Resolve(lib, def)
}

func (b *Builder) buildStep(ctx context.Context, eng engine, step stack.Step, base string) (float64, bool, error) {
	ok, err := eng.exists(ctx, step.Tag)
	if err != nil {
		return 0, false, err
	}
	if ok {
		b.logf("%s: reusing %s", step.Name, step.Tag)
		return 0, true, nil
	}
	b.logf("%s: building %s", step.Name, step.Tag)
	t := time.Now()
	if err := eng.build(ctx, step.Tag, base, step.Dockerfile, step.Context); err != nil {
		return 0, false, err
	}
	return seconds(t), false, nil
}

// pack boots a builder machine from the archive and packs it into stem.
// A pack made with --from-vm drops the image's ENV and WORKDIR but keeps
// the machine's own -e and -w, so the image config is passed as flags.
// The machine is deleted on every path. Before creating it, pack deletes the
// builder machines earlier runs left behind.
func (b *Builder) pack(ctx context.Context, machine, archive string, cfg imageConfig, stem string, timings *Timings) (err error) {
	created := false
	defer func() {
		if !created {
			return
		}
		if derr := b.deleteMachine(ctx, machine); derr != nil {
			err = errors.Join(err, derr)
		}
	}()

	if err := b.sweep(ctx); err != nil {
		return err
	}

	t := time.Now()
	args := append([]string{"machine", "create", "--name", machine, "--image", archive}, b.builderLabels()...)
	for _, kv := range cfg.Env {
		args = append(args, "-e", kv)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "-w", cfg.WorkingDir)
	}
	b.logf("creating builder machine %s", machine)
	// A create that fails part way may still leave the machine behind.
	created = true
	if err := b.Run.Run(ctx, "smolvm", args...); err != nil {
		return fmt.Errorf("builder machine: %w", err)
	}
	if err := b.Run.Run(ctx, "smolvm", "machine", "start", "--name", machine); err != nil {
		return fmt.Errorf("builder machine: %w", err)
	}
	if err := b.Run.Run(ctx, "smolvm", "machine", "stop", "--name", machine); err != nil {
		return fmt.Errorf("builder machine: %w", err)
	}
	timings.Builder = seconds(t)

	t = time.Now()
	b.logf("packing %s", machine)
	if err := b.Run.Run(ctx, "smolvm", "pack", "create", "--from-vm", machine, "-o", stem); err != nil {
		return fmt.Errorf("pack: %w", err)
	}
	timings.Pack = seconds(t)
	return nil
}

// deleteMachine deletes machine even when ctx is already done, so an
// interrupted build still removes it.
func (b *Builder) deleteMachine(ctx context.Context, machine string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	b.logf("deleting builder machine %s", machine)
	_, err := b.Run.Output(ctx, "smolvm", "machine", "delete", "--name", machine, "-f")
	if err == nil {
		return nil
	}
	// A create that failed before registering the machine leaves nothing.
	if exists, lerr := b.machineExists(ctx, machine); lerr == nil && !exists {
		return nil
	}
	return fmt.Errorf("deleting builder machine %s (label %s): %w", machine, BuilderLabel, err)
}

func (b *Builder) machineExists(ctx context.Context, machine string) (bool, error) {
	out, err := b.Run.Output(ctx, "smolvm", "machine", "ls", "-q")
	if err != nil {
		return false, err
	}
	for name := range strings.FieldsSeq(string(out)) {
		if name == machine {
			return true, nil
		}
	}
	return false, nil
}

// hashFile returns the file's SHA-256 and size after flushing it to disk.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path) //nolint:gosec // the pack this build just wrote
	if err != nil {
		return "", 0, err
	}
	defer f.Close() //nolint:errcheck // read-only
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	if err := f.Sync(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func seconds(since time.Time) float64 {
	return math.Round(time.Since(since).Seconds()*10) / 10
}
