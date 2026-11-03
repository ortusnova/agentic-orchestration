// Package daemon is the worker daemon: it checks in with the control
// plane, polls for jobs while it has a free run slot and room above its
// reserve and floor, runs each job in a machine, renews each job's
// lease, and reports each outcome once.
package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/cp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/flows"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// Options configures a daemon.
type Options struct {
	Config  config.Config
	Probe   hostinfo.Probe
	Backend machine.Backend
	Log     *slog.Logger
	// TLS is the client TLS of the control plane and the flows; nil
	// builds it from the configuration.
	TLS *tls.Config
}

// Daemon is one worker.
type Daemon struct {
	cfg     config.Config
	probe   hostinfo.Probe
	backend machine.Backend
	log     *slog.Logger
	tls     *tls.Config
	http    *http.Client
	cp      *cp.Client

	heartbeat atomic.Int64
	lease     atomic.Int64
	backendOK atomic.Bool
	wake      chan struct{}

	mu   sync.Mutex
	jobs map[string]*job
	wg   sync.WaitGroup

	// The machines no Sortie started, as last listed, and when each
	// was first seen.
	umu       sync.Mutex
	unmanaged []protocol.Unmanaged
	firstSeen map[string]time.Time
	listedAt  time.Time
}

// DefaultHeartbeat is used until the first check-in answers.
const DefaultHeartbeat = 5 * time.Second

// New returns a daemon.
func New(opt Options) (*Daemon, error) {
	if opt.Probe == nil {
		opt.Probe = hostinfo.Host{}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Backend == nil {
		return nil, errors.New("daemon: no machine backend")
	}
	tlsConfig := opt.TLS
	if tlsConfig == nil {
		var err error
		if tlsConfig, err = cp.TLSConfig(opt.Config.ControlPlane.TLS); err != nil {
			return nil, err
		}
	}
	client := cp.HTTPClient(tlsConfig)
	d := &Daemon{
		cfg: opt.Config, probe: opt.Probe, backend: opt.Backend, log: opt.Log, tls: tlsConfig, http: client,
		cp: &cp.Client{BaseURL: opt.Config.ControlPlane.URL, WorkerID: opt.Config.WorkerID,
			IdentityHeader: opt.Config.ControlPlane.IdentityHeader, HTTP: client},
		wake: make(chan struct{}, 1),
		jobs: map[string]*job{},
	}
	d.heartbeat.Store(int64(DefaultHeartbeat))
	d.lease.Store(int64(3 * DefaultHeartbeat))
	return d, nil
}

func (d *Daemon) heartbeatInterval() time.Duration { return time.Duration(d.heartbeat.Load()) }

func (d *Daemon) leaseDuration() time.Duration { return time.Duration(d.lease.Load()) }

func (d *Daemon) jobsDir() string { return filepath.Join(d.cfg.StateDir, "jobs") }

// Run works until ctx ends, then stops every job's machine, reports
// those jobs as failed, and returns.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.jobsDir(), 0o700); err != nil {
		return fmt.Errorf("daemon: state_dir: %w", err)
	}
	if err := d.backend.Available(ctx); err != nil {
		d.log.Warn("machine backend unavailable; declaring no backend", slog.String("backend", d.backend.Name()), slog.String("error", err.Error()))
	} else {
		d.backendOK.Store(true)
	}
	d.log.Info("worker daemon starting", slog.String("worker", d.cfg.WorkerID), slog.String("control_plane", d.cfg.ControlPlane.URL))
	backoff := time.Second
	for {
		err := d.checkIn(ctx)
		if err == nil {
			break
		}
		d.log.Warn("check-in failed; retrying", slog.String("error", err.Error()), slog.Duration("in", backoff))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
	d.pollWhileFree(ctx)
	t := time.NewTimer(d.heartbeatInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case <-t.C:
		case <-d.wake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		if err := d.checkIn(ctx); err != nil && ctx.Err() == nil {
			d.log.Warn("check-in failed", slog.String("error", err.Error()))
		}
		d.pollWhileFree(ctx)
		t.Reset(d.heartbeatInterval())
	}
}

func (d *Daemon) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// reserved is what the running jobs were promised.
func (d *Daemon) reserved() (n int, r protocol.Requires) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, j := range d.jobs {
		n++
		r.CPU += j.offer.Job.Requires.CPU
		r.MemGiB += j.offer.Job.Requires.MemGiB
		r.DiskGiB += j.offer.Job.Requires.DiskGiB
	}
	return n, r
}

// floor2 rounds down to hundredths, never below zero, so the daemon
// never declares more than it has.
func floor2(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Floor(x*100+1e-9) / 100
}

