# dispatch

Starts the katoptra mirrors' workflows on UTC slots, from a systemd timer on a NixOS host.
`README.md` is for users; this file is the design.

## The files

- `schedules/` -- `Slot` (a bitmask of UTC hours, each at `HH:42`), the named aliases,
  `Latest`, `Job`, and one file per katoptra repo registering its jobs. Routine changes
  touch only this directory.
- `tick.go` -- the state file, `Plan`, and `Tick`: lock, load, plan, write ahead, dispatch.
- `github.go` -- App JWT, the org's installation, installation token, `workflow_dispatch`,
  retries. No SDK.
- `main.go` -- flags, credentials from `$CREDENTIALS_DIRECTORY`, the healthcheck ping, the
  exit code.
- `module.nix` -- the timer and the hardened oneshot. `flake.nix` -- package, module, checks.
- `Taskfile.yml`, `Dockerfile` -- every Go command runs in the toolbox image.

## Constraints

- It dispatches. It does not run work or watch outcomes; each mirror pings its own
  healthcheck.
- katoptra repositories only; the App is installed on the `katoptra` org alone.
- Standard library only (`vendorHash = null`). Go 1.26, matching nixos-26.05's
  `buildGoModule`; the toolbox image pins the same.
- UTC throughout. No DST handling anywhere.
- No secrets in the repo, the store, or the state file. The module takes file paths.

## Must knows

- **At most once, by writing ahead.** `Tick` records every due slot in `state.json` before
  the first POST. A crash or a failed dispatch loses that slot; nothing retries it, the next
  slot is the retry. Moving the write after the dispatch turns this into at-least-once, and
  `TestSlotIsRecordedBeforeTheDispatch` fails.
- **The token comes before the write.** `Dispatcher.Prepare` mints the installation token
  before anything is recorded, since minting starts no run. A GitHub or network outage there
  leaves the slots unrecorded and the next tick tries again, instead of losing a daily
  mirror's day. `TestPrepareFailureRecordsNothing` holds it.
- **Catch-up is `Plan`, not systemd.** The timer has no `Persistent=`. Every tick compares
  each job's latest slot with its recorded one, so any number of missed slots fire once.
  A timer per slot with `Persistent=true` would fire each missed slot instead.
- **A corrupt `state.json` stops everything.** It is never treated as empty: empty state
  fires every job for slots already sent. Repair or delete it by hand; deleting fires each
  job once.
- **A new job fires at the next tick**, for its latest slot, because it has no entry.
- **The timer is `*:02/5 UTC`.** It starts at :02 so `:42` is a tick; `UTC` because the
  jgrid.net hosts run in `America/Los_Angeles`. Changing `schedules.Minute` means changing
  the timer too.
- **Every dispatch is `ref: main`, empty inputs.** A repo whose default branch is not
  `main` answers 404, which is fatal.
- **Retries (10 s, 20 s, 40 s) depend on whether a repeat is harmless.** A 4xx other than
  408/429 is never retried. The two mint requests retry any 5xx, 408, 429 or network error.
  A dispatch POST retries only 408, 429 and a connection that never opened: a 5xx or a
  timeout can follow a dispatch GitHub already accepted, and a retry would be a second run.
- **GitHub gets two minutes a tick** (`githubBudget`), retries included; a retry that would
  pass the deadline is not made. The rest of `TimeoutStartSec=4min` is the ping's, so a
  failing tick still reaches `/fail`.
- **The binary is renamed in `postInstall`.** `buildGoModule` names it after the module
  path's last element, `dispatch`; the unit runs `katoptra-dispatch`. The `module` check
  fails if `ExecStart` is not an executable.
- **Journal priorities** (`<3>`, `<4>`) are written only when `JOURNAL_STREAM` is set, so
  a hand run prints plain lines.

## Verifying a change

`task check` is what CI runs: gofmt, go vet, go test, in the toolbox image.

`nix flake check` builds the package (its tests run in `checkPhase`) and renders the two
units from a minimal system. Without nix on the laptop:

    container run --rm --memory 4G -v "$PWD":/work -w /work nixos/nix sh -c \
      'git config --global --add safe.directory /work &&
       NIX_CONFIG="experimental-features = nix-command flakes" nix flake check'

Files must be `git add`ed for the flake to see them. Only a real tick proves the GitHub App
contract: on the host, `sudo systemctl start katoptra-dispatch` and read the journal.
