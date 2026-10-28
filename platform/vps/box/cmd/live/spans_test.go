package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/platform/vps/provider/dockerpolicy"
)

const (
	providerTrace = "0af7651916cd43dd8448eb211c80319c"
	providerSpan  = "b7ad6b7169203331"
	providerAt    = "00-" + providerTrace + "-" + providerSpan + "-01"
)

type collector struct {
	connections atomic.Int64

	mu      sync.Mutex
	spans   []*tracev1.Span
	headers []http.Header
}

func (c *collector) received() ([]*tracev1.Span, []http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*tracev1.Span(nil), c.spans...), append([]http.Header(nil), c.headers...)
}

func collectorOn(t *testing.T, mode os.FileMode) (string, *collector) {
	t.Helper()
	base, err := os.MkdirTemp("", "spans")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "d")
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "otlp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			req := &collectortrace.ExportTraceServiceRequest{}
			if err := proto.Unmarshal(raw, req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			c.mu.Lock()
			c.headers = append(c.headers, r.Header.Clone())
			for _, resource := range req.GetResourceSpans() {
				for _, scope := range resource.GetScopeSpans() {
					c.spans = append(c.spans, scope.GetSpans()...)
				}
			}
			c.mu.Unlock()
			answer, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
			w.Header().Set("Content-Type", "application/x-protobuf")
			_, _ = w.Write(answer)
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				c.connections.Add(1)
			}
		},
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket, c
}

func named(spans []*tracev1.Span, name string) *tracev1.Span {
	for _, span := range spans {
		if span.GetName() == name {
			return span
		}
	}
	return nil
}

func TestASealGivenATraceParentAndSocketExportsItsSpanUnderThatParentWithoutTheValue(t *testing.T) {
	socket, collected := collectorOn(t, 0o700)
	root := tierRoot(t)
	if got := sealed(t, root, "", "production", "init"); got.code != 0 {
		t.Fatalf("seal production init = %+v", got)
	}
	plaintext := "postgres://ada:hunter2@db.internal/shop"
	encoded := base64.StdEncoding.EncodeToString([]byte(plaintext))

	got := sealed(t, root, encoded+"\n", "production", "seal", "--traceparent="+providerAt, "--trace-socket="+socket, shopDatabase)
	if got.code != 0 {
		t.Fatalf("a traced seal = %+v, want it sealed as an untraced one is", got)
	}

	spans, headers := collected.received()
	command := named(spans, sealNamed)
	if command == nil {
		t.Fatalf("the collector received %d spans and none named %q", len(spans), sealNamed)
	}
	for _, span := range spans {
		if id := hex.EncodeToString(span.GetTraceId()); id != providerTrace {
			t.Errorf("span %q is in trace %s, want the provider's %s", span.GetName(), id, providerTrace)
		}
	}
	if parent := hex.EncodeToString(command.GetParentSpanId()); parent != providerSpan {
		t.Errorf("%q has parent %s, want the provider's span %s", sealNamed, parent, providerSpan)
	}
	exported := prototext.Format(&tracev1.ScopeSpans{Spans: spans})
	for _, secret := range []string{plaintext, encoded, strings.TrimSpace(got.stdout)} {
		if strings.Contains(exported, secret) {
			t.Errorf("the exported spans hold %q:\n%s", secret, exported)
		}
	}
	for _, header := range headers {
		if said := header.Get("Authorization"); said != "" {
			t.Errorf("the box sent Authorization %q, want no credential on the box: the provider's end of the forward adds it", said)
		}
	}
}

func TestACommandGivenNoTraceParentOrNoSocketConnectsToNothing(t *testing.T) {
	socket, collected := collectorOn(t, 0o700)
	root := stateRoot(t)

	for _, flags := range [][]string{
		nil,
		{"--trace-socket=" + socket},
		{"--traceparent=" + providerAt},
	} {
		if got := kv(t, root, "", append(flags, "production", "list", "p")...); got.code != 0 {
			t.Fatalf("kv %q list = %+v", flags, got)
		}
	}
	if n := collected.connections.Load(); n != 0 {
		t.Errorf("the collector took %d connections, want none from a command not handed both a traceparent and a socket", n)
	}
}

func TestASocketInADirectoryOthersCanEnterCarriesNoSpans(t *testing.T) {
	socket, collected := collectorOn(t, 0o755)
	root := stateRoot(t)

	if got := kv(t, root, "", "--traceparent="+providerAt, "--trace-socket="+socket, "production", "list", "p"); got.code != 0 {
		t.Fatalf("kv list = %+v, want the command run whether or not its spans can leave", got)
	}
	if n := collected.connections.Load(); n != 0 {
		t.Errorf("the collector took %d connections over a socket in a directory others can enter, want none: root would otherwise post to whatever socket an unprivileged caller names", n)
	}
}

func TestARecordsCommandExportsItsSpanWithoutTheValuesItWrites(t *testing.T) {
	socket, collected := collectorOn(t, 0o700)
	root := stateRoot(t)

	got := kv(t, root, `[{"path":"p/a","revision":"","value":{"token":"sk_live_hunter2"}}]`,
		"--traceparent="+providerAt, "--trace-socket="+socket, "production", "cas")
	if got.code != 0 {
		t.Fatalf("a traced cas = %+v", got)
	}
	spans, _ := collected.received()
	if named(spans, recordsNamed) == nil {
		t.Fatalf("the collector received %d spans and none named %q", len(spans), recordsNamed)
	}
	if exported := prototext.Format(&tracev1.ScopeSpans{Spans: spans}); strings.Contains(exported, "sk_live_hunter2") {
		t.Errorf("the exported spans hold the written value:\n%s", exported)
	}
}

