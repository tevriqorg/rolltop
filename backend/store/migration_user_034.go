package store

func userMailAIConversationScopeMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion034,
		Label:   "user schema 034 mail ai conversation scopes",
		Statements: []string{
			`ALTER TABLE mail_ai_threads ADD COLUMN scope_kind TEXT NOT NULL DEFAULT 'global'`,
			`ALTER TABLE mail_ai_threads ADD COLUMN scope_key TEXT NOT NULL DEFAULT 'global'`,
			`ALTER TABLE mail_ai_threads ADD COLUMN context_json TEXT NOT NULL DEFAULT '{}'`,
			`CREATE INDEX idx_mail_ai_threads_user_scope_updated
 ON mail_ai_threads(user_id, scope_kind, scope_key, updated_at DESC, id DESC)`,
		},
	}
}
