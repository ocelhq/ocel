package runtrace

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func startRun(t *testing.T) *Trace {
	t.Helper()
	r, err := Open(t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func loggedEvents(t *testing.T, r *Trace) []*streamv1.RunEvent {
	t.Helper()
	var out []*streamv1.RunEvent
	for _, line := range readLines(t, r.LogPath()) {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("log line %q is not a run event: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestEveryEventDebugIncludedLandsInTheRunsNDJSONFileAsARunEvent(t *testing.T) {
	r := startRun(t)
	at := timestamppb.New(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	sent := []*streamv1.RunEvent{
		{Operation: &progressv1.OperationEvent{Time: at, Level: progressv1.Level_LEVEL_INFO, Phase: progressv1.Phase_PHASE_BUILD, Message: "Building project"}},
		{Operation: &progressv1.OperationEvent{Time: at, Level: progressv1.Level_LEVEL_DEBUG, Subject: "fake", Message: "engine line", Body: &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDERR}}}},
		{Operation: &progressv1.OperationEvent{Time: at, Level: progressv1.Level_LEVEL_ERROR}, Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{Detail: "boom"}}},
	}
	for _, ev := range sent {
		r.Receive(ev)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	got := loggedEvents(t, r)
	if len(got) != len(sent) {
		t.Fatalf("the log holds %d events, want %d", len(got), len(sent))
	}
	for i := range sent {
		if !proto.Equal(got[i], sent[i]) {
			t.Errorf("logged event %d = %s, want %s", i, protojson.Format(got[i]), protojson.Format(sent[i]))
		}
	}
}

func started(id, parent []byte, message string) *streamv1.RunEvent {
	return &streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO,
		Phase:   progressv1.Phase_PHASE_PROVISION,
		SpanId:  id,
		Message: message, Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{ParentSpanId: parent}}},
	}
}

func ended(id []byte, start, end time.Time, status progressv1.SpanStatus, attrs ...*progressv1.SpanAttribute) *streamv1.RunEvent {
	return &streamv1.RunEvent{Operation: &progressv1.OperationEvent{Time: timestamppb.New(end),
		Level:  progressv1.Level_LEVEL_INFO,
		Phase:  progressv1.Phase_PHASE_PROVISION,
		SpanId: id, Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
			Status:            status,
			StartTimeUnixNano: start.UnixNano(),
			Attributes:        attrs,
		}}},
	}
}

func TestAnEndedScopeBecomesASpanInTheTraceWithItsAttributes(t *testing.T) {
	r := startRun(t)
	unit := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	phase := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	r.Receive(started(unit, nil, "web"))
	r.Receive(started(phase, unit, "Provisioning"))
	r.Receive(ended(phase, start, start.Add(2*time.Second), progressv1.SpanStatus_SPAN_STATUS_ERROR,
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, Value: "fake:bucket"},
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, Value: "42"},
	))
	r.Receive(ended(unit, start, start.Add(3*time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	web := spanNamed(t, spans, "web")
	provisioning := spanNamed(t, spans, "Provisioning")
	if web.SpanId != "0101010101010101" || provisioning.SpanId != "0202020202020202" {
		t.Errorf("span ids = %s, %s, want the scopes' own ids", web.SpanId, provisioning.SpanId)
	}
	if provisioning.ParentSpanId != web.SpanId {
		t.Errorf("Provisioning's parent = %q, want the web unit %q it started under", provisioning.ParentSpanId, web.SpanId)
	}
	if provisioning.Status == nil || provisioning.Status.Code != "STATUS_CODE_ERROR" {
		t.Errorf("Provisioning status = %+v, want STATUS_CODE_ERROR", provisioning.Status)
	}
	if provisioning.Start != "1790510400000000000" || provisioning.End != "1790510402000000000" {
		t.Errorf("Provisioning ran %s..%s, want its start and its ended event's time", provisioning.Start, provisioning.End)
	}
	want := map[string]map[string]any{
		"ocel.phase":          {"stringValue": "provision"},
		"ocel.resource_type":  {"stringValue": "fake:bucket"},
		"ocel.resource_count": {"intValue": "42"},
	}
	if len(provisioning.Attributes) != len(want) {
		t.Fatalf("Provisioning attributes = %v, want %v", provisioning.Attributes, want)
	}
	for _, a := range provisioning.Attributes {
		for kind, value := range want[a.Key] {
			if a.Value[kind] != value {
				t.Errorf("attribute %s = %v, want %s %v", a.Key, a.Value, kind, value)
			}
		}
	}
}

func TestAWaitNeverPersistsTheSessionTokenInItsAddress(t *testing.T) {
	const token = "s3cr3t-session-token"
	r := startRun(t)
	waiting := &streamv1.RunEvent{Cli: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{Url: "http://127.0.0.1:41234/#t=" + token}}}
	r.Receive(waiting)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	got := loggedEvents(t, r)
	if len(got) != 1 || got[0].GetWaiting().GetUrl() != "http://127.0.0.1:41234/" {
		t.Fatalf("logged %d events, want one wait recorded at its address without the token", len(got))
	}
	if waiting.GetWaiting().GetUrl() != "http://127.0.0.1:41234/#t="+token {
		t.Errorf("the event other sinks see now reads %q, want it untouched", waiting.GetWaiting().GetUrl())
	}
}

func TestANonNumericValueForANumericKeyIsKeptAsAString(t *testing.T) {
	r := startRun(t)
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.Receive(started(id, nil, "malformed byte count"))
	r.Receive(ended(id, now, now, progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED,
		&progressv1.SpanAttribute{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Value: "not-a-number"}))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	attrs := spanNamed(t, readTraceDoc(t, r), "malformed byte count").Attributes
	if len(attrs) != 2 || attrs[1].Key != "ocel.bytes" || attrs[1].Value["stringValue"] != "not-a-number" {
		t.Errorf("attributes = %v, want ocel.bytes kept as the string it was sent as", attrs)
	}
}

func TestARunClosedByItsBusAndByItsOwnerClosesOnce(t *testing.T) {
	r := startRun(t)
	if err := r.Close(); err != nil {
		t.Fatalf("first Close() = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil: the first already closed the run", err)
	}
}
