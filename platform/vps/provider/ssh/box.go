package ssh

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Conn interface {
	Stream(ctx context.Context, command string, stdin io.Reader) (Result, error)
	RunLines(ctx context.Context, command string, each func(Line) error) error
	Run(ctx context.Context, command string) (string, error)
	Preflight(ctx context.Context) (Facts, error)
	Destination() Destination
	ForwardPort(ctx context.Context, remote string) (string, func(), error)
	ForwardSpans() string
}

type Dial func(ctx context.Context) (Conn, error)

type Acting int

const (
	Login Acting = iota
	Root
	RootWhenAble
	Docker
	Deploy
	Granted
)

const (
	AMD64 = "amd64"
	ARM64 = "arm64"
)

const DockerReach = "docker version"

type Box struct {
	dial Dial
	wait func(context.Context, time.Duration) error

	mu        sync.Mutex
	principal string

	preflighting sync.Mutex
	preflighted  bool
	preflightErr error
	login        memo[bool]
	docker       memo[prefix]
	arch         memo[string]

	takesTraceFlags func(context.Context) (bool, error)
	traceFlagsTaken memo[bool]
}

func NewBox(dial Dial, wait func(context.Context, time.Duration) error) *Box {
	if wait == nil {
		wait = waited
	}
	return &Box{dial: dial, wait: wait}
}

func waited(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type memo[T any] struct {
	mu    sync.Mutex
	done  bool
	value T
}

func (m *memo[T]) get(read func() (T, error)) (T, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return m.value, nil
	}
	value, err := read()
	if err == nil {
		m.done, m.value = true, value
	}
	return value, err
}

func (m *memo[T]) set(value T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done, m.value = true, value
}

func (m *memo[T]) forget() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var zero T
	m.done, m.value = false, zero
}

type prefix string

const (
	asLogin prefix = ""
	viaSudo prefix = "sudo -n "
)

func (p prefix) wrapping(command string) string {
	if p == asLogin {
		return command
	}
	return string(p) + "sh -c " + shell.Quote(command)
}

func (p prefix) invoking(argv []string) string { return string(p) + shell.Words(argv) }

func (p prefix) sudo() bool { return strings.HasPrefix(string(p), string(viaSudo)) }

func (b *Box) conn(ctx context.Context) (Conn, error) {
	conn, err := b.dial(ctx)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.principal = conn.Destination().Principal()
	b.mu.Unlock()
	return conn, nil
}

func (b *Box) Named() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.principal == "" {
		return "this host"
	}
	return b.principal
}

func (b *Box) Destination(ctx context.Context) (Destination, error) {
	conn, err := b.conn(ctx)
	if err != nil {
		return Destination{}, err
	}
	return conn.Destination(), nil
}

func (b *Box) Principal(ctx context.Context) (string, error) {
	dest, err := b.Destination(ctx)
	return dest.Principal(), err
}

func (b *Box) Address(ctx context.Context) (string, error) {
	dest, err := b.Destination(ctx)
	if err != nil {
		return "", err
	}
	if dest.Address == "" {
		return "", refusal.Refuse(refusal.CodeNotReady, "%s resolves to no address", b.Named())
	}
	return dest.Address, nil
}

func (b *Box) KnownHostRemoval(ctx context.Context) (string, error) {
	dest, err := b.Destination(ctx)
	return dest.Forget(), err
}

func (b *Box) ForwardPort(ctx context.Context, remote string) (string, func(), error) {
	conn, err := b.conn(ctx)
	if err != nil {
		return "", nil, err
	}
	return conn.ForwardPort(ctx, remote)
}

func (b *Box) Wait(ctx context.Context, delay time.Duration) error { return b.wait(ctx, delay) }

func Architecture(reported string) (string, error) {
	switch reported {
	case "x86_64", AMD64:
		return AMD64, nil
	case "aarch64", ARM64:
		return ARM64, nil
	default:
		return "", refusal.Refuse(refusal.CodeDenied,
			"this host's architecture %q is not %s or %s", reported, AMD64, ARM64)
	}
}

