package store

func userMailAIConversationMigrationSet() migrationSet {
	return migrationSet{
		Scope:   "user",
		Version: UserSchemaVersion033,
		Label:   "user schema 033 mail ai conversations",
		Statements: []string{
			`CREATE TABLE mail_ai_threads (
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 id TEXT NOT NULL COLLATE BINARY,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(user_id, id))`,
			`CREATE INDEX idx_mail_ai_threads_user_updated
 ON mail_ai_threads(user_id, updated_at DESC, id DESC)`,
			`CREATE TABLE mail_ai_messages (
 user_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL COLLATE BINARY,
 position INTEGER NOT NULL,
 message_id TEXT NOT NULL COLLATE BINARY,
 role TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(user_id, thread_id, position),
 UNIQUE(user_id, thread_id, message_id),
 FOREIGN KEY(user_id, thread_id) REFERENCES mail_ai_threads(user_id, id) ON DELETE CASCADE)`,
			`CREATE INDEX idx_mail_ai_messages_user_thread
 ON mail_ai_messages(user_id, thread_id, position)`,
		},
	}
}
