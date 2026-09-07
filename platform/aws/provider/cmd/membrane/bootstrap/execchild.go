package main

import (
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

	if alive, err := awaitListening(port, exited, budget); err != nil {
		if alive {
			_ = cmd.Process.Kill()
			<-exited
		}
		return nil, err
	}
	go supervise(strings.Join(command, " "), exited)
	return &execChild{upstream{port: port, client: newLoopbackClient()}}, nil
}

func executableEnv(port int, extraEnv []string) []string {
	env := append(os.Environ(), providerkit.InjectedPortName+"="+strconv.Itoa(port))
	return append(env, extraEnv...)
}

func awaitListening(port int, exited <-chan error, budget time.Duration) (alive bool, err error) {
	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(budget)
	for {
		conn, err := net.DialTimeout("tcp", address, listenDialTimeout)
		if err == nil {
			conn.Close()
			return true, nil
		}
		select {
		case err := <-exited:
			return false, fmt.Errorf("the app exited before it listened on %s: %w", address, err)
		case <-time.After(listenPollInterval):
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("the app did not listen on %s within %s", address, budget)
		}
	}
}

func bringUpExecutable(command []string, port int, live *liveValues, prefetch <-chan error, env []string, budget time.Duration) (*execChild, error) {
	child, err := startExecutable(command, port, env, budget)
	if err != nil {
		if prefetchErr := live.prefetchError(); prefetchErr != nil {
			return nil, fmt.Errorf("failed to resolve this deployment's live variables: %w", prefetchErr)
		}
		return nil, err
	}
	if err := live.join(prefetch); err != nil {
		return nil, fmt.Errorf("failed to resolve this deployment's live variables: %w", err)
	}
	return child, nil
}
