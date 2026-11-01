package layers_test

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/latest"
	"github.com/ortusnova/agentic-orchestration/smol-builder/layers"
)

// dockerfile is the pins one layer's Dockerfile declares.
type dockerfile struct {
	layer string
	args  map[string]string
	from  []string
	text  string
}

var (
	argLine  = regexp.MustCompile(`^ARG\s+([A-Z0-9_]+)=(\S+)$`)
	fromLine = regexp.MustCompile(`^FROM\s+(\S+)`)
)

func readDockerfiles(t *testing.T) []dockerfile {
	t.Helper()
	dirs, err := fs.ReadDir(layers.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	var out []dockerfile
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, err := fs.ReadFile(layers.FS, path.Join(d.Name(), "Dockerfile"))
		if err != nil {
			t.Fatal(err)
		}
		df := dockerfile{layer: d.Name(), args: map[string]string{}, text: string(data)}
		sc := bufio.NewScanner(strings.NewReader(df.text))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if m := argLine.FindStringSubmatch(line); m != nil {
				df.args[m[1]] = m[2]
			}
			if m := fromLine.FindStringSubmatch(line); m != nil {
				df.from = append(df.from, m[1])
			}
		}
		out = append(out, df)
	}
	return out
}

// githubRepo returns the repository a Dockerfile downloads ${arg} from, out
// of its GitHub release download URL.
func githubRepo(df dockerfile, arg string) string {
	re := regexp.MustCompile(`https://github\.com/([^/\s"]+/[^/\s"]+)/releases/download/[^\s"]*\$\{` + arg + `\}`)
	if m := re.FindStringSubmatch(df.text); m != nil {
		return m[1]
	}
	return ""
}

// The layers download linux/amd64 artifacts, and the base image is pinned to
// that platform's manifest.
var imagePlatform = latest.Platform{OS: "linux", Architecture: "amd64"}

// TestLatestLayerPins fails for every layer pin that is behind its latest
// stable release, and for a pin it does not know how to check.
func TestLatestLayerPins(t *testing.T) {
	if os.Getenv("SMOL_BUILDER_LATEST_TEST") != "1" {
		t.Skip("set SMOL_BUILDER_LATEST_TEST=1 to compare the layer pins with their latest releases")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := latest.New()

	// Each version pin, by its ARG, with its dependency name and lookup.
	type check struct {
		name   string
		lookup func(df dockerfile) (latest.Version, error)
	}
	checks := map[string]check{
		"OPENCODE_VERSION": {"opencode", nil},
		"UV_VERSION":       {"uv", nil},
		"GO_VERSION":       {"go", func(dockerfile) (latest.Version, error) { return c.GoStable(ctx) }},
		"NODE_VERSION":     {"node", func(dockerfile) (latest.Version, error) { return c.NodeLTS(ctx) }},
		"PYTHON_VERSION":   {"cpython", func(dockerfile) (latest.Version, error) { return c.CPython(ctx) }},
		"RUSTUP_VERSION":   {"rustup", func(dockerfile) (latest.Version, error) { return c.RustupStable(ctx) }},
		"RUST_TOOLCHAIN":   {"rust", func(dockerfile) (latest.Version, error) { return c.RustStable(ctx) }},
	}
	for arg, ck := range checks {
		if ck.lookup == nil {
			ck.lookup = func(df dockerfile) (latest.Version, error) {
				repo := githubRepo(df, arg)
				if repo == "" {
					return latest.Version{}, fmt.Errorf("layer %s: no GitHub release download URL uses ${%s}", df.layer, arg)
				}
				return c.GitHubRelease(ctx, repo)
			}
			checks[arg] = ck
		}
	}

	seen := map[string]bool{}
	for _, df := range readDockerfiles(t) {
		for arg, pinned := range df.args {
			if strings.HasSuffix(arg, "_SHA256") {
				continue
			}
			ck, ok := checks[arg]
			if !ok {
				t.Errorf("layer %s: ARG %s=%s has no version check", df.layer, arg, pinned)
				continue
			}
			seen[arg] = true
			t.Run(ck.name, func(t *testing.T) {
				ours, ok := latest.ParseVersion(pinned)
				if !ok {
					t.Fatalf("%s: pinned version %q (ARG %s in layer %s) is not MAJOR.MINOR.PATCH", ck.name, pinned, arg, df.layer)
				}
				newest, err := ck.lookup(df)
				if err != nil {
					t.Fatalf("%s: %v", ck.name, err)
				}
				if ours.Less(newest) {
					t.Errorf("%s: using %s, latest stable is %s", ck.name, ours, newest)
				} else {
					t.Logf("%s: using %s, the latest stable", ck.name, ours)
				}
			})
		}
		for _, ref := range df.from {
			if strings.HasPrefix(ref, "$") {
				continue
			}
			image, _, _ := strings.Cut(path.Base(ref), "@")
			t.Run(image, func(t *testing.T) { checkImage(ctx, t, c, df.layer, ref) })
		}
	}
	for arg, ck := range checks {
		if !seen[arg] {
			t.Errorf("%s: no layer declares ARG %s", ck.name, arg)
		}
	}
}

// checkImage compares a FROM reference pinned by digest with the current
// digest of its tag.
func checkImage(ctx context.Context, t *testing.T, c *latest.Client, layer, ref string) {
	name, digest, ok := strings.Cut(ref, "@")
	if !ok {
		t.Fatalf("layer %s: FROM %s is not pinned by digest", layer, ref)
	}
	repo, tag, ok := strings.Cut(name, ":")
	if !ok {
		t.Fatalf("layer %s: FROM %s has no tag", layer, ref)
	}
	repo, ok = strings.CutPrefix(repo, "docker.io/")
	if !ok {
		t.Fatalf("layer %s: FROM %s is not on Docker Hub, the only registry this test reads", layer, ref)
	}
	current, err := c.ImageDigest(ctx, repo, tag, imagePlatform)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if digest != current {
		t.Errorf("%s: using %s, current %s/%s digest of the tag is %s", name, digest, imagePlatform.OS, imagePlatform.Architecture, current)
	} else {
		t.Logf("%s: using %s, the current digest", name, digest)
	}
}