// Free is what the daemon declares free: what the host has available,
// less the reserve or the floor, less what its running jobs were
// promised. A machine touches its memory and disk lazily, so the host's
// figures can still include part of a running job's share; counting
// the job's whole share errs toward keeping the reserve.
func (d *Daemon) Free(s hostinfo.Snapshot) protocol.Free {
	_, r := d.reserved()
	return protocol.Free{
		CPU:     floor2(s.CPUs - d.cfg.Reserve.CPU - r.CPU),
		MemGiB:  floor2(s.MemAvailableGiB - d.cfg.ReserveMemGiB() - r.MemGiB),
		DiskGiB: floor2(s.DiskAvailableGiB - d.cfg.FloorDiskGiB() - r.DiskGiB),
	}
}

// runSlots is how many jobs run at once on the host of snapshot s.
func (d *Daemon) runSlots(s hostinfo.Snapshot) int { return d.cfg.Slots(s.CPUs, s.MemTotalGiB) }

// Declaration is the check-in body for snapshot s.
func (d *Daemon) Declaration(s hostinfo.Snapshot) protocol.Declaration {
	running, _ := d.reserved()
	backends := []string{}
	if d.backendOK.Load() {
		backends = append(backends, d.backend.Name())
	}
	_, r := d.reserved()
	return protocol.Declaration{OS: s.OS, Arch: s.Arch, KVM: s.KVM, Nested: s.Nested && (d.cfg.Smolvm.Nested == nil || *d.cfg.Smolvm.Nested), Backends: backends,
		RunSlots: d.runSlots(s), Running: running, Free: d.Free(s),
		Used:      protocol.Free{CPU: r.CPU, MemGiB: r.MemGiB, DiskGiB: r.DiskGiB},
		Unmanaged: d.unmanagedSeen()}
}

func (d *Daemon) checkIn(ctx context.Context) error {
	s, err := d.probe.Snapshot(d.cfg.DiskPath)
	if err != nil {
		return err
	}
	d.listUnmanaged(ctx)
	t, err := d.cp.CheckIn(ctx, d.Declaration(s))
	if err != nil {
		return err
	}
	d.heartbeat.Store(int64(time.Duration(t.HeartbeatMS) * time.Millisecond))
	if t.LeaseMS > 0 {
		d.lease.Store(int64(time.Duration(t.LeaseMS) * time.Millisecond))
	}
	return nil
}

func fits(f protocol.Free, cpu, mem, disk float64) bool {
	return f.CPU >= cpu && f.MemGiB >= mem && f.DiskGiB >= disk
}

// pollWhileFree polls while a run slot is free and the free resources
// it would declare hold the smallest configured job.
func (d *Daemon) pollWhileFree(ctx context.Context) {
	for ctx.Err() == nil {
		if !d.backendOK.Load() {
			return
		}
		s, err := d.probe.Snapshot(d.cfg.DiskPath)
		if err != nil {
			d.log.Warn("cannot read the host", slog.String("error", err.Error()))
			return
		}
		if n, _ := d.reserved(); n >= d.runSlots(s) {
			return
		}
		free, sj := d.Free(s), d.cfg.SmallestJob
		if !fits(free, sj.CPU, sj.MemGiB, sj.DiskGiB) {
			d.log.Debug("not polling: less free than the smallest job needs",
				slog.Float64("free_cpu", free.CPU), slog.Float64("free_mem_gib", free.MemGiB), slog.Float64("free_disk_gib", free.DiskGiB))
			return
		}
		offer, err := d.cp.Poll(ctx)
		switch {
		case errors.Is(err, cp.ErrNotCheckedIn):
			if err := d.checkIn(ctx); err != nil {
				d.log.Warn("check-in failed", slog.String("error", err.Error()))
			}
			return
		case err != nil:
			if ctx.Err() == nil {
				d.log.Warn("poll failed", slog.String("error", err.Error()))
			}
			return
		case offer == nil:
			return
		}
		d.accept(offer)
	}
}

func jobKey(id string, attempt int) string { return fmt.Sprintf("%s#%d", id, attempt) }

// refusal says why the daemon will not start an offered job, or "".
func (d *Daemon) refusal(o *protocol.Offer) string {
	if o.Run.Backend != d.backend.Name() {
		return fmt.Sprintf("run.backend %q is not served by this worker (it runs %s)", o.Run.Backend, d.backend.Name())
	}
	if _, err := d.cfg.Smolvm.Pack(o.Run.Image); err != nil {
		return err.Error()
	}
	s, err := d.probe.Snapshot(d.cfg.DiskPath)
	if err != nil {
		return "cannot read the host: " + err.Error()
	}
	if n, _ := d.reserved(); n >= d.runSlots(s) {
		return fmt.Sprintf("all %d run slots are in use", d.runSlots(s))
	}
	f, r := d.Free(s), o.Job.Requires
	switch {
	case r.MemGiB > f.MemGiB:
		return fmt.Sprintf("the job needs %g GiB memory; %g GiB is free above the %g GiB reserve", r.MemGiB, f.MemGiB, d.cfg.ReserveMemGiB())
	case r.DiskGiB > f.DiskGiB:
		return fmt.Sprintf("the job needs %g GiB disk; %g GiB is free above the %g GiB floor", r.DiskGiB, f.DiskGiB, d.cfg.FloorDiskGiB())
	case r.CPU > f.CPU:
		return fmt.Sprintf("the job needs %g CPUs; %g are free", r.CPU, f.CPU)
	}
	return ""
}

