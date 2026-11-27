package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v4"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// SchemaFile is the schema's name; it sits beside every environment file.
const SchemaFile = "environment.schema.json"

var dnsLabel = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Env is one environment file, valid against its schema.
type Env struct {
	Name    string `json:"-"`
	Path    string `json:"-"`
	Version int    `json:"version"`
	// Stage0Names binds the hosts installed under the stage-0 names to
	// this environment until they are migrated.
	Stage0Names bool            `json:"stage0_names"`
	Hosts       map[string]Host `json:"hosts"`
	Control     struct {
		FrontPort int `json:"front_port"`
	} `json:"control"`
	Ports struct {
		Sortie        int `json:"sortie"`
		Workbench     int `json:"workbench"`
		Nono          int `json:"nono"`
		NonoLiveProof int `json:"nono_live_proof"`
		EgressGate    int `json:"egress_gate"`
		PreviewsFrom  int `json:"previews_from"`
	} `json:"ports"`
	Tracker struct {
		URL       string `json:"url"`
		Workspace string `json:"workspace"`
		Project   string `json:"project"`
	} `json:"tracker"`
	LocalNames struct {
		Workbench string `json:"workbench"`
	} `json:"local_names"`
}

// Host is what one host of an environment runs.
type Host struct {
	Runs []string `json:"runs"`
	// SSH is the destination the control host reaches the host by; the
	// machine id when empty.
	SSH string `json:"ssh"`
}

// Destination is the SSH destination of host name.
func (e *Env) Destination(name string) string {
	if d := e.Hosts[name].SSH; d != "" {
		return d
	}
	return name
}

// InvalidError lists every invalid entry of a file, each by its path.
type InvalidError struct {
	File    string
	Entries []string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("%s is not a valid environment file:\n  %s", e.File, strings.Join(e.Entries, "\n  "))
}

// Load reads an environment file and checks it against the schema beside
// it, then against the rules a schema cannot say.
func Load(path string) (*Env, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".yml")
	if name == filepath.Base(path) || !dnsLabel.MatchString(name) {
		return nil, fmt.Errorf("%s: an environment file is <env>.yml, with <env> a DNS label ([a-z][a-z0-9-]*)", path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 G703 -- the operator names the file
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, &InvalidError{File: path, Entries: []string{"/: " + err.Error()}}
	}
	if doc == nil {
		return nil, &InvalidError{File: path, Entries: []string{"/: the file is empty"}}
	}
	asJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, &InvalidError{File: path, Entries: []string{"/: " + err.Error()}}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return nil, err
	}
	schema, err := compileSchema(filepath.Join(filepath.Dir(path), SchemaFile))
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(inst); err != nil {
		var verr *jsonschema.ValidationError
		if !errors.As(err, &verr) {
			return nil, err
		}
		return nil, &InvalidError{File: path, Entries: schemaEntries(verr, inst)}
	}
	env := &Env{Name: name, Path: path}
	if err := json.Unmarshal(asJSON, env); err != nil {
		return nil, err
	}
	if entries := env.rules(); len(entries) > 0 {
		return nil, &InvalidError{File: path, Entries: entries}
	}
	return env, nil
}

func compileSchema(path string) (*jsonschema.Schema, error) {
	f, err := os.Open(path) // #nosec G304 G703 -- beside the environment file
	if err != nil {
		return nil, fmt.Errorf("the schema: %w", err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("the schema %s: %w", path, err)
	}
	c := jsonschema.NewCompiler()
	url := "file://" + filepath.ToSlash(path)
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("the schema %s: %w", path, err)
	}
	return c.Compile(url)
}

var printer = message.NewPrinter(language.English)

