package logentries

import (
	"context"
	"errors"
	"math"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/api/option"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	logtype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tailServer struct {
	loggingpb.UnimplementedLoggingServiceV2Server
	mu       sync.Mutex
	requests []*loggingpb.TailLogEntriesRequest
	session  func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error
}

func (s *tailServer) TailLogEntries(stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	call := len(s.requests)
	s.mu.Unlock()
	return s.session(call, stream)
}

func (s *tailServer) received() []*loggingpb.TailLogEntriesRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func serveTail(t *testing.T, session func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error) (*logging.Client, *tailServer) {
	t.Helper()
	fake := &tailServer{session: session}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	loggingpb.RegisterLoggingServiceV2Server(server, fake)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client, err := logging.NewClient(context.Background(), option.WithGRPCConn(conn), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, fake
}

func hold(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
	<-stream.Context().Done()
	return nil
}

func sendThenHold(stream loggingpb.LoggingServiceV2_TailLogEntriesServer, response *loggingpb.TailLogEntriesResponse) error {
	if err := stream.Send(response); err != nil {
		return err
	}
	return hold(0, stream)
}

func ignoreEvents([]Event) error { return nil }
func ignoreNotices(Notice) error { return nil }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTailSendsTheSourcesFilter(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, hold)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := Query{
		Sources:  []Source{{Service: "web", Revision: "web-00001"}},
		Since:    since,
		Contains: "boom",
	}

	done := make(chan error, 1)
	go func() { done <- Tail(ctx, client, "acme-prod", q, ignoreEvents, ignoreNotices) }()
	waitFor(t, func() bool { return len(fake.received()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Tail after cancel: %v", err)
	}

	got := fake.received()[0]
	want := `resource.type="cloud_run_revision" AND ((resource.labels.service_name="web" AND resource.labels.revision_name="web-00001")) AND "boom" AND (log_id("run.googleapis.com/stdout") OR log_id("run.googleapis.com/stderr"))`
	if got.Filter != want {
		t.Errorf("filter = %s, want %s", got.Filter, want)
	}
	if !slices.Equal(got.ResourceNames, []string{"projects/acme-prod"}) {
		t.Errorf("resource names = %v, want [projects/acme-prod]", got.ResourceNames)
	}
	if got.BufferWindow.AsDuration() != 2*time.Second {
		t.Errorf("buffer window = %v, want 2s", got.BufferWindow.AsDuration())
	}
}

func streamedEntry(at time.Time, text string) *loggingpb.LogEntry {
	return &loggingpb.LogEntry{
		Timestamp: timestamppb.New(at),
		Payload:   &loggingpb.LogEntry_TextPayload{TextPayload: text},
		Labels:    map[string]string{"instanceId": "instance-1"},
		Resource: &monitoredres.MonitoredResource{
			Type:   "cloud_run_revision",
			Labels: map[string]string{"service_name": "web", "revision_name": "web-00001"},
		},
	}
}

func webQuery() Query {
	return Query{Sources: []Source{{Service: "web", Revision: "web-00001", Label: "web"}}, Since: since}
}

func TestTailEmitsEntriesAsTheyArrive(t *testing.T) {
	t.Parallel()
	first, second := since.Add(time.Second), since.Add(2*time.Second)
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		if err := stream.Send(&loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(first, "listening")}}); err != nil {
			return err
		}
		warned := streamedEntry(second, "")
		warned.Payload = &loggingpb.LogEntry_JsonPayload{JsonPayload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"msg": structpb.NewStringValue("slow"),
		}}}
		warned.Severity = logtype.LogSeverity_WARNING
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{warned}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu      sync.Mutex
		batches [][]Event
	)
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, client, "acme-prod", webQuery(), func(events []Event) error {
			mu.Lock()
			defer mu.Unlock()
			batches = append(batches, events)
			return nil
		}, ignoreNotices)
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(batches) == 2 })
	cancel()
	<-done

	want := [][]Event{
		{{Time: first, Label: "web", Instance: "instance-1", Text: "listening"}},
		{{Time: second, Label: "web", Instance: "instance-1", Text: `{"msg":"slow"}`, Severity: "WARNING"}},
	}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("events = %+v, want %+v", batches, want)
	}
}

func noticesFrom(t *testing.T, response *loggingpb.TailLogEntriesResponse, count int) []Notice {
	t.Helper()
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		return sendThenHold(stream, response)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu      sync.Mutex
		notices []Notice
	)
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, client, "acme-prod", webQuery(), ignoreEvents, func(n Notice) error {
			mu.Lock()
			defer mu.Unlock()
			notices = append(notices, n)
			return nil
		})
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(notices) == count })
	cancel()
	<-done
	return notices
}

