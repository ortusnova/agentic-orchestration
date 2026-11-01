package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/runner"
	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/stack"
	"github.com/ortusnova/agentic-orchestration/smol-builder/layers"
)

const header = "ARG BASE\nFROM ${BASE}\n"

type call struct {
	argv   []string
	ctxErr error
}

func (c call) String() string { return strings.Join(c.argv, " ") }

// fakeTools stands in for the container engine and smolvm. It keeps the
// images and machines that exist, writes the files the real programs would,
// and fails any call that fail picks.
type fakeTools struct {
	t      *testing.T
	engine string
	mu     sync.Mutex
	calls  []call
	images map[string]bool
	// machines maps each machine that exists to its labels.
	machines map[string]map[string]string
	packs    int
	// fail returns the error for a call, or nil to let it succeed.
	fail func(ctx context.Context, argv []string) error
	// noPackFile makes pack create succeed without writing the pack.
	noPackFile bool
}

func newFakeTools(t *testing.T, engine string) *fakeTools {
	return &fakeTools{t: t, engine: engine, images: map[string]bool{}, machines: map[string]map[string]string{}}
}

func exitErr(argv []string, code int) error {
	return &runner.Error{Command: strings.Join(argv, " "), ExitCode: code, Err: fmt.Errorf("exit status %d", code)}
}

func (f *fakeTools) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.do(ctx, append([]string{name}, args...))
}

func (f *fakeTools) Run(ctx context.Context, name string, args ...string) error {
	_, err := f.do(ctx, append([]string{name}, args...))
	return err
}

func flagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func (f *fakeTools) do(ctx context.Context, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{argv: argv, ctxErr: ctx.Err()})
	if f.fail != nil {
		if err := f.fail(ctx, argv); err != nil {
			return nil, err
		}
	}
	verb := strings.Join(argv[:min(3, len(argv))], " ")
	last := argv[len(argv)-1]
	switch {
	case argv[0] == f.engine && verb == f.engine+" image exists":
		if !f.images[last] {
			return nil, exitErr(argv, 1)
		}
	case argv[0] == f.engine && verb == f.engine+" image inspect":
		if !f.images[last] {
			return nil, exitErr(argv, 1)
		}
		return []byte(`[{"Config":{"Env":["PATH=/usr/local/go/bin:/usr/bin","SORTIE_STACK_LAYERS=base+go"],"WorkingDir":"/workspace"}}]`), nil
	case argv[0] == f.engine && argv[1] == "build":
		if _, err := os.Stat(last); err != nil {
			f.t.Errorf("build context %s: %v", last, err)
		}
		if _, err := os.Stat(flagValue(argv, "-f")); err != nil {
			f.t.Errorf("build Dockerfile: %v", err)
		}
		f.images[flagValue(argv, "-t")] = true
	case argv[0] == f.engine && argv[1] == "save":
		if err := os.WriteFile(flagValue(argv, "-o"), []byte("archive"), 0o600); err != nil {
			return nil, err
		}
	case verb == "smolvm machine create":
		labels := map[string]string{}
		for i, a := range argv {
			if a == "--label" && i+1 < len(argv) {
				k, v, _ := strings.Cut(argv[i+1], "=")
				labels[k] = v
			}
		}
		f.machines[flagValue(argv, "--name")] = labels
	case verb == "smolvm machine delete":
		delete(f.machines, flagValue(argv, "--name"))
	case verb == "smolvm machine ls" && last == "--json":
		ms := []listedMachine{}
		for name, labels := range f.machines {
			ms = append(ms, listedMachine{Name: name, Labels: labels})
		}
		return json.Marshal(ms)
	case verb == "smolvm machine ls":
		var names []string
		for m := range f.machines {
			names = append(names, m)
		}
		return []byte(strings.Join(names, "\n")), nil
	case verb == "smolvm pack create":
		if f.noPackFile {
			return nil, nil
		}
		f.packs++
		stem := flagValue(argv, "-o")
		if err := os.WriteFile(stem, []byte("launcher"), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(stem+".smolmachine", fmt.Appendf(nil, "pack %d", f.packs), 0o600); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (f *fakeTools) argvs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.String()
	}
	return out
}

