package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// A job reports the step it is at by writing it to its step file, the
// file StepFileEnv names in the guest: a line of text, the step, or a
// JSON object {"step", "phase"}. The last line written counts. Each
// heartbeat carries it to the control plane.
const (
	StepFileEnv = "SORTIE_STEP_FILE"
	StepFile    = "step"
	// MaxStep bounds a reported step or phase, in characters.
	MaxStep = 200
	// maxStepRead bounds how much of the step file is read, from its end.
	maxStepRead = 4 << 10
)

// beat is the heartbeat body: the job's step as its step file says now.
func (j *job) beat() protocol.Beat {
	return ReadStep(filepath.Join(j.d.JobDir(j.id(), j.attempt()), "share", StepFile))
}

// ReadStep reads a step file. A missing, empty or unreadable file
// reports no step.
func ReadStep(path string) protocol.Beat {
	f, err := os.Open(path) // #nosec G304 -- the job's own directory
	if err != nil {
		return protocol.Beat{}
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return protocol.Beat{}
	}
	if size := info.Size(); size > maxStepRead {
		if _, err := f.Seek(size-maxStepRead, io.SeekStart); err != nil {
			return protocol.Beat{}
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStepRead))
	if err != nil {
		return protocol.Beat{}
	}
	var line []byte
	for l := range bytes.SplitSeq(b, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			line = bytes.TrimSpace(l)
		}
	}
	if line == nil {
		return protocol.Beat{}
	}
	beat := protocol.Beat{Step: clean(string(line))}
	if line[0] == '{' {
		var v struct {
			Step  string `json:"step"`
			Phase string `json:"phase"`
		}
		if json.Unmarshal(line, &v) != nil {
			return protocol.Beat{}
		}
		beat = protocol.Beat{Step: clean(v.Step), Phase: clean(v.Phase)}
	}
	if beat.Step == "" && beat.Phase == "" {
		return protocol.Beat{}
	}
	at := info.ModTime().UTC()
	beat.StepAt = &at
	return beat
}

// clean keeps a step printable and short.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxStep {
		s = string(r[:MaxStep])
	}
	return s
}