func (b *Box) Arch(ctx context.Context) (string, error) {
	return b.arch.get(func() (string, error) {
		said, err := b.Run(ctx, Login, "ask this host what it runs on", "uname -m", nil)
		if err != nil {
			return "", err
		}
		return Architecture(strings.TrimSpace(said))
	})
}

func (b *Box) Elevation(ctx context.Context) error {
	_, err := b.elevated(ctx)
	return err
}

func (b *Box) elevated(ctx context.Context) (prefix, error) {
	if err := b.preflightOnce(ctx); err != nil {
		return asLogin, err
	}
	return b.root(ctx)
}

func (b *Box) preflightOnce(ctx context.Context) error {
	b.preflighting.Lock()
	defer b.preflighting.Unlock()
	if b.preflighted {
		return b.preflightErr
	}
	conn, err := b.conn(ctx)
	if err != nil {
		return err
	}
	facts, err := conn.Preflight(ctx)
	b.preflighted, b.preflightErr = true, err
	if err == nil {
		b.login.set(facts.Root)
	}
	return err
}

func (b *Box) root(ctx context.Context) (prefix, error) {
	root, err := b.login.get(func() (bool, error) {
		conn, err := b.conn(ctx)
		if err != nil {
			return false, err
		}
		said, err := conn.Run(ctx, "id -u")
		return strings.TrimSpace(said) == "0", err
	})
	if err != nil {
		return asLogin, err
	}
	if root {
		return asLogin, nil
	}
	return viaSudo, nil
}

func (b *Box) deploying(ctx context.Context) (prefix, error) {
	dest, err := b.Destination(ctx)
	if err != nil {
		return asLogin, err
	}
	if dest.User == boxapi.DeployLogin {
		return asLogin, nil
	}
	elevation, err := b.elevated(ctx)
	if err != nil {
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			return asLogin, err
		}
		return asLogin, refusal.Refuse(refusal.CodeDenied,
			"%s belongs to %s, and %s cannot act as it: %s\nDeploy as %s (set `user` under `ssh` to it), or as a login with sudo without a password",
			boxapi.StateRoot, boxapi.DeployLogin, dest.User, refused.Message, boxapi.DeployLogin)
	}
	if elevation == asLogin {
		return prefix("runuser -u " + boxapi.DeployLogin + " -- env PATH=" + boxapi.DeployPath + " "), nil
	}
	return elevation + prefix("-u "+boxapi.DeployLogin+" env PATH="+boxapi.DeployPath+" "), nil
}

func (b *Box) dockering(ctx context.Context) (prefix, error) {
	return b.docker.get(func() (prefix, error) {
		if deploy, ok := b.deployLoginOnBox(ctx); ok {
			result, err := b.streamAs(ctx, deploy, DockerReach+" >/dev/null", nil)
			if err != nil {
				return asLogin, err
			}
			if result.Code != 0 {
				return asLogin, refusal.Refuse(refusal.CodeNotReady,
					"%s cannot run docker as %s: %s", b.Named(), boxapi.DeployLogin, Reason(result))
			}
			return deploy, nil
		}
		result, err := b.streamAs(ctx, asLogin, DockerReach+" >/dev/null", nil)
		if err != nil {
			return asLogin, err
		}
		if result.Code == 0 {
			return asLogin, nil
		}
		if !IsSocketDenied(result.Stderr) {
			return asLogin, refusal.Refuse(refusal.CodeNotReady, "%s cannot run docker: %s", b.Named(), Reason(result))
		}
		return b.elevated(ctx)
	})
}

func (b *Box) deployLoginOnBox(ctx context.Context) (prefix, bool) {
	dest, err := b.Destination(ctx)
	if err != nil || dest.User == boxapi.DeployLogin {
		return asLogin, false
	}
	result, err := b.streamAs(ctx, asLogin, "id -u "+shell.Quote(boxapi.DeployLogin)+" >/dev/null 2>&1 && [ -x "+shell.Quote(boxapi.DockerShims+"/docker")+" ]", nil)
	if err != nil || result.Code != 0 {
		return asLogin, false
	}
	deploy, err := b.deploying(ctx)
	if err != nil {
		return asLogin, false
	}
	return deploy, true
}

