package registry

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Record is what a broker advertises about its session.
type Record struct {
	Key       string    `json:"key"`
	PID       int       `json:"pid"`
	Socket    string    `json:"socket"`
	HTTPURL   string    `json:"http_url,omitempty"`
	Issue     string    `json:"issue,omitempty"`
	Rule      string    `json:"rule,omitempty"`
	ChainID   string    `json:"chain_id,omitempty"`
	Attempt   string    `json:"attempt,omitempty"`
	Agent     Agent     `json:"agent"`
	SessionID string    `json:"session_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Agent names the agent behind a session, as its initialize result
// reported it.
type Agent struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// recordSuffix ends every registry record file name.
const recordSuffix = ".json"

// SanitizeKey turns s into a name safe for a file: every character other
// than an ASCII letter, a digit, '-', and '_' becomes '_'. A key that
// sanitizes to nothing becomes "session".
func SanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if strings.Trim(b.String(), "_") == "" {
		return "session"
	}
	return b.String()
}

// KeyFor returns the registry key for a session: the issue identifier
// when there is one, and the working directory's base name otherwise,
// either one sanitized.
func KeyFor(identifier, workdir string) string {
	if strings.TrimSpace(identifier) != "" {
		return SanitizeKey(identifier)
	}
	return SanitizeKey(filepath.Base(workdir))
}

// maxSocketPath is the longest path a Unix socket address can hold:
// sun_path is 108 bytes on Linux and 104 on macOS, each with its NUL.
func maxSocketPath() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}

// SocketPath returns the socket path of the broker with pid for key:
// KEY.PID.sock in dir. When that is longer than a socket address holds,
// it is a short, stable name for it in $XDG_RUNTIME_DIR (else the
// temporary directory) instead. A client reads the path from the
// broker's record, so it finds either one.
func SocketPath(dir, key string, pid int) string {
	p := filepath.Join(dir, fmt.Sprintf("%s.%d.sock", key, pid))
	if len(p) <= maxSocketPath() {
		return p
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	sum := sha256.Sum256([]byte(p))
	return filepath.Join(base, fmt.Sprintf("acp-broker-%x.sock", sum[:8]))
}

func recordPath(dir, key string, pid int) string {
	return filepath.Join(dir, fmt.Sprintf("%s.%d%s", key, pid, recordSuffix))
}

// Write stores rec in dir, replacing the broker's previous record. The
// file appears atomically, so a reader never sees a partial record.
func Write(dir string, rec Record) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := recordPath(dir, rec.Key, rec.PID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove deletes the record and socket of the broker with pid for key,
// and nothing else.
func Remove(dir, key string, pid int) {
	_ = os.Remove(recordPath(dir, key, pid))
	_ = os.Remove(SocketPath(dir, key, pid))
}

// List returns the records in dir of live sessions, newest first: the
// broker process exists and its socket accepts a connection. A record
// whose process no longer exists is stale; List removes it with its
// socket. An empty key lists every key.
func List(dir, key string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []Record
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, recordSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a file in our own registry directory
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(data, &rec) != nil || rec.PID <= 0 {
			continue
		}
		if key != "" && rec.Key != key {
			continue
		}
		if !processAlive(rec.PID) {
			Remove(dir, rec.Key, rec.PID)
			continue
		}
		if !SocketAccepts(rec.Socket) {
			continue
		}
		live = append(live, rec)
	}
	slices.SortFunc(live, func(a, b Record) int { return b.StartedAt.Compare(a.StartedAt) })
	return live, nil
}
