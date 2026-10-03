package logentries

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/api/logging/v2"
)

const maxPageSize = 1000

type Source struct {
	Service  string
	Revision string
	Label    string
}

type Query struct {
	Sources      []Source
	Since, Until time.Time
	Limit        int
	Contains     string
}

type Event struct {
	Time     time.Time
	Label    string
	Instance string
	Text     string
	Severity string
}

func Read(ctx context.Context, logs *logging.Service, project string, q Query) ([]Event, error) {
	if len(q.Sources) == 0 || q.Limit <= 0 {
		return nil, nil
	}
	if q.Since.IsZero() {
		return nil, errors.New("read log entries: no Since given, and without one the read would scan the project's whole log retention")
	}
	req := &logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        filter(q),
		OrderBy:       "timestamp desc",
		PageSize:      int64(min(q.Limit, maxPageSize)),
	}
	var events []Event
	for len(events) < q.Limit {
		page, err := listed(ctx, logs, req)
		if err != nil {
			return nil, fmt.Errorf("read log entries of project %s: %w", project, err)
		}
		for _, entry := range page.Entries {
			event, ok, err := eventOf(entry, q.Sources)
			if err != nil {
				return nil, err
			}
			if ok && len(events) < q.Limit {
				events = append(events, event)
			}
		}
		if page.NextPageToken == "" {
			break
		}
		req.PageToken = page.NextPageToken
	}
	slices.Reverse(events)
	return events, nil
}

func eventOf(entry *logging.LogEntry, sources []Source) (Event, bool, error) {
	text := entry.TextPayload
	if text == "" && len(entry.JsonPayload) > 0 {
		text = string(entry.JsonPayload)
	}
	if text == "" {
		return Event{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return Event{}, false, fmt.Errorf("read the time of log entry %s: %w", entry.InsertId, err)
	}
	event := Event{Time: at, Text: text, Instance: entry.Labels["instanceId"]}
	if entry.Severity != "DEFAULT" {
		event.Severity = entry.Severity
	}
	if entry.Resource != nil {
		event.Label = labelOf(sources, entry.Resource.Labels["service_name"], entry.Resource.Labels["revision_name"])
	}
	return event, true, nil
}

func labelOf(sources []Source, service, revision string) string {
	for _, source := range sources {
		if source.Service == service && source.Revision == revision {
			return source.Label
		}
	}
	for _, source := range sources {
		if source.Service == service && source.Revision == "" {
			return source.Label
		}
	}
	return ""
}