func TestTailReportsSuppressedEntries(t *testing.T) {
	t.Parallel()

	notices := noticesFrom(t, &loggingpb.TailLogEntriesResponse{
		Entries: []*loggingpb.LogEntry{streamedEntry(since, "kept")},
		SuppressionInfo: []*loggingpb.TailLogEntriesResponse_SuppressionInfo{
			{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_RATE_LIMIT, SuppressedCount: 120},
			{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_NOT_CONSUMED, SuppressedCount: 7},
		},
	}, 2)

	want := []Notice{{Reason: RateLimit, Count: 120}, {Reason: NotConsumed, Count: 7}}
	if !slices.Equal(notices, want) {
		t.Errorf("notices = %+v, want %+v", notices, want)
	}
}

func TestTailReportsASuppressionWithoutAKnownReasonAsUnspecified(t *testing.T) {
	t.Parallel()

	notices := noticesFrom(t, &loggingpb.TailLogEntriesResponse{
		SuppressionInfo: []*loggingpb.TailLogEntriesResponse_SuppressionInfo{
			{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_REASON_UNSPECIFIED, SuppressedCount: 3},
			{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_Reason(99), SuppressedCount: 4},
		},
	}, 2)

	want := []Notice{{Reason: Unspecified, Count: 3}, {Reason: Unspecified, Count: 4}}
	if !slices.Equal(notices, want) {
		t.Errorf("notices = %+v, want %+v", notices, want)
	}
}

func TestTailReconnectsOnceWhenTheStreamEnds(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		err := stream.Send(&loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(since, "call "+strconv.Itoa(call))}})
		if err != nil || call > 1 {
			<-stream.Context().Done()
			return err
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu      sync.Mutex
		texts   []string
		notices []Notice
	)
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, client, "acme-prod", webQuery(), func(events []Event) error {
			mu.Lock()
			defer mu.Unlock()
			for _, event := range events {
				texts = append(texts, event.Text)
			}
			return nil
		}, func(s Notice) error {
			mu.Lock()
			defer mu.Unlock()
			notices = append(notices, s)
			return nil
		})
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(texts) == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Tail after cancel: %v", err)
	}

	if want := []string{"call 1", "call 2"}; !slices.Equal(texts, want) {
		t.Errorf("texts = %v, want %v", texts, want)
	}
	if want := []Notice{{Reason: Reconnected}}; !slices.Equal(notices, want) {
		t.Errorf("notices = %+v, want %+v", notices, want)
	}
	received := fake.received()
	if len(received) != 2 || received[0].Filter != received[1].Filter {
		t.Errorf("sessions received = %v, want two with the same filter", received)
	}
}

func TestTailFailsWhenTheStreamEndsAgainAfterReconnecting(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(int, loggingpb.LoggingServiceV2_TailLogEntriesServer) error { return nil })

	err := Tail(context.Background(), client, "acme-prod", webQuery(), ignoreEvents, ignoreNotices)

	if err == nil || !errors.Is(err, errStreamEnded) {
		t.Fatalf("Tail = %v, want the stream ending twice to fail", err)
	}
	if n := len(fake.received()); n != 2 {
		t.Errorf("sessions opened = %d, want 2: the first and one reconnect", n)
	}
}

func TestTailReconnectsAfterATransientFailure(t *testing.T) {
	t.Parallel()
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			client, _ := serveTail(t, func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
				if call == 1 {
					return status.Error(code, "restarting")
				}
				return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(since, "back")}})
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var seen atomic.Int64
			done := make(chan error, 1)
			go func() {
				done <- Tail(ctx, client, "acme-prod", webQuery(), func([]Event) error { seen.Add(1); return nil }, ignoreNotices)
			}()
			waitFor(t, func() bool { return seen.Load() == 1 })
			cancel()

			if err := <-done; err != nil {
				t.Errorf("Tail after cancel: %v", err)
			}
		})
	}
}

func TestTailWaitsBeforeReconnecting(t *testing.T) {
	t.Parallel()
	var (
		mu     sync.Mutex
		starts []time.Time
	)
	client, _ := serveTail(t, func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		if call == 1 {
			return nil
		}
		return hold(call, stream)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Tail(ctx, client, "acme-prod", webQuery(), ignoreEvents, ignoreNotices) }()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(starts) == 2 })
	cancel()
	<-done

	if gap := starts[1].Sub(starts[0]); gap < retryBackoff/2 {
		t.Errorf("reconnected after %v, want at least %v", gap, retryBackoff/2)
	}
}

func TestTailAsksAgainAfterBeingThrottled(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(call int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		if call < 3 {
			return status.Error(codes.ResourceExhausted, "too many tail sessions")
		}
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(since, "in")}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var seen atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, client, "acme-prod", webQuery(), func([]Event) error { seen.Add(1); return nil }, ignoreNotices)
	}()
	waitFor(t, func() bool { return seen.Load() == 1 })
	cancel()

	if err := <-done; err != nil {
		t.Errorf("Tail after cancel: %v", err)
	}
	if n := len(fake.received()); n != 3 {
		t.Errorf("sessions opened = %d, want 3: two throttled, one served", n)
	}
}

