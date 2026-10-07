package session

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	forwardFirstWait = 20 * time.Millisecond
	forwardLongWait  = time.Second
)

func (s *Session) ForwardPort(ctx context.Context, remote string) (string, error) {
	local, err := freeLoopbackAddress()
	if err != nil {
		return "", err
	}
	spec := local + ":" + remote
	input, held, err := os.Pipe()
	if err != nil {
		return "", refusal.Refuse(refusal.CodeNotReady, "open the input that holds a forward open: %s", err)
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
		_ = held.Close()
		return "", s.refuseUnreached(ctx, err, "")
	}
	var waitErr error
	exited := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(exited)
		_ = held.Close()
		s.cancelForward(spec)
	}()
	if err := awaitListening(ctx, local, exited, &waitErr); err != nil {
		_ = cmd.Process.Kill()
		<-exited
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", refusal.Refuse(refusal.CodeNotReady,
			"%s over ssh: forwarding %s to %s: %s", s.dest.Principal(), local, remote, terse(failure{err: err, stderr: strings.TrimSpace(stderr.String())}))
	}
	return local, nil
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
			return nil
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
