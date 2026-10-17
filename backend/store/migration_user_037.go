package store

func userProviderOutboxMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion037,
		Label:   "user schema 037 provider outbox",
		Statements: []string{
			`CREATE TABLE provider_outbox_jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL,
 provider TEXT NOT NULL,
 source_id TEXT NOT NULL,
 submission_key TEXT NOT NULL,
 subject TEXT NOT NULL DEFAULT '',
 thread_id TEXT NOT NULL DEFAULT '',
 blob_id INTEGER NOT NULL REFERENCES blobs(id) ON DELETE RESTRICT,
 raw_sha256 TEXT NOT NULL,
 raw_size INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 native_message_id TEXT NOT NULL DEFAULT '',
 attempt_count INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 acknowledged_at INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(user_id, provider, source_id, submission_key))`,
			`CREATE INDEX idx_provider_outbox_pending ON provider_outbox_jobs(user_id, state, updated_at, id)`,
		},
	}
}
