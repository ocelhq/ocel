package logentries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	logtype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	bufferWindow = 2 * time.Second

	Reconnected = "RECONNECTED"
)

var errStreamEnded = errors.New("the stream ended")

type Suppressed struct {
	Reason string
	Count  int
}

func Tail(ctx context.Context, client *logging.Client, project string, q Query, emit func([]Event) error, notice func(Suppressed) error) error {
	if len(q.Sources) == 0 {
		return nil
	}
	req := &loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        tailFilter(q),
		BufferWindow:  durationpb.New(bufferWindow),
	}
	err := followed(ctx, client, req, q.Sources, emit, notice)
	if !errors.Is(err, errStreamEnded) {
		return tailed(project, err)
	}
	if err := notice(Suppressed{Reason: Reconnected}); err != nil {
		return err
	}
	return tailed(project, followed(ctx, client, req, q.Sources, emit, notice))
}

func tailed(project string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("tail log entries of project %s: %w", project, err)
}

func followed(ctx context.Context, client *logging.Client, req *loggingpb.TailLogEntriesRequest, sources []Source, emit func([]Event) error, notice func(Suppressed) error) error {
	stream, err := client.TailLogEntries(ctx)
	if err == nil {
		err = stream.Send(req)
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
	case errors.Is(err, io.EOF) || status.Code(err) == codes.Unavailable:
		return fmt.Errorf("%w: %w", errStreamEnded, err)
	}
	return err
}

func deliver(batch *loggingpb.TailLogEntriesResponse, sources []Source, emit func([]Event) error, notice func(Suppressed) error) error {
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
		if err := notice(Suppressed{Reason: info.GetReason().String(), Count: int(info.GetSuppressedCount())}); err != nil {
			return err
		}
	}
	return nil
}

func tailEventOf(entry *loggingpb.LogEntry, sources []Source) (Event, bool) {
	text := entry.GetTextPayload()
	if text == "" && entry.GetJsonPayload() != nil {
		if encoded, err := json.Marshal(entry.GetJsonPayload().AsMap()); err == nil {
			text = string(encoded)
		} else {
			text = entry.GetJsonPayload().String()
		}
	}
	if text == "" {
		return Event{}, false
	}
	event := Event{Time: entry.GetTimestamp().AsTime(), Text: text, Instance: entry.GetLabels()["instanceId"]}
	if entry.GetSeverity() != logtype.LogSeverity_DEFAULT {
		event.Severity = entry.GetSeverity().String()
	}
	if resource := entry.GetResource(); resource != nil {
		event.Label = labelOf(sources, resource.GetLabels()["service_name"], resource.GetLabels()["revision_name"])
	}
	return event, true
}
