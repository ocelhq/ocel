package vps

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type host struct {
	target Target
	root   bool
	probed bool
}

func (h host) dest() string {
	if h.target.Alias != "" {
		return h.target.Alias
	}
	if h.target.User != "" {
		return h.target.User + "@" + h.target.Host
	}
	return h.target.Host
}

func (h host) sshArgs(strict string, batch bool) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=" + strict,
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath(h.dest()),
		"-o", "ControlPersist=60",
	}
	if batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	if h.target.Port != 0 {
		args = append(args, "-p", strconv.Itoa(h.target.Port))
	}
	if h.target.IdentityFile != "" {
		args = append(args, "-i", h.target.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	return args
}

func controlPath(dest string) string {
	safe := strings.NewReplacer("/", "_", "@", "_", ":", "_").Replace(dest)
	return "/tmp/ocel-proto-" + safe
}

func (h host) run(ctx context.Context, stdin []byte, command string) (string, string, error) {
	args := append(h.sshArgs("yes", true), h.dest(), command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), err
}

func (h host) check(ctx context.Context, command string) (string, error) {
	out, errOut, err := h.run(ctx, nil, command)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(errOut))
	}
	return strings.TrimSpace(out), nil
}

func (h host) ok(ctx context.Context, command string) bool {
	_, _, err := h.run(ctx, nil, command)
	return err == nil
}

func (h *host) probe(ctx context.Context) error {
	if h.probed {
		return nil
	}
	if err := h.trust(ctx); err != nil {
		return err
	}
	who, err := h.check(ctx, "id -un")
	if err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady, "cannot run commands on %s: %v", h.dest(), err)
	}
	h.root = who == "root"
	if !h.root && !h.ok(ctx, "sudo -n true") {
		return providerkit.Refuse(providerkit.CodeDenied,
			"%s logs in as %q, which is neither root nor able to sudo without a password", h.dest(), who)
	}
	h.probed = true
	return nil
}

func (h host) sudo(command string) string {
	if h.root {
		return command
	}
	return "sudo -n " + command
}

func (h host) trust(ctx context.Context) error {
	if _, _, err := h.run(ctx, nil, "true"); err == nil {
		return nil
	}
	_, errOut, _ := h.run(ctx, nil, "true")
	if strings.Contains(errOut, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		return providerkit.Refuse(providerkit.CodeDenied,
			"the host key for %s has changed since it was recorded; if this is expected, run `ssh-keygen -R %s` and try again",
			h.dest(), h.hostForKnownHosts())
	}
	if !strings.Contains(errOut, "Host key verification failed") && !strings.Contains(errOut, "No ED25519 host key is known") {
		return providerkit.Refuse(providerkit.CodeNotReady, "cannot reach %s: %s", h.dest(), strings.TrimSpace(errOut))
	}
	print, scanErr := h.fingerprint(ctx)
	if scanErr != nil {
		return providerkit.Refuse(providerkit.CodeDenied, "%s is not a known host and its key could not be scanned: %v", h.dest(), scanErr)
	}
	return providerkit.Refuse(providerkit.CodeDenied,
		"%s is not a known host.\n\n  Its ed25519 key is %s\n\nAccept it with `ssh-keyscan -H %s >> ~/.ssh/known_hosts`, or `ssh %s` once by hand, then try again.",
		h.dest(), print, h.hostForKnownHosts(), h.dest())
}

func (h host) hostForKnownHosts() string {
	if h.target.Host != "" {
		return h.target.Host
	}
	return h.target.Alias
}

func (h host) fingerprint(ctx context.Context) (string, error) {
	target := h.hostForKnownHosts()
	scanArgs := []string{"-t", "ed25519"}
	if h.target.Port != 0 {
		scanArgs = append(scanArgs, "-p", strconv.Itoa(h.target.Port))
	}
	scanArgs = append(scanArgs, target)
	scan := exec.CommandContext(ctx, "ssh-keyscan", scanArgs...)
	var scanned, scanErr bytes.Buffer
	scan.Stdout = &scanned
	scan.Stderr = &scanErr
	if err := scan.Run(); err != nil || scanned.Len() == 0 {
		return "", fmt.Errorf("ssh-keyscan %s: %v", target, strings.TrimSpace(scanErr.String()))
	}
	keygen := exec.CommandContext(ctx, "ssh-keygen", "-lf", "-")
	keygen.Stdin = bytes.NewReader(scanned.Bytes())
	printed, err := keygen.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(printed))
	if len(fields) < 2 {
		return "", fmt.Errorf("ssh-keygen printed %q", strings.TrimSpace(string(printed)))
	}
	return fields[1], nil
}
