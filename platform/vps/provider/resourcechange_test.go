package vps_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (b *box) serving(command string) (ssh.Result, bool) {
	if !b.remembers {
		return ssh.Result{}, false
	}
	if strings.Contains(command, "docker inspect --type container") {
		for name, serving := range b.containers {
			if strings.Contains(command, quoted(name)+" 2>&1") {
				return ssh.Result{Stdout: serving + "\n"}, true
			}
		}
		return ssh.Result{}, true
	}
	for _, line := range strings.Split(command, "\n") {
		fields := strings.Fields(line)
		for at := range fields {
			switch {
			case at+2 < len(fields) && fields[at] == "docker" && fields[at+1] == "rm" && fields[at+2] == "--force":
				for _, name := range fields[at+3:] {
					delete(b.containers, unquoted(name))
				}
			case at+1 < len(fields) && fields[at] == "'docker'" && fields[at+1] == "'run'":
				b.remember(fields[at:])
			}
		}
	}
	return ssh.Result{}, false
}

func (b *box) remember(run []string) {
	var name, image, digest string
	for at := 0; at+1 < len(run); at++ {
		value := unquoted(run[at+1])
		switch unquoted(run[at]) {
		case "--name":
			name = value
		case "--label":
			if ref, labelled := strings.CutPrefix(value, host.LabelRef+"="); labelled {
				image = ref
			}
			if env, labelled := strings.CutPrefix(value, host.LabelEnv+"="); labelled {
				digest = env
			}
		}
	}
	if name == "" || digest == "" {
		return
	}
	if b.containers == nil {
		b.containers = map[string]string{}
	}
	b.containers[name] = "running " + image + " " + digest
}

func (b *box) since(mark int) []string { return b.commands()[mark:] }

func refuseWrites(t *testing.T, commands []string) {
	t.Helper()
	writes := []string{
		"'--request' 'PUT'", "'--request' 'DELETE'", "'--request' 'POST'",
		"'docker' 'run'", "docker rm", "docker rename", "docker stop", "docker start",
		"docker volume create", "docker volume rm", "docker network create",
		`ln "$writing"`, `mv "$staged"`, "install -m", "rm -f",
	}
	for _, command := range commands {
		for _, write := range writes {
			if strings.Contains(command, write) {
				t.Errorf("planning ran %q, and a plan changes nothing on the box:\n%s", write, command)
			}
		}
		if strings.Contains(command, shell.Words([]string{boxapi.LiveBinary, boxapi.SealCommand})) && !strings.Contains(command, "'open'") {
			t.Errorf("planning sealed a value, and a plan keeps nothing on the box:\n%s", command)
		}
	}
}
