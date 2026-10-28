package ssh

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	spanSocketFile  = "otlp.sock"
	spanHolderLimit = 5 * time.Second

	spanDirCommand = `d=$(mktemp -d "${TMPDIR:-/tmp}/ocel-spans.XXXXXXXXXX") || exit 1; ` +
		`trap 'rm -rf "$d"' EXIT; trap 'exit 1' HUP INT TERM; echo "$d"; cat >/dev/null`
)

type spanForward struct {
	remote  string
	local   string
	relay   *http.Server
	holder  *tracing.Cmd
	release io.Closer
	held    chan struct{}
	span    trace.Span
}

func (s *Session) ForwardSpans() string {
	s.spansMade.Do(func() { s.spans = s.forwardSpans() })
	if s.spans == nil {
		return ""
	}
	select {
	case <-s.spans.held:
		return ""
	default:
		return s.spans.remote
	}
}

func (s *Session) forwardSpans() *spanForward {
	collector := tracing.ReadCollector()
	if collector.URL == "" || s.control == "" {
		return nil
	}
	relay, err := tracing.NewCollectorProxy(collector)
	if err != nil {
		return nil
	}
	ctx, span := otel.Tracer(scope).Start(trace.ContextWithSpanContext(context.Background(), s.openedUnder), "forward spans from the box",
		trace.WithAttributes(attribute.String("ssh.principal", s.dest.Principal())))
	forward := &spanForward{local: s.control + "-spans", held: make(chan struct{}), span: span}
	if err := forward.listen(relay); err != nil {
		tracing.EndSpan(span, err)
		return nil
	}
	if err := s.holdSpanDir(ctx, forward); err != nil {
		forward.stop(ctx, s)
		tracing.EndSpan(span, err)
		return nil
	}
	span.SetAttributes(attribute.String("ssh.forward.remote", forward.remote), attribute.String("ssh.forward.local", forward.local))
	if _, err := output(ctx, "ssh", append(s.args(), "-O", "forward", "-R", forward.spec(), s.dest.Written)...); err != nil {
		forward.remote = ""
		forward.stop(ctx, s)
		tracing.EndSpan(span, err)
		return nil
	}
	return forward
}

func (f *spanForward) listen(relay http.Handler) error {
	if err := os.Remove(f.local); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", f.local)
	if err != nil {
		return err
	}
	if err := os.Chmod(f.local, 0o600); err != nil {
		listener.Close()
		return err
	}
	f.relay = &http.Server{Handler: relay, ReadHeaderTimeout: connectTimeout}
	go func() { _ = f.relay.Serve(listener) }()
	return nil
}

func (s *Session) holdSpanDir(ctx context.Context, f *spanForward) error {
	holder := tracing.Command(ctx, "ssh", append(s.args(), s.dest.Written, spanDirCommand)...)
	stdin, err := holder.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		return err
	}
	if err := holder.Start(); err != nil {
		return err
	}
	f.holder, f.release = holder, stdin
	go func() {
		_ = holder.Wait()
		close(f.held)
	}()
	made := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		made <- strings.TrimSpace(line)
	}()
	timer := time.NewTimer(connectTimeout)
	defer timer.Stop()
	select {
	case dir := <-made:
		if !strings.HasPrefix(dir, "/") {
			return errors.New("the box made no directory to hold the span socket")
		}
		f.remote = dir + "/" + spanSocketFile
		return nil
	case <-timer.C:
		return errors.New("the box made no directory to hold the span socket in time")
	}
}

func (f *spanForward) spec() string { return f.remote + ":" + f.local }

func (f *spanForward) stop(ctx context.Context, s *Session) {
	if f.remote != "" {
		_, _ = output(ctx, "ssh", append(s.args(), "-O", "cancel", "-R", f.spec(), s.dest.Written)...)
	}
	if f.holder != nil {
		_ = f.release.Close()
		select {
		case <-f.held:
		case <-time.After(spanHolderLimit):
			_ = f.holder.Process.Kill()
			<-f.held
		}
	}
	if f.relay != nil {
		_ = f.relay.Close()
		_ = os.Remove(f.local)
	}
}

func (s *Session) closeSpanForward() {
	s.spansMade.Do(func() {})
	if s.spans == nil {
		return
	}
	ctx := trace.ContextWithSpan(context.Background(), s.spans.span)
	s.spans.stop(ctx, s)
	s.spans.span.End()
}
