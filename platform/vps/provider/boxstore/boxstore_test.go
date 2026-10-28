package boxstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"go.opentelemetry.io/otel/trace"
)

var builtAgent = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "ocel-live")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "live")
	build := exec.Command("go", "build", "-o", binary, "./cmd/live")
	build.Dir = filepath.Join("..", "..", "box")
	if said, err := build.CombinedOutput(); err != nil {
		return "", errors.New(string(said))
	}
	return binary, nil
})

type agentHere struct {
	t         *testing.T
	agent     string
	stateRoot string
	tierRoot  string
	spans     string
	current   bool
	ran       []string
}

func newAgentHere(t *testing.T) *agentHere {
	t.Helper()
	agent, err := builtAgent()
	if err != nil {
		t.Fatalf("build ocel-live: %v", err)
	}
	here := &agentHere{t: t, agent: agent, stateRoot: t.TempDir(), tierRoot: t.TempDir()}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := os.MkdirAll(boxapi.KeyValuesDir(here.stateRoot, tier), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(here.tierRoot, string(tier)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return here
}

func (h *agentHere) box() *ssh.Box {
	box := ssh.NewBox(func(context.Context) (ssh.Conn, error) { return h, nil }, nil)
	box.SendTraceFlagsWhen(func(context.Context) (bool, error) { return h.current, nil })
	return box
}

func (h *agentHere) predatingTraceFlags() {
	h.t.Helper()
	old := filepath.Join(h.t.TempDir(), "live")
	script := "#!/bin/sh\nfor arg; do case \"$arg\" in --trace*) echo \"flag provided but not defined: ${arg%%=*}\" >&2; exit 2;; esac; done\nexec " + shell.Quote(h.agent) + " \"$@\"\n"
	if err := os.WriteFile(old, []byte(script), 0o755); err != nil {
		h.t.Fatal(err)
	}
	h.agent, h.current = old, false
}

func (h *agentHere) Stream(ctx context.Context, command string, stdin io.Reader) (ssh.Result, error) {
	h.ran = append(h.ran, command)
	agent := shell.Quote(boxapi.LiveBinary)
	command = strings.Replace(command, agent+" "+shell.Quote(boxapi.RecordsCommand), shell.Quote(h.agent)+" kv --state-root "+shell.Quote(h.stateRoot), 1)
	command = strings.Replace(command, agent+" "+shell.Quote(boxapi.SealCommand), shell.Quote(h.agent)+" seal --tier-root "+shell.Quote(h.tierRoot), 1)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	case errors.As(err, &exited):
		return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: exited.ExitCode()}, nil
	default:
		return ssh.Result{}, err
	}
}

func (h *agentHere) RunLines(context.Context, string, func(ssh.Line) error) error { return nil }

func (h *agentHere) Run(ctx context.Context, command string) (string, error) {
	if command == "id -u" {
		return "0\n", nil
	}
	result, err := h.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (h *agentHere) Preflight(context.Context) (ssh.Facts, error) { return ssh.Facts{Root: true}, nil }

func (h *agentHere) Destination() ssh.Destination {
	return ssh.Destination{User: "root", Written: "box"}
}

func (h *agentHere) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("no port is forwarded here")
}

func (h *agentHere) ForwardSpans() string { return h.spans }

func TestTheRecordsTheProviderKeepsOnTheBoxConformThroughTheBoxAgent(t *testing.T) {
	conformance.RunStore(t, boxstore.NewKeyValues(newAgentHere(t).box()))
}

func TestAnEntryWrittenThroughTheAgentIsTheFileTheBoxReadsInProcess(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")
	revision, err := boxstore.NewKeyValues(here.box()).Write(context.Background(), records.Entry{Key: key, Value: json.RawMessage(`{"parts": ["cache"]}`)})
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	path, err := boxapi.PathOf(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(boxapi.KeyValuesDir(here.stateRoot, key.Partition.Tier), path+boxapi.EntrySuffix))
	if err != nil {
		t.Fatalf("the entry's file = %v", err)
	}
	var read boxapi.EntryFile
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatalf("the entry's file %s = %v", raw, err)
	}
	if read.Revision != revision || string(read.Value) != `{"parts":["cache"]}` {
		t.Errorf("the box reads %s at %s, want the compacted value at %s", read.Value, read.Revision, revision)
	}
}

