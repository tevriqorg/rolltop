package store

func userAccountConnectorMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion032,
		Label:   "user schema 032 account connectors",
		Statements: []string{
			`CREATE TABLE account_connections (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 provider TEXT NOT NULL,
 external_subject TEXT NOT NULL COLLATE BINARY,
 email TEXT NOT NULL COLLATE NOCASE,
 display_name TEXT NOT NULL DEFAULT '',
 encrypted_refresh_token TEXT NOT NULL,
 scopes_json TEXT NOT NULL DEFAULT '[]',
 capabilities_json TEXT NOT NULL DEFAULT '[]',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(user_id, provider, external_subject))`,
			`CREATE INDEX idx_account_connections_user_provider
 ON account_connections(user_id, provider, id)`,
		},
	}
}
