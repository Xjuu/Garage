// Package jobs runs one long task at a time in the background and exposes its
// progress, so the dashboard's buttons return immediately instead of holding a
// request open for the length of a mailbox sweep.
package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
)

// maxLines caps the retained log so a huge run cannot grow without bound.
const maxLines = 500

type Status struct {
	State    State     `json:"state"`
	Kind     string    `json:"kind"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
	Log      []string  `json:"log"`
	Error    string    `json:"error,omitempty"`
	Result   string    `json:"result,omitempty"`
}

// Runner allows a single job at a time. A second request while one is in
// flight is refused rather than queued: two concurrent mailbox sweeps would
// duplicate work and race on the same rows.
type Runner struct {
	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
}

func New() *Runner {
	return &Runner{status: Status{State: StateIdle, Log: []string{}}}
}

// Fn is the work itself. It reports progress through log.
type Fn func(ctx context.Context, log func(string)) (result string, err error)

// Start launches a job unless one is already running.
func (r *Runner) Start(kind string, fn Fn) error {
	r.mu.Lock()
	if r.status.State == StateRunning {
		r.mu.Unlock()
		return fmt.Errorf("a %s job is already running", r.status.Kind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.status = Status{
		State: StateRunning, Kind: kind,
		Started: time.Now(), Log: []string{},
	}
	r.mu.Unlock()

	go func() {
		defer cancel()
		result, err := fn(ctx, r.appendLog)

		r.mu.Lock()
		defer r.mu.Unlock()
		r.status.Finished = time.Now()
		r.status.Result = result
		if err != nil {
			r.status.State = StateFailed
			r.status.Error = err.Error()
		} else {
			r.status.State = StateDone
		}
	}()
	return nil
}

// Cancel stops the running job, if any.
func (r *Runner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.State == StateRunning && r.cancel != nil {
		r.cancel()
		r.appendLogLocked("cancellation requested")
	}
}

func (r *Runner) appendLog(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLogLocked(line)
}

func (r *Runner) appendLogLocked(line string) {
	r.status.Log = append(r.status.Log, time.Now().Format("15:04:05")+"  "+line)
	if len(r.status.Log) > maxLines {
		r.status.Log = r.status.Log[len(r.status.Log)-maxLines:]
	}
}

// Status returns a copy, safe to serialise while the job keeps running.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.Log = append([]string(nil), r.status.Log...)
	return s
}

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.State == StateRunning
}
