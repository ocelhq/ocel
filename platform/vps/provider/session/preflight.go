package session

import (
	"bufio"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

var bootstrapTools = []string{
	"install", "stat", "sha256sum", "flock", "getent", "groupadd", "useradd", "usermod", "userdel",
}

var survey = `printf 'uid=%s\n' "$(id -u)"
printf 'arch=%s\n' "$(uname -m)"
printf 'kernel=%s\n' "$(uname -r)"
. /etc/os-release 2>/dev/null && printf 'os=%s\n' "$PRETTY_NAME"
[ -d /run/systemd/system ] && printf 'systemd=yes\n' || printf 'systemd=no\n'
sudo -n true >/dev/null 2>&1 && printf 'sudo=yes\n' || printf 'sudo=no\n'
held=''
for tool in ` + toolList + `; do
PATH="$PATH:/usr/sbin:/sbin" command -v "$tool" >/dev/null 2>&1 && held="$held $tool"
done
printf 'tools=%s\n' "$held"`

var toolList = strings.Join(bootstrapTools, " ")

func BootstrapTools() []string { return slices.Clone(bootstrapTools) }

type Facts struct {
	Root    bool
	Sudo    bool
	Systemd bool
	OS      string
	Arch    string
	Kernel  string
	Tools   []string
}

type Requirement struct {
	Name   string
	Detail string
	Met    func(Facts) bool
	Unmet  func(Facts, string) string
}

func Requirements() []Requirement {
	return []Requirement{
		{
			Name:   "root, or sudo without a password",
			Detail: "every byte a bootstrap writes it writes as root: the directories under /etc, /var/lib and /usr/local, the deploy login, and the password it locks",
			Met:    func(facts Facts) bool { return facts.Root || facts.Sudo },
			Unmet: func(_ Facts, principal string) string {
				return fmt.Sprintf("%s can neither act as root nor run sudo without a password, and bootstrap writes as root throughout.\nGrant passwordless sudo to that login, or point ocel at root, then try again", principal)
			},
		},
		{
			Name:   "systemd",
			Detail: "everything ocel bootstraps onto a host is a systemd unit",
			Met:    func(facts Facts) bool { return facts.Systemd },
			Unmet: func(_ Facts, principal string) string {
				return fmt.Sprintf("%s runs no systemd, and everything ocel bootstraps onto a host is a systemd unit.\nOcel has nothing to offer this machine", principal)
			},
		},
		{
			Name:   strings.Join(bootstrapTools, ", "),
			Detail: "the commands a bootstrap surveys and writes with, on root's PATH",
			Met:    func(facts Facts) bool { return len(absent(facts.Tools)) == 0 },
			Unmet: func(facts Facts, principal string) string {
				return fmt.Sprintf("%s is missing %s, and a bootstrap is those commands and little else.\nInstall what it lacks, then try again", principal, strings.Join(absent(facts.Tools), ", "))
			},
		},
	}
}

func absent(held []string) []string {
	var missing []string
	for _, tool := range bootstrapTools {
		if !slices.Contains(held, tool) {
			missing = append(missing, tool)
		}
	}
	return missing
}

func (s *Session) Facts(ctx context.Context) (Facts, error) {
	rendered, err := s.Run(ctx, survey)
	if err != nil {
		return Facts{}, err
	}
	return readFacts(rendered), nil
}

func (s *Session) Preflight(ctx context.Context) (Facts, error) {
	facts, err := s.Facts(ctx)
	if err != nil {
		return Facts{}, err
	}
	return facts, met(facts, s.dest.Principal())
}

func met(facts Facts, principal string) error {
	for _, need := range Requirements() {
		if !need.Met(facts) {
			return providerkit.Refuse(providerkit.CodeDenied, "%s", need.Unmet(facts, principal))
		}
	}
	return nil
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
		case "tools":
			facts.Tools = strings.Fields(value)
		}
	}
	return facts
}
