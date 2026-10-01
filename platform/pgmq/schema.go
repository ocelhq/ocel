package pgmq

const schema = `
CREATE SCHEMA IF NOT EXISTS ocel;

CREATE TABLE IF NOT EXISTS ocel.records (
	purpose    text NOT NULL,
	topic      text NOT NULL,
	key        text NOT NULL,
	value      jsonb,
	expires_at timestamptz NOT NULL,
	PRIMARY KEY (purpose, topic, key)
);
CREATE INDEX IF NOT EXISTS records_by_expiry ON ocel.records (expires_at);

CREATE TABLE IF NOT EXISTS ocel.runs (
	execution     text PRIMARY KEY,
	topic         text NOT NULL,
	consumer      text NOT NULL,
	status        text NOT NULL,
	payload       jsonb,
	output        jsonb,
	error         text NOT NULL DEFAULT '',
	attempts      integer NOT NULL DEFAULT 0,
	tags          text[] NOT NULL DEFAULT '{}',
	metadata      jsonb,
	created_at    timestamptz NOT NULL,
	due_at        timestamptz,
	started_at    timestamptz,
	finished_at   timestamptz,
	expires_at    timestamptz,
	revision      text NOT NULL,
	message_id    text NOT NULL DEFAULT '',
	published_at  timestamptz,
	queue         text NOT NULL DEFAULT '',
	queue_message bigint,
	key           text NOT NULL DEFAULT '',
	lane          text NOT NULL DEFAULT '',
	max_attempts  integer NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS runs_by_topic ON ocel.runs (topic, created_at DESC, execution DESC);
CREATE INDEX IF NOT EXISTS runs_by_creation ON ocel.runs (created_at DESC, execution DESC);
CREATE INDEX IF NOT EXISTS runs_by_queue_status ON ocel.runs (queue, status);
CREATE INDEX IF NOT EXISTS delayed_runs_by_due ON ocel.runs (due_at) WHERE status = 'delayed';
CREATE INDEX IF NOT EXISTS unstarted_runs_by_expiry ON ocel.runs (expires_at) WHERE attempts = 0 AND status IN ('queued', 'delayed');
CREATE INDEX IF NOT EXISTS runs_by_finish ON ocel.runs (finished_at);

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