func TestAPinGivenATraceParentAndSocketExportsItsSpan(t *testing.T) {
	socket, collected := collectorOn(t, 0o700)
	dir := t.TempDir()

	var stdout, stderr strings.Builder
	printPin(dir, []string{"--traceparent=" + providerAt, "--trace-socket=" + socket, "shop"}, &stdout, &stderr)

	spans, _ := collected.received()
	if span := named(spans, pinNamed); span == nil || hex.EncodeToString(span.GetParentSpanId()) != providerSpan {
		t.Errorf("the collector received %d spans, want one named %q under the provider's span", len(spans), pinNamed)
	}
}

func TestADockerArgvLedByTraceFlagsHandsDockerTheRestAndExportsTheDockerItRuns(t *testing.T) {
	socket, collected := collectorOn(t, 0o700)
	docker := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	errs, err := os.CreateTemp(t.TempDir(), "errs")
	if err != nil {
		t.Fatal(err)
	}
	defer errs.Close()

	export, rest := leadingSpanExport([]string{"--traceparent=" + providerAt, "--trace-socket=" + socket, "image", "inspect", "ghcr.io/shop/web@sha256:0"})
	if want := "image inspect ghcr.io/shop/web@sha256:0"; strings.Join(rest, " ") != want {
		t.Fatalf("the argv docker is handed is %q, want %q, the trace flags taken off its front", rest, want)
	}
	ctx, end := export.start(context.Background(), dockerNamed, rest)
	code := ranDocker(ctx, docker, dockerpolicy.Invocation{Argv: rest}, errs)
	end(code)
	if code != 0 {
		t.Fatalf("docker exited %d", code)
	}

	spans, _ := collected.received()
	command := named(spans, dockerNamed)
	if command == nil {
		t.Fatalf("the collector received %d spans and none named %q", len(spans), dockerNamed)
	}
	ran := named(spans, "docker")
	if ran == nil || string(ran.GetParentSpanId()) != string(command.GetSpanId()) {
		t.Errorf("%q holds no docker process among %d spans, want the docker it ran", dockerNamed, len(spans))
	}
}

func listenerCounting(t *testing.T, socket string) *atomic.Int64 {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	accepted := &atomic.Int64{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	return accepted
}

func TestASocketPathThatClimbsOutOfASymlinkIntoAnotherDirectoryConnectsToNothing(t *testing.T) {
	root, err := os.MkdirTemp("", "climb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{"private", 0o700}, {"public", 0o755}, {"elsewhere/x/y", 0o755}, {"k", 0o755}} {
		if err := os.MkdirAll(filepath.Join(root, dir.path), dir.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, dir.path), dir.mode); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"elsewhere/private": "public", "k/L": "elsewhere/x/y", "k/public": "public"} {
		if err := os.Symlink(filepath.Join(root, target), filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	accepted := listenerCounting(t, filepath.Join(root, "public", "S"))
	caller := uint32(os.Geteuid())

	for _, named := range []string{
		filepath.Join(root, "k", "L") + "/../../private/S",
		filepath.Join(root, "k", "public", "S"),
	} {
		if conn, err := dialSpans(named, caller)(context.Background(), "", ""); err == nil {
			conn.Close()
			t.Errorf("dialSpans(%q) connected, want it refused: the kernel resolves it to a socket in a directory others can enter", named)
		}
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener in the public directory took %d connections, want none", n)
	}
}

func TestASocketWhoseDirectoryOrListenerIsNotTheCallersConnectsToNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "otlp.sock")
	accepted := listenerCounting(t, socket)
	caller := uint32(os.Geteuid())

	if conn, err := dialSpans(socket, caller+1)(context.Background(), "", ""); err == nil {
		conn.Close()
		t.Errorf("dialSpans as uid %d connected to a socket uid %d listens on, want it refused", caller+1, caller)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener took %d connections from a command run for another user, want none", n)
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := refuseListenerOtherThan(conn, caller+1); err == nil {
		t.Errorf("a listener running as uid %d passed for uid %d", caller, caller+1)
	}
	if err := refuseListenerOtherThan(conn, caller); err != nil {
		t.Errorf("a listener running as the caller was refused: %v", err)
	}
}

func TestTheCallerIsTheUserWhoRanSudoOrElseTheProcessItself(t *testing.T) {
	for _, tc := range []struct {
		euid    int
		sudoUID string
		want    uint32
		refused bool
	}{
		{euid: 0, sudoUID: "1000", want: 1000},
		{euid: 0, want: 0},
		{euid: 1001, sudoUID: "0", want: 1001},
		{euid: 1001, want: 1001},
		{euid: 0, sudoUID: "ada", refused: true},
	} {
		lookup := func(name string) (string, bool) {
			if name == sudoUIDEnv && tc.sudoUID != "" {
				return tc.sudoUID, true
			}
			return "", false
		}
		got, err := invokingUID(tc.euid, lookup)
		if tc.refused {
			if err == nil {
				t.Errorf("invokingUID(%d, SUDO_UID=%q) = %d, want a refusal", tc.euid, tc.sudoUID, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("invokingUID(%d, SUDO_UID=%q) = %d, %v, want %d", tc.euid, tc.sudoUID, got, err, tc.want)
		}
	}
}
