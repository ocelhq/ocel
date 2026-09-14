package child

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const ListenPollInterval = 20 * time.Millisecond

const ListenDialTimeout = 250 * time.Millisecond

type Options struct {
	Command []string
	Dir     string
	Env     []string
	Stdout  io.Writer
	Stderr  io.Writer
}

type Exit struct {
	Code int
	Err  error
}

func (e Exit) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

type Process struct {
	pid    int
	what   string
	exited chan Exit
}

func Start(opts Options) (*Process, error) {
	if len(opts.Command) == 0 {
		return nil, errors.New("no command to run")
	}
	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", strings.Join(opts.Command, " "), err)
	}
	p := &Process{pid: cmd.Process.Pid, what: strings.Join(opts.Command, " "), exited: make(chan Exit, 1)}
	go p.reap()
	return p, nil
}

func (p *Process) PID() int { return p.pid }

func (p *Process) Exited() <-chan Exit { return p.exited }

func (p *Process) Signal(sig os.Signal) error {
	held, err := os.FindProcess(p.pid)
	if err != nil {
		return err
	}
	return held.Signal(sig)
}

func (p *Process) Stop(grace time.Duration) Exit {
	_ = p.Signal(syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case exit := <-p.exited:
		return exit
	case <-timer.C:
		_ = p.Signal(syscall.SIGKILL)
		return <-p.exited
	}
}

func (p *Process) reap() {
	waited := p.pid
	if os.Getpid() == 1 {
		waited = -1
	}
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(waited, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			p.exited <- Exit{Code: 1, Err: fmt.Errorf("wait for %s: %w", p.what, err)}
			return
		}
		if pid != p.pid {
			continue
		}
		p.exited <- exitOf(status)
		return
	}
}

func exitOf(status syscall.WaitStatus) Exit {
	if status.Signaled() {
		return Exit{Code: 128 + int(status.Signal()), Err: fmt.Errorf("killed by %s", status.Signal())}
	}
	if code := status.ExitStatus(); code != 0 {
		return Exit{Code: code, Err: fmt.Errorf("exit status %d", code)}
	}
	return Exit{}
}

func WatchListening(address string, exited <-chan Exit) <-chan error {
	out := make(chan error, 1)
	go func() {
		for {
			conn, err := net.DialTimeout("tcp", address, ListenDialTimeout)
			if err == nil {
				conn.Close()
				out <- nil
				return
			}
			select {
			case exit := <-exited:
				out <- fmt.Errorf("the app exited before it listened on %s: %w", address, exit)
				return
			case <-time.After(ListenPollInterval):
			}
		}
	}()
	return out
}

func FreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
