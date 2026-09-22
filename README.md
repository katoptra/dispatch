<p align="center">
  <a href="https://github.com/katoptra">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://katoptra.org/brand/katoptra-mark-dark-224.png">
      <img src="https://katoptra.org/brand/katoptra-mark-224.png" alt="Katoptra" width="112">
    </picture>
  </a>
</p>

<h1 align="center">dispatch</h1>

<p align="center">The scheduler that starts every katoptra mirror.</p>

<p align="center">
  <a href="https://github.com/katoptra/dispatch/actions/workflows/check.yml"><img src="https://github.com/katoptra/dispatch/actions/workflows/check.yml/badge.svg" alt="check"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/katoptra/dispatch" alt="license"></a>
  <a href="https://github.com/katoptra/dispatch#how-it-works"><img src="https://healthchecks.io/b/2/254c8ab8-5b1c-40e5-ae69-f34413b6b053.svg" alt="tick"></a>
</p>

No mirror schedules itself; each waits
for a `workflow_dispatch`, and this repository sends it. A systemd timer on a NixOS host
ticks every five minutes, fires each job whose latest slot has not been fired yet, then
pings one healthcheck. A host that was down fires each missed job once, for the latest
of its slots, when it comes back. No slot fires twice. It is one Go binary with no
dependency outside the standard library, and it runs on a one-core VPS.

## How to use

A job is one workflow in one katoptra repository and the UTC hours it runs at. Every
slot fires at `HH:42`:

| Slot | UTC | Pacific, winter |
|---|---|---|
| `Hourly` | :42 | :42 |
| `Evening` | 05:42 | 21:42 |
| `Overnight` | 11:42 | 03:42 |
| `Morning` | 17:42 | 09:42 |
| `Afternoon` | 23:42 | 15:42 |

`S0` through `S23` name every hour; the four daily names are aliases for `S5`, `S11`,
`S17` and `S23`. Each repository gets one file in [`schedules/`](schedules), named for
the half of `owner/name` after the slash:

```go
// schedules/ctan.go
var _ = register(Job{Repo: "katoptra/ctan", File: "sync.yml", Slots: Hourly})
```

Two slots are `Morning | Evening`. A misspelt slot fails to compile.

The workflow must hold up three things this repository cannot check. A workflow that
fails any of them is dispatched into silence:

1. It declares `workflow_dispatch:` in `on:`.
2. It declares a `concurrency` group with `cancel-in-progress: false`, so a dispatch that
   arrives during a run queues instead of doubling up.
3. It pings its own healthcheck. The scheduler never learns whether a run passed.

A change reaches the host when the host's flake lock moves to the new commit. A new job
fires at the next tick, for its latest slot.

## How it works

Every five minutes, at `:02`, `:07` and on through `:57`, the timer starts one oneshot
tick:

```mermaid
flowchart LR
  timer["systemd timer<br/>*:02/5 UTC"] --> lock --> load["load state.json"] --> plan
  plan --> prepare["prepare<br/>mint the App's token"] --> record["record every due slot"] --> dispatch["dispatch<br/>POST workflow_dispatch, ref main"] --> ping["ping the healthcheck"]
  plan -. "nothing due" .-> ping
  prepare -. "GitHub is down: nothing recorded" .-> ping
```

- **Catch-up is `plan`, not systemd.** Each tick compares every job's latest slot with the
  one `state.json` last recorded for it. Any number of missed slots come out as one due
  slot, so a host down for a day fires each job once when it returns.
- **At most once, by writing ahead.** Every due slot is recorded before the first POST.
  A crash or a failed dispatch loses that slot; nothing retries it, and the job's next
  slot is the retry. The token is minted before the write, since minting starts no run,
  so a GitHub outage at that step records nothing and the next tick tries again.
