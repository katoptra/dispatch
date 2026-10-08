package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/katoptra/dispatch/schedules"
)

// stateFile contains the last dispatched slot of each job, by job id.
const stateFile = "state.json"

// State is a map from a job id to the last dispatched slot of that job, in UTC.
type State map[string]time.Time

// LoadState reads the state file in dir. A missing file is empty state: the first tick. A
// file that it cannot read, or that has an incorrect format, is an error, not empty state.
// This is because empty state starts each job again for dispatched slots.
func LoadState(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("state file %s is corrupt: %w", filepath.Join(dir, stateFile), err)
	}
	// json.Unmarshal accepts `null`. But a `null` file is not a missing file: LoadState
	// gives an error for it, not empty state.
	if s == nil {
		return nil, fmt.Errorf("state file %s holds no object", filepath.Join(dir, stateFile))
	}
	return s, nil
}

// SaveState replaces the state file in dir in one step. After a crash, dir contains the
// full previous file or the full new file.
func SaveState(dir string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, stateFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after os.Rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, stateFile)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Fire is one job to dispatch for one slot.
type Fire struct {
	Job  schedules.Job
	Slot time.Time
}

// Plan gives the jobs to dispatch at now. A job is in the result if it has no recorded
// slot, or if its latest slot is after its recorded slot. If there are one or more slots
// after the recorded slot, the job is in the result one time, for the latest slot.
func Plan(jobs []schedules.Job, s State, now time.Time) (fires []Fire, warnings []string) {
	for _, j := range jobs {
		due, ok := j.Slots.Latest(now)
		if !ok {
			continue
		}
		last, seen := s[j.ID()]
		switch {
		case !seen || due.After(last):
			fires = append(fires, Fire{j, due})
		case last.After(due):
			warnings = append(warnings, fmt.Sprintf(
				"%s: state records slot %s, after the latest slot %s; is the clock behind? not firing",
				j.ID(), stamp(last), stamp(due)))
		}
	}
	return fires, warnings
}

// next is the state to write before Tick dispatches fires. It keeps the entry of each job
// in jobs, and it records the slot of each fire. It does not keep the entry of a job that
// is not in jobs.
func next(jobs []schedules.Job, s State, fires []Fire) State {
	n := State{}
	for _, j := range jobs {
		if t, ok := s[j.ID()]; ok {
			n[j.ID()] = t
		}
	}
	for _, f := range fires {
		n[f.Job.ID()] = f.Slot
	}
	return n
}

// Dispatcher starts workflow runs. Prepare does the steps that start no run (the
// credentials). Thus, if Prepare gives an error, Tick can stop before it records the slots.
// Dispatch starts one run.
type Dispatcher interface {
	Prepare(ctx context.Context) error
	Dispatch(ctx context.Context, repo, file string) error
}

// Tick is one tick of the timer: lock, plan, prepare, record, dispatch. Each error goes to
// log. The caller reads log.Errors() for the exit code and the ping.
//
// Tick records the slots before it dispatches. Thus, after a crash or an error from
// Dispatch, that slot gets no run. A slot does not get two runs: a maximum of one run for
// each slot. Tick does Prepare before it records the slots. Thus, if GitHub or the network
// is not available before a run can start, the slot gets its run at the next tick, after
// five minutes.
func Tick(ctx context.Context, dir string, jobs []schedules.Job, now time.Time, d Dispatcher, dryRun bool, log *Log) {
	unlock, err := lock(dir)
	if err != nil {
		log.Error("%v", err)
		return
	}
	defer unlock()

	s, err := LoadState(dir)
	if err != nil {
		log.Error("%v; nothing fired, and nothing will until it is repaired or removed by hand", err)
		return
	}
	fires, warnings := Plan(jobs, s, now)
	for _, w := range warnings {
		log.Warn("%s", w)
	}
	if dryRun {
		for _, f := range fires {
			log.Info("due %s for slot %s (last fired %s)", f.Job.ID(), stamp(f.Slot), lastFired(s, f.Job))
		}
		if len(fires) == 0 {
			log.Info("nothing due")
		}
		return
	}
	if len(fires) == 0 {
		return
	}
	if err := d.Prepare(ctx); err != nil {
		log.Error("%v; nothing fired or recorded, the next tick tries again", err)
		return
	}
	if err := SaveState(dir, next(jobs, s, fires)); err != nil {
		log.Error("save state: %v; nothing fired", err)
		return
	}
	for _, f := range fires {
		if err := d.Dispatch(ctx, f.Job.Repo, f.Job.File); err != nil {
			log.Error("dispatch %s for slot %s: %v; that slot is lost", f.Job.ID(), stamp(f.Slot), err)
			continue
		}
		log.Info("fired %s for slot %s", f.Job.ID(), stamp(f.Slot))
	}
}

// lock gets an exclusive flock on the directory dir, not on a file in it. systemd does not
// start a oneshot unit while that unit operates. The flock also prevents two ticks at the
// same time when you start the binary manually.
func lock(dir string) (func(), error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open state directory: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: another tick holds it: %w", dir, err)
	}
	return func() { f.Close() }, nil
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04Z") }

func lastFired(s State, j schedules.Job) string {
	if t, ok := s[j.ID()]; ok {
		return stamp(t)
	}
	return "never"
}

// Log writes to the journal through stdout and stderr. It keeps the errors for the ping.
// When systemd starts the binary (JOURNAL_STREAM is set), the warning and error lines have
// the sd-daemon priority prefix. Thus, `journalctl -p err` finds the errors.
type Log struct {
	Out, Err io.Writer
	Journal  bool
	errs     []string
}

func (l *Log) Info(format string, a ...any) { fmt.Fprintf(l.Out, format+"\n", a...) }

func (l *Log) Warn(format string, a ...any) { l.write(l.Err, "<4>", format, a...) }

func (l *Log) Error(format string, a ...any) {
	l.errs = append(l.errs, fmt.Sprintf(format, a...))
	l.write(l.Err, "<3>", format, a...)
}

func (l *Log) write(w io.Writer, prio, format string, a ...any) {
	if !l.Journal {
		prio = ""
	}
	fmt.Fprintf(w, prio+format+"\n", a...)
}

// Errors gives each error that Error recorded before this call.
func (l *Log) Errors() []string { return l.errs }
