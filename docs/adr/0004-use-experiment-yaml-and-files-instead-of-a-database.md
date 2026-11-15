---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Use experiment.yaml and files instead of a database

## Context and Problem Statement

Breadboard needs to store each experiment's intent (services, apps, connections, TLS overrides), generated state (secrets, certificates, runtime compose files) and install-level settings (domain, CA). It must also version these formats and migrate them as Breadboard evolves. Where does this data live, and what is the source of truth?

## Decision Drivers

* Experiment intent must be reviewable and committable alongside the user's code.
* Secrets must never be committed.
* Formats must carry versions and upgrade forward; an older `bb` must refuse to write newer state.
* The CLI and a running web UI must not change one experiment at the same time.

## Considered Options

* `experiment.yaml` as the source of truth, with versioned state files in `BB_HOME` and the project's `.bb/` directory
* A SQLite database in `BB_HOME` as the source of truth
* Everything, including secrets, in `experiment.yaml`

## Decision Outcome

Chosen option: "`experiment.yaml` as the source of truth, with versioned state files in `BB_HOME` and the project's `.bb/` directory", because it keeps intent in a committable file, keeps secrets out of git without editing the user's `.gitignore`, and needs no database once Breadboard runs only on the host (ADR-0001).

`BB_HOME` holds `config.yaml` (loaded through bkit `settings`) and the CA. Each project's `.bb/` directory holds secrets, leaf certificates, runtime files and a lock file; it contains its own `.gitignore` with `*`. Writes are atomic, state-changing operations take an `flock`, every file carries a format version, and `bb migrate` upgrades `experiment.yaml` only on request.

### Consequences

* Good, because experiments are plain files that can be diffed, reviewed and shared.
* Good, because deleting `.bb/` after `bb down --volumes` cleanly resets an experiment.
* Good, because there is no database driver or migration runner to maintain.
* Bad, because cross-experiment queries (list every experiment on the machine) require scanning or a registry file.
* Bad, because each file format needs its own upgrade path and fixtures.

### Confirmation

Phase 1 tests prove atomic writes, refusal of a newer `formatVersion`, `ErrBusy` for a second lock holder, and comment-preserving edits to `experiment.yaml`. `Init` is tested to create `.bb/` at `0700` with secrets and keys at `0600`.

## Pros and Cons of the Options

### `experiment.yaml` as the source of truth, with versioned state files

* Good, because intent is human-readable and committable.
* Good, because secrets and certificates stay outside git automatically.
* Bad, because several small formats must each be versioned.

### A SQLite database in `BB_HOME` as the source of truth

* Good, because one schema with migrations covers every kind of state.
* Good, because bkit `rdb` and its SQLite driver were available.
* Bad, because intent would no longer be a committable file.
* Bad, because it was chosen while the UI ran in a container, where SQLite locking across Docker Desktop's file sharing is unreliable and WAL mode cannot work.

### Everything, including secrets, in `experiment.yaml`

* Good, because there is exactly one file.
* Bad, because secrets would be committed with the user's code.

## More Information

Recorded as D-025 and D-026 in `docs/design/decisions.md`; the superseded SQLite decisions are D-020 to D-022. File layouts are in `docs/design/architecture.md`.