// schemaEntries flattens a validation error into one line per invalid
// entry, each starting with the entry's path in the file.
func schemaEntries(e *jsonschema.ValidationError, doc any) []string {
	var out []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		at := pointer(e.InstanceLocation)
		switch k := e.ErrorKind.(type) {
		case *kind.Required:
			for _, m := range k.Missing {
				out = append(out, pointer(append(slices.Clone(e.InstanceLocation), m))+": missing")
			}
			return
		case *kind.AdditionalProperties:
			for _, p := range k.Properties {
				out = append(out, pointer(append(slices.Clone(e.InstanceLocation), p))+": not a key of the schema")
			}
			return
		case *kind.PropertyNames:
			// The validator gives a propertyNames error a location it
			// later reuses, so find the key's object in the document.
			for _, obj := range objectsHolding(doc, k.Property, nil) {
				out = append(out, pointer(append(obj, k.Property))+": the key is not a DNS label ([a-z][a-z0-9-]*)")
			}
			return
		}
		if len(e.Causes) == 0 {
			out = append(out, at+": "+e.ErrorKind.LocalizedString(printer))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(e)
	sort.Strings(out)
	return slices.Compact(out)
}

// objectsHolding lists the paths of the objects in doc that have key.
func objectsHolding(doc any, key string, at []string) [][]string {
	var out [][]string
	switch v := doc.(type) {
	case map[string]any:
		if _, ok := v[key]; ok {
			out = append(out, slices.Clone(at))
		}
		for k, c := range v {
			out = append(out, objectsHolding(c, key, append(slices.Clone(at), k))...)
		}
	case []any:
		for i, c := range v {
			out = append(out, objectsHolding(c, key, append(slices.Clone(at), fmt.Sprint(i)))...)
		}
	}
	return out
}

func pointer(tokens []string) string {
	if len(tokens) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(t))
	}
	return b.String()
}

// rules are the checks the schema cannot say: one control host, and no
// port used twice within the file.
func (e *Env) rules() []string {
	var out []string
	control := e.HostsRunning("control")
	switch len(control) {
	case 0:
		out = append(out, "/hosts: no host runs control; exactly one must")
	case 1:
	default:
		out = append(out, fmt.Sprintf("/hosts: %s run control; exactly one must", strings.Join(control, " and ")))
	}
	seen := map[int]string{}
	for _, p := range e.PortEntries() {
		if first, ok := seen[p.Port]; ok {
			out = append(out, fmt.Sprintf("%s: port %d is also %s", p.Path, p.Port, first))
			continue
		}
		seen[p.Port] = p.Path
	}
	return out
}