func (d *Daemon) accept(o *protocol.Offer) {
	key := jobKey(o.Job.ID, o.Job.Attempt)
	log := d.log.With(slog.String("job", o.Job.ID), slog.Int("attempt", o.Job.Attempt))
	d.mu.Lock()
	_, dup := d.jobs[key]
	d.mu.Unlock()
	if dup {
		log.Warn("offered a job attempt this worker already runs; ignoring")
		return
	}
	fj, err := flows.New(o, d.http, d.tls)
	reason := ""
	if err != nil {
		reason = err.Error()
	} else {
		reason = d.refusal(o)
	}
	if reason != "" {
		log.Warn("not starting the job", slog.String("reason", reason))
		d.wg.Go(func() {
			d.report(log, o.Job.ID, o.Job.Attempt, protocol.Report{Outcome: protocol.OutcomeFailed, Reason: "not started: " + reason, Machine: protocol.MachineDeleted})
		})
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	j := &job{d: d, offer: *o, key: key, flows: fj, log: log, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	d.mu.Lock()
	d.jobs[key] = j
	d.mu.Unlock()
	log.Info("job taken", slog.String("image", o.Run.Image))
	d.wg.Go(j.heartbeats)
	d.wg.Go(j.run)
}

func (d *Daemon) finished(j *job) {
	d.mu.Lock()
	delete(d.jobs, j.key)
	d.mu.Unlock()
	d.signal()
}

// report sends one report, retrying a failed connection until the
// lease would have run out.
func (d *Daemon) report(log *slog.Logger, id string, attempt int, r protocol.Report) {
	deadline := time.Now().Add(d.leaseDuration())
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := d.cp.Report(ctx, id, attempt, r)
		cancel()
		var se *cp.StatusError
		switch {
		case err == nil:
			log.Info("reported", slog.String("outcome", r.Outcome), slog.String("machine", r.Machine), slog.String("reason", r.Reason))
			return
		case errors.Is(err, cp.ErrLeaseLost):
			log.Warn("report refused: the lease is gone", slog.String("outcome", r.Outcome))
			return
		case errors.Is(err, cp.ErrJobGone):
			log.Warn("report refused: the control plane no longer wants the job", slog.String("outcome", r.Outcome), slog.String("error", err.Error()))
			return
		case errors.As(err, &se) && se.Status < 500:
			log.Error("report refused", slog.String("error", err.Error()))
			return
		}
		if time.Now().After(deadline) {
			log.Error("report not delivered before the lease ran out", slog.String("error", err.Error()))
			return
		}
		log.Warn("report failed; retrying", slog.String("error", err.Error()))
		time.Sleep(time.Second)
	}
}

var (
	errLeaseLost = errors.New("the control plane ended the attempt: lease lost")
	errJobGone   = errors.New("the control plane no longer wants the job")
	errSilent    = errors.New("no heartbeat accepted for longer than the lease")
	errShutdown  = errors.New("worker daemon shut down")
)

func (d *Daemon) shutdown() {
	d.mu.Lock()
	jobs := make([]*job, 0, len(d.jobs))
	for _, j := range d.jobs {
		jobs = append(jobs, j)
	}
	d.mu.Unlock()
	for _, j := range jobs {
		j.cancel(errShutdown)
	}
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		d.log.Error("jobs did not end within two minutes of shutdown")
	}
}

// Listeners returns the address of every socket the daemon listens on
// now: the forwarders of its running jobs.
func (d *Daemon) Listeners() []net.Addr {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []net.Addr
	for _, j := range d.jobs {
		out = append(out, j.listeners()...)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].String() < out[b].String() })
	return out
}

// JobDir is the state directory of a job's attempt.
func (d *Daemon) JobDir(id string, attempt int) string {
	return filepath.Join(d.jobsDir(), safeName(id)+"-"+fmt.Sprint(attempt))
}

func safeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

// MachineName is the name of a job attempt's machine.
func MachineName(id string, attempt int) string {
	return "wd-" + safeName(id) + "-" + fmt.Sprint(attempt)
}
