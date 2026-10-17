# Runtime Deployment Governance

This document defines the canonical build, deployment, rollback, and acceptance path for Rolltop runtime hosts such as Vela.

## Authority

For Mail Hub / Personal Mail Entry product scope and real-provider gates, `tevriqorg/infra#39` is the durable authority. This document governs **how** repository runtime artifacts are produced and deployed; it does not authorize real Gmail OAuth, Gmail API access, or backfill.

## Build Provenance

Deployable production artifacts for this public repository are built only in GitHub Actions.

Canonical workflow:

```text
.github/workflows/runtime-artifacts.yml
```

The workflow must:

1. check out the exact repository commit;
2. use the toolchain declared by the repository (`go.mod` for Go and workflow-pinned Node setup);
3. build the Rolltop Linux binary;
4. auto-discover and build every `plugins/*/backend` Go plugin;
5. build the main frontend and plugin frontends;
6. package frontend-only plugins as well as plugins that also have a backend;
7. emit `PROVENANCE.txt` with repository, commit, Actions run, toolchain, and discovered plugin sets;
8. emit `SHA256SUMS` and verify it before upload;
9. upload one deployable `rolltop-runtime-linux-amd64` artifact.

A hard-coded plugin deployment list is not acceptable because it can drift when a new plugin is added.

## Deployment Host Rules

A deployment host may:

- download the GitHub Actions artifact;
- verify provenance and hashes;
- create a rollback snapshot;
- stage and atomically replace runtime files;
- restart managed services;
- run health, log, SQLite count, and synthetic acceptance checks.

A deployment host must not manufacture production artifacts. In particular, do not run release:

```text
go build
go build -buildmode=plugin
npm run build
npm run build:plugins
```

on Vela or another production/test deployment host for files that will be treated as the deployed release.

Development compilation is not a substitute for release provenance. If an emergency local build is ever used for diagnosis, it must not be promoted as the canonical deployment; restore the host from an Actions artifact before acceptance is closed.

## Pre-Deployment Verification

Before replacing live files, verify all of the following:

- the artifact comes from `tevriqorg/rolltop`;
- `PROVENANCE.txt` commit equals the canonical commit intended for deployment;
- the referenced Actions run completed successfully;
- `sha256sum -c SHA256SUMS` passes for every packaged file;
- the backend plugin count equals the workflow-discovered backend plugin set;
- frontend-only plugin outputs are present when the repository build creates them;
- the runtime host has enough free space for staging plus one rollback snapshot.

Do not deploy a PR merge-ref artifact as the final canonical runtime. After merge, use the artifact produced by the canonical `main` push run.

## Go Plugin ABI Rule

Rolltop and all `.so` backend plugins must be built by the same Actions workflow and Go toolchain. Never mix a core binary from one build with plugins from another host/toolchain. This rule exists to prevent Go plugin ABI mismatch and silent runtime drift.

## Safe Deployment

1. Confirm the current services are healthy and record a small pre-deploy data baseline.
2. Keep one known-good rollback snapshot outside the Git worktree.
3. Stage the Actions artifact without altering live files.
4. Verify staged files against the artifact.
5. Replace the core binary, backend plugins, main frontend, and plugin frontend outputs as one release set.
6. Restart the managed services.
7. If startup or ABI checks fail, restore the rollback snapshot instead of rebuilding on the host.

Private Mail Hub databases, blobs, Evidence, model/provider secrets, OAuth material, and user mail are never part of a public runtime artifact.

## Acceptance

A deployment is not complete until the exact canonical runtime passes:

- Rolltop startup/ready health;
- Mail AI health or documented safe degradation;
- synthetic source health when that fixture is part of the environment;
- no `panic`, `fatal`, migration failure, or Go plugin ABI mismatch in recent logs;
- stable message/provider/Evidence/outbox counts across a restart when no test intentionally changes them;
- live runtime files match the verified Actions artifact;
- repository worktree is canonical `main` and clean;
- real-provider guardrails remain unchanged unless the durable authority explicitly advanced them.

## Cleanup And Rollback Retention

After successful acceptance:

- remove transient Actions ZIP/tar extraction directories and staging copies;
- remove superseded deployment-only binary backups when a newer verified rollback snapshot exists;
- retain private data backups separately from disposable build artifacts;
- keep one clearly identified known-good rollback snapshot for the current release boundary;
- delete merged short-lived corrective branches; do not delete unmerged branches merely because they look old—reconcile them first.

The goal is a runtime host that contains live files, private data, logs, and one deliberate rollback path—not a second build farm.