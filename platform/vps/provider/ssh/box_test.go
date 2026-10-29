package ssh

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestWhatTheBoxIsAskedToDoIsASpanOverTheCommandsItTook(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	sshSaying(t, resolved, keyed, "echo 'no such unit' >&2; exit 5")
	session := &Session{target: Target{Host: "203.0.113.10", User: "root"}, dest: Destination{User: "root", Written: "203.0.113.10"}}
	box := NewBox(func(context.Context) (Conn, error) { return session, nil }, nil)

	for name, asked := range map[string]func(string) error{
		"Run": func(what string) error { _, err := box.Run(ctx, Login, what, "systemctl restart web", nil); return err },
		"Attempt": func(what string) error {
			_, _, err := box.Attempt(ctx, Login, what, "systemctl restart web", nil)
			return err
		},
		"RunLive": func(what string) error {
			_, err := box.RunLive(ctx, Login, what, LiveCommand{Fixed: []string{"systemctl", "restart"}, Rest: []string{"web"}}, nil)
			return err
		},
	} {
		what := "restart web through " + name
		if err := asked(what); err == nil {
			t.Fatalf("%s() = nil, want the refusal", name)
		}
		step := stepFor(t, spans.Ended(), what)
		if step.Name() != "run box command" || step.Status().Code != codes.Error {
			t.Errorf("%s span = %q with status %v, want run box command failed", name, step.Name(), step.Status().Code)
		}
		if commands := tracingtest.ChildrenOf(spans.Ended(), step); len(commands) != 1 || commands[0].Name() != "ssh" {
			t.Errorf("%s span holds %d spans, want the ssh process it ran", name, len(commands))
		}
	}
}

func stepFor(t *testing.T, spans []sdktrace.ReadOnlySpan, what string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if purpose, ok := tracingtest.Attributes(span)["vps.command.purpose"]; ok && purpose.AsString() == what {
			return span
		}
	}
	t.Fatalf("no span for %q", what)
	return nil
}

type forwardingSpans struct {
	socket string
	asked  []string
}

func (c *forwardingSpans) Stream(_ context.Context, command string, _ io.Reader) (Result, error) {
	c.asked = append(c.asked, command)
	return Result{}, nil
}

func (c *forwardingSpans) Run(context.Context, string) (string, error) { return "1000\n", nil }

func (c *forwardingSpans) RunLines(_ context.Context, command string, _ func(Line) error) error {
	c.asked = append(c.asked, command)
	return nil
}

func (c *forwardingSpans) Preflight(context.Context) (Facts, error) {
	return Facts{Sudo: true, Systemd: true}, nil
}

func (c *forwardingSpans) Destination() Destination {
	return Destination{Written: "box.example", Address: "203.0.113.7", Port: 22, User: "ubuntu"}
}

func (c *forwardingSpans) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("this box forwards no port")
}

func (c *forwardingSpans) ForwardSpans() string { return c.socket }

func TestACommandRunUnderASpanTellsTheBoxItsParentAndSocketInsideTheElevationAndNeverTheCollectorToken(t *testing.T) {
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{URL: "http://127.0.0.1:4318/v1/traces", Token: "s3cret-collector-token"})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	ctx, spans := tracingtest.Record(t)
	conn := &forwardingSpans{socket: "/tmp/ocel-spans.abc/otlp.sock"}
	box := NewBox(func(context.Context) (Conn, error) { return conn, nil }, nil)

	if _, err := box.Run(ctx, Root, "read what the box is told", "printenv "+boxapi.TraceParentEnvVar+" "+boxapi.TraceSocketEnvVar, nil); err != nil {
		t.Fatal(err)
	}
	if err := box.RunLines(ctx, Root, "printenv "+boxapi.TraceSocketEnvVar, func(Line) error { return nil }); err != nil {
		t.Fatal(err)
	}

	step := stepFor(t, spans.Ended(), "read what the box is told")
	parent := "00-" + step.SpanContext().TraceID().String() + "-" + step.SpanContext().SpanID().String() + "-01"
	for ran, want := range map[string]string{
		conn.asked[len(conn.asked)-2]: parent + "\n/tmp/ocel-spans.abc/otlp.sock\n",
		conn.asked[len(conn.asked)-1]: "/tmp/ocel-spans.abc/otlp.sock\n",
	} {
		inside, elevated := strings.CutPrefix(ran, string(viaSudo))
		if !elevated {
			t.Fatalf("the box ran %q, want it through sudo", ran)
		}
		told, err := exec.Command("/bin/sh", "-c", inside).Output()
		if err != nil || string(told) != want {
			t.Errorf("%q, run as sudo runs it with a reset environment, printed %q, %v, want %q", ran, told, err, want)
		}
	}
	for _, ran := range conn.asked {
		if strings.Contains(ran, "s3cret-collector-token") {
			t.Errorf("the box ran %q, holding the collector's token", ran)
		}
	}
}

