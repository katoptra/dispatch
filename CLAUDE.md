# dispatch

This repository is the scheduler. It starts the workflows of the katoptra mirrors at UTC
slots. A systemd timer on a NixOS host starts it. `README.md` is for users. This file is
the design.

## The files

- `schedules/`: `Slot` (a bitmask of UTC hours, each at `HH:42`), the slot names (`Hourly`,
  `Evening`, `Overnight`, `Morning`, `Afternoon`), `Latest`, `Job`, and one file for each
  katoptra repository. Each of these files adds the jobs of its repository. A usual change
  is only in this directory.
- `tick.go`: the state file, `Plan`, and `Tick`. `Tick` does these steps in this sequence:
  lock, load, plan, prepare, record (the write before the first POST), dispatch.
- `github.go`: the App JWT, the installation of the App on the organization, the
  installation token, `workflow_dispatch`, and the retries. It uses no SDK.
- `main.go`: the flags, the credentials from `$CREDENTIALS_DIRECTORY`, the healthcheck ping,
  and the exit code.
- `module.nix`: the timer, and the oneshot service with its systemd protections.
- `flake.nix`: the package, the module, and the checks.
- `Taskfile.yml` and `Dockerfile`: each Go command operates in the toolbox image.

## Constraints

- The scheduler only dispatches. It does not do the work of a run, and it does not monitor
  the result of a run. Each mirror sends pings to its healthcheck.
- It dispatches workflows only in katoptra repositories. The App is installed only on the
  `katoptra` organization.
- It uses only the Go standard library (`vendorHash = null`).
- It uses Go 1.26, the same version as the `buildGoModule` of nixos-26.05. The toolbox image
  also uses Go 1.26: its `FROM` line holds that version and a digest.
- All times are in UTC. No part of the code adjusts a time for DST.
- The repository, the Nix store and the state file contain no secrets. The module gets file
  paths, not secrets.
- Writing: obey ASD-STE100 and the rules in the
  [Writing section of the org CONTRIBUTING](https://github.com/katoptra/.github/blob/main/CONTRIBUTING.md#writing).
  Read that section before you write.

## Must knows

- **At-most-once, because the write comes first.** Before the first POST, `Tick` records in
  `state.json` the slot of each job that it will dispatch. After a crash or an error in a
  workflow start, that slot gets no run. The scheduler does not try that slot again: the
  next slot is the retry. If you move the write after the POST, a slot can get two runs
  (at-least-once). `TestSlotIsRecordedBeforeTheDispatch` finds that error.
- **The token comes before the write.** `Dispatcher.Prepare` gets the installation token
  before `Tick` records a slot, because a token request starts no run. If GitHub or the
  network is not available at that step, `Tick` records no slot, and the next tick tries
  again. Thus, a daily mirror gets its run for that day.
  `TestPrepareFailureRecordsNothing` makes sure that this sequence does not change.
- **`Plan`, not systemd, finds the slots that had no run.** The timer has no
  `Persistent=`. Each tick compares the latest slot of each job with the slot that the
  state records for it. Thus, a job that had no run for one or more slots gets one run. If
  each slot has its timer with `Persistent=true`, systemd starts a run for each of these
  slots.
- **A corrupted `state.json` stops all jobs.** `LoadState` gives an error for it, not empty
  state, because empty state starts each job again for slots that had a run. Repair or
  delete the file manually. If you delete it, each job starts one time.
- **A new job starts at the next tick**, for its latest slot, because the state has no entry
  for it.
- **The timer is `*:02/5 UTC`.** The timer starts at :02. Thus, `:42` is a tick. The
  calendar has `UTC` because the time zone of the jgrid.net hosts is `America/Los_Angeles`.
  If you change `schedules.Minute`, also change the timer.
- **The scheduler dispatches on `ref: main`, with empty inputs.** If the default branch of
  a repository is not `main`, GitHub sends a 404. The scheduler does not try again, and
  that slot gets no run.
- **A retry (after 10 s, 20 s, 40 s) occurs only when a second request is safe.**
  - A 4xx other than 408 and 429 gets no retry.
  - The two token requests try again after a 5xx, 408, 429, or network error.
  - A workflow start tries again only after a 408, a 429, or a connection that did not
    open. A 5xx or a timeout can come after GitHub accepted the workflow start, and a retry
    then starts a second run.
- **GitHub gets two minutes in each tick** (`githubBudget`), with the retries included. If
  a retry cannot start in the two minutes, the scheduler does not make it. The other two
  minutes of `TimeoutStartSec=4min` are for the ping. Thus, a tick with errors can send its
  ping to `/fail`.
- **`postInstall` changes the name of the binary.** `buildGoModule` gives the binary the
  name of the last part of the module path: `dispatch`. The unit starts
  `katoptra-dispatch`. If `ExecStart` is not an executable, the `module` check stops with
  an error.
- **`Log` writes the journal priorities** (`<3>`, `<4>`) only when `JOURNAL_STREAM` is set.
  Thus, a manual start of the binary prints its lines without these prefixes.

## Verifying a change

CI uses `task check`: gofmt, go vet and go test, in the toolbox image.

`nix flake check` builds the package and makes the two units from a small NixOS system
that has the module. The tests of the package operate in `checkPhase`. If the laptop does
not have nix, use this command:

    container run --rm --memory 4G -v "$PWD":/work -w /work nixos/nix sh -c \
      'git config --global --add safe.directory /work &&
       NIX_CONFIG="experimental-features = nix-command flakes" nix flake check'

The flake uses only the files in the git index. Use `git add` on each new file first.

Only a tick on the host can show that the GitHub App contract is correct. On the host, use
`sudo systemctl start katoptra-dispatch`. Then read the journal.
