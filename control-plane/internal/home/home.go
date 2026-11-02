// Package home is the operator's control-plane directory, CP_HOME: the
// CA, the worker list, Pomerium's rendered configuration, and one file
// per secret, which the compose project's start turns into podman
// secrets.
package home

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/pki"
	"github.com/ortusnova/agentic-orchestration/control-plane/internal/pomerium"
	"github.com/ortusnova/agentic-orchestration/control-plane/internal/workers"
)

// Secret is one secret file the compose project reads at start.
type Secret struct {
	// Name is the file in secrets/ and the podman secret's name.
	Name string
	// What it is and who reads it.
	What string
	// Generated secrets are made by init; the operator provides the rest.
	Generated bool
}

// Secrets lists every secret of the compose project.
var Secrets = []Secret{
	{"cp-sortie-control-token", "Sortie's control token: Sortie, the workbench and the egress gate", true},
	{"cp-nono-pass", "the egress gate's credential to nono's proxy: nono and the egress gate", true},
	{"cp-workbench-secret", "the workbench's session-signing key", true},
	{"cp-viewer-token", "the token of acp-broker's viewer listener, for the sessions Sortie runs; the workbench gets it from Sortie", true},
	{"cp-huly-secret", "Huly's token-signing secret: every Huly service", true},
	{"cp-pomerium-cert", "Pomerium's server certificate, from the control plane's CA", true},
	{"cp-pomerium-key", "Pomerium's server key", true},
	{"cp-egress-ca-cert", "the egress CA nono intercepts TLS with; worker hosts' machines trust it", true},
	{"cp-egress-ca-key", "the egress CA's key: nono", true},
	{"cp-github-token", "the forge token: Sortie pushes and opens pull requests with it, nono replaces the forge placeholder with it, the workbench reads pull requests", false},
	{"cp-anthropic-token", "the model credential (Claude subscription token): nono replaces the model placeholder with it", false},
	{"cp-huly-token", "Sortie's Huly login token (dev/huly_project.py prints it)", false},
	{"cp-workbench-password-hash", "the workbench's local-mode password hash (workbench hash-password)", false},
}

// Home is a control-plane directory.
type Home struct{ Dir string }

// Default is CP_HOME, else ~/.local/share/ortus-nova/control-plane.
func Default() (Home, error) {
	if d := os.Getenv("CP_HOME"); d != "" {
		return Home{Dir: d}, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return Home{}, err
	}
	return Home{Dir: filepath.Join(h, ".local", "share", "ortus-nova", "control-plane")}, nil
}

// Paths in the directory.
func (h Home) CACert() string     { return filepath.Join(h.Dir, "ca", "ca.crt") }
func (h Home) CAKey() string      { return filepath.Join(h.Dir, "ca", "ca.key") }
func (h Home) WorkerList() string { return filepath.Join(h.Dir, "workers.json") }
func (h Home) WorkerDir(n string) string {
	return filepath.Join(h.Dir, "workers", n)
}
func (h Home) PomeriumDir() string    { return filepath.Join(h.Dir, "pomerium") }
func (h Home) PomeriumConfig() string { return filepath.Join(h.PomeriumDir(), "config.yaml") }
func (h Home) SecretsDir() string     { return filepath.Join(h.Dir, "secrets") }
func (h Home) Secret(n string) string { return filepath.Join(h.SecretsDir(), n) }
func (h Home) SettingsFile() string   { return filepath.Join(h.Dir, "settings.json") }

// Settings are the operator's choices that init records.
type Settings struct {
	// PublicURL is the https URL worker hosts dial: Pomerium.
	PublicURL string `json:"public_url"`
	// ServerNames are the names Pomerium's certificate holds.
	ServerNames []string `json:"server_names"`
}

// LoadSettings reads settings.json.
func (h Home) LoadSettings() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(h.SettingsFile())
	if errors.Is(err, fs.ErrNotExist) {
		return s, fmt.Errorf("%s is missing: run cpctl init first", h.SettingsFile())
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", h.SettingsFile(), err)
	}
	return s, nil
}