func (b *Box) ForgetDocker() { b.docker.forget() }

func IsSocketDenied(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "permission denied")
}

func (b *Box) prefixFor(ctx context.Context, as Acting) (prefix, error, error) {
	switch as {
	case Root:
		p, err := b.elevated(ctx)
		return p, nil, err
	case RootWhenAble:
		p, err := b.elevated(ctx)
		if err != nil {
			return asLogin, err, nil
		}
		return p, nil, nil
	case Docker:
		p, err := b.dockering(ctx)
		return p, nil, err
	case Deploy:
		p, err := b.deploying(ctx)
		return p, nil, err
	case Granted:
		p, err := b.root(ctx)
		return p, nil, err
	default:
		return asLogin, nil, nil
	}
}

func (b *Box) streamAs(ctx context.Context, p prefix, command string, stdin io.Reader) (Result, error) {
	conn, err := b.conn(ctx)
	if err != nil {
		return Result{}, err
	}
	result, err := conn.Stream(ctx, p.wrapping(exportingSpans(ctx, conn, command)), stdin)
	result.sudo = p.sudo()
	return result, err
}

func (b *Box) TraceFlags(ctx context.Context) []string {
	conn, err := b.conn(ctx)
	if err != nil {
		return nil
	}
	parent, socket := spanDestination(ctx, conn)
	if parent == "" || !b.isTraceFlagsTaken(ctx) {
		return nil
	}
	return []string{"--" + boxapi.TraceParentFlag + "=" + parent, "--" + boxapi.TraceSocketFlag + "=" + socket}
}

func (b *Box) SendTraceFlagsWhen(takes func(context.Context) (bool, error)) {
	b.takesTraceFlags = takes
}

func (b *Box) isTraceFlagsTaken(ctx context.Context) bool {
	taken, _ := b.traceFlagsTaken.get(func() (bool, error) {
		if b.takesTraceFlags == nil {
			return false, nil
		}
		takes, err := b.takesTraceFlags(ctx)
		return err == nil && takes, nil
	})
	return taken
}

func exportingSpans(ctx context.Context, conn Conn, command string) string {
	parent, socket := spanDestination(ctx, conn)
	if parent == "" {
		return command
	}
	return "export " + boxapi.TraceParentEnvVar + "=" + shell.Quote(parent) + " " + boxapi.TraceSocketEnvVar + "=" + shell.Quote(socket) + "; " + command
}

func spanDestination(ctx context.Context, conn Conn) (string, string) {
	if !trace.SpanContextFromContext(ctx).IsSampled() {
		return "", ""
	}
	parent := tracing.TraceParent(ctx)
	if parent == "" {
		return "", ""
	}
	socket := conn.ForwardSpans()
	if socket == "" {
		return "", ""
	}
	return parent, socket
}

func (b *Box) Assume(ctx context.Context, as Acting) error {
	_, _, err := b.prefixFor(ctx, as)
	return err
}

func (b *Box) Stream(ctx context.Context, as Acting, command string, stdin io.Reader) (Result, error) {
	p, unelevated, err := b.prefixFor(ctx, as)
	if err != nil {
		return Result{}, err
	}
	result, err := b.streamAs(ctx, p, command, stdin)
	result.unelevated = unelevated
	return result, err
}

func startCommandSpan(ctx context.Context, what string) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(ctx, "run box command", trace.WithAttributes(attribute.String("vps.command.purpose", what)))
}

func (b *Box) Run(ctx context.Context, as Acting, what, command string, stdin io.Reader) (_ string, err error) {
	ctx, span := startCommandSpan(ctx, what)
	defer func() { tracing.EndSpan(span, err) }()
	result, err := b.Stream(ctx, as, command, stdin)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", b.Refuse(what, result)
	}
	return result.Stdout, nil
}