- **Retries are only where a repeat is harmless.** 10 s, 20 s, 40 s. The token requests
  retry any 5xx, 408, 429 or network error. A dispatch retries only 408, 429 and a
  connection that never opened: a 5xx or a timeout can follow a dispatch GitHub already
  accepted, and a retry would be a second run. GitHub gets two minutes a tick, retries
  included, and the rest of the unit's four is the ping's, so a failing tick still
  reaches `/fail`.
- **One healthcheck for the scheduler.** A clean tick pings the URL; a tick with any
  error pings `/fail` with the error text; a dead host pings nothing.

The files, the constraints and the reasoning behind each choice are in
[`CLAUDE.md`](CLAUDE.md).

## Want your own?

### 1. Fork it

Fork [katoptra/dispatch](https://github.com/katoptra/dispatch) and replace the files in
[`schedules/`](schedules) with your own jobs. The package's test refuses a job outside a
`katoptra/` repository; change that prefix to your organization's.

### 2. The GitHub App

1. Create an App owned by your organization: Repository permissions, Actions, Read and
   write, and nothing else; no webhook.
2. Install it on the organization with access to all repositories, so a new repository
   is covered without another step.
3. Note the App ID and generate a private key. The key is used as GitHub issues it; no
   conversion.

### 3. The healthcheck

One healthchecks.io check with a period of 5 minutes and a grace of 10.

### 4. The host

Add this flake as an input and give the module three files. How the host renders them is
its own business; they are read through `LoadCredential=`, so root-owned 0400 files work.

```nix
{
  imports = [ inputs.katoptra-dispatch.nixosModules.default ];
  services.katoptra-dispatch = {
    enable = true;
    appIdFile = "/run/secrets/dispatch-app-id";
    privateKeyFile = "/run/secrets/dispatch-private-key";
    healthcheckUrlFile = "/run/secrets/dispatch-healthcheck-url";
  };
}
```

The paths are strings: a Nix path literal would copy the secret into the store.

### 5. Prove it, run it

On a laptop with go-task and Docker or Apple `container`:

```sh
task check    # gofmt, go vet, go test, inside the toolbox image; what CI runs
task targets  # every job and its UTC slot times, read from schedules/
```

`nix flake check` builds the package and renders the two units; [`CLAUDE.md`](CLAUDE.md)
has the command for a laptop without nix. Only a real tick proves the App: on the host,
`sudo systemctl start katoptra-dispatch` and read the journal.

## Operating it

`task` alone prints the menu. From a laptop:

```sh
task targets           # what runs and when
task runs              # each target's recent runs on GitHub; needs gh logged in
task runs LIMIT=20     # more of them
```

Every run a target shows as `workflow_dispatch` came from here. On the host:

```sh
systemctl list-timers katoptra-dispatch.timer
journalctl -u katoptra-dispatch -n 50
journalctl -u katoptra-dispatch -p err
sudo cat /var/lib/private/katoptra-dispatch/state.json
sudo STATE_DIRECTORY=/var/lib/private/katoptra-dispatch katoptra-dispatch --dry-run
```

`--dry-run` prints what is due and why, and writes, dispatches and pings nothing.

- **The healthcheck goes quiet.** The host or its timer is down. Nothing is lost: when it
  comes back, the next tick fires every job that missed a slot, once.
- **A tick pings `/fail`.** The error text is in the ping's body and in
  `journalctl -u katoptra-dispatch -p err`. The slot it failed on is gone; the job's next
  slot is the retry.
- **A dispatch answers 404.** Every dispatch is to `ref: main`, so the repository's
  default branch is not `main`, or the workflow file is not there.
- **Every tick fails on `state.json`.** A corrupt state file stops everything, since
  empty state would fire every job again. Repair it by hand, or delete it: every job then
  fires once.

## Reference

[`CLAUDE.md`](CLAUDE.md) is the design: the files, the constraints, and what breaks if a
choice is undone. The spec behind it, with the options weighed and rejected, is under
[`docs/superpowers/specs/`](docs/superpowers/specs).

Pull requests are welcome.

MIT licensed. Built by [Josh Vaughen](https://ijosh.com).
