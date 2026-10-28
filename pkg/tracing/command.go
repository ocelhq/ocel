package tracing

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const (
	ExecutablePathKey   attribute.Key = "process.executable.path"
	CommandArgsKey      attribute.Key = "process.command_args"
	CommandLineKey      attribute.Key = "process.command_line"
	WorkingDirectoryKey attribute.Key = "process.working_directory"
	PIDKey              attribute.Key = "process.pid"
	ExitCodeKey         attribute.Key = "process.exit.code"
	StdoutTailKey       attribute.Key = "process.stdout.tail"
	StderrTailKey       attribute.Key = "process.stderr.tail"
	StdinBytesKey       attribute.Key = "process.stdin.bytes"
)

type Cmd struct {
	*exec.Cmd

	ExportsSpans bool

	exporting bool
	ctx       context.Context
	span      trace.Span
	attrs     []attribute.KeyValue
	stdin     *countedReader
	stdout    *Tail
	stderr    *Tail
	begun     sync.Once
	ended     sync.Once
}

func Command(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{Cmd: exec.Command(name, args...), ctx: ctx}
}

func CommandContext(ctx context.Context, name string, args ...string) *Cmd {
	return &Cmd{Cmd: exec.CommandContext(ctx, name, args...), ctx: ctx}
}

func (c *Cmd) SetAttributes(attrs ...attribute.KeyValue) {
	if c.span != nil {
		c.span.SetAttributes(attrs...)
		return
	}
	c.attrs = append(c.attrs, attrs...)
}

func (c *Cmd) StdoutPipe() (io.ReadCloser, error) {
	pipe, err := c.Cmd.StdoutPipe()
	if err != nil || content.IsOmitted(c.ctx) {
		return pipe, err
	}
	c.stdout = &Tail{}
	return teeReadCloser{Reader: io.TeeReader(pipe, c.stdout), Closer: pipe}, nil
}

func (c *Cmd) StderrPipe() (io.ReadCloser, error) {
	pipe, err := c.Cmd.StderrPipe()
	if err != nil || content.IsOmitted(c.ctx) {
		return pipe, err
	}
	c.stderr = &Tail{}
	return teeReadCloser{Reader: io.TeeReader(pipe, c.stderr), Closer: pipe}, nil
}

type teeReadCloser struct {
	io.Reader
	io.Closer
}

func (c *Cmd) Run() error {
	c.begin(true)
	err := c.Cmd.Run()
	c.end(err)
	return err
}

func (c *Cmd) Start() error {
	c.begin(true)
	err := c.Cmd.Start()
	if err != nil {
		c.end(err)
	}
	return err
}

func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	c.end(err)
	return err
}

func (c *Cmd) Output() ([]byte, error) {
	c.begin(false)
	out, err := c.Cmd.Output()
	if c.recordsOutput() {
		c.stdout = NewTail(string(out))
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			c.stderr = NewTail(string(exit.Stderr))
		}
	}
	c.end(err)
	return out, err
}

func (c *Cmd) CombinedOutput() ([]byte, error) {
	c.begin(false)
	out, err := c.Cmd.CombinedOutput()
	if c.recordsOutput() {
		c.stdout = NewTail(string(out))
	}
	c.end(err)
	return out, err
}

func (c *Cmd) recordsOutput() bool {
	return c.span != nil && c.span.IsRecording() && !content.IsOmitted(c.ctx)
}

func (c *Cmd) begin(teeOutput bool) {
	c.begun.Do(func() {
		var ctx context.Context
		ctx, c.span = otel.Tracer(Scope).Start(c.ctx, filepath.Base(c.Args[0]))
		c.record(teeOutput)
		if c.ExportsSpans {
			collector := ReadCollector()
			c.exporting = collector.URL != "" && trace.SpanContextFromContext(c.ctx).IsValid()
			c.Env = append(c.environment(), childEnv(ctx, collector)...)
		}
	})
}

func (c *Cmd) IsExportingSpans() bool {
	return c.exporting
}

func (c *Cmd) environment() []string {
	if c.Env == nil {
		return os.Environ()
	}
	return slices.Clip(c.Env)
}

func (c *Cmd) record(teeOutput bool) {
	if !c.span.IsRecording() {
		return
	}
	dir := c.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	c.span.SetAttributes(
		ExecutablePathKey.String(c.Path),
		CommandArgsKey.StringSlice(c.Args),
		WorkingDirectoryKey.String(dir),
	)
	c.span.SetAttributes(commandLine(c.Cmd)...)
	c.span.SetAttributes(explicitEnv(c.Env)...)
	c.span.SetAttributes(c.attrs...)
	if _, isFile := c.Stdin.(*os.File); c.Stdin != nil && !isFile {
		c.stdin = &countedReader{Reader: c.Stdin}
		c.Stdin = c.stdin
	}
	if teeOutput && c.recordsOutput() {
		if c.stdout == nil && c.stderr == nil && isSameWriter(c.Stdout, c.Stderr) {
			c.Stdout, c.stdout = teeTail(c.Stdout)
			c.Stderr = c.Stdout
			return
		}
		if c.stdout == nil {
			c.Stdout, c.stdout = teeTail(c.Stdout)
		}
		if c.stderr == nil {
			c.Stderr, c.stderr = teeTail(c.Stderr)
		}
	}
}

type countedReader struct {
	io.Reader
	bytes int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

func (c *Cmd) end(err error) {
	c.ended.Do(func() {
		if c.span == nil {
			return
		}
		if c.Process != nil {
			c.span.SetAttributes(PIDKey.Int(c.Process.Pid))
		}
		if c.ProcessState != nil {
			c.span.SetAttributes(ExitCodeKey.Int(c.ProcessState.ExitCode()))
		}
		if c.stdin != nil {
			c.span.SetAttributes(StdinBytesKey.Int64(c.stdin.bytes))
		}
		if c.stdout != nil {
			c.span.SetAttributes(StdoutTailKey.String(c.stdout.String()))
		}
		if c.stderr != nil {
			c.span.SetAttributes(StderrTailKey.String(c.stderr.String()))
		}
		EndSpan(c.span, err)
	})
}

func explicitEnv(env []string) []attribute.KeyValue {
	if env == nil {
		return nil
	}
	inherited := map[string]string{}
	for _, keyValue := range os.Environ() {
		if name, value, ok := strings.Cut(keyValue, "="); ok {
			inherited[name] = value
		}
	}
	var attrs []attribute.KeyValue
	for _, keyValue := range env {
		name, value, ok := strings.Cut(keyValue, "=")
		if !ok {
			continue
		}
		if was, found := inherited[name]; found && was == value {
			continue
		}
		attrs = append(attrs, attribute.String(envAttributePrefix+name, value))
	}
	return attrs
}

func teeTail(w io.Writer) (io.Writer, *Tail) {
	if w == nil {
		return nil, nil
	}
	if _, isFile := w.(*os.File); isFile {
		return w, nil
	}
	tail := &Tail{}
	return io.MultiWriter(w, tail), tail
}

func isSameWriter(a, b io.Writer) (same bool) {
	defer func() { _ = recover() }()
	return a != nil && a == b
}
