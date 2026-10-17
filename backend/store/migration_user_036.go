package store

func userProviderSyncGateMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion036,
		Label:   "user schema 036 provider sync gates",
		Statements: []string{
			`CREATE TABLE provider_sync_gates (
 user_id INTEGER NOT NULL,
 provider TEXT NOT NULL,
 source_id TEXT NOT NULL,
 census_message_count INTEGER NOT NULL DEFAULT 0,
 census_estimated_raw_bytes INTEGER NOT NULL DEFAULT 0,
 census_expected_evidence_bytes INTEGER NOT NULL DEFAULT 0,
 census_recommended_free_disk INTEGER NOT NULL DEFAULT 0,
 census_method TEXT NOT NULL DEFAULT '',
 census_exact INTEGER NOT NULL DEFAULT 0,
 census_at INTEGER NOT NULL DEFAULT 0,
 backfill_approved_at INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(user_id, provider, source_id))`,
			`CREATE INDEX idx_provider_sync_gates_user_provider ON provider_sync_gates(user_id, provider, updated_at DESC)`,
		},
	}
}
