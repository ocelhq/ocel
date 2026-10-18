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
	"github.com/ocelhq/ocel/pkg/runs"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type store struct {
	db querier
}

func (e *Engine) Store() runs.Store { return store{db: e.pool} }

const runColumns = `execution, topic, consumer, status, payload, output, error, attempts, max_attempts, tags, metadata,
	message_id, published_at, key, lane, delivery, held, lease_until,
	created_at, due_at, started_at, finished_at, expires_at, revision`

func runValues(run runs.Run, revision keyvalue.Revision) []any {
	return []any{
		run.Execution, run.Topic, run.Consumer, string(run.Status), jsonOrNull(run.Payload), jsonOrNull(run.Output), run.Error,
		run.Attempts, run.MaxAttempts, orEmpty(run.Tags), jsonOrNull(run.Metadata),
		run.Message, nullTime(run.PublishedAt), run.Key, string(run.Lane), run.Delivery, run.Held, nullTime(run.LeaseUntil),
		run.CreatedAt, nullTime(run.DueAt), nullTime(run.StartedAt), nullTime(run.FinishedAt), nullTime(run.ExpiresAt), string(revision),
	}
}

func scanRun(row pgx.Row) (runs.Run, error) {
	var run runs.Run
	var status, lane, revision string
	var payload, output, metadata []byte
	var published, lease, due, started, finished, expires *time.Time
	if err := row.Scan(&run.Execution, &run.Topic, &run.Consumer, &status, &payload, &output, &run.Error,
		&run.Attempts, &run.MaxAttempts, &run.Tags, &metadata,
		&run.Message, &published, &run.Key, &lane, &run.Delivery, &run.Held, &lease,
		&run.CreatedAt, &due, &started, &finished, &expires, &revision); err != nil {
		return runs.Run{}, err
	}
	run.Status, run.Lane, run.Revision = provider.RunStatus(status), provider.Lane(lane), keyvalue.Revision(revision)
	run.Payload, run.Output, run.Metadata = compactJSON(payload), compactJSON(output), compactJSON(metadata)
	run.PublishedAt, run.LeaseUntil = timeOf(published), timeOf(lease)
	run.DueAt, run.StartedAt, run.FinishedAt, run.ExpiresAt = timeOf(due), timeOf(started), timeOf(finished), timeOf(expires)
	return run, nil
}

func placeholders(n, from int) string {
	marks := make([]string, n)
	for i := range marks {
		marks[i] = fmt.Sprintf("$%d", from+i)
	}
	return strings.Join(marks, ", ")
}

func (s store) CreateRun(ctx context.Context, run runs.Run) (keyvalue.Revision, error) {
	revision, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	values := runValues(run, revision)
	tag, err := s.db.Exec(ctx, `INSERT INTO ocel.runs (`+runColumns+`) VALUES (`+placeholders(len(values), 1)+`) ON CONFLICT (execution) DO NOTHING`, values...)
	if err != nil {
		return "", fmt.Errorf("record run %s: %w", run.Execution, err)
	}
	if tag.RowsAffected() == 0 {
		return "", keyvalue.ErrStale
	}
	return revision, nil
}

func (s store) ReadRun(ctx context.Context, topic, execution string) (runs.Run, error) {
	run, err := scanRun(s.db.QueryRow(ctx, `SELECT `+runColumns+` FROM ocel.runs WHERE execution = $1 AND topic = $2`, execution, topic))
	if errors.Is(err, pgx.ErrNoRows) {
		return runs.Run{}, keyvalue.ErrNotFound
	}
	if err != nil {
		return runs.Run{}, fmt.Errorf("read run %s: %w", execution, err)
	}
	return run, nil
}

func (s store) WriteRun(ctx context.Context, run runs.Run) (keyvalue.Revision, error) {
	if run.Revision == "" {
		return "", keyvalue.ErrStale
	}
	revision, err := keyvalue.NewRevision()
	if err != nil {
		return "", err
	}
	values := runValues(run, revision)
	columns := strings.Split(strings.Join(strings.Fields(runColumns), ""), ",")
	assignments := make([]string, 0, len(columns))
	for i, column := range columns[2:] {
		assignments = append(assignments, fmt.Sprintf("%s = $%d", column, i+3))
	}
	tag, err := s.db.Exec(ctx, `UPDATE ocel.runs SET `+strings.Join(assignments, ", ")+`
		WHERE execution = $1 AND topic = $2 AND revision = $`+fmt.Sprint(len(values)+1), append(values, string(run.Revision))...)
	if err != nil {
		return "", fmt.Errorf("write run %s: %w", run.Execution, err)
	}
	if tag.RowsAffected() == 0 {
		return "", keyvalue.ErrStale
	}
	return revision, nil
}

