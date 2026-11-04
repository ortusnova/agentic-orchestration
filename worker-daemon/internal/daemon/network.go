package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/egress"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// offerNetwork is the machine's network for an offer's run.egress
// (design D8), nil for an offer without one, and the plan it comes from.
// Every credential host, of run.egress.credential_hosts and of
// run.credentials, is left out, so that it stays on the egress flow.
func offerNetwork(o protocol.Offer) (*machine.Network, egress.Plan, error) {
	e, err := egress.Parse(o.Run.Egress)
	if err != nil || e == nil {
		return nil, egress.Plan{}, err
	}
	var creds []string
	for _, c := range o.Run.Credentials {
		creds = append(creds, c.Hosts...)
	}
	plan := egress.PlanOf(e, creds)
	return plan.Network(), plan, nil
}

// logNetwork writes the machine's network to the job's log: each
// allowed host with its source, harness or repository.
func logNetwork(log *slog.Logger, n *machine.Network, plan egress.Plan) {
	if n == nil {
		if !plan.Empty() {
			log.Warn("egress: no host is allowed directly, so the udp and private entries give no network")
		}
		log.Info("egress: the machine has no network; its only way out is the egress flow")
		return
	}
	for _, h := range plan.Hosts {
		log.Info("egress: host allowed", slog.String("host", h.Name), slog.String("source", strings.Join(h.Sources, ",")))
	}
	for _, u := range plan.UDP {
		log.Info("egress: udp allowed", slog.String("udp", u))
	}
	for _, p := range plan.Private {
		log.Info("egress: private range allowed", slog.String("cidr", p))
	}
	log.Info("egress: private ranges denied", slog.String("cidrs", strings.Join(n.Deny, ",")))
	for _, h := range plan.Withheld {
		log.Warn("egress: host left on the egress flow: it is or covers a credential host", slog.String("host", h))
	}
}

// RefusalsFile is where the daemon keeps the egress refusals of a job's
// attempt, after its machine is gone.
func (d *Daemon) RefusalsFile(id string, attempt int) string {
	return filepath.Join(d.cfg.StateDir, "refusals", safeName(id), fmt.Sprint(attempt)+".json")
}

// Refusals is the record [Daemon.RefusalsFile] holds.
type Refusals struct {
	Job     string    `json:"job"`
	Attempt int       `json:"attempt"`
	Machine string    `json:"machine"`
	ReadAt  time.Time `json:"read_at"`
	// Events are the machine's egress events as smolvm prints them
	// (smolvm machine egress-events --json).
	Events json.RawMessage `json:"events"`
}

// keepRefusals reads the egress refusals of a job's machine, before it is
// deleted, and writes them to [Daemon.RefusalsFile]. A failure is logged:
// it does not keep the machine.
func (d *Daemon) keepRefusals(ctx context.Context, log *slog.Logger, id string, attempt int, m machine.Machine) {
	ev, ok := m.(machine.EgressEventer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := ev.EgressEvents(ctx)
	if err != nil {
		log.Error("egress: reading the machine's refusals failed", slog.String("machine", m.Name()), slog.String("error", err.Error()))
		return
	}
	out = bytes.TrimSpace(out)
	if !json.Valid(out) {
		quoted, _ := json.Marshal(string(out))
		out = quoted
	}
	b, err := json.MarshalIndent(Refusals{Job: id, Attempt: attempt, Machine: m.Name(), ReadAt: time.Now().UTC(), Events: out}, "", "  ")
	if err != nil {
		log.Error("egress: the machine's refusals", slog.String("error", err.Error()))
		return
	}
	file := d.RefusalsFile(id, attempt)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		log.Error("egress: keeping the machine's refusals failed", slog.String("error", err.Error()))
		return
	}
	tmp := file + ".tmp"
	err = os.WriteFile(tmp, append(b, '\n'), 0o600)
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp)
		log.Error("egress: keeping the machine's refusals failed", slog.String("error", err.Error()))
		return
	}
	log.Info("egress: the machine's refusals kept", slog.String("file", file))
}
