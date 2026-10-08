package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katoptra/dispatch/schedules"
)

var (
	ctan  = schedules.Job{Repo: "katoptra/ctan", File: "sync.yml", Slots: schedules.Hourly}
	tlnet = schedules.Job{Repo: "katoptra/tlnet", File: "sync.yml", Slots: schedules.Evening}
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// fake records each job that Dispatch gets, and reads the state file as it is at that time.
// It gives an error for each job in fail. Prepare gives prepErr.
type fake struct {
	dir      string
	fail     map[string]bool
	prepErr  error
	prepared int
	calls    []string
	seen     []State
}

func (f *fake) Prepare(context.Context) error {
	f.prepared++
	return f.prepErr
}

func (f *fake) Dispatch(_ context.Context, repo, file string) error {
	f.calls = append(f.calls, repo+"/"+file)
	s, err := LoadState(f.dir)
	if err != nil {
		return err
	}
	f.seen = append(f.seen, s)
	if f.fail[repo+"/"+file] {
		return errors.New("boom")
	}
	return nil
}

func tick(t *testing.T, dir string, jobs []schedules.Job, now string, d Dispatcher, dry bool) (*Log, string) {
	t.Helper()
	var out bytes.Buffer
	log := &Log{Out: &out, Err: &out}
	Tick(context.Background(), dir, jobs, utc(now), d, dry, log)
	return log, out.String()
}

func TestCatchUpFiresOnceForTheLatestSlot(t *testing.T) {
	dir := t.TempDir()
	must(t, SaveState(dir, State{ctan.ID(): utc("2026-09-21T02:42:00Z")}))
	f := &fake{dir: dir}
	log, _ := tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:50:00Z", f, false)
	if len(log.Errors()) != 0 || len(f.calls) != 1 {
		t.Fatalf("ten missed slots: calls %v, errors %v; want one call, no errors", f.calls, log.Errors())
	}
	s, _ := LoadState(dir)
	if !s[ctan.ID()].Equal(utc("2026-09-21T12:42:00Z")) {
		t.Errorf("recorded %v, want the latest slot 12:42", s[ctan.ID()])
	}
}

func TestSameSlotNeverFiresTwice(t *testing.T) {
	dir := t.TempDir()
	f := &fake{dir: dir}
	tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:42:30Z", f, false)
	tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:57:00Z", f, false)
	if len(f.calls) != 1 {
		t.Errorf("calls %v; want one dispatch for slot 12:42", f.calls)
	}
}

func TestSlotIsRecordedBeforeTheDispatch(t *testing.T) {
	dir := t.TempDir()
	f := &fake{dir: dir, fail: map[string]bool{ctan.ID(): true}}
	log, _ := tick(t, dir, []schedules.Job{ctan, tlnet}, "2026-09-21T12:45:00Z", f, false)
	if len(f.seen) != 2 {
		t.Fatalf("calls %v; want both jobs dispatched though the first failed", f.calls)
	}
	for _, s := range f.seen {
		if !s[ctan.ID()].Equal(utc("2026-09-21T12:42:00Z")) || !s[tlnet.ID()].Equal(utc("2026-09-21T05:42:00Z")) {
			t.Errorf("state during dispatch %v; want both slots already recorded", s)
		}
	}
	if errs := log.Errors(); len(errs) != 1 || !strings.Contains(errs[0], ctan.ID()) {
		t.Errorf("errors %v; want one naming %s", errs, ctan.ID())
	}
	f.fail = nil
	tick(t, dir, []schedules.Job{ctan, tlnet}, "2026-09-21T12:50:00Z", f, false)
	if len(f.calls) != 2 {
		t.Errorf("calls %v; a failed dispatch's slot must not be retried", f.calls)
	}
}

func TestPrepareFailureRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	f := &fake{dir: dir, prepErr: errors.New("installation token: 503")}
	log, _ := tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:45:00Z", f, false)
	if len(f.calls) != 0 || len(log.Errors()) != 1 {
		t.Errorf("calls %v, errors %v; want no dispatch and one error", f.calls, log.Errors())
	}
	if _, err := os.Stat(filepath.Join(dir, stateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a failed prepare recorded the slot")
	}
	f.prepErr = nil
	tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:50:00Z", f, false)
	if len(f.calls) != 1 {
		t.Errorf("calls %v; the next tick must fire the slot the failed prepare left", f.calls)
	}
}

func TestNothingDuePreparesNothing(t *testing.T) {
	dir := t.TempDir()
	must(t, SaveState(dir, State{ctan.ID(): utc("2026-09-21T12:42:00Z")}))
	f := &fake{dir: dir}
	tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:50:00Z", f, false)
	if f.prepared != 0 {
		t.Error("a tick with nothing due minted a token")
	}
}

func TestCorruptStateFiresNothing(t *testing.T) {
	for _, bad := range []string{"{not json", "null", ""} {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, stateFile), []byte(bad), 0o600))
		f := &fake{dir: dir}
		log, _ := tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:45:00Z", f, false)
		if len(f.calls) != 0 || len(log.Errors()) != 1 {
			t.Errorf("state %q: calls %v, errors %v; want no calls and one error", bad, f.calls, log.Errors())
		}
		b, _ := os.ReadFile(filepath.Join(dir, stateFile))
		if string(b) != bad {
			t.Errorf("state %q: a corrupt state file must be left as it was", bad)
		}
	}
}

func TestClockBehindStateWarnsAndSkips(t *testing.T) {
	dir := t.TempDir()
	must(t, SaveState(dir, State{ctan.ID(): utc("2026-09-21T14:42:00Z")}))
	f := &fake{dir: dir}
	log, out := tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:45:00Z", f, false)
	if len(f.calls) != 0 || len(log.Errors()) != 0 || !strings.Contains(out, "clock") {
		t.Errorf("calls %v, errors %v, output %q; want a clock warning and nothing else", f.calls, log.Errors(), out)
	}
}

func TestRemovedJobIsDroppedFromState(t *testing.T) {
	dir := t.TempDir()
	must(t, SaveState(dir, State{"katoptra/gone/sync.yml": utc("2026-09-01T00:42:00Z")}))
	tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:45:00Z", &fake{dir: dir}, false)
	s, _ := LoadState(dir)
	if _, ok := s["katoptra/gone/sync.yml"]; ok || len(s) != 1 {
		t.Errorf("state %v; want only %s", s, ctan.ID())
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	dir := t.TempDir()
	f := &fake{dir: dir}
	_, out := tick(t, dir, []schedules.Job{ctan}, "2026-09-21T12:45:00Z", f, true)
	if len(f.calls) != 0 || !strings.Contains(out, ctan.ID()) {
		t.Errorf("calls %v, output %q; want the due job printed and nothing dispatched", f.calls, out)
	}
	if _, err := os.Stat(filepath.Join(dir, stateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a dry run wrote the state file")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