func TestTheTraceFlagsNameTheCallersSpanAndTheForwardedSocket(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	forwarding := NewBox(func(context.Context) (Conn, error) {
		return &forwardingSpans{socket: "/tmp/ocel-spans.abc/otlp.sock"}, nil
	}, nil)
	unforwarded := NewBox(func(context.Context) (Conn, error) { return &forwardingSpans{}, nil }, nil)
	for _, box := range []*Box{forwarding, unforwarded} {
		box.SendTraceFlagsWhen(func(context.Context) (bool, error) { return true, nil })
	}

	parent := trace.SpanContextFromContext(ctx)
	want := []string{
		"--traceparent=00-" + parent.TraceID().String() + "-" + parent.SpanID().String() + "-01",
		"--trace-socket=/tmp/ocel-spans.abc/otlp.sock",
	}
	if got := forwarding.TraceFlags(ctx); !slices.Equal(got, want) {
		t.Errorf("TraceFlags() = %q, want %q", got, want)
	}
	if got := forwarding.TraceFlags(context.Background()); got != nil {
		t.Errorf("TraceFlags() outside any span = %q, want none", got)
	}
	if got := unforwarded.TraceFlags(ctx); got != nil {
		t.Errorf("TraceFlags() over a session that forwards no spans = %q, want none", got)
	}
}

func TestTraceFlagsGoOnlyToAnOcelLiveTheBoxIsKnownToTakeThemIn(t *testing.T) {
	ctx, _ := tracingtest.Record(t)
	forwarding := func(context.Context) (Conn, error) {
		return &forwardingSpans{socket: "/tmp/ocel-spans.abc/otlp.sock"}, nil
	}

	unasked := NewBox(forwarding, nil)
	if got := unasked.TraceFlags(ctx); got != nil {
		t.Errorf("TraceFlags() on a box no one said takes them = %q, want none", got)
	}

	for _, said := range []struct {
		named string
		takes bool
		err   error
	}{
		{named: "an ocel-live that predates them", takes: false},
		{named: "an ocel-live that could not be read", takes: true, err: errors.New("the survey failed")},
	} {
		asked := 0
		box := NewBox(forwarding, nil)
		box.SendTraceFlagsWhen(func(context.Context) (bool, error) {
			asked++
			return said.takes, said.err
		})
		for range 3 {
			if got := box.TraceFlags(ctx); got != nil {
				t.Errorf("TraceFlags() for %s = %q, want none: an old ocel-live refuses the flags and the seal, record or pin fails", said.named, got)
			}
		}
		if asked != 1 {
			t.Errorf("the box was asked %d times about %s, want once a session", asked, said.named)
		}
	}

	asked := 0
	takes := NewBox(forwarding, nil)
	takes.SendTraceFlagsWhen(func(context.Context) (bool, error) {
		asked++
		return true, nil
	})
	takes.TraceFlags(ctx)
	if got := takes.TraceFlags(ctx); len(got) != 2 {
		t.Errorf("TraceFlags() for an ocel-live that takes them = %q, want the parent and the socket", got)
	}
	if asked != 1 {
		t.Errorf("the box was asked %d times, want once a session", asked)
	}
	if takes.TraceFlags(context.Background()); asked != 1 {
		t.Errorf("a command outside any span asked the box whether it takes trace flags, want no question when there is nothing to send")
	}
}

func TestACommandOutsideAnySpanRunsAsWritten(t *testing.T) {
	conn := &forwardingSpans{socket: "/tmp/ocel-spans.abc/otlp.sock"}
	box := NewBox(func(context.Context) (Conn, error) { return conn, nil }, nil)

	if _, err := box.Run(context.Background(), Login, "pull web", "docker pull ghcr.io/shop/web", nil); err != nil {
		t.Fatal(err)
	}
	if ran := conn.asked[len(conn.asked)-1]; ran != "docker pull ghcr.io/shop/web" {
		t.Errorf("the box ran %q, want the command as written", ran)
	}
}

func TestTheDeployLoginRunsFromTheRootOfTheBoxNotTheLoginsHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	command := fromRoot + "sh -c " + shell.Quote("pwd")
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = home
	said, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	if got := strings.TrimSpace(string(said)); got != "/" {
		t.Errorf("the deploy login's command ran in %q, want / so a home it cannot enter is never its working directory", got)
	}
	argv := fromRoot + shell.Words([]string{"sh", "-c", "pwd"})
	cmd = exec.Command("/bin/sh", "-c", argv)
	cmd.Dir = home
	if said, err := cmd.Output(); err != nil || strings.TrimSpace(string(said)) != "/" {
		t.Errorf("an argv run as the deploy login ran in %q, %v, want /", said, err)
	}
}
