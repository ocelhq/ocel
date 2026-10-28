//go:build unix

package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type localShell struct {
	t    *testing.T
	path string
}

func (l localShell) command(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+l.path+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func (l localShell) Stream(ctx context.Context, command string, stdin io.Reader) (ssh.Result, error) {
	cmd := l.command(ctx, command)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &stdout, &stderr
	err := cmd.Run()
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: exited.ExitCode()}, nil
	}
	return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func (l localShell) RunLines(ctx context.Context, command string, each func(ssh.Line) error) error {
	cmd := l.command(context.Background(), command)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	l.t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	var mu sync.Mutex
	for pipe, reader := range map[ssh.Pipe]io.Reader{ssh.Stdout: stdout, ssh.Stderr: stderr} {
		go func() {
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				mu.Lock()
				_ = each(ssh.Line{Pipe: pipe, Text: scanner.Text()})
				mu.Unlock()
			}
		}()
	}
	<-ctx.Done()
	input.Close()
	return ctx.Err()
}

func (l localShell) Run(ctx context.Context, command string) (string, error) {
	result, err := l.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (l localShell) Preflight(context.Context) (ssh.Facts, error) { return ssh.Facts{}, nil }

func (l localShell) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("a local shell forwards no port")
}

func (l localShell) ForwardSpans() string { return "" }

func (l localShell) Destination() ssh.Destination { return ssh.Destination{} }

const standInDocker = `#!/bin/sh
[ "$1" = logs ] || exit 0
for name; do :; done
state=STATE
if [ "$name" = gone ]; then
	echo "Error response from daemon: No such container: gone" >&2
	exit 1
fi
runs=$(cat "$state/$name.runs" 2>/dev/null || echo 0)
runs=$((runs + 1))
echo "$runs" >"$state/$name.runs"
echo "$$" >>"$state/pids"
if [ "$name" = restarting ] && [ "$runs" = 1 ]; then
	echo "2026-03-01T10:00:01Z first run"
	exit 0
fi
if [ "$name" = restarting ]; then
	echo "2026-03-01T10:00:01Z first run"
	echo "2026-03-01T10:00:05Z second run"
	exec sleep 300
fi
echo "2026-03-01T10:00:01Z out of $name"
echo "2026-03-01T10:00:02Z err of $name" >&2
exec sleep 300
`

func processRunning(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	state := strings.TrimSpace(string(out))
	return err == nil && state != "" && !strings.HasPrefix(state, "Z")
}

func TestFollowContainerLogsOnABoxFollowsEveryContainerAndStopsThemWhenTheCallerLeaves(t *testing.T) {
	t.Parallel()

	bin, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(strings.ReplaceAll(standInDocker, "STATE", state)), 0o755); err != nil {
		t.Fatal(err)
	}
	box := localShell{t: t, path: bin}
	h := New(ssh.NewBox(func(context.Context) (ssh.Conn, error) { return box, nil }, nil), "shop", Keys{}, nil, front.Proxy{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		mu    sync.Mutex
		texts = map[int][]string{}
		gone  []int
	)
	returned := make(chan error, 1)
	go func() {
		returned <- h.containers.FollowLogs(ctx, []string{"web", "restarting", "gone"}, followSince, func(container int, line release.Line) error {
			mu.Lock()
			defer mu.Unlock()
			text := line.Text
			if line.Stderr {
				text = "stderr: " + text
			}
			texts[container] = append(texts[container], text)
			return nil
		}, func(container int) error {
			mu.Lock()
			defer mu.Unlock()
			gone = append(gone, container)
			return nil
		})
	}()

	settled := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(texts[0]) == 2 && len(texts[1]) == 2 && len(gone) == 1
	}
	for deadline := time.Now().Add(10 * time.Second); !settled() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if got := strings.Join(texts[0], "|"); !strings.Contains(got, "out of web") || !strings.Contains(got, "stderr: err of web") {
		t.Errorf("the box followed web as %q, want its stdout and its stderr apart", got)
	}
	if got := strings.Join(texts[1], "|"); got != "first run|second run" {
		t.Errorf("the box followed a container that restarted as %q, want each line once across both runs", got)
	}
	if len(gone) != 1 || gone[0] != 2 {
		t.Errorf("the box reported %v gone, want the container that does not exist", gone)
	}
	mu.Unlock()

	cancel()
	if err := <-returned; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("FollowContainerLogs() after its caller left = %v", err)
	}
	pids, err := os.ReadFile(filepath.Join(state, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(pids)) {
		pid, _ := strconv.Atoi(field)
		deadline := time.Now().Add(5 * time.Second)
		for processRunning(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if processRunning(pid) {
			t.Errorf("docker logs (pid %d) was still following on the box 5s after its caller left", pid)
		}
	}
}
