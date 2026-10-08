// katoptra-dispatch is one tick of the katoptra scheduler. It finds each job with a latest
// slot that the state does not hold. It records that slot first, and then it starts the job.
// At the end, it sends a ping to the healthcheck of the scheduler. A systemd timer starts it
// at intervals of five minutes (refer to module.nix).
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/katoptra/dispatch/schedules"
)

// pingLimit is the maximum length of the error text in a /fail ping. healthchecks.io keeps
// 100 kB.
const pingLimit = 10 << 10

// githubBudget is the maximum time for all GitHub requests in a tick, with the retries
// included. Thus, the ping (a maximum of 3 x 30 s + 4 s) can complete in the
// TimeoutStartSec=4min of the unit.
const githubBudget = 2 * time.Minute

func main() {
	dryRun := flag.Bool("dry-run", false, "print what is due and why; write, dispatch and ping nothing")
	list := flag.Bool("list", false, "print every job, its slots and their UTC times, as a table")
	flag.Parse()
	if *list {
		printJobs(os.Stdout)
		return
	}
	log := &Log{Out: os.Stdout, Err: os.Stderr, Journal: os.Getenv("JOURNAL_STREAM") != ""}
	os.Exit(run(*dryRun, log))
}

// printJobs prints the table that `task targets` shows. `task runs` reads its first column.
func printJobs(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSLOT\tUTC")
	for _, j := range schedules.Jobs() {
		times := strings.Join(j.Slots.Times(), ", ")
		if j.Slots == schedules.Hourly {
			times = fmt.Sprintf("every hour at :%02d", schedules.Minute)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", j.ID(), j.Slots, times)
	}
	tw.Flush()
}

func run(dryRun bool, log *Log) int {
	ctx, cancel := context.WithTimeout(context.Background(), githubBudget)
	defer cancel()
	dir := os.Getenv("STATE_DIRECTORY")
	if dir == "" {
		log.Error("STATE_DIRECTORY is not set: run under systemd, or set it to the state directory")
		return 1
	}
	if dryRun {
		Tick(ctx, dir, schedules.Jobs(), time.Now(), nil, true, log)
		return exitCode(log)
	}

	creds := os.Getenv("CREDENTIALS_DIRECTORY")
	url, err := credential(creds, "healthcheck-url")
	if err != nil {
		log.Error("%v; cannot ping, so this goes to the journal alone", err)
		return 1
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if gh, err := newGitHub(creds, client); err != nil {
		log.Error("%v", err)
	} else {
		Tick(ctx, dir, schedules.Jobs(), time.Now(), gh, false, log)
	}
	if err := ping(client, url, log.Errors(), time.Sleep); err != nil {
		log.Warn("healthcheck ping did not get through: %v", err)
	}
	return exitCode(log)
}

func exitCode(log *Log) int {
	if len(log.Errors()) > 0 {
		return 1
	}
	return 0
}

func newGitHub(creds string, client *http.Client) (*GitHub, error) {
	appID, err := credential(creds, "app-id")
	if err != nil {
		return nil, err
	}
	pem, err := credential(creds, "private-key")
	if err != nil {
		return nil, err
	}
	key, err := ParseKey([]byte(pem))
	if err != nil {
		return nil, err
	}
	return &GitHub{
		API: "https://api.github.com", AppID: appID, Org: "katoptra", Key: key,
		HTTP: client, Sleep: time.Sleep, Now: time.Now,
	}, nil
}

// credential reads one file that systemd put in $CREDENTIALS_DIRECTORY with LoadCredential=.
func credential(dir, name string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("credential %s: CREDENTIALS_DIRECTORY is not set", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("credential %s: %w", name, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("credential %s is empty", name)
	}
	return v, nil
}

// ping sends the result of the tick to healthchecks.io. If the tick has no errors, ping
// sends to the URL. If not, it sends to /fail, with the errors as the body. It sends the
// request a maximum of three times. If the ping does not get to healthchecks.io, the caller
// records a warning, not an error. The URL is the credential of the check. Thus, no error
// from ping contains the URL.
func ping(client *http.Client, url string, errs []string, sleep func(time.Duration)) error {
	body := strings.Join(errs, "\n")
	if len(errs) > 0 {
		url = strings.TrimRight(url, "/") + "/fail"
	}
	if len(body) > pingLimit {
		body = body[:pingLimit]
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			sleep(2 * time.Second)
		}
		var res *http.Response
		res, err = client.Post(url, "text/plain", bytes.NewReader([]byte(body)))
		if ue, ok := err.(*neturl.Error); ok {
			err = ue.Err // *url.Error prints the URL
		}
		if err != nil {
			continue
		}
		res.Body.Close()
		if res.StatusCode/100 == 2 {
			return nil
		}
		err = fmt.Errorf("healthchecks.io answered %d", res.StatusCode)
	}
	return err
}
