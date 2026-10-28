package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/content"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	spanFlushBudget  = 2 * time.Second
	spanService      = "ocel-live"
	spanHost         = "ocel-spans"
	answerLimitBytes = 64 << 10

	exportedResourceSpans protowire.Number = 1
	scope                                  = "github.com/ocelhq/ocel/platform/vps/box/cmd/live"
)

type spanExport struct {
	parent string
	socket string
}

func (e *spanExport) register(flags *flag.FlagSet) {
	flags.StringVar(&e.parent, boxapi.TraceParentFlag, "", "the W3C traceparent of the provider's span this command runs under")
	flags.StringVar(&e.socket, boxapi.TraceSocketFlag, "", "the socket the provider forwards this command's spans back over")
}

func leadingSpanExport(argv []string) (spanExport, []string) {
	var export spanExport
	for len(argv) > 0 {
		if parent, ok := strings.CutPrefix(argv[0], "--"+boxapi.TraceParentFlag+"="); ok {
			export.parent = parent
		} else if socket, ok := strings.CutPrefix(argv[0], "--"+boxapi.TraceSocketFlag+"="); ok {
			export.socket = socket
		} else {
			break
		}
		argv = argv[1:]
	}
	return export, argv
}

func (e spanExport) start(ctx context.Context, name string, args []string) (context.Context, func(code int)) {
	ctx, flush := e.export(ctx)
	ctx, span := otel.Tracer(scope).Start(content.Omit(ctx), name, trace.WithAttributes(tracing.CommandArgsKey.StringSlice(args)))
	return ctx, func(code int) {
		span.SetAttributes(tracing.ExitCodeKey.Int(code))
		if code != 0 {
			span.SetStatus(codes.Error, fmt.Sprintf("exited %d", code))
		}
		span.End()
		flush()
	}
}

func (e spanExport) export(ctx context.Context) (context.Context, func()) {
	if e.parent == "" || e.socket == "" {
		return ctx, func() {}
	}
	parented := tracing.ContextWithTraceParent(ctx, e.parent)
	if !trace.SpanContextFromContext(parented).IsValid() {
		return ctx, func() {}
	}
	caller, err := invokingUID(os.Geteuid(), os.LookupEnv)
	if err != nil {
		return ctx, func() {}
	}
	exporter := otlptrace.NewUnstarted(collectorClient{http: &http.Client{
		Timeout:   spanFlushBudget,
		Transport: &http.Transport{DialContext: dialSpans(e.socket, caller)},
	}})
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", spanService))),
	)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {}))
	otel.SetTracerProvider(provider)
	return parented, func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spanFlushBudget)
		defer cancel()
		_ = provider.Shutdown(flushCtx)
	}
}

type collectorClient struct{ http *http.Client }

func (collectorClient) Start(context.Context) error { return nil }

func (collectorClient) Stop(context.Context) error { return nil }

func (c collectorClient) UploadTraces(ctx context.Context, spans []*tracev1.ResourceSpans) error {
	var body []byte
	for _, resource := range spans {
		raw, err := proto.Marshal(resource)
		if err != nil {
			return err
		}
		body = protowire.AppendBytes(protowire.AppendTag(body, exportedResourceSpans, protowire.BytesType), raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+spanHost+tracing.CollectorPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", tracing.CollectorMediaType)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, answerLimitBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the collector answered %s", resp.Status)
	}
	return nil
}

func invokingUID(euid int, lookup func(string) (string, bool)) (uint32, error) {
	if euid != 0 {
		return uint32(euid), nil
	}
	named, ok := lookup(sudoUIDEnv)
	if !ok {
		return 0, nil
	}
	uid, err := strconv.ParseUint(named, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s=%q names no uid", sudoUIDEnv, named)
	}
	return uint32(uid), nil
}

func dialSpans(socket string, caller uint32) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		resolved, err := resolveCallersSocket(socket, caller)
		if err != nil {
			return nil, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", resolved)
		if err != nil {
			return nil, err
		}
		if err := refuseListenerOtherThan(conn, caller); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

func resolveCallersSocket(socket string, caller uint32) (string, error) {
	if !filepath.IsAbs(socket) || slices.Contains(strings.Split(socket, "/"), "..") {
		return "", fmt.Errorf("%s climbs a directory with .. or is not absolute", socket)
	}
	resolved, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return "", err
	}
	dir, err := readOwned(filepath.Dir(resolved), caller)
	if err != nil {
		return "", err
	}
	if !dir.IsDir() || dir.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is not a directory only its owner can enter", filepath.Dir(resolved))
	}
	listening, err := readOwned(resolved, caller)
	if err != nil {
		return "", err
	}
	if listening.Mode().Type() != os.ModeSocket {
		return "", fmt.Errorf("%s is not a socket", resolved)
	}
	return resolved, nil
}

func readOwned(path string, caller uint32) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != caller {
		return nil, fmt.Errorf("%s is not owned by uid %d, who ran this command", path, caller)
	}
	return info, nil
}

func refuseListenerOtherThan(conn net.Conn, caller uint32) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("the span socket is not a unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var asked error
	if err := raw.Control(func(fd uintptr) {
		cred, asked = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if asked != nil {
		return asked
	}
	if cred.Uid != caller {
		return fmt.Errorf("the span socket is answered by uid %d, not uid %d, who ran this command", cred.Uid, caller)
	}
	return nil
}
