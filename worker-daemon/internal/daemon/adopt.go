package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/cp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/flows"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// errLeave is the cause a job ends with when the daemon stops: the job,
// its machine and its state are left for the daemon's next start.
var errLeave = errors.New("worker daemon stopping: the job is left for its next start")

// recordFile holds a job's state in its job directory, so a daemon that
// restarts adopts the job. It holds no job token: a daemon keeps those in
// memory only, and one that restarts takes them again from the control
// plane (cp.Client.Resume).
const recordFile = "job.json"

// record is what a restarted daemon needs of a job: its offer without
// its tokens, and, once the agent may run, the seed the workspace holds.
type record struct {
	Offer protocol.Offer `json:"offer"`
	Seed  *seed          `json:"seed,omitempty"`
}

// save writes the job's record; a nil s says the agent has not run yet.
func (j *job) save(dir string, s *seed) error {
	rec := record{Offer: j.offer, Seed: s}
	rec.Offer.Flows = map[string]protocol.FlowAccess{}
	for name, fa := range j.offer.Flows {
		rec.Offer.Flows[name] = protocol.FlowAccess{URL: fa.URL}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, recordFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, recordFile))
}

// loadRecord reads a job directory's record, with its offer's tokens.
func loadRecord(dir string) (record, error) {
	var rec record
	b, err := os.ReadFile(filepath.Join(dir, recordFile)) // #nosec G304 -- the daemon's own state
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, err
	}
	if rec.Offer.Job.ID == "" {
		return rec, errors.New("the record names no job")
	}
	return rec, nil
}

// adopt takes the jobs a previous daemon left, before the first
// check-in, so the check-in declares them. A job whose agent may run and
// whose machine still runs is adopted: its lease is renewed, its stdio
// stream dialed again and its agent started again in the same machine.
// A job whose machine is gone, or that had not reached its agent, is
// reported failed and forgotten. A machine this worker's daemon created
// that no job record names is deleted.
func (d *Daemon) adopt(ctx context.Context) {
	known := map[string]bool{}
	adopted, notAdopted := 0, 0
	entries, err := os.ReadDir(d.jobsDir())
	if err != nil {
		d.log.Warn("reading the jobs left by the last start failed", slog.String("error", err.Error()))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(d.jobsDir(), e.Name())
		rec, err := loadRecord(dir)
		if err != nil {
			d.log.Warn("a job directory holds no job: removing it", slog.String("dir", dir), slog.String("error", err.Error()))
			_ = os.RemoveAll(dir)
			continue
		}
		known[MachineName(rec.Offer.Job.ID, rec.Offer.Job.Attempt)] = true
		if d.adoptJob(ctx, dir, rec) {
			adopted++
		} else {
			notAdopted++
		}
	}
	d.log.Info("jobs left by the last start", slog.Int("adopted", adopted), slog.Int("not_adopted", notAdopted))
	d.deleteStrays(ctx, known)
}

