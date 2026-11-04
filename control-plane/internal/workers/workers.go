// Package workers is the control plane's worker list: each worker host's
// name, the fingerprint of its client certificate, and the flows it may
// reach. Pomerium's routes are rendered from it, so a worker removed
// from the list reaches no route once the list is applied.
package workers

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
	"time"
)

// Flows are the routes Pomerium serves a worker: the worker API
// (control) and the five data-plane flows of a job.
var Flows = []string{"control", "stdio", "egress", "mcp", "bundles", "usage"}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

var fingerprintRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Worker is one worker host the control plane admits.
type Worker struct {
	// Name is the worker's identity: the DNS name in its certificate,
	// which Pomerium asserts to Sortie.
	Name string `json:"name"`
	// Fingerprint is the SHA-256 of its certificate, lower-case hex.
	Fingerprint string `json:"fingerprint"`
	// NotAfter is when the certificate expires.
	NotAfter time.Time `json:"not_after"`
	// Flows lists the routes the worker may reach.
	Flows []string `json:"flows"`
}

// List is the worker list file.
type List struct {
	Workers []Worker `json:"workers"`
}

// ValidName reports whether name can name a worker: a DNS label, so it
// fits a certificate's DNS name and Sortie's identity header.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("worker name %q: want a DNS label (lower-case letters, digits and '-', at most 63)", name)
	}
	return nil
}

// Validate checks every entry and that no name or fingerprint repeats.
func (l *List) Validate() error {
	names := map[string]bool{}
	fps := map[string]string{}
	for _, w := range l.Workers {
		if err := ValidName(w.Name); err != nil {
			return err
		}
		if names[w.Name] {
			return fmt.Errorf("worker %s is listed twice", w.Name)
		}
		names[w.Name] = true
		if !fingerprintRE.MatchString(w.Fingerprint) {
			return fmt.Errorf("worker %s: fingerprint %q is not a SHA-256 in lower-case hex", w.Name, w.Fingerprint)
		}
		if other, ok := fps[w.Fingerprint]; ok {
			return fmt.Errorf("workers %s and %s share a certificate", other, w.Name)
		}
		fps[w.Fingerprint] = w.Name
		if len(w.Flows) == 0 {
			return fmt.Errorf("worker %s reaches no flow", w.Name)
		}
		for _, f := range w.Flows {
			if !slices.Contains(Flows, f) {
				return fmt.Errorf("worker %s: no flow %q; flows are %v", w.Name, f, Flows)
			}
		}
	}
	return nil
}

// Load reads a worker list; a missing file is an empty list.
func Load(file string) (*List, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the operator's file
	if errors.Is(err, os.ErrNotExist) {
		return &List{}, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var l List
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &l, nil
}

// Save writes the list, sorted by name, replacing file atomically.
func (l *List) Save(file string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	sort.Slice(l.Workers, func(i, j int) bool { return l.Workers[i].Name < l.Workers[j].Name })
	if l.Workers == nil {
		l.Workers = []Worker{}
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Get returns the worker named name.
func (l *List) Get(name string) (Worker, bool) {
	for _, w := range l.Workers {
		if w.Name == name {
			return w, true
		}
	}
	return Worker{}, false
}

// Put adds w, or replaces the worker of the same name.
func (l *List) Put(w Worker) {
	for i := range l.Workers {
		if l.Workers[i].Name == w.Name {
			l.Workers[i] = w
			return
		}
	}
	l.Workers = append(l.Workers, w)
}

// Remove drops the worker named name and reports whether it was listed.
func (l *List) Remove(name string) bool {
	n := len(l.Workers)
	l.Workers = slices.DeleteFunc(l.Workers, func(w Worker) bool { return w.Name == name })
	return len(l.Workers) != n
}

// Allowed returns the fingerprints of the workers that may reach flow,
// sorted.
func (l *List) Allowed(flow string) []string {
	var out []string
	for _, w := range l.Workers {
		if slices.Contains(w.Flows, flow) {
			out = append(out, w.Fingerprint)
		}
	}
	sort.Strings(out)
	return out
}
