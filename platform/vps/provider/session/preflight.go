package session

import (
	"bufio"
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const survey = `printf 'uid=%s\n' "$(id -u)"
printf 'arch=%s\n' "$(uname -m)"
printf 'kernel=%s\n' "$(uname -r)"
. /etc/os-release 2>/dev/null && printf 'os=%s\n' "$PRETTY_NAME"
[ -d /run/systemd/system ] && printf 'systemd=yes\n' || printf 'systemd=no\n'
sudo -n true >/dev/null 2>&1 && printf 'sudo=yes\n' || printf 'sudo=no\n'`

type Facts struct {
	Root    bool
	Sudo    bool
	Systemd bool
	OS      string
	Arch    string
	Kernel  string
}

func (s *Session) Preflight(ctx context.Context) (Facts, error) {
	rendered, err := s.Run(ctx, survey)
	if err != nil {
		return Facts{}, err
	}
	facts := readFacts(rendered)
	if !facts.Root && !facts.Sudo {
		return facts, providerkit.Refuse(providerkit.CodeDenied,
			"%s can neither act as root nor run sudo without a password, and bootstrap writes as root throughout.\nGrant passwordless sudo to that login, or point ocel at root, then try again",
			s.dest.Principal())
	}
	if !facts.Systemd {
		return facts, providerkit.Refuse(providerkit.CodeDenied,
			"%s runs no systemd, and everything ocel bootstraps onto a host is a systemd unit.\nOcel has nothing to offer this machine",
			s.dest.Written)
	}
	return facts, nil
}

func readFacts(rendered string) Facts {
	var facts Facts
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		key, value, split := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !split {
			continue
		}
		switch key {
		case "uid":
			facts.Root = value == "0"
		case "sudo":
			facts.Sudo = value == "yes"
		case "systemd":
			facts.Systemd = value == "yes"
		case "os":
			facts.OS = value
		case "arch":
			facts.Arch = value
		case "kernel":
			facts.Kernel = value
		}
	}
	return facts
}
