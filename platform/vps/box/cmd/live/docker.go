package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/ocelhq/ocel/platform/vps/box/agent"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/dockerpolicy"
)

const (
	dockerPath    = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	refusedExit   = 125
	sudoUIDEnv    = "SUDO_UID"
	sudoGIDEnv    = "SUDO_GID"
	signalledExit = 128
)

func dockerCommand(argv []string, errs *os.File) int {
	policy, docker, err := openPolicy()
	if err != nil {
		fmt.Fprintln(errs, "ocel-live docker: "+err.Error())
		return refusedExit
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	if len(argv) > 0 && argv[0] == boxapi.DockerPullAs {
		return pullAs(ctx, docker, argv[1:], errs)
	}
	inv, err := policy.Open(ctx, argv)
	if err != nil {
		fmt.Fprintln(errs, "ocel-live docker: refused: "+err.Error())
		return refusedExit
	}
	defer inv.Close()
	return ranDocker(ctx, docker, inv, errs)
}

func openPolicy() (dockerpolicy.Policy, string, error) {
	raw, err := os.ReadFile(boxapi.DockerGrantsPath)
	if err != nil {
		return dockerpolicy.Policy{}, "", fmt.Errorf("read what bootstrap granted deploys: %w", err)
	}
	var grants boxapi.DockerGrants
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grants); err != nil {
		return dockerpolicy.Policy{}, "", fmt.Errorf("%s is not a grant this ocel reads: %w", boxapi.DockerGrantsPath, err)
	}
	inspect, err := agent.NewDocker()
	if err != nil {
		return dockerpolicy.Policy{}, "", err
	}
	uid, gid, err := caller()
	if err != nil {
		return dockerpolicy.Policy{}, "", err
	}
	docker, err := lookDocker()
	if err != nil {
		return dockerpolicy.Policy{}, "", err
	}
	return dockerpolicy.Policy{DockerGrants: grants, StateRoot: boxapi.StateRoot, Inspect: inspect, OwnerUID: uid, OwnerGID: gid}, docker, nil
}

func caller() (int, int, error) {
	uid, err := strconv.Atoi(os.Getenv(sudoUIDEnv))
	if err != nil {
		return 0, 0, fmt.Errorf("ocel-live docker runs only through sudo, which names its caller in %s", sudoUIDEnv)
	}
	gid, err := strconv.Atoi(os.Getenv(sudoGIDEnv))
	if err != nil {
		return 0, 0, fmt.Errorf("ocel-live docker runs only through sudo, which names its caller's group in %s", sudoGIDEnv)
	}
	return uid, gid, nil
}

func lookDocker() (string, error) {
	for _, dir := range strings.Split(dockerPath, ":") {
		candidate := dir + "/docker"
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("no docker binary on " + dockerPath)
}

func dockerEnv() []string {
	return []string{"PATH=" + dockerPath, "HOME=" + boxapi.DockerConfigDir, "DOCKER_CONFIG=" + boxapi.DockerConfigDir}
}

func ranDocker(ctx context.Context, docker string, inv dockerpolicy.Invocation, errs *os.File) int {
	run := exec.Command(docker, inv.Argv...)
	run.Env, run.Dir = dockerEnv(), "/"
	run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, errs
	if inv.Stdin != nil {
		run.Stdin = inv.Stdin
	}
	if inv.Stdout != nil {
		run.Stdout = inv.Stdout
	}
	run.ExtraFiles = inv.Extra
	return waited(ctx, run, errs)
}

func waited(ctx context.Context, run *exec.Cmd, errs *os.File) int {
	if err := run.Start(); err != nil {
		fmt.Fprintln(errs, "ocel-live docker: "+err.Error())
		return refusedExit
	}
	done := make(chan error, 1)
	go func() { done <- run.Wait() }()
	select {
	case err := <-done:
		return exitOf(err)
	case <-ctx.Done():
		_ = run.Process.Signal(syscall.SIGTERM)
		return exitOf(<-done)
	}
}

func exitOf(err error) int {
	var exited *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exited) && exited.ExitCode() >= 0:
		return exited.ExitCode()
	case errors.As(err, &exited):
		return signalledExit + int(syscall.SIGTERM)
	}
	return refusedExit
}

func pullAs(ctx context.Context, docker string, argv []string, errs *os.File) int {
	if len(argv) != 3 || strings.HasPrefix(argv[0], "-") || strings.HasPrefix(argv[1], "-") || strings.HasPrefix(argv[2], "-") {
		fmt.Fprintln(errs, "ocel-live docker: refused: pull-as takes a registry, a username and an image, and reads the password from stdin")
		return refusedExit
	}
	server, username, image := argv[0], argv[1], argv[2]
	config, err := os.MkdirTemp(boxapi.DockerConfigDir, "pull-")
	if err != nil {
		fmt.Fprintln(errs, "ocel-live docker: "+err.Error())
		return refusedExit
	}
	defer os.RemoveAll(config)
	login := exec.Command(docker, "--config", config, "login", "--username", username, "--password-stdin", server)
	login.Env, login.Dir = dockerEnv(), "/"
	login.Stdin, login.Stdout, login.Stderr = os.Stdin, errs, errs
	if code := waited(ctx, login, errs); code != 0 {
		return code
	}
	pull := exec.Command(docker, "--config", config, "pull", image)
	pull.Env, pull.Dir = dockerEnv(), "/"
	pull.Stdout, pull.Stderr = os.Stdout, errs
	return waited(ctx, pull, errs)
}
