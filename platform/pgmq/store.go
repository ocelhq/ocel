package pgmq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/taskruns"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db querier
}

func (e *Engine) Store() Store { return Store{db: e.pool} }

func (s Store) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	return ensureRecord(ctx, s.db, record)
}

func ensureRecord(ctx context.Context, db querier, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	var created bool
	var value []byte
	var expires time.Time
	err := db.QueryRow(ctx, `
		INSERT INTO ocel.records (purpose, topic, key, value, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (purpose, topic, key) DO UPDATE
			SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at
			WHERE ocel.records.expires_at <= clock_timestamp()
		RETURNING value, expires_at, true`,
		string(record.Purpose), record.Topic, record.Key, jsonOrNull(record.Value), record.ExpiresAt,
	).Scan(&value, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		err = db.QueryRow(ctx, `SELECT value, expires_at FROM ocel.records WHERE purpose = $1 AND topic = $2 AND key = $3`,
			string(record.Purpose), record.Topic, record.Key).Scan(&value, &expires)
	}
	if err != nil {
		return provider.ExpiringRecord{}, false, fmt.Errorf("ensure the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	record.Value = compactJSON(value)
	record.ExpiresAt = expires
	return record, created, nil
}

func (s Store) ReadRun(ctx context.Context, execution string) (provider.Run, error) {
	return readRun(ctx, s.db, execution)
}

const runColumns = `execution, topic, consumer, status, payload, output, error, attempts, tags, metadata,
	created_at, due_at, started_at, finished_at, expires_at, revision`

func readRun(ctx context.Context, db querier, execution string) (provider.Run, error) {
	run, err := scanRun(db.QueryRow(ctx, `SELECT `+runColumns+` FROM ocel.runs WHERE execution = $1`, execution))
	if errors.Is(err, pgx.ErrNoRows) {
		return provider.Run{}, keyvalue.ErrNotFound
	}
	if err != nil {
		return provider.Run{}, fmt.Errorf("read run %s: %w", execution, err)
	}
	return run, nil
}

func scanRun(row pgx.Row) (provider.Run, error) {
	var run provider.Run
	var status, revision string
	var payload, output, metadata []byte
	var due, started, finished, expires *time.Time
	if err := row.Scan(&run.Execution, &run.Topic, &run.Consumer, &status, &payload, &output, &run.Error, &run.Attempts,
		&run.Tags, &metadata, &run.CreatedAt, &due, &started, &finished, &expires, &revision); err != nil {
		return provider.Run{}, err
	}
	run.Status = provider.RunStatus(status)
	run.Revision = keyvalue.Revision(revision)
	run.Payload, run.Output, run.Metadata = compactJSON(payload), compactJSON(output), compactJSON(metadata)
	run.DueAt, run.StartedAt, run.FinishedAt, run.ExpiresAt = timeOf(due), timeOf(started), timeOf(finished), timeOf(expires)
	return run, nil
}

func (s Store) WriteRun(ctx context.Context, run provider.Run) (keyvalue.Revision, error) {
	return writeRun(ctx, s.db, run)
}

func writeRun(ctx context.Context, db querier, run provider.Run) (keyvalue.Revision, error) {
	next, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	args := []any{run.Execution, run.Topic, run.Consumer, string(run.Status), jsonOrNull(run.Payload), jsonOrNull(run.Output),
		run.Error, run.Attempts, orEmpty(run.Tags), jsonOrNull(run.Metadata), run.CreatedAt,
		nullTime(run.DueAt), nullTime(run.StartedAt), nullTime(run.FinishedAt), nullTime(run.ExpiresAt), string(next)}
	var tag pgconn.CommandTag
	if run.Revision == "" {
		tag, err = db.Exec(ctx, `INSERT INTO ocel.runs (`+runColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			ON CONFLICT (execution) DO NOTHING`, args...)
	} else {
		tag, err = db.Exec(ctx, `UPDATE ocel.runs SET topic = $2, consumer = $3, status = $4, payload = $5, output = $6,
			error = $7, attempts = $8, tags = $9, metadata = $10, created_at = $11, due_at = $12, started_at = $13,
			finished_at = $14, expires_at = $15, revision = $16
			WHERE execution = $1 AND revision = $17`, append(args, string(run.Revision))...)
	}
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.Execution, err)
	}
	if tag.RowsAffected() == 0 {
		return "", keyvalue.ErrStale
	}
	return next, nil
}

func (s Store) ListRuns(ctx context.Context, filter provider.RunFilter) (provider.RunPage, error) {
	return listRuns(ctx, s.db, filter)
}

func listRuns(ctx context.Context, db querier, filter provider.RunFilter) (provider.RunPage, error) {
	limit := taskruns.PageLimit(filter.Limit)
	var where []string
	var args []any
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Topic != "" {
		where = append(where, "topic = "+arg(filter.Topic))
	}
	if len(filter.Statuses) > 0 {
		statuses := make([]string, len(filter.Statuses))
		for i, status := range filter.Statuses {
			statuses[i] = string(status)
		}
		where = append(where, "status = ANY("+arg(statuses)+")")
	}
	if len(filter.Tags) > 0 {
		where = append(where, "tags @> "+arg(filter.Tags))
	}
	if filter.Cursor != "" {
		created, execution, err := taskruns.ParseCursor(filter.Cursor)
		if err != nil {
			return provider.RunPage{}, err
		}
		where = append(where, "(created_at, execution) < ("+arg(created)+", "+arg(execution)+")")
	}
	query := `SELECT ` + runColumns + ` FROM ocel.runs`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC, execution DESC LIMIT " + arg(limit+1)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return provider.RunPage{}, fmt.Errorf("list runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (provider.Run, error) { return scanRun(row) })
	if err != nil {
		return provider.RunPage{}, fmt.Errorf("list runs: %w", err)
	}
	page := provider.RunPage{Runs: runs}
	if len(runs) > limit {
		page.Runs = runs[:limit]
		last := page.Runs[limit-1]
		page.NextCursor = taskruns.CursorOf(last.CreatedAt, last.Execution)
	}
	return page, nil
}

func jsonOrNull(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func compactJSON(value []byte) json.RawMessage {
	if value == nil {
		return nil
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, value); err != nil {
		return value
	}
	return compacted.Bytes()
}

func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
