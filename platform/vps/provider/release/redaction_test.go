//go:build unix

package release

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const secretValue = "postgres://app:hunter2hunter2@db/app"

type catConn struct{}

func (catConn) said(command string) string {
	switch {
	case strings.Contains(command, "docker logs"), strings.Contains(command, "containerlogs"):
		return "0 2026-10-27T10:00:00Z connecting to " + secretValue + "\n"
	case strings.Contains(command, "docker inspect"):
		return `["DATABASE_URL=` + secretValue + `"]`
	}
	return ""
}

func (c catConn) Stream(ctx context.Context, command string, _ io.Reader) (ssh.Result, error) {
	var stdout bytes.Buffer
	cmd := tracing.CommandContext(ctx, "cat")
	cmd.Stdin, cmd.Stdout = strings.NewReader(c.said(command)), &stdout
	err := cmd.Run()
	return ssh.Result{Stdout: stdout.String()}, err
}

func (c catConn) RunLines(ctx context.Context, command string, each func(ssh.Line) error) error {
	result, err := c.Stream(ctx, command, nil)
	if err != nil {
		return err
	}
	for line := range strings.Lines(result.Stdout) {
		if err := each(ssh.Line{Pipe: ssh.Stdout, Text: strings.TrimSuffix(line, "\n")}); err != nil {
			return err
		}
	}
	return nil
}

func (c catConn) Run(ctx context.Context, command string) (string, error) {
	result, err := c.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (catConn) Preflight(context.Context) (ssh.Facts, error) { return ssh.Facts{Root: true}, nil }

func (catConn) Destination() ssh.Destination {
	return ssh.Destination{Address: "203.0.113.10", Port: 22, User: boxapi.DeployLogin}
}

func (catConn) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("this box forwards no port")
}

func (catConn) ForwardSpans() string { return "" }

func TestAContainersLogsNeverReachTheTrace(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	box := ssh.NewBox(func(context.Context) (ssh.Conn, error) { return catConn{}, nil }, nil)
	containers := &Containers{box: box}
	since := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

	if logs := containers.readRedactedLogs(ctx, ssh.Docker, "web", LogCommand("web")); strings.Contains(logs, secretValue) {
		t.Fatalf("readRedactedLogs() = %q, want the value redacted", logs)
	}
	if _, err := containers.ReadLogs(ctx, "web", since, time.Time{}, 10, ""); err != nil {
		t.Fatal(err)
	}
	err := containers.FollowLogs(ctx, []string{"web"}, since, func(int, Line) error { return nil }, func(int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	for _, span := range spans.Ended() {
		for key, value := range tracingtest.Attributes(span) {
			if strings.Contains(value.String(), "hunter2") {
				t.Errorf("%s span records %s = %q, want no line a container logged", span.Name(), key, value.String())
			}
		}
	}
}
