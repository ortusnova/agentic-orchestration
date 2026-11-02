// Package config reads the worker daemon's configuration file, a JSON
// object. The file holds no credential: the one secret a worker keeps
// is its client certificate's key, which the file names by path.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// Config is the daemon's configuration.
type Config struct {
	// WorkerID names this worker to the control plane, in the identity
	// header. Behind a proxy that authenticates the client certificate,
	// the proxy sets the header and this value is overridden.
	WorkerID string `json:"worker_id"`

	ControlPlane ControlPlane `json:"control_plane"`

	// RunSlots is how many jobs run at once.
	RunSlots int `json:"run_slots"`

	// Reserve is what the daemon never declares free: memory, and CPUs.
	Reserve *Reserve `json:"reserve"`

	// Floor is the disk the daemon never declares free.
	Floor *Floor `json:"floor"`

	// DiskPath is the file system whose free space is declared: where
	// smolvm keeps its machines. Default: StateDir.
	DiskPath string `json:"disk_path"`

	// SmallestJob is what the smallest job this worker takes needs; the
	// daemon does not poll while less than that is free.
	SmallestJob Size `json:"smallest_job"`

	// StateDir holds one directory per running job: the workspace seed,
	// the exported bundle and the forwarders' sockets. No token is
	// written there.
	StateDir string `json:"state_dir"`

	Smolvm Smolvm `json:"smolvm"`

	// UsageCommands maps an agent kind, the base name of the run's
	// command, to the command run in the machine after the run to read
	// its token usage. "*" applies to every other kind.
	UsageCommands map[string][]string `json:"usage_commands"`

	// LogLevel is debug, info, warn or error.
	LogLevel string `json:"log_level"`
}

// ControlPlane is where and how the daemon dials the control plane.
type ControlPlane struct {
	// URL is the control plane's base URL: Pomerium's https URL, with
	// the client certificate in TLS (design D18).
	URL string `json:"url"`

	// IdentityHeader names the header that carries WorkerID.
	IdentityHeader string `json:"identity_header"`

	// TLS holds the client certificate the control plane issued, and
	// the CA that signs the control plane's certificate.
	TLS TLS `json:"tls"`
}

// TLS names the client certificate and trust files.
type TLS struct {
	CertFile   string `json:"cert_file"`
	KeyFile    string `json:"key_file"`
	CAFile     string `json:"ca_file"`
	ServerName string `json:"server_name"`
}

// Reserve is held back from the free memory and CPUs.
type Reserve struct {
	MemGiB *float64 `json:"mem_gib"`
	CPU    float64  `json:"cpu"`
}

// Floor is held back from the free disk.
type Floor struct {
	DiskGiB *float64 `json:"disk_gib"`
}

// Size is a job's CPUs, memory and disk.
type Size struct {
	CPU     float64 `json:"cpu"`
	MemGiB  float64 `json:"mem_gib"`
	DiskGiB float64 `json:"disk_gib"`
}

// Smolvm configures the smolvm backend.
type Smolvm struct {
	// Binary is the smolvm program. Default: smolvm on PATH.
	Binary string `json:"binary"`

	// Packs maps a run's image reference to a pack file on this host.
	Packs map[string]string `json:"packs"`

	// PackDir, when set, also resolves an image reference ending in
	// .smolmachine to the file of that base name in this directory.
	PackDir string `json:"pack_dir"`

	// User is the user commands in the machine run as; "" keeps the
	// pack's.
	User string `json:"user"`
}

// Defaults.
const (
	DefaultRunSlots = 1
	DefaultLogLevel = "info"
)

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Load reads and validates the file at path.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the file
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

