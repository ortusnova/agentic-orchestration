package stack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/smol-builder/layers"
)

const goodHeader = "ARG BASE\nFROM ${BASE}\nRUN true\n"

// writeFiles creates each path (slash-separated, relative to dir) with its
// content.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testLibrary(t *testing.T) *Library {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"base/Dockerfile":   "FROM debian\n",
		"go/Dockerfile":     goodHeader,
		"node/Dockerfile":   goodHeader,
		"python/Dockerfile": goodHeader,
		"notalayer/README":  "no Dockerfile here",
	})
	lib, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func TestOpenLibrary(t *testing.T) {
	lib := testLibrary(t)
	if got, want := lib.Names(), []string{"go", "node", "python"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if lib.Has("base") || lib.Has("notalayer") {
		t.Fatal("base and directories without a Dockerfile must not be selectable")
	}
}

func TestOpenLibraryWithoutBase(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go/Dockerfile": goodHeader})
	_, err := OpenLibrary(dir)
	if err == nil || !strings.Contains(err.Error(), `"base"`) {
		t.Fatalf("err = %v, want one naming the missing base layer", err)
	}
}

func TestShippedLibrary(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractLibrary(layers.FS, dir); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := lib.Names(), []string{"go", "node", "python", "rust"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "base", "opencode.json")); err != nil {
		t.Fatalf("base layer file not extracted: %v", err)
	}
	// Every library layer except base must itself be a valid chain step.
	for _, name := range lib.Names() {
		if err := checkProjectHeader(filepath.Join(lib.LayerDir(name), "Dockerfile")); err != nil {
			t.Errorf("layer %s: %v", name, err)
		}
	}
}

