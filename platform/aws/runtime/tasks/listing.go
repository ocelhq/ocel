package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const maxRunPage = 1000

const cursorMark = "before:"

func cursorOf(execution string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorMark + execution))
}

func parseCursor(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	execution, marked := strings.CutPrefix(string(raw), cursorMark)
	if err != nil || !marked || execution == "" {
		return "", fmt.Errorf("cursor %q: %w", cursor, runs.ErrUnknownCursor)
	}
	return execution, nil
}

type runFilter struct {
	expression string
	names      map[string]string
	values     map[string]any
}

func filterOf(consumer string, statuses []provider.RunStatus, tags []string) runFilter {
	filter := runFilter{names: map[string]string{}, values: map[string]any{}}
	var clauses []string
	if consumer != "" {
		filter.names["#consumer"] = "consumer"
		filter.values[":consumer"] = consumer
		clauses = append(clauses, "#consumer = :consumer")
	}
	if len(statuses) > 0 {
		filter.names["#status"] = "status"
		var placeholders []string
		for i, status := range statuses {
			placeholder := fmt.Sprintf(":status%d", i)
			filter.values[placeholder] = string(status)
			placeholders = append(placeholders, placeholder)
		}
		clauses = append(clauses, "#status IN ("+strings.Join(placeholders, ", ")+")")
	}
	for i, tag := range tags {
		placeholder := fmt.Sprintf(":tag%d", i)
		filter.names["#tags"] = "tags"
		filter.values[placeholder] = tag
		clauses = append(clauses, "contains(#tags, "+placeholder+")")
	}
	filter.expression = strings.Join(clauses, " AND ")
	if len(filter.names) == 0 {
		filter.names = nil
	}
	return filter
}

func (s store) listMatching(ctx context.Context, topic, before string, filter runFilter, want int) ([]runItem, error) {
	var matched []runItem
	page := runPage{}
	for {
		next, err := s.queryRuns(ctx, topic, before, filter.expression, filter.names, filter.values, int32(max(want-len(matched), 25)), page.next)
		if err != nil {
			return nil, err
		}
		matched = append(matched, next.items...)
		if len(matched) >= want || len(next.next) == 0 {
			break
		}
		page = next
	}
	if len(matched) > want {
		matched = matched[:want]
	}
	return matched, nil
}

func (s store) listRuns(ctx context.Context, topics []string, filter provider.RunFilter) (provider.RunPage, error) {
	limit := runs.PageLimit(filter.Limit)
	before := ""
	if filter.Cursor != "" {
		parsed, err := parseCursor(filter.Cursor)
		if err != nil {
			return provider.RunPage{}, err
		}
		before = parsed
	}
	matching := filterOf("", filter.Statuses, filter.Tags)
	var found []runItem
	for _, topic := range topics {
		matched, err := s.listMatching(ctx, topic, before, matching, limit+1)
		if err != nil {
			return provider.RunPage{}, err
		}
		found = append(found, matched...)
	}
	slices.SortFunc(found, func(a, b runItem) int { return strings.Compare(b.SK, a.SK) })
	var page provider.RunPage
	if len(found) > limit {
		found = found[:limit]
		page.NextCursor = cursorOf(found[limit-1].SK)
	}
	for _, item := range found {
		page.Runs = append(page.Runs, item.run())
	}
	return page, nil
}

func (t Tasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	var listed []string
	if req.GetTask() != "" {
		if _, err := t.task(req.GetTask()); err != nil {
			return &taskv1.ListRunsResponse{}, nil
		}
		listed = []string{req.GetTask()}
	} else {
		for name := range t.engine.cfg.Topology.Topics {
			if _, isTask := t.engine.taskConsumer(name); isTask {
				listed = append(listed, name)
			}
		}
	}
	page, err := t.engine.store.listRuns(ctx, listed, provider.RunFilter{
		Statuses: runs.StatusesOf(req.GetStatuses()),
		Tags:     req.GetTags(),
		Cursor:   req.GetCursor(),
		Limit:    int(req.GetLimit()),
	})
	if errors.Is(err, runs.ErrUnknownCursor) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	resp := &taskv1.ListRunsResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		resp.Runs = append(resp.Runs, runs.NewRunMessage(run))
	}
	return resp, nil
}
