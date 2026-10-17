# AGENTS.md

## Authority And Scope

Rolltop is a Go, SQLite, Bleve, local-blob email mirror with provider adapters and the Mail AI workspace.

For Mail Hub / Personal Mail Entry work, **fresh-read `tevriqorg/infra#39` before implementation**. That Issue is the durable requirement and real-provider gate authority. Chat history, old Issue comments, stale branches, and this file do not override it.

`main` plus current tests are the implementation truth. If repository guidance conflicts with accepted requirements, update the durable authority/guidance first rather than silently choosing one.

## Data And Security Rules

- Keep all user-owned data scoped by `user_id` at every layer: SQLite rows, blob paths, search documents, sync runs, AI threads/workspace items, and HTTP reads.
- Do not accept `user_id` from normal browser routes. Admin routes may manage local users but must not expose another user's mail.
- Do not log app passwords, OAuth refresh/access tokens, session tokens, model API keys, raw message bodies, attachment bodies, or Mail AI private content.
- Keep IMAP credentials and provider refresh tokens encrypted with server-side key material.
- Keep tests for tenant isolation current when changing sync, search, message, attachment, blob, provider, AI, or route behavior.
- Preserve Evidence independently from mutable provider/Mirror state unless the governing requirement explicitly changes that retention model.

## Provider And Mutation Rules

- Provider mutations are supported capabilities; do **not** reintroduce the old blanket ban on archive/move/trash/restore/labels/read/star operations.
- Route provider-backed actions through the provider capability/API. Do not replace provider APIs with DOM automation.
- Preserve the risk-tiered approval policy: reads are automatic; high-impact external actions such as send/reply/forward/trash require the approval behavior defined by the current authority.
- Keep sync incremental where the provider supports it, persist provider cursors, and make cursor expiry/recovery explicit and duplicate-safe.
- **Real Google/Gmail remains gated by `tevriqorg/infra#39`.** Do not configure real Google OAuth credentials, call real Gmail APIs, unlock backfill, or perform real-provider mutations until the Issue authorizes the corresponding C1/C2 stage.

## Build And Deployment Governance

This is a public repository. Deployable runtime artifacts must be built by GitHub Actions, not on deployment hosts.

- The canonical bare-metal deploy artifact is produced by `.github/workflows/runtime-artifacts.yml`.
- A deployment host such as Vela may download, verify, stage, deploy, restart, inspect logs, query health, and run acceptance checks. It must **not** run release `go build`, `go build -buildmode=plugin`, `npm build`, or `npm run build:plugins` to manufacture deployable production files.
- Core binary and all Go backend plugins must come from the **same Actions artifact/toolchain** to avoid Go plugin ABI drift.
- Backend plugins and plugin frontend outputs must be auto-discovered by the workflow; do not maintain a hand-written deployment list that can silently omit a plugin.
- Before deployment, verify `PROVENANCE.txt`, canonical commit/run identity, and `SHA256SUMS`. After deployment, verify live files against the artifact and run restart/data-stability acceptance.
- Keep one known-good rollback snapshot. Remove transient downloads/staging directories after acceptance so the runtime host does not accumulate disposable build material.
- Full operational rules are in [`docs/runtime-deployment-governance.md`](docs/runtime-deployment-governance.md).

## Checks

For implementation changes, keep the relevant local test suites current. GitHub Actions is the final build/release gate.

```sh
go test ./...
npm test
```

`npm test` covers the frontend offline modules (search ranking, IndexedDB caches, outbox policy) via Vitest; keep those suites current when changing `frontend/src/lib/offline*`.

For deployment-bound changes, both the normal CI and **Runtime Artifacts** workflow must be green on the exact commit that will be deployed.