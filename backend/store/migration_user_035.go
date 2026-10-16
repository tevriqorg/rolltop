package store

func userMailAIWorkspaceMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion035,
		Label:   "user schema 035 mail ai workspace",
		Statements: []string{
			`CREATE TABLE mail_ai_workspace_items (
 user_id INTEGER NOT NULL,
 id TEXT NOT NULL,
 kind TEXT NOT NULL,
 title TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '',
 message_id INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'open',
 due_at TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(user_id, id))`,
			`CREATE INDEX idx_mail_ai_workspace_user_kind_updated
 ON mail_ai_workspace_items(user_id, kind, status, updated_at DESC, id DESC)`,
		},
	}
}