func TestTailFailsWhenEveryAttemptIsThrottled(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(int, loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		return status.Error(codes.ResourceExhausted, "too many tail sessions")
	})

	err := Tail(context.Background(), client, "acme-prod", webQuery(), ignoreEvents, ignoreNotices)

	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("Tail = %v, want the throttle", err)
	}
	if n := len(fake.received()); n != retryAttempts {
		t.Errorf("sessions opened = %d, want %d", n, retryAttempts)
	}
}

func TestTailReturnsAStreamErrorWithoutReconnecting(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(int, loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		return status.Error(codes.PermissionDenied, "logging.logEntries.list denied")
	})

	err := Tail(context.Background(), client, "acme-prod", webQuery(), ignoreEvents, ignoreNotices)

	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Tail = %v, want the permission refusal", err)
	}
	if !strings.Contains(err.Error(), "acme-prod") {
		t.Errorf("Tail = %v, want it to name the project", err)
	}
	if n := len(fake.received()); n != 1 {
		t.Errorf("sessions opened = %d, want 1: a refusal is not a stream ending", n)
	}
}

func TestTailStopsWhenEmitFails(t *testing.T) {
	t.Parallel()
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(since, "line")}})
	})
	broken := errors.New("the reader went away")

	err := Tail(context.Background(), client, "acme-prod", webQuery(), func([]Event) error { return broken }, ignoreNotices)

	if !errors.Is(err, broken) {
		t.Errorf("Tail = %v, want the emit error", err)
	}
}

func TestTailStopsWhenNoticeFails(t *testing.T) {
	t.Parallel()
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{
			SuppressionInfo: []*loggingpb.TailLogEntriesResponse_SuppressionInfo{
				{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_RATE_LIMIT, SuppressedCount: 1},
			},
		})
	})
	broken := errors.New("the reader went away")

	err := Tail(context.Background(), client, "acme-prod", webQuery(), ignoreEvents, func(Notice) error { return broken })

	if !errors.Is(err, broken) {
		t.Errorf("Tail = %v, want the notice error", err)
	}
}

func TestTailStopsWhenTheReconnectNoticeFails(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, func(int, loggingpb.LoggingServiceV2_TailLogEntriesServer) error { return nil })
	broken := errors.New("the reader went away")

	err := Tail(context.Background(), client, "acme-prod", webQuery(), ignoreEvents, func(Notice) error { return broken })

	if !errors.Is(err, broken) {
		t.Errorf("Tail = %v, want the notice error", err)
	}
	if n := len(fake.received()); n != 1 {
		t.Errorf("sessions opened = %d, want 1: no reconnect once the reader is gone", n)
	}
}

func TestTailOpensNoStreamWithoutSources(t *testing.T) {
	t.Parallel()
	client, fake := serveTail(t, hold)

	err := Tail(context.Background(), client, "acme-prod", Query{Since: since}, ignoreEvents, ignoreNotices)

	if err != nil {
		t.Fatalf("Tail = %v, want nil", err)
	}
	if n := len(fake.received()); n != 0 {
		t.Errorf("sessions opened = %d, want 0", n)
	}
}

func TestTailWritesANonFiniteNumberInAJSONPayloadAsAString(t *testing.T) {
	t.Parallel()
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		odd := streamedEntry(since, "")
		odd.Payload = &loggingpb.LogEntry_JsonPayload{JsonPayload: &structpb.Struct{Fields: map[string]*structpb.Value{
			"ratio":   structpb.NewNumberValue(math.NaN()),
			"ceiling": structpb.NewNumberValue(math.Inf(1)),
			"floor":   structpb.NewNumberValue(math.Inf(-1)),
		}}}
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{odd}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	texts := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Tail(ctx, client, "acme-prod", webQuery(), func(events []Event) error {
			for _, event := range events {
				texts <- event.Text
			}
			return nil
		}, ignoreNotices)
	}()
	got := <-texts
	cancel()
	<-done

	if want := `{"ceiling":"Infinity","floor":"-Infinity","ratio":"NaN"}`; got != want {
		t.Errorf("text = %s, want %s", got, want)
	}
}

func TestTailEndsTheStreamWhenEmitFails(t *testing.T) {
	t.Parallel()
	ended := make(chan struct{})
	client, _ := serveTail(t, func(_ int, stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
		defer close(ended)
		return sendThenHold(stream, &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{streamedEntry(since, "line")}})
	})

	err := Tail(context.Background(), client, "acme-prod", webQuery(), func([]Event) error { return errors.New("the reader went away") }, ignoreNotices)
	if err == nil {
		t.Fatal("Tail = nil, want the emit error")
	}

	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Error("the server's stream is still open after Tail returned")
	}
}