func writeFile(path string, b []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil { // #nosec G703 -- a path under the operator's CP_HOME
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Init makes what the control plane needs and does not yet have: the
// CA, Pomerium's server certificate for the public URL's host and
// extra names, the egress CA, and every generated secret. It never
// replaces a file that exists, except Pomerium's certificate when the
// names change. It returns what it made.
func (h Home) Init(publicURL string, extraNames []string) ([]string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("public URL %q: want https://host[:port]", publicURL)
	}
	names := append([]string{u.Hostname()}, extraNames...)
	var made []string
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return nil, err
	}
	var ca *pki.CA
	if exists(h.CAKey()) {
		if ca, err = pki.LoadCA(h.CACert(), h.CAKey()); err != nil {
			return nil, err
		}
	} else {
		if ca, err = pki.NewCA("Ortus Nova control plane CA"); err != nil {
			return nil, err
		}
		if err := pki.WritePair(h.CACert(), h.CAKey(), ca.Cert, ca.Key); err != nil {
			return nil, err
		}
		made = append(made, h.CACert())
	}
	old, _ := h.LoadSettings()
	s := Settings{PublicURL: publicURL, ServerNames: names}
	sameNames := strings.Join(old.ServerNames, ",") == strings.Join(names, ",")
	if !sameNames || !exists(h.Secret("cp-pomerium-key")) {
		srv, err := ca.IssueServer(names)
		if err != nil {
			return nil, err
		}
		if err := pki.WritePair(h.Secret("cp-pomerium-cert"), h.Secret("cp-pomerium-key"), srv.Cert, srv.Key); err != nil {
			return nil, err
		}
		if err := os.Chmod(h.Secret("cp-pomerium-cert"), 0o600); err != nil {
			return nil, err
		}
		made = append(made, h.Secret("cp-pomerium-cert"))
	}
	if !exists(h.Secret("cp-egress-ca-key")) {
		eca, err := pki.NewCA("Ortus Nova egress CA")
		if err != nil {
			return nil, err
		}
		if err := pki.WritePair(h.Secret("cp-egress-ca-cert"), h.Secret("cp-egress-ca-key"), eca.Cert, eca.Key); err != nil {
			return nil, err
		}
		if err := os.Chmod(h.Secret("cp-egress-ca-cert"), 0o600); err != nil {
			return nil, err
		}
		made = append(made, h.Secret("cp-egress-ca-cert"))
	}
	for _, sec := range Secrets {
		if !sec.Generated || exists(h.Secret(sec.Name)) || strings.Contains(sec.Name, "-ca-") || strings.HasPrefix(sec.Name, "cp-pomerium-") {
			continue
		}
		if err := writeFile(h.Secret(sec.Name), []byte(rand.Text()+rand.Text()+"\n"), 0o600); err != nil {
			return nil, err
		}
		made = append(made, h.Secret(sec.Name))
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFile(h.SettingsFile(), append(b, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := h.Render(); err != nil {
		return nil, err
	}
	return made, nil
}

// MissingSecrets lists the secrets that have no file, or an empty one.
func (h Home) MissingSecrets() []Secret {
	var out []Secret
	for _, s := range Secrets {
		fi, err := os.Stat(h.Secret(s.Name))
		if err != nil || fi.Size() == 0 {
			out = append(out, s)
		}
	}
	return out
}

// Render writes Pomerium's configuration from the worker list, and the
// CA certificate beside it.
func (h Home) Render() error {
	s, err := h.LoadSettings()
	if err != nil {
		return err
	}
	l, err := workers.Load(h.WorkerList())
	if err != nil {
		return err
	}
	ps := pomerium.Defaults
	ps.PublicURL = s.PublicURL
	b, err := pomerium.Render(ps, l)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(h.CACert())
	if err != nil {
		return err
	}
	// Pomerium runs as a user of its own in its container, so its
	// directory is readable by all; it holds no secret.
	if err := os.MkdirAll(h.PomeriumDir(), 0o755); err != nil { // #nosec G301 -- Pomerium's user reads it
		return err
	}
	if err := os.Chmod(h.PomeriumDir(), 0o755); err != nil { // #nosec G302 -- Pomerium's user reads it
		return err
	}
	if err := writeFile(filepath.Join(h.PomeriumDir(), "ca.crt"), ca, 0o644); err != nil {
		return err
	}
	return writeFile(h.PomeriumConfig(), b, 0o644)
}

// Issued is a worker's certificate as issue leaves it for the operator.
type Issued struct {
	Worker            workers.Worker
	Dir               string
	Cert, Key, CAFile string
}

// IssueWorker issues name a client certificate, lists it for flows (all
// when empty), and renders Pomerium's configuration. A worker already
// listed gets a new certificate; its old one stops working once the
// configuration is applied.
func (h Home) IssueWorker(name string, flows []string) (*Issued, error) {
	if err := workers.ValidName(name); err != nil {
		return nil, err
	}
	if len(flows) == 0 {
		flows = workers.Flows
	}
	ca, err := pki.LoadCA(h.CACert(), h.CAKey())
	if err != nil {
		return nil, err
	}
	l, err := workers.Load(h.WorkerList())
	if err != nil {
		return nil, err
	}
	leaf, err := ca.IssueWorker(name)
	if err != nil {
		return nil, err
	}
	w := workers.Worker{Name: name, Fingerprint: pki.Fingerprint(leaf.Cert), NotAfter: leaf.Cert.NotAfter.UTC().Truncate(time.Second), Flows: flows}
	l.Put(w)
	if err := l.Validate(); err != nil {
		return nil, err
	}
	out := &Issued{Worker: w, Dir: h.WorkerDir(name)}
	out.Cert, out.Key, out.CAFile = filepath.Join(out.Dir, "worker.crt"), filepath.Join(out.Dir, "worker.key"), filepath.Join(out.Dir, "ca.crt")
	if err := pki.WritePair(out.Cert, out.Key, leaf.Cert, leaf.Key); err != nil {
		return nil, err
	}
	if err := writeFile(out.CAFile, pki.CertPEM(ca.Cert), 0o644); err != nil {
		return nil, err
	}
	if err := l.Save(h.WorkerList()); err != nil {
		return nil, err
	}
	return out, h.Render()
}

// RevokeWorker removes name from the worker list, deletes what issue
// left for it, and renders Pomerium's configuration.
func (h Home) RevokeWorker(name string) error {
	l, err := workers.Load(h.WorkerList())
	if err != nil {
		return err
	}
	if !l.Remove(name) {
		return fmt.Errorf("worker %s is not listed", name)
	}
	if err := l.Save(h.WorkerList()); err != nil {
		return err
	}
	if err := os.RemoveAll(h.WorkerDir(name)); err != nil {
		return err
	}
	return h.Render()
}
