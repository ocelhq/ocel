package pgmq

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	newRevisionSQL = "md5(random()::text || clock_timestamp()::text)"

	keepMessage   = "true"
	deleteMessage = "CASE WHEN queue_message IS NOT NULL THEN pgmq.delete(queue, queue_message) END"
	delayMessage  = "CASE WHEN queue_message IS NOT NULL THEN (SELECT count(*) > 0 FROM pgmq.set_vt(queue, queue_message, @visible_at::timestamptz)) END"
)

var (
	waitingStatuses    = []provider.RunStatus{provider.RunQueued, provider.RunDelayed}
	unfinishedStatuses = []provider.RunStatus{provider.RunQueued, provider.RunDelayed, provider.RunExecuting}
)

type runMove struct {
	from    []provider.RunStatus
	to      provider.RunStatus
	where   string
	set     string
	args    pgx.NamedArgs
	message string
}

func (m runMove) statement(returning string) (string, pgx.NamedArgs) {
	args := pgx.NamedArgs{"from": statusNames(m.from), "to": string(m.to)}
	maps.Copy(args, m.args)
	assignments := []string{"status = @to", "revision = " + newRevisionSQL}
	if m.set != "" {
		assignments = append(assignments, m.set)
	}
	conditions := []string{"status = ANY(@from)"}
	if m.where != "" {
		conditions = append(conditions, "("+m.where+")")
	}
	message := m.message
	if message == "" {
		message = keepMessage
	}
	selected := []string{message}
	if returning != "" {
		selected = append(selected, returning)
	}
	return `WITH moved AS (
		UPDATE ocel.runs SET ` + strings.Join(assignments, ", ") + `
		WHERE ` + strings.Join(conditions, " AND ") + `
		RETURNING *
	)
	SELECT ` + strings.Join(selected, ", ") + ` FROM moved`, args
}

func (m runMove) of(execution string) runMove {
	m.args = maps.Clone(m.args)
	if m.args == nil {
		m.args = pgx.NamedArgs{}
	}
	m.args["execution"] = execution
	if m.where == "" {
		m.where = "execution = @execution"
	} else {
		m.where = "execution = @execution AND (" + m.where + ")"
	}
	return m
}

func moveRun(ctx context.Context, db querier, execution string, m runMove) (bool, error) {
	query, args := m.of(execution).statement("")
	var message any
	err := db.QueryRow(ctx, query, args).Scan(&message)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func moveRuns(ctx context.Context, db querier, m runMove) ([]string, error) {
	query, args := m.statement("execution")
	rows, err := db.Query(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var message any
		var execution string
		return execution, row.Scan(&message, &execution)
	})
}

func statusNames(statuses []provider.RunStatus) []string {
	names := make([]string, len(statuses))
	for i, status := range statuses {
		names[i] = string(status)
	}
	return names
}