// adoptJob takes one job a previous daemon left in dir, and reports
// whether it runs on.
func (d *Daemon) adoptJob(ctx context.Context, dir string, rec record) bool {
	o := rec.Offer
	log := d.log.With(slog.String("job", o.Job.ID), slog.Int("attempt", o.Job.Attempt))
	fail := func(m machine.Machine, why string) {
		state := protocol.MachineDeleted
		if m != nil {
			dctx, cancel := context.WithTimeout(ctx, d.heartbeatInterval()+time.Minute)
			if err := m.Delete(dctx); err != nil {
				log.Error("deleting the machine failed", slog.String("error", err.Error()))
				state = protocol.MachineRunning
			}
			cancel()
		}
		_ = os.RemoveAll(dir)
		log.Warn("job left by the last start not adopted: reporting it failed", slog.String("reason", why))
		d.wg.Go(func() {
			d.report(log, o.Job.ID, o.Job.Attempt, protocol.Report{Outcome: protocol.OutcomeFailed, Reason: why, Machine: state})
		})
	}
	jctx, cancel := context.WithCancelCause(context.Background())
	j := &job{d: d, offer: o, key: jobKey(o.Job.ID, o.Job.Attempt), log: log, ctx: jctx, cancel: cancel, done: make(chan struct{})}
	spec, img, err := j.spec(dir)
	if err != nil {
		cancel(nil)
		fail(nil, err.Error())
		return false
	}
	var m machine.Machine
	if r, ok := d.backend.(machine.Reopener); ok {
		if m, err = r.Reopen(ctx, spec); err != nil {
			cancel(nil)
			log.Warn("the job's machine cannot be read; leaving the job for the next start", slog.String("error", err.Error()))
			return false
		}
	}
	if m == nil {
		cancel(nil)
		fail(nil, "the worker daemon restarted and the job's machine "+spec.Name+" is gone")
		return false
	}
	if rec.Seed == nil {
		cancel(nil)
		fail(m, "the worker daemon restarted before the job's agent started")
		return false
	}
	if j.inputEnv, err = imageEnv(img); err != nil {
		cancel(nil)
		fail(m, err.Error())
		return false
	}
	j.m, j.state = m, protocol.MachineRunning
	d.mu.Lock()
	d.jobs[j.key] = j
	d.mu.Unlock()
	log.Info("job adopted from the last start: resuming it", slog.String("machine", m.Name()))
	s := *rec.Seed
	d.wg.Go(j.heartbeats)
	d.wg.Go(func() {
		j.finish(func(dir string) (string, string) {
			if err := j.resume(); err != nil {
				return failed("resume: %v", err)
			}
			if err := j.serveForwarders(dir); err != nil {
				return failed("forwarder: %v", err)
			}
			return j.agentAndResult(m, filepath.Join(dir, "share"), s, true)
		})
	})
	return true
}

// resume takes the adopted job's offer again from the control plane, its
// new flow tokens with it, for at most a lease. A 410 or 409 ends the job
// with nothing reported, as from a heartbeat; a lease with no answer
// ends it as a silent control plane does.
func (j *job) resume() error {
	var o *protocol.Offer
	start := time.Now()
	err := j.retry(j.ctx, "resume", func() (bool, error) {
		ctx, cancel := context.WithTimeout(j.ctx, j.d.heartbeatInterval()+5*time.Second)
		defer cancel()
		var err error
		o, err = j.d.cp.Resume(ctx, j.id(), j.attempt())
		if errors.Is(err, cp.ErrLeaseLost) {
			j.cancel(errLeaseLost)
			return false, err
		}
		if se, ok := errors.AsType[*cp.StatusError](err); ok && se.Status < 500 && se.Status != http.StatusGone {
			return false, err
		}
		return err != nil, err
	})
	if err != nil {
		if j.ctx.Err() == nil && time.Since(start) > j.d.leaseDuration() {
			j.cancel(errSilent)
		}
		return err
	}
	j.offer.Flows = o.Flows
	fj, err := flows.New(&j.offer, j.d.http, j.d.tls)
	if err != nil {
		return err
	}
	j.flows = fj
	j.log.Info("job resumed: dialing its stdio stream again")
	return nil
}

// deleteStrays deletes each machine this worker's daemon created that
// no job record names, as when its state directory was removed. A
// machine of another worker or of no daemon is left alone.
func (d *Daemon) deleteStrays(ctx context.Context, known map[string]bool) {
	lister, ok := d.backend.(machine.Lister)
	if !ok {
		return
	}
	reopener, ok := d.backend.(machine.Reopener)
	if !ok {
		return
	}
	listed, err := lister.List(ctx)
	if err != nil {
		d.log.Warn("listing the host's machines failed", slog.String("error", err.Error()))
		return
	}
	for _, l := range listed {
		job := l.Labels[machine.LabelJob]
		attempt, aerr := strconv.Atoi(l.Labels["worker-daemon.attempt"])
		if job == "" || l.Labels[machine.LabelWorker] != d.cfg.WorkerID || known[l.Name] {
			continue
		}
		if aerr == nil && l.Name != MachineName(job, attempt) {
			continue
		}
		m, err := reopener.Reopen(ctx, machine.Spec{Name: l.Name})
		if err != nil || m == nil {
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, time.Minute)
		err = m.Delete(dctx)
		cancel()
		if err != nil {
			d.log.Error("deleting a machine with no job failed", slog.String("machine", l.Name), slog.String("error", err.Error()))
			continue
		}
		d.log.Warn("deleted a machine this worker's daemon created and no job names", slog.String("machine", l.Name))
	}
}
