package session

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	forwardFirstWait = 20 * time.Millisecond
	forwardLongWait  = time.Second
	forwardAttempts  = 3
)

func (s *Session) ForwardPort(ctx context.Context, remote string) (string, func(), error) {
	for attempt := 1; ; attempt++ {
		local, stop, portTaken, err := s.forwardFromFreePort(ctx, remote)
		if !portTaken || attempt == forwardAttempts {
			return local, stop, err
		}
	}
}

func (s *Session) forwardFromFreePort(ctx context.Context, remote string) (string, func(), bool, error) {
	local, err := freeLoopbackAddress()
	if err != nil {
		return "", nil, false, err
	}
	spec := local + ":" + remote
	input, stdinWriter, err := os.Pipe()
	if err != nil {
		return "", nil, false, refusal.Refuse(refusal.CodeNotReady, "open the input that holds a forward open: %s", err)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ssh", append(s.args(),
		"-o", "ExitOnForwardFailure=yes",
		"-L", spec,
		s.dest.Written, "exec cat >/dev/null")...)
	cmd.Stdin, cmd.Stderr = input, &stderr
	err = cmd.Start()
	_ = input.Close()
	if err != nil {
		_ = stdinWriter.Close()
		return "", nil, false, s.refuseUnreached(ctx, err, "")
	}
	var waitErr error
	exited := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(exited)
		_ = stdinWriter.Close()
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			<-exited
			s.cancelForward(spec)
		})
	}
	if err := awaitListening(ctx, local, exited, &waitErr); err != nil {
		stop()
		if ctx.Err() != nil {
			return "", nil, false, ctx.Err()
		}
		said := strings.TrimSpace(stderr.String())
		refused := refusal.Refuse(refusal.CodeNotReady,
			"%s over ssh: forwarding %s to %s: %s", s.dest.Principal(), local, remote, terse(failure{err: err, stderr: said}))
		return "", nil, strings.Contains(said, "Address already in use"), refused
	}
	return local, stop, false, nil
}

func (s *Session) cancelForward(spec string) {
	if s.control == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), reach)
	defer cancel()
	_, _ = output(ctx, "ssh", append(s.args(), "-O", "cancel", "-L", spec, s.dest.Written)...)
}

func awaitListening(ctx context.Context, local string, exited <-chan struct{}, waitErr *error) error {
	wait := forwardFirstWait
	deadline := time.Now().Add(reach)
	for {
		if conn, err := net.DialTimeout("tcp", local, wait); err == nil {
			_ = conn.Close()
			select {
			case <-exited:
				return errors.New("ssh exited, and another process answers on the port it was to forward from")
			default:
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the forward never started listening")
		}
		timer := time.NewTimer(wait)
		select {
		case <-exited:
			timer.Stop()
			if *waitErr != nil {
				return *waitErr
			}
			return errors.New("ssh exited before the forward listened")
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		wait = min(wait*2, forwardLongWait)
	}
}

func freeLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", refusal.Refuse(refusal.CodeNotReady, "find a free loopback port to forward from: %s", err)
	}
	address := listener.Addr().String()
	return address, listener.Close()
}