// HostNames lists the environment's machine ids, sorted.
func (e *Env) HostNames() []string {
	names := make([]string, 0, len(e.Hosts))
	for n := range e.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HostsRunning lists the hosts whose runs hold part, sorted.
func (e *Env) HostsRunning(part string) []string {
	var out []string
	for _, n := range e.HostNames() {
		if slices.Contains(e.Hosts[n].Runs, part) {
			out = append(out, n)
		}
	}
	return out
}

// ControlURL is the control plane's address, which every worker dials:
// Pomerium on the control host, by the name every host of the
// environment knows it by, its machine id.
func (e *Env) ControlURL() string {
	return fmt.Sprintf("https://%s:%d", e.HostsRunning("control")[0], e.Control.FrontPort)
}

// destinations lists each host and its SSH destination, NAME=DEST, by
// machine id.
func (e *Env) destinations() string {
	var out []string
	for _, n := range e.HostNames() {
		out = append(out, n+"="+e.Destination(n))
	}
	return strings.Join(out, " ")
}

// PortEntry is one port of the file, by its path.
type PortEntry struct {
	Path string
	Port int
}

// PortEntries lists every port the environment holds on its hosts.
func (e *Env) PortEntries() []PortEntry {
	return []PortEntry{
		{"/control/front_port", e.Control.FrontPort},
		{"/ports/sortie", e.Ports.Sortie},
		{"/ports/workbench", e.Ports.Workbench},
		{"/ports/nono", e.Ports.Nono},
		{"/ports/nono_live_proof", e.Ports.NonoLiveProof},
		{"/ports/egress_gate", e.Ports.EgressGate},
		{"/ports/previews_from", e.Ports.PreviewsFrom},
	}
}

// NotHostError is a host the environment file does not declare.
type NotHostError struct {
	Host string
	Env  *Env
}

func (e *NotHostError) Error() string {
	declares := strings.Join(e.Env.HostNames(), " ")
	return fmt.Sprintf("%s is not a host of environment %s (%s declares: %s)", e.Host, e.Env.Name, e.Env.Path, declares)
}

// Assignments are the environment's values as shell assignments, one per
// line, each value quoted for the shell. With host set, they add what that
// host runs, and a host the file does not declare is refused.
func (e *Env) Assignments(host string) (string, error) {
	path, err := filepath.Abs(e.Path)
	if err != nil {
		return "", err
	}
	vars := [][2]string{
		{"ORTUS_NOVA_ENV", e.Name},
		{"ORTUS_NOVA_ENV_FILE", path},
		{"ORTUS_NOVA_ENV_HOSTS", strings.Join(e.HostNames(), " ")},
		{"ORTUS_NOVA_ENV_CONTROL_HOST", e.HostsRunning("control")[0]},
		{"ORTUS_NOVA_ENV_WORKER_HOSTS", strings.Join(e.HostsRunning("worker"), " ")},
		{"ORTUS_NOVA_ENV_HOST_SSH", e.destinations()},
		{"ORTUS_NOVA_ENV_FRONT_PORT", fmt.Sprint(e.Control.FrontPort)},
		{"ORTUS_NOVA_ENV_CONTROL_URL", e.ControlURL()},
		{"ORTUS_NOVA_ENV_SORTIE_PORT", fmt.Sprint(e.Ports.Sortie)},
		{"ORTUS_NOVA_ENV_WORKBENCH_PORT", fmt.Sprint(e.Ports.Workbench)},
		{"ORTUS_NOVA_ENV_NONO_PORT", fmt.Sprint(e.Ports.Nono)},
		{"ORTUS_NOVA_ENV_NONO_LIVE_PROOF_PORT", fmt.Sprint(e.Ports.NonoLiveProof)},
		{"ORTUS_NOVA_ENV_EGRESS_GATE_PORT", fmt.Sprint(e.Ports.EgressGate)},
		{"ORTUS_NOVA_ENV_PREVIEW_FIRST_PORT", fmt.Sprint(e.Ports.PreviewsFrom)},
		{"ORTUS_NOVA_ENV_TRACKER_URL", e.Tracker.URL},
		{"ORTUS_NOVA_ENV_TRACKER_WORKSPACE", e.Tracker.Workspace},
		{"ORTUS_NOVA_ENV_TRACKER_PROJECT", e.Tracker.Project},
		{"ORTUS_NOVA_ENV_WORKBENCH_LOCAL_NAME", e.LocalNames.Workbench},
		{"ORTUS_NOVA_ENV_STAGE0_NAMES", fmt.Sprint(e.Stage0Names)},
	}
	if host != "" {
		h, ok := e.Hosts[host]
		if !ok {
			return "", &NotHostError{Host: host, Env: e}
		}
		vars = append(vars, [2]string{"ORTUS_NOVA_ENV_HOST", host}, [2]string{"ORTUS_NOVA_ENV_HOST_RUNS", strings.Join(h.Runs, " ")})
	}
	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "%s=%s\n", v[0], shellQuote(v[1]))
	}
	return b.String(), nil
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@,+-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CheckDir loads every environment file in dir and refuses a port or a
// local name that two of them share, naming both files and the value, so
// environments can coexist on one host.
func CheckDir(dir string) ([]*Env, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s holds no environment file (<env>.yml)", dir)
	}
	sort.Strings(paths)
	var envs []*Env
	var problems []string
	for _, p := range paths {
		env, err := Load(p)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		envs = append(envs, env)
	}
	type owner struct{ file, path string }
	ports := map[int]owner{}
	stage0 := ""
	names := map[string]owner{}
	for _, env := range envs {
		for _, p := range env.PortEntries() {
			if o, ok := ports[p.Port]; ok {
				problems = append(problems, fmt.Sprintf("port %d is %s in %s and %s in %s", p.Port, o.path, o.file, p.Path, env.Path))
				continue
			}
			ports[p.Port] = owner{env.Path, p.Path}
		}
		if env.Stage0Names {
			if stage0 != "" {
				problems = append(problems, fmt.Sprintf("/stage0_names is set in %s and %s; at most one environment holds the stage-0 installs", stage0, env.Path))
			}
			stage0 = env.Path
		}
		if n := env.LocalNames.Workbench; n != "" {
			if o, ok := names[n]; ok {
				problems = append(problems, fmt.Sprintf("local name %s is %s in %s and /local_names/workbench in %s", n, o.path, o.file, env.Path))
				continue
			}
			names[n] = owner{env.Path, "/local_names/workbench"}
		}
	}
	if len(problems) > 0 {
		return envs, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return envs, nil
}