func TestABoxWithNoAgentKeepsNoRecordsAndRefusesAWriteNamingBootstrap(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	here.agent = filepath.Join(t.TempDir(), "absent")
	store := boxstore.NewKeyValues(here.box())
	key := stackrecords.ProjectKey(environment.TierPreview, "shop")

	if _, err := store.Read(context.Background(), key); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() on a box with no agent = %v, want not found", err)
	}
	if listed, err := store.List(context.Background(), key.Partition); err != nil || len(listed) != 0 {
		t.Errorf("List() on a box with no agent = %v, %v, want nothing", listed, err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: key, Value: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("Write() on a box with no agent = %v, want a refusal naming bootstrap", err)
	}
}

func TestAValueTheProviderSealsThroughTheAgentOpensOnTheBoxAndNowhereElse(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	if said, err := exec.Command(here.agent, boxapi.SealCommand, "--tier-root", here.tierRoot, string(environment.TierProduction), boxapi.SealVerbInit).CombinedOutput(); err != nil {
		t.Fatalf("seal init = %v: %s", err, said)
	}
	cipher := boxstore.NewCipher(here.box())
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

	sealed, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("postgres://example"))
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	key, err := os.ReadFile(filepath.Join(here.tierRoot, string(environment.TierProduction), machine.SealKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fake.NewCipherWithKeys(map[environment.Tier][]byte{environment.TierProduction: key}).Open(context.Background(), environment.TierProduction, bound, sealed)
	if err != nil || string(opened) != "postgres://example" {
		t.Fatalf("the box opens %q, %v, want the value sealed", opened, err)
	}
	if _, err := cipher.Open(context.Background(), environment.TierProduction, seal.BindToProject("other", "variable", "production", "*", "/", "DATABASE_URL"), sealed); err == nil {
		t.Error("a value sealed for shop opened for other")
	}
	if want := shell.Words(boxstore.SealCommand(environment.TierProduction, boxapi.SealVerbSeal, bound).Argv(nil)); here.ran[0] != want {
		t.Errorf("the seal ran as %q, want %q: the sudoers line names exactly that command", here.ran[0], want)
	}
}

type collectedExports struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *collectedExports) received() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.bodies...)
}

func spanSocket(t *testing.T) (string, *collectedExports) {
	t.Helper()
	base, err := os.MkdirTemp("", "spans")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "otlp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	c := &collectedExports{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket, c
}

func TestASealAndARecordWriteUnderASpanHandOcelLiveTheirParentAndSocketWithinTheSudoersLineAndNeverTheToken(t *testing.T) {
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "s3cret-collector-token"})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	ctx, _ := tracingtest.Record(t)
	here := newAgentHere(t)
	socket, exports := spanSocket(t)
	here.spans, here.current = socket, true
	if said, err := exec.Command(here.agent, boxapi.SealCommand, "--tier-root", here.tierRoot, string(environment.TierProduction), boxapi.SealVerbInit).CombinedOutput(); err != nil {
		t.Fatalf("seal init = %v: %s", err, said)
	}
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

	if _, err := boxstore.NewCipher(here.box()).Seal(ctx, environment.TierProduction, bound, []byte("postgres://ada:hunter2@db")); err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if _, err := boxstore.NewKeyValues(here.box()).Write(ctx, records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "shop"), Value: json.RawMessage(`{"token":"sk_live_hunter2"}`)}); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	sudoers := shell.Words([]string{boxapi.LiveBinary, boxapi.SealCommand, string(environment.TierProduction), boxapi.SealVerbSeal}) + " "
	if sealing := here.ran[0]; !strings.HasPrefix(sealing, sudoers) || !strings.Contains(sealing, "--traceparent=") || !strings.Contains(sealing, "--trace-socket="+socket) {
		t.Errorf("the seal ran as %q, want the trace flags after %q, where the sudoers line matches anything", sealing, sudoers)
	}
	for _, ran := range here.ran {
		if strings.Contains(ran, "s3cret-collector-token") {
			t.Errorf("the box ran %q, holding the collector's token", ran)
		}
	}
	run := trace.SpanContextFromContext(ctx).TraceID()
	exported := bytes.Join(exports.received(), nil)
	for _, name := range []string{"ocel-live " + boxapi.SealCommand, "ocel-live " + boxapi.RecordsCommand} {
		if !bytes.Contains(exported, []byte(name)) {
			t.Errorf("the box exported no span named %q", name)
		}
	}
	if !bytes.Contains(exported, run[:]) {
		t.Errorf("the box's spans are not in the run's trace %s", run)
	}
	for _, secret := range []string{"postgres://ada:hunter2@db", "sk_live_hunter2"} {
		if bytes.Contains(exported, []byte(secret)) {
			t.Errorf("the box's spans hold %q", secret)
		}
	}
}

