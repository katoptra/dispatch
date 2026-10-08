// Package schedules tells the scheduler which jobs to start, and when. Each katoptra
// repository has one file here. The file name is the part of "owner/name" after the slash.
// Each file adds the jobs of its repository to the list below. A change to the jobs or to
// their slots is only in this directory.
//
// Before you add a workflow, make sure that it has these three items:
//
//  1. It has `workflow_dispatch:` in `on:`.
//  2. It has a `concurrency` group with `cancel-in-progress: false`. Thus, a workflow start
//     during a run waits in the queue, and two runs do not operate at the same time.
//  3. The workflow sends a ping to its healthcheck. This repository does not get the
//     result of a run.
//
// No code here can examine these items. If a workflow does not have all three, a problem
// with its runs can stay unknown.
package schedules

import (
	"fmt"
	"strings"
	"time"
)

// Slot is a set of hours of the UTC day. The slot of each hour is at HH:42. To put slots
// together, use |. For example, a job in two slots has `Morning | Evening`. An incorrect
// name causes a compile error.
type Slot uint32

// The 24 hourly slots, S0 at 00:42 UTC to S23 at 23:42 UTC.
const (
	S0 Slot = 1 << iota
	S1
	S2
	S3
	S4
	S5
	S6
	S7
	S8
	S9
	S10
	S11
	S12
	S13
	S14
	S15
	S16
	S17
	S18
	S19
	S20
	S21
	S22
	S23
)

// Names for the daily slots, six hours apart. The Pacific times in the comments are winter
// times. In summer, each Pacific time is one hour after the time in the comment.
const (
	Evening   = S5  // 05:42 UTC, 21:42 PST
	Overnight = S11 // 11:42 UTC, 03:42 PST
	Morning   = S17 // 17:42 UTC, 09:42 PST
	Afternoon = S23 // 23:42 UTC, 15:42 PST
	Hourly    = Slot(1<<24 - 1)
)

// Minute is the minute of the hour for each slot. It is not minute 0: when the load on
// GitHub is high, GitHub decreases the load at minute 0 first. It is also on a tick of the
// */5 timer that starts at :02.
const Minute = 42

// Latest gives the latest slot time in s: the last slot time at or before now, in UTC. The
// slots of the same day and of the day before are always sufficient to find it. The bool is
// false when s contains no hour.
func (s Slot) Latest(now time.Time) (time.Time, bool) {
	now = now.UTC()
	for day := 0; day < 2; day++ {
		d := now.AddDate(0, 0, -day)
		for hour := 23; hour >= 0; hour-- {
			if s&(1<<hour) == 0 {
				continue
			}
			t := time.Date(d.Year(), d.Month(), d.Day(), hour, Minute, 0, 0, time.UTC)
			if !t.After(now) {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// Times gives the time of each slot in s as "HH:42" UTC, in the sequence of the day.
func (s Slot) Times() []string {
	var out []string
	for hour := 0; hour < 24; hour++ {
		if s&(1<<hour) != 0 {
			out = append(out, fmt.Sprintf("%02d:%02d", hour, Minute))
		}
	}
	return out
}

var names = map[Slot]string{
	Hourly: "Hourly", Evening: "Evening", Overnight: "Overnight", Morning: "Morning", Afternoon: "Afternoon",
}

// String gives the name of s as a schedule file writes it: "Hourly", "Morning",
// "Evening | Morning", "S3".
func (s Slot) String() string {
	if n, ok := names[s]; ok {
		return n
	}
	var parts []string
	for hour := 0; hour < 24; hour++ {
		h := Slot(1) << hour
		if s&h == 0 {
			continue
		}
		if n, ok := names[h]; ok {
			parts = append(parts, n)
		} else {
			parts = append(parts, fmt.Sprintf("S%d", hour))
		}
	}
	return strings.Join(parts, " | ")
}

// Job is one workflow in one repository. The scheduler dispatches it on `main` in each of
// its slots.
type Job struct {
	Repo  string // "katoptra/<name>"
	File  string // the name of the workflow file, for example "sync.yml"
	Slots Slot
}

// ID is the key of the job in the state file: "katoptra/ctan/sync.yml".
func (j Job) ID() string { return j.Repo + "/" + j.File }

var jobs []Job

func register(j ...Job) bool {
	jobs = append(jobs, j...)
	return true
}

// Jobs gives all jobs that register added, in a new slice.
func Jobs() []Job { return append([]Job(nil), jobs...) }