func (b *Box) Attempt(ctx context.Context, as Acting, what, command string, stdin io.Reader) (_, _ string, err error) {
	ctx, span := startCommandSpan(ctx, what)
	defer func() { tracing.EndSpan(span, err) }()
	result, err := b.Stream(ctx, as, command, stdin)
	if err != nil {
		return "", "", err
	}
	if result.Code != 0 {
		return "", Reason(result), b.Refuse(what, result)
	}
	return result.Stdout, "", nil
}

type LiveCommand struct {
	Fixed []string
	Rest  []string
}

func (c LiveCommand) Argv(traceFlags []string) []string {
	return slices.Concat(c.Fixed, traceFlags, c.Rest)
}

func (b *Box) RunLive(ctx context.Context, as Acting, what string, command LiveCommand, stdin io.Reader) (_ string, err error) {
	ctx, span := startCommandSpan(ctx, what)
	defer func() { tracing.EndSpan(span, err) }()
	return b.runArgv(ctx, as, what, command.Argv(b.TraceFlags(ctx)), stdin)
}

func (b *Box) StreamLive(ctx context.Context, as Acting, what string, command LiveCommand, stdin io.Reader) (_ Result, err error) {
	ctx, span := startCommandSpan(ctx, what)
	defer func() { tracing.EndSpan(span, err) }()
	return b.Stream(ctx, as, shell.Words(command.Argv(b.TraceFlags(ctx))), stdin)
}

func (b *Box) runArgv(ctx context.Context, as Acting, what string, argv []string, stdin io.Reader) (string, error) {
	p, unelevated, err := b.prefixFor(ctx, as)
	if err != nil {
		return "", err
	}
	conn, err := b.conn(ctx)
	if err != nil {
		return "", err
	}
	result, err := conn.Stream(ctx, p.invoking(argv), stdin)
	if err != nil {
		return "", err
	}
	result.sudo, result.unelevated = p.sudo(), unelevated
	if result.Code != 0 {
		return "", b.Refuse(what, result)
	}
	return result.Stdout, nil
}

func (b *Box) RunLines(ctx context.Context, as Acting, command string, each func(Line) error) error {
	p, _, err := b.prefixFor(ctx, as)
	if err != nil {
		return err
	}
	conn, err := b.conn(ctx)
	if err != nil {
		return err
	}
	return conn.RunLines(ctx, p.wrapping(exportingSpans(ctx, conn, command)), each)
}

func (b *Box) Said(ctx context.Context, as Acting, command string) (string, error) {
	result, err := b.Stream(ctx, as, command, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout + result.Stderr), nil
}

func (b *Box) Evidence(ctx context.Context, as Acting, command string) string {
	said, err := b.Said(ctx, as, command)
	if err != nil {
		return "(could not be read: " + err.Error() + ")"
	}
	return said
}

const SaidLines = 4

func IsUnstarted(code int) bool { return code == 126 || code == 127 }

func (b *Box) Refuse(what string, result Result) error {
	failed := refusal.CodeNotReady
	if IsSudoRefused(result) {
		failed = refusal.CodeDenied
	}
	why := refusal.Refuse(failed, "%s on %s: %s", what, b.Named(), Reason(result))
	if result.unelevated == nil {
		return why
	}
	return refusal.Refuse(refusal.CodeNotReady, "%v\ncould not elevate: %v", why, result.unelevated)
}

var sudoRefusals = []string{"sudo: a password is required", "is not in the sudoers file", "is not allowed to execute"}

func IsSudoRefused(result Result) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(result.Stderr), "\n")
	return result.sudo && result.Code == 1 && slices.ContainsFunc(sudoRefusals, func(message string) bool { return strings.Contains(first, message) })
}

func Reason(result Result) string {
	said := strings.TrimSpace(result.Stderr)
	if said == "" && IsUnstarted(result.Code) {
		said = strings.TrimSpace(result.Stdout)
	}
	if said == "" {
		return "no reason given"
	}
	if lines := strings.Split(said, "\n"); len(lines) > SaidLines {
		said = strings.Join(lines[:SaidLines], "\n")
	}
	return said
}