func lookPath(present ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(present, name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

type fixture struct {
	repo, libDir, outDir, output string
	tools                        *fakeTools
	builder                      *Builder
	log                          *bytes.Buffer
}

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

func newFixture(t *testing.T, engine, toml string, repoFiles map[string]string) *fixture {
	t.Helper()
	f := &fixture{repo: t.TempDir(), libDir: t.TempDir(), outDir: t.TempDir(), log: &bytes.Buffer{}}
	f.output = filepath.Join(f.outDir, "app.smolmachine")
	writeFiles(t, f.libDir, map[string]string{
		"base/Dockerfile": "FROM debian\n",
		"go/Dockerfile":   header,
		"node/Dockerfile": header,
	})
	writeFiles(t, f.repo, repoFiles)
	writeFiles(t, f.repo, map[string]string{stack.FileName: toml})
	f.tools = newFakeTools(t, engine)
	f.builder = &Builder{
		Run:      f.tools,
		LookPath: lookPath(engine, "smolvm"),
		Log:      f.log,
		TempDir:  t.TempDir(),
		Library:  layers.FS,
		Host:     testHost(),
	}
	return f
}

// fakeHost is a host where the processes in running run, each with its
// start time.
type fakeHost struct {
	boot    string
	self    Process
	running map[int]string
}

func testHost() *fakeHost {
	self := Process{PID: 4242, Start: "777"}
	return &fakeHost{boot: "boot-now", self: self, running: map[int]string{self.PID: self.Start}}
}

func (h *fakeHost) BootID() string { return h.boot }
func (h *fakeHost) Self() Process  { return h.self }
func (h *fakeHost) Running(p Process) bool {
	start, ok := h.running[p.PID]
	return ok && (p.Start == "" || p.Start == start)
}

func (f *fixture) build(ctx context.Context) (*Report, error) {
	return f.builder.Build(ctx, Options{Repo: f.repo, Output: f.output, LayersDir: f.libDir})
}

func (f *fixture) chain(t *testing.T) []stack.Step {
	t.Helper()
	lib, err := stack.OpenLibrary(f.libDir)
	if err != nil {
		t.Fatal(err)
	}
	def, err := stack.Load(f.repo, lib)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := stack.Resolve(lib, def)
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

// findCall returns the first call whose command line starts with prefix.
func (f *fixture) findCall(t *testing.T, prefix string) call {
	t.Helper()
	for _, c := range f.tools.calls {
		if strings.HasPrefix(c.String(), prefix) {
			return c
		}
	}
	t.Fatalf("no call starts with %q", prefix)
	return call{}
}

// createdMachine is the builder machine name the build chose.
func (f *fixture) createdMachine(t *testing.T) string {
	t.Helper()
	return flagValue(f.findCall(t, "smolvm machine create").argv, "--name")
}

// packStem is the temporary output stem the build chose.
func (f *fixture) packStem(t *testing.T) string {
	t.Helper()
	return flagValue(f.findCall(t, "smolvm pack create").argv, "-o")
}

// assertOutDir checks that the output directory holds exactly want.
func (f *fixture) assertOutDir(t *testing.T, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(f.outDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output directory holds %v, want %v", got, want)
	}
}

func TestBuildCommandSequence(t *testing.T) {
	f := newFixture(t, "podman", "layers = [\"go\"]\nproject_layer = \"ci/agent.Dockerfile\"",
		map[string]string{"ci/agent.Dockerfile": header + "RUN apt-get install -y libpq-dev\n"})
	report, err := f.build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v\nlog:\n%s", err, f.log)
	}
	steps := f.chain(t)
	base, goStep, project := steps[0], steps[1], steps[2]
	machine := f.createdMachine(t)
	stem := f.packStem(t)
	archive := flagValue(f.findCall(t, "podman save").argv, "-o")

	want := []string{
		"podman image exists " + base.Tag,
		"podman build -t " + base.Tag + " -f " + base.Dockerfile + " " + base.Context,
		"podman image exists " + goStep.Tag,
		"podman build -t " + goStep.Tag + " --build-arg BASE=" + base.Tag + " -f " + goStep.Dockerfile + " " + goStep.Context,
		"podman image exists " + project.Tag,
		"podman build -t " + project.Tag + " --build-arg BASE=" + goStep.Tag + " -f " +
			filepath.Join(project.Context, "agent.Dockerfile") + " " + project.Context,
		"podman save --format docker-archive -o " + archive + " " + project.Tag,
		"podman image inspect " + project.Tag,
		"smolvm machine ls --json",
		"smolvm machine create --name " + machine + " --image " + archive + " --label smol-builder=1" +
			" --label smol-builder.pid=4242 --label smol-builder.start=777 --label smol-builder.boot=boot-now" +
			" -e PATH=/usr/local/go/bin:/usr/bin -e SORTIE_STACK_LAYERS=base+go -w /workspace",
		"smolvm machine start --name " + machine,
		"smolvm machine stop --name " + machine,
		"smolvm pack create --from-vm " + machine + " -o " + stem,
		"smolvm machine delete --name " + machine + " -f",
	}
	if got := f.tools.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if !strings.HasPrefix(machine, BuilderPrefix) {
		t.Errorf("machine %q lacks prefix %q", machine, BuilderPrefix)
	}
	if filepath.Dir(filepath.Dir(stem)) != f.outDir {
		t.Errorf("pack stem %s is not in a directory in the output directory", stem)
	}
	if len(f.tools.machines) != 0 {
		t.Errorf("machines left: %v", f.tools.machines)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("archive %s left behind: %v", archive, err)
	}
	f.assertOutDir(t, "app.smolmachine")

	data, err := os.ReadFile(f.output)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	wantReport := &Report{
		Layers:     []string{"base", "go", "project"},
		Image:      project.Tag,
		Reused:     []string{},
		Pack:       f.output,
		PackSHA256: hex.EncodeToString(sum[:]),
		PackBytes:  int64(len(data)),
	}
	got := *report
	got.Timings = Timings{}
	if !reflect.DeepEqual(&got, wantReport) {
		t.Fatalf("report = %+v, want %+v", got, *wantReport)
	}
	if len(report.Timings.Layers) != 3 {
		t.Errorf("timings.layers = %v, want one per step", report.Timings.Layers)
	}
	js, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"layers":`, `"image":`, `"pack":`, `"pack_sha256":`, `"pack_bytes":`, `"timings":`} {
		if !strings.Contains(string(js), key) {
			t.Errorf("report JSON %s lacks %s", js, key)
		}
	}
}

func TestBuildReusesExistingTags(t *testing.T) {
	f := newFixture(t, "podman", `layers = ["go", "node"]`, nil)
	steps := f.chain(t)
	f.tools.images[steps[0].Tag] = true
	f.tools.images[steps[1].Tag] = true

	report, err := f.build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var builds []string
	for _, c := range f.tools.argvs() {
		if strings.HasPrefix(c, "podman build") {
			builds = append(builds, c)
		}
	}
	if len(builds) != 1 || !strings.Contains(builds[0], "-t "+steps[2].Tag+" --build-arg BASE="+steps[1].Tag+" ") {
		t.Fatalf("builds = %v, want only node on top of go", builds)
	}
	if !reflect.DeepEqual(report.Reused, []string{"base", "go"}) {
		t.Fatalf("reused = %v", report.Reused)
	}
	if report.Timings.Layers["base"] != 0 || report.Timings.Layers["go"] != 0 {
		t.Fatalf("reused steps have timings %v", report.Timings.Layers)
	}
}

func TestBuildWithDocker(t *testing.T) {
	f := newFixture(t, "docker", `layers = []`, nil)
	if _, err := f.build(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.tools.argvs()
	tag := f.chain(t)[0].Tag
	if calls[0] != "docker image inspect "+tag {
		t.Errorf("existence check = %q", calls[0])
	}
	var save string
	for _, c := range calls {
		if strings.HasPrefix(c, "docker save") {
			save = c
		}
	}
	if !strings.HasPrefix(save, "docker save -o ") || strings.Contains(save, "--format") {
		t.Errorf("save = %q, want docker save -o <archive> <tag>", save)
	}
}

func TestBuildPrefersPodman(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	f.builder.LookPath = lookPath("docker", "podman", "smolvm")
	if _, err := f.build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := f.tools.argvs(); !strings.HasPrefix(calls[0], "podman ") {
		t.Fatalf("first call %q, want podman", calls[0])
	}
}

func TestBuildMissingPrograms(t *testing.T) {
	tests := []struct {
		name    string
		present []string
		names   []string
	}{
		{name: "no engine", present: []string{"smolvm"}, names: []string{"podman", "docker"}},
		{name: "no smolvm", present: []string{"podman"}, names: []string{"smolvm"}},
		{name: "nothing", present: nil, names: []string{"podman", "docker"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "podman", `layers = []`, nil)
			f.builder.LookPath = lookPath(tt.present...)
			_, err := f.build(context.Background())
			if err == nil {
				t.Fatal("Build succeeded")
			}
			for _, n := range tt.names {
				if !strings.Contains(err.Error(), n) {
					t.Errorf("error %q does not name %s", err, n)
				}
			}
			if len(f.tools.calls) != 0 {
				t.Errorf("ran %v without the required programs", f.tools.argvs())
			}
		})
	}
}

func TestBuildInvalidDefinitionRunsNothing(t *testing.T) {
	f := newFixture(t, "podman", `layers = ["haskell"]`, nil)
	_, err := f.build(context.Background())
	if err == nil || !strings.Contains(err.Error(), `"haskell"`) {
		t.Fatalf("err = %v", err)
	}
	if len(f.tools.calls) != 0 {
		t.Fatalf("ran %v", f.tools.argvs())
	}
}

// writePrevious puts a previous pack at the output path and returns its bytes.
func (f *fixture) writePrevious(t *testing.T) []byte {
	t.Helper()
	prev := []byte("previous pack")
	if err := os.WriteFile(f.output, prev, 0o644); err != nil {
		t.Fatal(err)
	}
	return prev
}

func (f *fixture) assertOutputUnchanged(t *testing.T, prev []byte) {
	t.Helper()
	got, err := os.ReadFile(f.output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prev) {
		t.Fatalf("output changed to %q", got)
	}
	f.assertOutDir(t, "app.smolmachine")
}

func TestBuildLayerFailureKeepsOutput(t *testing.T) {
	f := newFixture(t, "podman", `layers = ["go", "node"]`, nil)
	prev := f.writePrevious(t)
	goTag := f.chain(t)[1].Tag
	f.tools.fail = func(_ context.Context, argv []string) error {
		if argv[1] == "build" && flagValue(argv, "-t") == goTag {
			return exitErr(argv, 1)
		}
		return nil
	}
	_, err := f.build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "layer go:") {
		t.Fatalf("err = %v, want one naming layer go", err)
	}
	for _, c := range f.tools.argvs() {
		if strings.HasPrefix(c, "smolvm") || strings.Contains(c, f.chain(t)[2].Tag) {
			t.Errorf("ran %q after the failed step", c)
		}
	}
	f.assertOutputUnchanged(t, prev)
}

func TestBuildDeletesBuilderOnFailure(t *testing.T) {
	for _, failing := range []string{"machine create", "machine start", "machine stop", "pack create"} {
		t.Run(failing, func(t *testing.T) {
			f := newFixture(t, "podman", `layers = ["go"]`, nil)
			prev := f.writePrevious(t)
			f.tools.fail = func(_ context.Context, argv []string) error {
				if argv[0] == "smolvm" && strings.Join(argv[1:3], " ") == failing {
					if failing == "pack create" {
						// A pack that dies part way leaves its files behind.
						stem := flagValue(argv, "-o")
						_ = os.WriteFile(stem, []byte("partial"), 0o600)
						_ = os.WriteFile(stem+".smolmachine", []byte("partial"), 0o600)
					}
					return exitErr(argv, 2)
				}
				return nil
			}
			_, err := f.build(context.Background())
			if err == nil {
				t.Fatal("Build succeeded")
			}
			machine := f.createdMachine(t)
			if calls := f.tools.argvs(); !slices.Contains(calls, "smolvm machine delete --name "+machine+" -f") {
				t.Fatalf("builder %s not deleted; calls:\n%s", machine, strings.Join(calls, "\n"))
			}
			if len(f.tools.machines) != 0 {
				t.Fatalf("machines left: %v", f.tools.machines)
			}
			f.assertOutputUnchanged(t, prev)
		})
	}
}

func TestBuildDeletesBuilderWhenCancelled(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	prev := f.writePrevious(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.tools.fail = func(ctx context.Context, argv []string) error {
		if strings.Join(argv[:3], " ") == "smolvm pack create" {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if _, err := f.build(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	var deleted *call
	for i, c := range f.tools.calls {
		if strings.HasPrefix(c.String(), "smolvm machine delete") {
			deleted = &f.tools.calls[i]
		}
	}
	if deleted == nil {
		t.Fatal("builder not deleted")
	}
	if deleted.ctxErr != nil {
		t.Fatalf("delete ran with a done context: %v", deleted.ctxErr)
	}
	f.assertOutputUnchanged(t, prev)
}

func TestBuildDeleteFailureFailsBuild(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	prev := f.writePrevious(t)
	f.tools.fail = func(_ context.Context, argv []string) error {
		if strings.Join(argv[:3], " ") == "smolvm machine delete" {
			return exitErr(argv, 1)
		}
		return nil
	}
	_, err := f.build(context.Background())
	machine := f.createdMachine(t)
	if err == nil || !strings.Contains(err.Error(), machine) || !strings.Contains(err.Error(), BuilderLabel) {
		t.Fatalf("err = %v, want one naming %s and its label", err, machine)
	}
	f.assertOutputUnchanged(t, prev)
}

func TestBuildCreateFailureWithoutMachine(t *testing.T) {
	// A create that fails before registering the machine: the delete fails
	// too, but nothing is left, so only the create is reported.
	f := newFixture(t, "podman", `layers = []`, nil)
	f.tools.fail = func(_ context.Context, argv []string) error {
		switch strings.Join(argv[:min(3, len(argv))], " ") {
		case "smolvm machine create", "smolvm machine delete":
			return exitErr(argv, 1)
		}
		return nil
	}
	_, err := f.build(context.Background())
	if err == nil || strings.Contains(err.Error(), "deleting") {
		t.Fatalf("err = %v, want only the create failure", err)
	}
}

func TestBuildMissingPackFile(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	prev := f.writePrevious(t)
	f.tools.noPackFile = true
	if _, err := f.build(context.Background()); err == nil || !strings.Contains(err.Error(), "pack") {
		t.Fatalf("err = %v", err)
	}
	f.assertOutputUnchanged(t, prev)
}

func TestBuildReplacesOutput(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	f.writePrevious(t)
	report, err := f.build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(f.output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pack 1" || report.PackBytes != int64(len(got)) {
		t.Fatalf("output %q, report bytes %d", got, report.PackBytes)
	}
	f.assertOutDir(t, "app.smolmachine")
}

func TestBuildMissingOutputDirectory(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	f.output = filepath.Join(f.outDir, "missing", "app.smolmachine")
	if _, err := f.build(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
	if len(f.tools.calls) != 0 {
		t.Fatalf("ran %v", f.tools.argvs())
	}
}

func TestBuildShippedLibrary(t *testing.T) {
	f := newFixture(t, "podman", `layers = ["go", "rust"]`, nil)
	report, err := f.builder.Build(context.Background(), Options{Repo: f.repo, Output: f.output})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.Layers, []string{"base", "go", "rust"}) {
		t.Fatalf("layers = %v", report.Layers)
	}
	// The fake checked every build context existed when it was built; the
	// extracted library must be gone afterwards.
	entries, err := os.ReadDir(f.builder.TempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory holds %d entries after the build", len(entries))
	}
}

func TestBuildSweepsLeftBehindBuilders(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	host := f.builder.Host.(*fakeHost)
	host.running[100] = "500" // another build, still running
	host.running[200] = "999" // a process that reused a dead build's ID
	builder := func(pid, start, boot string) map[string]string {
		return map[string]string{"smol-builder": "1", LabelPID: pid, LabelStart: start, LabelBoot: boot}
	}
	f.tools.machines = map[string]map[string]string{
		"smol-builder-otherboot": builder("100", "500", "boot-before"),
		"smol-builder-dead":      builder("300", "1", "boot-now"),
		"smol-builder-reused":    builder("200", "600", "boot-now"),
		"smol-builder-live":      builder("100", "500", "boot-now"),
		"smol-builder-old":       {"smol-builder": "1"},
		"smol-builder-unlabeled": {LabelPID: "300", LabelBoot: "boot-before"},
		"user-vm":                {},
	}

	if _, err := f.build(context.Background()); err != nil {
		t.Fatalf("Build: %v\nlog:\n%s", err, f.log)
	}
	var kept []string
	for name := range f.tools.machines {
		kept = append(kept, name)
	}
	slices.Sort(kept)
	if want := []string{"smol-builder-live", "smol-builder-old", "smol-builder-unlabeled", "user-vm"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("machines left: %v, want %v", kept, want)
	}

	// Every left-behind machine goes before the build creates its own.
	calls := f.tools.argvs()
	create := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "smolvm machine create") })
	for _, name := range []string{"smol-builder-otherboot", "smol-builder-dead", "smol-builder-reused"} {
		i := slices.Index(calls, "smolvm machine delete --name "+name+" -f")
		if i < 0 || i > create {
			t.Errorf("%s deleted at call %d, the build's own machine created at %d", name, i, create)
		}
	}
	if !strings.Contains(f.log.String(), "keeping builder machine smol-builder-old") {
		t.Errorf("log does not say why smol-builder-old is kept:\n%s", f.log)
	}
}

func TestSweepWithoutBootID(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	host := f.builder.Host.(*fakeHost)
	host.boot = ""
	host.running[100] = ""
	f.tools.machines = map[string]map[string]string{
		"smol-builder-running": {"smol-builder": "1", LabelPID: "100", LabelBoot: "boot-before"},
		"smol-builder-dead":    {"smol-builder": "1", LabelPID: "300", LabelBoot: "boot-before"},
	}
	if _, err := f.build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.tools.machines["smol-builder-running"]; !ok {
		t.Error("deleted a machine whose process runs, on a host with no boot ID")
	}
	if _, ok := f.tools.machines["smol-builder-dead"]; ok {
		t.Error("kept a machine whose process is gone")
	}
}

func TestSweepFailureStopsTheBuild(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	f.tools.machines["smol-builder-dead"] = map[string]string{"smol-builder": "1", LabelPID: "300"}
	f.tools.fail = func(_ context.Context, argv []string) error {
		if strings.HasPrefix(strings.Join(argv, " "), "smolvm machine delete") {
			return exitErr(argv, 1)
		}
		return nil
	}
	_, err := f.build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "smol-builder-dead") {
		t.Fatalf("err = %v, want one naming the machine it could not delete", err)
	}
	for _, c := range f.tools.argvs() {
		if strings.HasPrefix(c, "smolvm machine create") {
			t.Fatalf("created a machine after the sweep failed: %s", c)
		}
	}
}

func TestSystemHostKnowsItself(t *testing.T) {
	h := systemHost{}
	self := h.Self()
	if self.PID != os.Getpid() || !h.Running(self) {
		t.Fatalf("Self() = %+v, Running = false", self)
	}
	if self.Start != "" && h.Running(Process{PID: self.PID, Start: self.Start + "0"}) {
		t.Error("a process with another start time counts as this one")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if h.Running(Process{PID: cmd.Process.Pid}) {
		t.Errorf("process %d exited but counts as running", cmd.Process.Pid)
	}
	if runtime.GOOS == "linux" && (h.BootID() == "" || self.Start == "") {
		t.Errorf("on Linux, BootID() = %q and Self().Start = %q; want both", h.BootID(), self.Start)
	}
}

func TestBuildSweepsLeftBehindDirectories(t *testing.T) {
	f := newFixture(t, "podman", `layers = []`, nil)
	f.builder.Host.(*fakeHost).running[100] = "500"
	dead := `{"smol-builder.pid":"300","smol-builder.boot":"boot-now"}`
	live := `{"smol-builder.pid":"100","smol-builder.start":"500","smol-builder.boot":"boot-now"}`
	otherBoot := `{"smol-builder.pid":"100","smol-builder.start":"500","smol-builder.boot":"boot-before"}`
	tmp, out := f.builder.TempDir, f.outDir
	writeFiles(t, tmp, map[string]string{
		"smol-builder-dead/" + ownerFile:      dead,
		"smol-builder-dead/image.tar":         "archive",
		"smol-builder-otherboot/" + ownerFile: otherBoot,
		"smol-builder-live/" + ownerFile:      live,
		"smol-builder-foreign/image.tar":      "not ours",
	})
	writeFiles(t, out, map[string]string{
		".app.smolmachine.dead.tmp/" + ownerFile:   dead,
		".app.smolmachine.dead.tmp/pack":           "launcher",
		".app.smolmachine.foreign.tmp/x":           "not ours",
		".other.smolmachine.dead.tmp/" + ownerFile: dead,
	})
	if _, err := f.build(context.Background()); err != nil {
		t.Fatalf("Build: %v\nlog:\n%s", err, f.log)
	}
	for dir, want := range map[string]bool{
		filepath.Join(tmp, "smol-builder-dead"):            false,
		filepath.Join(tmp, "smol-builder-otherboot"):       false,
		filepath.Join(tmp, "smol-builder-live"):            true,
		filepath.Join(tmp, "smol-builder-foreign"):         true,
		filepath.Join(out, ".app.smolmachine.dead.tmp"):    false,
		filepath.Join(out, ".app.smolmachine.foreign.tmp"): true,
		filepath.Join(out, ".other.smolmachine.dead.tmp"):  true,
	} {
		_, err := os.Stat(dir)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists = %v, want %v", dir, exists, want)
		}
	}
}
