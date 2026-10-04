package gcp_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"google.golang.org/api/logging/v2"
	"google.golang.org/genproto/googleapis/api/monitoredres"
	logtype "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

type loggedGCP struct {
	mutex    sync.Mutex
	requests []logging.ListLogEntriesRequest
}

func (l *loggedGCP) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	var listed logging.ListLogEntriesRequest
	_ = json.NewDecoder(req.Body).Decode(&listed)
	l.mutex.Lock()
	l.requests = append(l.requests, listed)
	l.mutex.Unlock()
	entry := func(service, revision, text string) map[string]any {
		return map[string]any{
			"timestamp":   "2026-01-05T12:00:00Z",
			"textPayload": text,
			"severity":    "ERROR",
			"labels":      map[string]string{"instanceId": "instance-1"},
			"resource":    map[string]any{"labels": map[string]string{"service_name": service, "revision_name": revision}},
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"entries": []any{
		entry("web-svc", "web-svc-00002", "served"),
		entry("mailer-svc", "mailer-svc-00001", "sent"),
	}})
}

func TestLogsReadsEachTargetFromItsServiceAndRevision(t *testing.T) {
	stub := &loggedGCP{}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", server.URL)
	p, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}

	var entries []provider.LogEntry
	err = p.Logs().Read(context.Background(), provider.LogQuery{
		Targets: []provider.LogTarget{
			{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web-server", Physical: "web-svc", Revision: "web-svc-00002"}},
			{App: "web", Release: "r1", Source: "mailer", Container: &provider.AppContainer{Name: "worker:mailer", Physical: "mailer-svc", Revision: "mailer-svc-00001"}},
		},
		Since:    time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:    20,
		Contains: "boom",
	}, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(stub.requests) != 1 {
		t.Fatalf("Read() made %d list requests, want 1", len(stub.requests))
	}
	filter := stub.requests[0].Filter
	for _, want := range []string{
		`resource.labels.service_name="web-svc" AND resource.labels.revision_name="web-svc-00002"`,
		`resource.labels.service_name="mailer-svc" AND resource.labels.revision_name="mailer-svc-00001"`,
		`"boom"`,
	} {
		if !strings.Contains(filter, want) {
			t.Errorf("Read() filtered by %q, want it to contain %q", filter, want)
		}
	}

	if len(entries) != 2 {
		t.Fatalf("Read() emitted %d entries, want 2", len(entries))
	}
	got := map[string]provider.LogEntry{}
	for _, entry := range entries {
		got[entry.Message] = entry
	}
	if served := got["served"]; served.Source != "http" || served.App != "web" || served.Release != "r1" || served.Severity != "ERROR" || served.Instance != "instance-1" {
		t.Errorf("Read() entry for the web function = %+v, want it labelled web/r1/http with severity ERROR from instance-1", served)
	}
	if sent := got["sent"]; sent.Source != "mailer" {
		t.Errorf("Read() entry for the mailer = %+v, want source mailer", sent)
	}
}

func TestLogsReadsNothingFromAnEmptyTargetList(t *testing.T) {
	stub := &loggedGCP{}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", server.URL)
	p, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}

	err = p.Logs().Read(context.Background(), provider.LogQuery{Since: time.Now().Add(-time.Hour), Limit: 10}, func([]provider.LogEntry) error {
		t.Error("Read() emitted a batch for no targets")
		return nil
	}, nil)
	if err != nil {
		t.Errorf("Read() error = %v, want none", err)
	}
	if len(stub.requests) != 0 {
		t.Errorf("Read() made %d requests with no targets", len(stub.requests))
	}
}

func TestTheDeployCredentialIsGrantedTheRoleThatReadsLogEntries(t *testing.T) {
	p, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}

	for _, purpose := range []edge.CredentialPurpose{edge.PurposeDeploy, edge.PurposeBootstrap} {
		document, err := p.Credentials().Permissions(purpose)
		if err != nil {
			t.Fatalf("Permissions(%s) error = %v", purpose, err)
		}
		if !slices.Contains(strings.Split(document.Document, "\n"), "roles/logging.viewer") {
			t.Errorf("the %s credential is granted %q, want roles/logging.viewer among them: a log read lists Cloud Logging entries", purpose, document.Document)
		}
	}
}

type tailedGCP struct {
	loggingpb.UnimplementedLoggingServiceV2Server
	mutex    sync.Mutex
	requests []*loggingpb.TailLogEntriesRequest
	response *loggingpb.TailLogEntriesResponse

	endsFirstStream bool
}

func (l *tailedGCP) TailLogEntries(stream loggingpb.LoggingServiceV2_TailLogEntriesServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	l.mutex.Lock()
	l.requests = append(l.requests, req)
	first := len(l.requests) == 1
	l.mutex.Unlock()
	if err := stream.Send(l.response); err != nil {
		return err
	}
	if first && l.endsFirstStream {
		return nil
	}
	<-stream.Context().Done()
	return nil
}

func tailingGCP(t *testing.T, stub *tailedGCP) provider.Provider {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	loggingpb.RegisterLoggingServiceV2Server(server, stub)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", "http://"+listener.Addr().String())
	p, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}
	return p
}

func tailedEntry(service, revision, text string) *loggingpb.LogEntry {
	return &loggingpb.LogEntry{
		Timestamp: timestamppb.New(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)),
		Payload:   &loggingpb.LogEntry_TextPayload{TextPayload: text},
		Severity:  logtype.LogSeverity_ERROR,
		Labels:    map[string]string{"instanceId": "instance-1"},
		Resource:  &monitoredres.MonitoredResource{Type: "cloud_run_revision", Labels: map[string]string{"service_name": service, "revision_name": revision}},
	}
}

