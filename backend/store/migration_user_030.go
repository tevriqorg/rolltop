package store

func userProviderHubMigrationSet() migrationSet {
	return migrationSet{Scope: "user", Version: UserSchemaVersion030, Label: "user schema 030 provider hub", Statements: []string{
		`CREATE TABLE provider_sources (
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 provider TEXT NOT NULL, source_id TEXT NOT NULL,
 account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
 mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
 cursor TEXT NOT NULL DEFAULT '', PRIMARY KEY(user_id,provider,source_id), UNIQUE(user_id,account_id))`,
		`CREATE TABLE provider_messages (
 user_id INTEGER NOT NULL, provider TEXT NOT NULL, source_id TEXT NOT NULL, native_id TEXT NOT NULL,
 message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL, folders TEXT NOT NULL, labels TEXT NOT NULL, oversized INTEGER NOT NULL,
 PRIMARY KEY(user_id,provider,source_id,native_id), UNIQUE(user_id,message_id),
 FOREIGN KEY(user_id,provider,source_id) REFERENCES provider_sources(user_id,provider,source_id) ON DELETE CASCADE)`,
		`CREATE TABLE provider_mutations (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, provider TEXT NOT NULL, source_id TEXT NOT NULL,
 mutation TEXT NOT NULL,
 FOREIGN KEY(user_id,provider,source_id) REFERENCES provider_sources(user_id,provider,source_id) ON DELETE CASCADE)`,
	}}
}