func (s store) DeleteRun(ctx context.Context, run runs.Run) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ocel.runs WHERE execution = $1 AND topic = $2 AND revision = $3`, run.Execution, run.Topic, string(run.Revision))
	if err != nil {
		return fmt.Errorf("delete run %s: %w", run.Execution, err)
	}
	if tag.RowsAffected() == 0 {
		return keyvalue.ErrStale
	}
	return nil
}

func whereFilter(filter runs.RunFilter) (string, []any, error) {
	var where []string
	var args []any
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	where = append(where, "topic = ANY("+arg(orEmpty(filter.Topics))+")")
	if filter.Consumer != "" {
		where = append(where, "consumer = "+arg(filter.Consumer))
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
		created, execution, err := runs.ParseCursor(filter.Cursor)
		if err != nil {
			return "", nil, err
		}
		where = append(where, "(created_at, execution) < ("+arg(created)+", "+arg(execution)+")")
	}
	return " WHERE " + strings.Join(where, " AND "), args, nil
}

func (s store) ListRuns(ctx context.Context, filter runs.RunFilter) (runs.RunPage, error) {
	limit := runs.PageLimit(filter.Limit)
	where, args, err := whereFilter(filter)
	if err != nil {
		return runs.RunPage{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+runColumns+` FROM ocel.runs`+where+fmt.Sprintf(" ORDER BY created_at DESC, execution DESC LIMIT $%d", len(args)+1), append(args, limit+1)...)
	if err != nil {
		return runs.RunPage{}, fmt.Errorf("list runs: %w", err)
	}
	listed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (runs.Run, error) { return scanRun(row) })
	if err != nil {
		return runs.RunPage{}, fmt.Errorf("list runs: %w", err)
	}
	page := runs.RunPage{Runs: listed}
	if len(listed) > limit {
		page.Runs = listed[:limit]
		last := page.Runs[limit-1]
		page.NextCursor = runs.CursorOf(last.CreatedAt, last.Execution)
	}
	return page, nil
}

func (s store) CountRuns(ctx context.Context, filter runs.RunFilter) (int64, error) {
	filter.Cursor = ""
	where, args, err := whereFilter(filter)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM ocel.runs`+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count runs: %w", err)
	}
	return count, nil
}

func (s store) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	var created bool
	var value []byte
	var expires time.Time
	err := s.db.QueryRow(ctx, `
		INSERT INTO ocel.records (purpose, topic, key, value, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (purpose, topic, key) DO UPDATE
			SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at
			WHERE ocel.records.expires_at <= clock_timestamp()
		RETURNING value, expires_at, true`,
		string(record.Purpose), record.Topic, record.Key, jsonOrNull(record.Value), record.ExpiresAt,
	).Scan(&value, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.db.QueryRow(ctx, `SELECT value, expires_at FROM ocel.records WHERE purpose = $1 AND topic = $2 AND key = $3`,
			string(record.Purpose), record.Topic, record.Key).Scan(&value, &expires)
	}
	if err != nil {
		return provider.ExpiringRecord{}, false, fmt.Errorf("ensure the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	record.Value = compactJSON(value)
	record.ExpiresAt = expires
	return record, created, nil
}

func (s store) ReplaceRecord(ctx context.Context, record provider.ExpiringRecord, held json.RawMessage) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE ocel.records SET value = $4, expires_at = $5
		WHERE purpose = $1 AND topic = $2 AND key = $3 AND value::text = $6`,
		string(record.Purpose), record.Topic, record.Key, jsonOrNull(record.Value), record.ExpiresAt, string(held))
	if err != nil {
		return fmt.Errorf("replace the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, keyvalue.ErrStale)
	}
	return nil
}

func (s store) DeleteRecord(ctx context.Context, record provider.ExpiringRecord) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM ocel.records WHERE purpose = $1 AND topic = $2 AND key = $3 AND value::text = $4`,
		string(record.Purpose), record.Topic, record.Key, string(record.Value)); err != nil {
		return fmt.Errorf("delete the %s record %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
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
