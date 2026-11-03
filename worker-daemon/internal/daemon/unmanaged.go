package daemon

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// ListEvery is how often the daemon lists the host's machines for the
// ones no Sortie started: at most once per check-in, and no more often
// than this.
var ListEvery = 30 * time.Second

// listUnmanaged lists the host's machines, when the backend can and the
// last list is older than ListEvery, and keeps the ones no Sortie
// started. A failed list keeps the last one.
func (d *Daemon) listUnmanaged(ctx context.Context) {
	lister, ok := d.backend.(machine.Lister)
	if !ok {
		return
	}
	d.umu.Lock()
	due := time.Since(d.listedAt) >= ListEvery
	d.umu.Unlock()
	if !due {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	listed, err := lister.List(lctx)
	if err != nil {
		d.log.Warn("listing the host's machines failed", slog.String("error", err.Error()))
		return
	}
	now := time.Now().UTC()
	d.umu.Lock()
	defer d.umu.Unlock()
	if d.firstSeen == nil {
		d.firstSeen = map[string]time.Time{}
	}
	seen := map[string]time.Time{}
	var out []protocol.Unmanaged
	for _, l := range listed {
		if !l.Unmanaged() {
			continue
		}
		first, ok := d.firstSeen[l.Name]
		if !ok {
			first = now
			d.log.Info("a machine no Sortie started runs on this host", slog.String("machine", l.Name))
		}
		seen[l.Name] = first
		since := first
		if !l.Created.IsZero() {
			since = l.Created.UTC()
		}
		out = append(out, protocol.Unmanaged{Name: l.Name, Since: since, State: l.State})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	d.firstSeen, d.unmanaged, d.listedAt = seen, out, now
}

// unmanagedSeen is the last list of machines no Sortie started; never nil.
func (d *Daemon) unmanagedSeen() []protocol.Unmanaged {
	d.umu.Lock()
	defer d.umu.Unlock()
	return append([]protocol.Unmanaged{}, d.unmanaged...)
}
