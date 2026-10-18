package pgmq

const schema = `
CREATE SCHEMA IF NOT EXISTS ocel;

CREATE TABLE IF NOT EXISTS ocel.records (
	purpose    text NOT NULL,
	topic      text NOT NULL,
	key        text NOT NULL,
	value      json,
	expires_at timestamptz NOT NULL,
	PRIMARY KEY (purpose, topic, key)
);
CREATE INDEX IF NOT EXISTS records_by_expiry ON ocel.records (expires_at);

CREATE TABLE IF NOT EXISTS ocel.runs (
	execution    text PRIMARY KEY,
	topic        text NOT NULL,
	consumer     text NOT NULL,
	status       text NOT NULL,
	payload      json,
	output       json,
	error        text NOT NULL DEFAULT '',
	attempts     integer NOT NULL DEFAULT 0,
	max_attempts integer NOT NULL DEFAULT 0,
	tags         text[] NOT NULL DEFAULT '{}',
	metadata     json,
	message_id   text NOT NULL DEFAULT '',
	published_at timestamptz,
	key          text NOT NULL DEFAULT '',
	lane         text NOT NULL DEFAULT '',
	delivery     text NOT NULL DEFAULT '',
	held         text NOT NULL DEFAULT '',
	lease_until  timestamptz,
	created_at   timestamptz NOT NULL,
	due_at       timestamptz,
	started_at   timestamptz,
	finished_at  timestamptz,
	expires_at   timestamptz,
	revision     text NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_by_topic ON ocel.runs (topic, created_at DESC, execution DESC);
CREATE INDEX IF NOT EXISTS runs_by_consumer_status ON ocel.runs (topic, consumer, status);
CREATE INDEX IF NOT EXISTS runs_by_finish ON ocel.runs (finished_at);
CREATE INDEX IF NOT EXISTS unfinished_runs_by_due ON ocel.runs (GREATEST(due_at, created_at)) WHERE finished_at IS NULL;

CREATE TABLE IF NOT EXISTS ocel.deployment (
	only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),
	slug     text NOT NULL
);

CREATE TABLE IF NOT EXISTS ocel.schedules (
	topic   text PRIMARY KEY,
	cron    text NOT NULL,
	next_at timestamptz NOT NULL
);
`
