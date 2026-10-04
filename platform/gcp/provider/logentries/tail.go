package logentries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	bufferWindow  = 2 * time.Second
	backfillLimit = 10000
)

type Reason string

const (
	RateLimit   Reason = "RATE_LIMIT"
	NotConsumed Reason = "NOT_CONSUMED"
	Unspecified Reason = "UNSPECIFIED"
	Reconnected Reason = "RECONNECTED"
)

var (
	errStreamEnded = errors.New("the stream ended")
	errThrottled   = errors.New("the stream was throttled")
	transientCodes = []codes.Code{codes.DeadlineExceeded, codes.Internal, codes.Unavailable}
)

type Notice struct {
	Reason Reason
	Count  int
}

func Tail(ctx context.Context, client *logging.Client, project string, q Query, emit func([]Event) error, notice func(Notice) error) error {
	if len(q.Sources) == 0 {
		return nil
	}
	req := &loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        tailFilter(q),
		BufferWindow:  durationpb.New(bufferWindow),
	}
	var backfilled map[string]bool
	backfill := func() error {
		if backfilled != nil {
			return nil
		}
		events, err := listSince(ctx, client, project, q)
		if err != nil {
			return fmt.Errorf("read log entries of project %s from %s: %w", project, q.Since.Format(time.RFC3339Nano), err)
		}
		backfilled = map[string]bool{}
		for _, event := range events {
			backfilled[event.ID] = true
		}
		if len(events) == 0 {
			return nil
		}
		return emit(events)
	}
	emitUnread := func(events []Event) error {
		unread := make([]Event, 0, len(events))
		for _, event := range events {
			if event.Time.Before(q.Since) || event.ID != "" && backfilled[event.ID] {
				continue
			}
			unread = append(unread, event)
		}
		if len(unread) == 0 {
			return nil
		}
		return emit(unread)
	}
	reconnected := false
	for attempt := 0; ; attempt++ {
		if attempt > 0 && !waited(ctx, attempt) {
			return nil
		}
		err := followStream(ctx, client, req, q.Sources, backfill, emitUnread, notice)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errThrottled) && attempt+1 < retryAttempts:
			continue
		case errors.Is(err, errStreamEnded) && !reconnected:
			reconnected = true
			if err := notice(Notice{Reason: Reconnected}); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("tail log entries of project %s: %w", project, err)
	}
}

func listSince(ctx context.Context, client *logging.Client, project string, q Query) ([]Event, error) {
	entries := client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        filter(Query{Sources: q.Sources, Since: q.Since, Contains: q.Contains}),
		OrderBy:       "timestamp asc",
		PageSize:      maxPageSize,
	})
	var events []Event
	for len(events) < backfillLimit {
		entry, err := entries.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		if event, ok := tailEventOf(entry, q.Sources); ok {
			events = append(events, event)
		}
	}
	return events, nil
}

func followStream(ctx context.Context, client *logging.Client, req *loggingpb.TailLogEntriesRequest, sources []Source, opened func() error, emit func([]Event) error, notice func(Notice) error) error {
	session, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.TailLogEntries(session)
	if err == nil {
		err = stream.Send(req)
	}
	if err == nil {
		if err := opened(); err != nil && ctx.Err() == nil {
			return err
		}
	}
	for err == nil {
		var batch *loggingpb.TailLogEntriesResponse
		if batch, err = stream.Recv(); err != nil {
			break
		}
		if err := deliver(batch, sources, emit, notice); err != nil {
			return err
		}
	}
	switch {
	case ctx.Err() != nil:
		return nil
	case errors.Is(err, io.EOF) || slices.Contains(transientCodes, status.Code(err)):
		return fmt.Errorf("%w: %w", errStreamEnded, err)
	case status.Code(err) == codes.ResourceExhausted:
		return fmt.Errorf("%w: %w", errThrottled, err)
	}
	return err
}

func deliver(batch *loggingpb.TailLogEntriesResponse, sources []Source, emit func([]Event) error, notice func(Notice) error) error {
	var events []Event
	for _, entry := range batch.GetEntries() {
		if event, ok := tailEventOf(entry, sources); ok {
			events = append(events, event)
		}
	}
	if len(events) > 0 {
		if err := emit(events); err != nil {
			return err
		}
	}
	for _, info := range batch.GetSuppressionInfo() {
		if err := notice(Notice{Reason: reasonOf(info.GetReason()), Count: int(info.GetSuppressedCount())}); err != nil {
			return err
		}
	}
	return nil
}

func tailEventOf(entry *loggingpb.LogEntry, sources []Source) (Event, bool) {
	text := entry.GetTextPayload()
	if text == "" && entry.GetJsonPayload() != nil {
		encoded, err := json.Marshal(entry.GetJsonPayload().AsMap())
		if err != nil {
			return Event{}, false
		}
		text = string(encoded)
	}
	if text == "" {
		return Event{}, false
	}
	return newEvent(sources, entry.GetInsertId(), entry.GetTimestamp().AsTime(), text, entry.GetSeverity().String(), entry.GetLabels(), entry.GetResource().GetLabels()), true
}

func reasonOf(reason loggingpb.TailLogEntriesResponse_SuppressionInfo_Reason) Reason {
	switch reason {
	case loggingpb.TailLogEntriesResponse_SuppressionInfo_RATE_LIMIT:
		return RateLimit
	case loggingpb.TailLogEntriesResponse_SuppressionInfo_NOT_CONSUMED:
		return NotConsumed
	}
	return Unspecified
}
