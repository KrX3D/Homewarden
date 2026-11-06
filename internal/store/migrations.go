package store

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a migration that has shipped; append a new one.
var migrations = []string{
	// 1: settings, roles, users, sessions.
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE roles (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	builtin     INTEGER NOT NULL DEFAULT 0,
	permissions TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	role_id       INTEGER NOT NULL REFERENCES roles(id),
	totp_secret   TEXT NOT NULL DEFAULT '',
	totp_enabled  INTEGER NOT NULL DEFAULT 0,
	disabled      INTEGER NOT NULL DEFAULT 0,
	prefs         TEXT NOT NULL DEFAULT '{}',
	created_at    INTEGER NOT NULL,
	last_login    INTEGER
);

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);

INSERT INTO roles(name, builtin, permissions) VALUES
	('Admin', 1, '["*"]'),
	('Operator', 1, '["devices.view","devices.manage","updates.run","backups.run","jobs.view"]'),
	('Viewer', 1, '["devices.view","jobs.view"]');
`,
	// 2: devices and their presence history.
	`
CREATE TABLE devices (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL,
	kind        TEXT NOT NULL DEFAULT 'generic',
	host        TEXT NOT NULL DEFAULT '',
	mac         TEXT NOT NULL DEFAULT '',
	parent_id   INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	group_name  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'active',
	source      TEXT NOT NULL DEFAULT 'manual',
	external_id TEXT NOT NULL DEFAULT '',
	presence    TEXT NOT NULL DEFAULT '{}',
	config      TEXT NOT NULL DEFAULT '{}',
	state       TEXT NOT NULL DEFAULT 'unknown',
	state_since INTEGER,
	last_seen   INTEGER,
	rtt_ms      REAL,
	notes       TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL
);
CREATE INDEX devices_status ON devices(status);
CREATE UNIQUE INDEX devices_source_ext ON devices(source, external_id) WHERE external_id <> '';

CREATE TABLE presence_events (
	id        INTEGER PRIMARY KEY,
	device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	at        INTEGER NOT NULL,
	state     TEXT NOT NULL
);
CREATE INDEX presence_events_device ON presence_events(device_id, at);
`,
	// 3: credentials vault and pinned SSH host keys.
	`
CREATE TABLE credentials (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	type       TEXT NOT NULL,
	username   TEXT NOT NULL DEFAULT '',
	secret     TEXT NOT NULL,
	public     TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE known_hosts (
	address     TEXT PRIMARY KEY,
	key_type    TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	key         TEXT NOT NULL,
	first_seen  INTEGER NOT NULL
);
`,
	// 4: installed integrations, scan results, version history and jobs.
	`
CREATE TABLE integrations (
	id           TEXT PRIMARY KEY,
	version      TEXT NOT NULL,
	source       TEXT NOT NULL,
	enabled      INTEGER NOT NULL DEFAULT 1,
	installed_at INTEGER NOT NULL
);

CREATE TABLE components (
	device_id        INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	component_id     TEXT NOT NULL,
	name             TEXT NOT NULL,
	grp              TEXT NOT NULL DEFAULT '',
	installed        TEXT NOT NULL DEFAULT '',
	available        TEXT NOT NULL DEFAULT '',
	update_available INTEGER NOT NULL DEFAULT 0,
	security         INTEGER NOT NULL DEFAULT 0,
	updatable        INTEGER NOT NULL DEFAULT 1,
	meta             TEXT NOT NULL DEFAULT '{}',
	updated_at       INTEGER NOT NULL,
	PRIMARY KEY (device_id, component_id)
);

CREATE TABLE version_history (
	id           INTEGER PRIMARY KEY,
	device_id    INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	component_id TEXT NOT NULL,
	name         TEXT NOT NULL,
	old_version  TEXT NOT NULL,
	new_version  TEXT NOT NULL,
	at           INTEGER NOT NULL
);
CREATE INDEX version_history_device ON version_history(device_id, at);

CREATE TABLE device_facts (
	device_id  INTEGER PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
	facts      TEXT NOT NULL DEFAULT '{}',
	scanned_at INTEGER NOT NULL,
	error      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE jobs (
	id          INTEGER PRIMARY KEY,
	type        TEXT NOT NULL,
	device_id   INTEGER REFERENCES devices(id) ON DELETE SET NULL,
	device_name TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	params      TEXT NOT NULL DEFAULT '{}',
	status      TEXT NOT NULL,
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	started_at  INTEGER,
	finished_at INTEGER,
	log         TEXT NOT NULL DEFAULT '',
	result      TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX jobs_created ON jobs(created_at);
CREATE INDEX jobs_device ON jobs(device_id, created_at);
`,
	// 5: backup targets and the backups stored on them.
	`
CREATE TABLE backup_targets (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	type       TEXT NOT NULL,
	config     TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL
);

CREATE TABLE backups (
	id        INTEGER PRIMARY KEY,
	device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	target_id INTEGER REFERENCES backup_targets(id) ON DELETE SET NULL,
	name      TEXT NOT NULL,
	path      TEXT NOT NULL,
	size      INTEGER NOT NULL DEFAULT 0,
	at        INTEGER NOT NULL,
	job_id    INTEGER
);
CREATE INDEX backups_device ON backups(device_id, at);
`,
	// 6: API tokens and actions waiting for a device to come online.
	`
CREATE TABLE api_tokens (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_used  INTEGER
);

CREATE TABLE pending_actions (
	device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	action    TEXT NOT NULL,
	params    TEXT NOT NULL DEFAULT '{}',
	since     INTEGER NOT NULL,
	PRIMARY KEY (device_id, action)
);
`,
}