func TestParseValid(t *testing.T) {
	lib := testLibrary(t)
	tests := []struct {
		name    string
		toml    string
		files   map[string]string
		layers  []string
		project string
	}{
		{name: "empty", toml: "", layers: nil},
		{name: "empty list", toml: "layers = []", layers: nil},
		{name: "sorted", toml: `layers = ["node", "go"]`, layers: []string{"go", "node"}},
		{
			name:    "project layer",
			toml:    "layers = [\"go\"]\nproject_layer = \"ci/agent.Dockerfile\"",
			files:   map[string]string{"ci/agent.Dockerfile": goodHeader},
			layers:  []string{"go"},
			project: "ci/agent.Dockerfile",
		},
		{
			name: "header with directives comments continuation and stage name",
			toml: `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "# syntax=docker/dockerfile:1\n\n# a comment\n" +
				"arg BASE\n  # indented comment\nFROM \\\n  ${BASE} as dev\nRUN true\n"},
			project: "Dockerfile",
		},
		{
			name:    "symlink inside the repository",
			toml:    `project_layer = "link/Dockerfile"`,
			files:   map[string]string{"real/Dockerfile": goodHeader},
			project: "link/Dockerfile",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, tt.files)
			if tt.name == "symlink inside the repository" {
				if err := os.Symlink("real", filepath.Join(repo, "link")); err != nil {
					t.Fatal(err)
				}
			}
			def, err := Parse([]byte(tt.toml), repo, lib)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(def.Layers, tt.layers) {
				t.Errorf("Layers = %v, want %v", def.Layers, tt.layers)
			}
			if def.ProjectLayer != tt.project {
				t.Errorf("ProjectLayer = %q, want %q", def.ProjectLayer, tt.project)
			}
			if tt.project != "" && !filepath.IsAbs(def.ProjectDockerfile) {
				t.Errorf("ProjectDockerfile = %q, want an absolute path", def.ProjectDockerfile)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	lib := testLibrary(t)
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{"Dockerfile": goodHeader})

	tests := []struct {
		name    string
		toml    string
		files   map[string]string
		symlink [2]string // link name in the repo, target
		names   []string  // substrings the error must contain
	}{
		{name: "unknown key", toml: "name = \"x\"\nlayers = []", names: []string{`"name"`}},
		{name: "unknown table", toml: "[extra]\nk = 1", names: []string{`"extra`}},
		{name: "unknown layer", toml: `layers = ["go", "haskell"]`, names: []string{`"haskell"`}},
		{name: "base listed", toml: `layers = ["base"]`, names: []string{`"base"`}},
		{name: "directory without Dockerfile", toml: `layers = ["notalayer"]`, names: []string{`"notalayer"`}},
		{name: "path-like layer", toml: `layers = ["../go"]`, names: []string{`"../go"`}},
		{name: "duplicate layer", toml: `layers = ["go", "node", "go"]`, names: []string{`duplicate layer "go"`}},
		{name: "layers wrong type", toml: `layers = "go"`, names: []string{"layers"}},
		{name: "project layer parent", toml: `project_layer = "../other/Dockerfile"`, names: []string{`"../other/Dockerfile"`, "leaves the repository"}},
		{name: "project layer nested parent", toml: `project_layer = "ci/../../Dockerfile"`, names: []string{`"ci/../../Dockerfile"`, "leaves the repository"}},
		{name: "project layer absolute", toml: `project_layer = "` + filepath.Join(outside, "Dockerfile") + `"`, names: []string{filepath.Join(outside, "Dockerfile"), "leaves the repository"}},
		{
			name:    "project layer symlink escapes",
			toml:    `project_layer = "escape/Dockerfile"`,
			symlink: [2]string{"escape", outside},
			names:   []string{`"escape/Dockerfile"`, "leaves the repository"},
		},
		{
			name:    "project layer file symlink escapes",
			toml:    `project_layer = "Dockerfile"`,
			symlink: [2]string{"Dockerfile", filepath.Join(outside, "Dockerfile")},
			names:   []string{`"Dockerfile"`, "leaves the repository"},
		},
		{name: "project layer missing", toml: `project_layer = "ci/agent.Dockerfile"`, names: []string{`"ci/agent.Dockerfile"`, "does not exist"}},
		{name: "project layer empty", toml: `project_layer = ""`, names: []string{`project_layer ""`}},
		{
			name:  "project layer is a directory",
			toml:  `project_layer = "ci"`,
			files: map[string]string{"ci/Dockerfile": goodHeader},
			names: []string{`"ci"`, "not a regular file"},
		},
		{
			name:  "project layer without ARG BASE",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "FROM debian\nRUN true\n"},
			names: []string{`project_layer "Dockerfile"`, `found "FROM debian"`},
		},
		{
			name:  "project layer ARG BASE with default",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE=debian\nFROM ${BASE}\n"},
			names: []string{`project_layer "Dockerfile"`, `found "ARG BASE=debian"`},
		},
		{
			name:  "project layer FROM another image",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE\nFROM debian\n"},
			names: []string{`project_layer "Dockerfile"`, `found "FROM debian"`},
		},
		{
			name:  "project layer FROM unbraced",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE\nFROM $BASE\n"},
			names: []string{`project_layer "Dockerfile"`, `found "FROM $BASE"`},
		},
		{
			name:  "project layer instruction between",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE\nARG OTHER\nFROM ${BASE}\n"},
			names: []string{`project_layer "Dockerfile"`, `found "ARG OTHER"`},
		},
		{
			name:  "project layer only ARG",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE\n"},
			names: []string{`project_layer "Dockerfile"`, "end of file"},
		},
		{
			name:  "project layer with a platform flag",
			toml:  `project_layer = "Dockerfile"`,
			files: map[string]string{"Dockerfile": "ARG BASE\nFROM --platform=linux/amd64 ${BASE}\n"},
			names: []string{`project_layer "Dockerfile"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFiles(t, repo, tt.files)
			if tt.symlink[0] != "" {
				if err := os.Symlink(tt.symlink[1], filepath.Join(repo, tt.symlink[0])); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Parse([]byte(tt.toml), repo, lib)
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			for _, n := range tt.names {
				if !strings.Contains(err.Error(), n) {
					t.Errorf("error %q does not name %q", err, n)
				}
			}
		})
	}
}

func TestLoad(t *testing.T) {
	lib := testLibrary(t)
	repo := t.TempDir()
	if _, err := Load(repo, lib); err == nil || !strings.Contains(err.Error(), FileName) {
		t.Fatalf("missing file: err = %v, want one naming %s", err, FileName)
	}
	writeFiles(t, repo, map[string]string{FileName: `layers = ["haskell"]`})
	_, err := Load(repo, lib)
	if err == nil || !strings.Contains(err.Error(), FileName) || !strings.Contains(err.Error(), `"haskell"`) {
		t.Fatalf("err = %v, want one naming the file and the layer", err)
	}
}

func TestExampleRepository(t *testing.T) {
	dir := t.TempDir()
	if err := ExtractLibrary(layers.FS, dir); err != nil {
		t.Fatal(err)
	}
	lib, err := OpenLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	def, err := Load(filepath.Join("..", "..", "examples", "go-libpq"), lib)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(def.Layers, []string{"go"}) || def.ProjectLayer != "ci/agent.Dockerfile" {
		t.Fatalf("definition %+v", def)
	}
}