func webAndMailer() []provider.LogTarget {
	return []provider.LogTarget{
		{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web-server", Physical: "web-svc", Revision: "web-svc-00002"}},
		{App: "web", Release: "r1", Source: "mailer", Container: &provider.AppContainer{Name: "worker:mailer", Physical: "mailer-svc", Revision: "mailer-svc-00001"}},
	}
}

func TestLogsTailsEachTargetFromItsServiceAndRevision(t *testing.T) {
	stub := &tailedGCP{response: &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{
		tailedEntry("web-svc", "web-svc-00002", "served"),
		tailedEntry("mailer-svc", "mailer-svc-00001", "sent"),
	}}}
	p := tailingGCP(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var entries []provider.LogEntry
	err := p.Logs().Read(ctx, provider.LogQuery{Targets: webAndMailer(), Since: time.Now(), Contains: "boom", Tail: true}, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		cancel()
		return nil
	}, func(provider.LogNotice) error { return nil })
	if err != nil {
		t.Fatalf("Read() of a tail error = %v, want none once the caller cancelled", err)
	}

	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	if len(stub.requests) != 1 {
		t.Fatalf("the tail opened %d streams, want 1", len(stub.requests))
	}
	for _, want := range []string{
		`resource.labels.service_name="web-svc" AND resource.labels.revision_name="web-svc-00002"`,
		`resource.labels.service_name="mailer-svc" AND resource.labels.revision_name="mailer-svc-00001"`,
		`"boom"`,
	} {
		if !strings.Contains(stub.requests[0].Filter, want) {
			t.Errorf("the tail filtered by %q, want it to contain %q", stub.requests[0].Filter, want)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("the tail emitted %d entries, want 2", len(entries))
	}
	got := map[string]provider.LogEntry{}
	for _, entry := range entries {
		got[entry.Message] = entry
	}
	if served := got["served"]; served.Source != "http" || served.App != "web" || served.Release != "r1" || served.Severity != "ERROR" || served.Instance != "instance-1" {
		t.Errorf("the tail's entry for the web function = %+v, want it labelled web/r1/http with severity ERROR from instance-1", served)
	}
	if sent := got["sent"]; sent.Source != "mailer" {
		t.Errorf("the tail's entry for the mailer = %+v, want source mailer", sent)
	}
}

func TestLogsTailTurnsEverySuppressionIntoASampledNotice(t *testing.T) {
	stub := &tailedGCP{response: &loggingpb.TailLogEntriesResponse{SuppressionInfo: []*loggingpb.TailLogEntriesResponse_SuppressionInfo{
		{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_RATE_LIMIT, SuppressedCount: 4},
		{Reason: loggingpb.TailLogEntriesResponse_SuppressionInfo_NOT_CONSUMED, SuppressedCount: 9},
	}}}
	p := tailingGCP(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var notices []provider.LogNotice
	err := p.Logs().Read(ctx, provider.LogQuery{Targets: webAndMailer(), Since: time.Now(), Tail: true}, func([]provider.LogEntry) error { return nil }, func(notice provider.LogNotice) error {
		notices = append(notices, notice)
		if len(notices) == 2 {
			cancel()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read() of a tail error = %v", err)
	}
	want := []provider.LogNotice{{Kind: provider.LogSampled, Omitted: 4}, {Kind: provider.LogSampled, Omitted: 9}}
	if !slices.Equal(notices, want) {
		t.Errorf("the tail sent the notices %+v, want %+v", notices, want)
	}
}

func TestLogsTailTellsTheCallerWhenTheStreamReconnected(t *testing.T) {
	stub := &tailedGCP{endsFirstStream: true, response: &loggingpb.TailLogEntriesResponse{Entries: []*loggingpb.LogEntry{tailedEntry("web-svc", "web-svc-00002", "served")}}}
	p := tailingGCP(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var notices []provider.LogNotice
	var read int
	err := p.Logs().Read(ctx, provider.LogQuery{Targets: webAndMailer(), Since: time.Now(), Tail: true}, func(batch []provider.LogEntry) error {
		read += len(batch)
		if read == 2 {
			cancel()
		}
		return nil
	}, func(notice provider.LogNotice) error {
		notices = append(notices, notice)
		return nil
	})
	if err != nil {
		t.Fatalf("Read() of a tail error = %v", err)
	}
	if want := []provider.LogNotice{{Kind: provider.LogReconnected}}; !slices.Equal(notices, want) {
		t.Errorf("the tail sent the notices %+v, want %+v", notices, want)
	}
}

func TestLogsTailsNothingFromAnEmptyTargetList(t *testing.T) {
	stub := &tailedGCP{response: &loggingpb.TailLogEntriesResponse{}}
	p := tailingGCP(t, stub)

	err := p.Logs().Read(context.Background(), provider.LogQuery{Since: time.Now(), Tail: true}, func([]provider.LogEntry) error {
		t.Error("Read() of a tail emitted a batch for no targets")
		return nil
	}, func(provider.LogNotice) error { return nil })
	if err != nil {
		t.Errorf("Read() error = %v, want none", err)
	}
	if len(stub.requests) != 0 {
		t.Errorf("Read() of a tail opened %d streams with no targets", len(stub.requests))
	}
}