func TestTheBoxSpansOfASealAndARecordNestUnderTheirRunBoxCommandSpan(t *testing.T) {
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "s3cret-collector-token"})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	ctx, spans := tracingtest.Record(t)
	here := newAgentHere(t)
	socket, _ := spanSocket(t)
	here.spans, here.current = socket, true
	if said, err := exec.Command(here.agent, boxapi.SealCommand, "--tier-root", here.tierRoot, string(environment.TierProduction), boxapi.SealVerbInit).CombinedOutput(); err != nil {
		t.Fatalf("seal init = %v: %s", err, said)
	}
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

	if _, err := boxstore.NewCipher(here.box()).Seal(ctx, environment.TierProduction, bound, []byte("postgres://ada:hunter2@db")); err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if _, err := boxstore.NewKeyValues(here.box()).Write(ctx, records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "shop"), Value: json.RawMessage(`{"parts":["cache"]}`)}); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	commands := map[string]bool{}
	for _, span := range spans.Ended() {
		if span.Name() == "run box command" {
			commands[span.SpanContext().SpanID().String()] = true
		}
	}
	for _, ran := range here.ran {
		_, after, found := strings.Cut(ran, "--traceparent=")
		if !found {
			t.Errorf("the box ran %q with no trace parent", ran)
			continue
		}
		parts := strings.Split(strings.Fields(strings.Trim(after, "'"))[0], "-")
		if len(parts) != 4 || !commands[parts[2]] {
			t.Errorf("the box ran %q under a parent that is no run box command span of %v: its spans would be the command's siblings", ran, commands)
		}
	}
}

func TestASealAndARecordOnABoxWhoseOcelLivePredatesTraceFlagsRunUntraced(t *testing.T) {
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "s3cret-collector-token"})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	ctx, _ := tracingtest.Record(t)
	here := newAgentHere(t)
	socket, exports := spanSocket(t)
	here.spans = socket
	here.predatingTraceFlags()
	if said, err := exec.Command(here.agent, boxapi.SealCommand, "--tier-root", here.tierRoot, string(environment.TierProduction), boxapi.SealVerbInit).CombinedOutput(); err != nil {
		t.Fatalf("seal init = %v: %s", err, said)
	}
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")

	cipher := boxstore.NewCipher(here.box())
	sealed, err := cipher.Seal(ctx, environment.TierProduction, bound, []byte("postgres://ada:hunter2@db"))
	if err != nil {
		t.Fatalf("Seal() = %v, want a box bootstrapped before trace flags to seal as it did", err)
	}
	if opened, err := cipher.Open(ctx, environment.TierProduction, bound, sealed); err != nil || string(opened) != "postgres://ada:hunter2@db" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	store := boxstore.NewKeyValues(here.box())
	if _, err := store.Write(ctx, records.Entry{Key: key, Value: json.RawMessage(`{"parts":["cache"]}`)}); err != nil {
		t.Fatalf("Write() = %v, want a box bootstrapped before trace flags to keep records as it did", err)
	}
	if _, err := store.Read(ctx, key); err != nil {
		t.Fatalf("Read() = %v", err)
	}
	for _, ran := range here.ran {
		if strings.Contains(ran, "--trace") {
			t.Errorf("the box ran %q, handing trace flags to an ocel-live that refuses them", ran)
		}
	}
	if got := exports.received(); len(got) != 0 {
		t.Errorf("the box exported %d requests, want none from an ocel-live that takes no trace flags", len(got))
	}

	here.current = true
	if _, err := boxstore.NewCipher(here.box()).Seal(ctx, environment.TierProduction, bound, []byte("x")); err == nil {
		t.Error("the stand-in for an old ocel-live sealed with trace flags, want it to refuse them as the released builds do")
	}
}
