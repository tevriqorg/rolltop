package store

func userEvidenceMigrationSet() migrationSet {
	return migrationSet{Scope: "user", Version: UserSchemaVersion031, Label: "user schema 031 mail evidence", Statements: []string{
		`CREATE TABLE evidence_observations (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 observation_key TEXT NOT NULL COLLATE BINARY,
 provider TEXT NOT NULL,
 source_account TEXT NOT NULL,
 provider_message_id TEXT NOT NULL,
 thread_id TEXT NOT NULL DEFAULT '',
 folders_json TEXT NOT NULL DEFAULT '[]',
 labels_json TEXT NOT NULL DEFAULT '[]',
 imap_uid INTEGER,
 imap_uid_validity INTEGER,
 message_id_header TEXT NOT NULL DEFAULT '',
 first_observed_at INTEGER NOT NULL,
 source_size INTEGER NOT NULL DEFAULT 0,
 raw_sha256 TEXT NOT NULL DEFAULT '' COLLATE BINARY,
 raw_path TEXT NOT NULL DEFAULT '',
 raw_size INTEGER NOT NULL DEFAULT 0,
 capture_status TEXT NOT NULL,
 mirror_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
 last_error TEXT NOT NULL DEFAULT '',
 UNIQUE(user_id, observation_key))`,
		`CREATE INDEX idx_evidence_observations_source
 ON evidence_observations(user_id, provider, source_account, provider_message_id, id)`,
		`CREATE INDEX idx_evidence_observations_raw
 ON evidence_observations(user_id, raw_sha256, id) WHERE raw_sha256 <> ''`,
		`CREATE TABLE evidence_outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 observation_id INTEGER NOT NULL REFERENCES evidence_observations(id) ON DELETE CASCADE,
 event_key TEXT NOT NULL COLLATE BINARY,
 event_type TEXT NOT NULL,
 payload TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 available_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 delivered_at INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '',
 UNIQUE(user_id, event_key))`,
		`CREATE INDEX idx_evidence_outbox_pending
 ON evidence_outbox(user_id, delivered_at, available_at, id)`,
	}}
}