// Parse reads and validates a configuration document.
func Parse(b []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if err := c.normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func fieldError(field, msg string) error { return fmt.Errorf("config: %s: %s", field, msg) }

func (c *Config) normalize() error {
	if !workerIDPattern.MatchString(c.WorkerID) {
		return fieldError("worker_id", fmt.Sprintf("must be letters, digits, '.', '_' or '-', got %q", c.WorkerID))
	}
	u, err := url.Parse(c.ControlPlane.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fieldError("control_plane.url", fmt.Sprintf("must be an absolute http or https URL without user information, got %q", c.ControlPlane.URL))
	}
	c.ControlPlane.URL = strings.TrimRight(c.ControlPlane.URL, "/")
	if c.ControlPlane.IdentityHeader == "" {
		c.ControlPlane.IdentityHeader = protocol.DefaultIdentityHeader
	} else if strings.ContainsAny(c.ControlPlane.IdentityHeader, " \t:") {
		return fieldError("control_plane.identity_header", "must be an HTTP header name")
	}
	t := c.ControlPlane.TLS
	if (t.CertFile == "") != (t.KeyFile == "") {
		return fieldError("control_plane.tls", "cert_file and key_file go together")
	}
	if (t.CertFile != "" || t.CAFile != "") && u.Scheme != "https" {
		return fieldError("control_plane.tls", "a certificate needs an https control_plane.url")
	}
	if c.RunSlots == 0 {
		c.RunSlots = DefaultRunSlots
	}
	if c.RunSlots < 0 {
		return fieldError("run_slots", "must be positive")
	}
	if c.Reserve == nil || c.Reserve.MemGiB == nil {
		return fieldError("reserve.mem_gib", "is required: the memory this host keeps for itself, in GiB")
	}
	if *c.Reserve.MemGiB < 0 || c.Reserve.CPU < 0 {
		return fieldError("reserve", "must not be negative")
	}
	if c.Floor == nil || c.Floor.DiskGiB == nil {
		return fieldError("floor.disk_gib", "is required: the disk this host keeps free, in GiB")
	}
	if *c.Floor.DiskGiB < 0 {
		return fieldError("floor.disk_gib", "must not be negative")
	}
	s := c.SmallestJob
	if s.CPU < 0 || s.MemGiB < 0 || s.DiskGiB < 0 {
		return fieldError("smallest_job", "must not be negative")
	}
	if c.StateDir == "" {
		return fieldError("state_dir", "is required")
	}
	if !filepath.IsAbs(c.StateDir) {
		return fieldError("state_dir", "must be absolute")
	}
	if c.DiskPath == "" {
		c.DiskPath = c.StateDir
	}
	if c.Smolvm.Binary == "" {
		c.Smolvm.Binary = "smolvm"
	}
	for ref, p := range c.Smolvm.Packs {
		if !filepath.IsAbs(p) {
			return fieldError("smolvm.packs."+ref, "must be an absolute path")
		}
	}
	if c.Smolvm.PackDir != "" && !filepath.IsAbs(c.Smolvm.PackDir) {
		return fieldError("smolvm.pack_dir", "must be absolute")
	}
	for kind, argv := range c.UsageCommands {
		if len(argv) == 0 || argv[0] == "" {
			return fieldError("usage_commands."+kind, "must be a non-empty argument list")
		}
	}
	switch c.LogLevel {
	case "":
		c.LogLevel = DefaultLogLevel
	case "debug", "info", "warn", "error":
	default:
		return fieldError("log_level", "must be debug, info, warn or error")
	}
	return nil
}

// ReserveMemGiB is the memory reserve.
func (c Config) ReserveMemGiB() float64 { return *c.Reserve.MemGiB }

// FloorDiskGiB is the disk floor.
func (c Config) FloorDiskGiB() float64 { return *c.Floor.DiskGiB }

// UsageCommand returns the usage command of an agent command, or nil.
func (c Config) UsageCommand(agentCommand string) []string {
	if argv, ok := c.UsageCommands[filepath.Base(agentCommand)]; ok {
		return argv
	}
	return c.UsageCommands["*"]
}

// ErrNoPack reports an image this worker has no pack for.
var ErrNoPack = errors.New("no pack on this worker")

// Pack returns the pack file of an image reference.
func (s Smolvm) Pack(image string) (string, error) {
	if p, ok := s.Packs[image]; ok {
		return p, nil
	}
	if s.PackDir != "" && strings.HasSuffix(image, ".smolmachine") {
		base := filepath.Base(image)
		if base != "." && base != ".." && base != "/" {
			return filepath.Join(s.PackDir, base), nil
		}
	}
	return "", fmt.Errorf("image %q: %w (smolvm.packs and smolvm.pack_dir name none)", image, ErrNoPack)
}
