package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const listenPollInterval = 20 * time.Millisecond

const listenDialTimeout = 250 * time.Millisecond

type execChild struct {
	upstream

	ready chan struct{}
}

func startExecutable(command []string, port int, extraEnv []string, budget time.Duration) (*execChild, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = taskRoot()
	cmd.Env = executableEnv(port, extraEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s from %s: %w", strings.Join(command, " "), cmd.Dir, err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	what := strings.Join(command, " ")
	c := &execChild{upstream: upstream{port: port, client: newLoopbackClient()}, ready: make(chan struct{})}
	listening := watchListening(port, exited)

	select {
	case err := <-listening:
		if err != nil {
			return nil, err
		}
		c.arm(what, exited)
	case <-time.After(budget):
		fmt.Fprintf(os.Stderr,
			"ocel: the app has not listened on 127.0.0.1:%d within %s; the runtime is serving invocations that wait for it\n",
			port, budget)
		go func() {
			if err := <-listening; err != nil {
				fmt.Fprintf(os.Stderr, "ocel: %v\n", err)
				os.Exit(1)
			}
			c.arm(what, exited)
		}()
	}
	return c, nil
}

func (c *execChild) arm(what string, exited <-chan error) {
	close(c.ready)
	go supervise(what, exited)
}

func (c *execChild) awaitReady(ctx context.Context) error {
	return awaitChildReady(ctx, c.ready)
}

func executableEnv(port int, extraEnv []string) []string {
	env := append(os.Environ(), providerkit.InjectedPortName+"="+strconv.Itoa(port))
	return append(env, extraEnv...)
}

func watchListening(port int, exited <-chan error) <-chan error {
	out := make(chan error, 1)
	go func() {
		address := "127.0.0.1:" + strconv.Itoa(port)
		for {
			conn, err := net.DialTimeout("tcp", address, listenDialTimeout)
			if err == nil {
				conn.Close()
				out <- nil
				return
			}
			select {
			case err := <-exited:
				out <- fmt.Errorf("the app exited before it listened on %s: %w", address, err)
				return
			case <-time.After(listenPollInterval):
			}
		}
	}()
	return out
}
