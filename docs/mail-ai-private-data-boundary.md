# Mail AI Private Data Boundary

Mail AI treats real mail and AI interaction history as private runtime data. Public repository fixtures may contain synthetic test data only; real message content, user prompts, assistant-visible history, tool results, and approval history must not become repository content.

## Storage boundary

Mail AI conversation state is stored server-side in the authenticated user's Rolltop data store. The configured runtime data directory is deployment state, not source code, and must remain outside the public source tree or under ignored runtime paths.

Browser memory may cache the currently rendered conversation, but it is not the persistence authority. Private conversation state must not rely on `localStorage` as its only source of truth.

## Logging boundary

Normal Rolltop and Mail AI logs may record operational metadata such as endpoint path, internal IDs, error type, safe status/code, health, and lifecycle events. They must not log raw prompts, message bodies, tool results, approval payloads, provider response bodies, or model exception text that may contain those values.
## Repository and CI guard

`.gitignore` excludes common runtime databases, logs, raw mail files, environment files, and deployment working directories. `npm run privacy:check` additionally inspects the tracked file set and fails CI if common private-runtime artifacts become tracked.

The same CI check guards the Mail AI bridge and sidecar against known unsafe logging patterns, including raw exception logging and raw error-message responses.

Synthetic fixtures are allowed to remain public when they are deliberately authored test data. If a future synthetic fixture needs a normally blocked raw-mail format, it must use an explicit narrow allow-list rather than weakening the private-data boundary globally.

## Operational rule

Debug exports, database copies, real-provider samples, and incident captures containing private mail or AI state stay in private runtime or the private infrastructure authority. They are never added to the public fork merely to make diagnosis easier.

When a production error needs investigation, prefer reproducible synthetic input and metadata-only diagnostics. Any temporary private capture must remain outside Git and normal logs and must be removed when no longer needed.
