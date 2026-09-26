package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const (
	deployUser  = "ocel-deploy"
	deployShell = "/bin/sh"
	dockerGroup = "docker"

	lockedPassword = "*"
	lockedFact     = "locked"

	getentNoSuchKey = 2
)

const (
	sshDir         = stateRoot + "/.ssh"
	authorizedKeys = sshDir + "/authorized_keys"
)

func DeployUser() string { return deployUser }

type login struct {
	name     string
	shell    string
	home     string
	group    string
	password string
}

func deployLogin() login {
	return login{name: deployUser, shell: deployShell, home: stateRoot, group: dockerGroup, password: lockedFact}
}

func (l login) described() []byte {
	return fmt.Appendf(nil, "shell=%s\nhome=%s\ngroup=%s\npassword=%s\n", l.shell, l.home, l.group, l.password)
}

func principal() Item {
	deploy := deployLogin()
	return Item{Kind: KindUser, Name: deploy.name, Owner: deploy.name, Content: deploy.described(), Note: "deploy login"}
}

func (l login) joined(flag string) string {
	if l.group == "" {
		return ""
	}
	return " " + flag + " " + quoted(l.group)
}

func (l login) command() string {
	lines := []string{"set -e"}
	if l.group != "" {
		lines = append(lines, "getent group "+quoted(l.group)+" >/dev/null 2>&1 || groupadd -r "+quoted(l.group))
	}
	fields := " -d " + quoted(l.home) + " -s " + quoted(l.shell) + " " + quoted(l.name)
	return strings.Join(append(lines,
		"getent group "+quoted(l.name)+" >/dev/null 2>&1 || groupadd -r "+quoted(l.name),
		"if getent passwd "+quoted(l.name)+" >/dev/null 2>&1; then",
		"usermod -g "+quoted(l.name)+l.joined("-aG")+fields,
		"else",
		"useradd -r -M -g "+quoted(l.name)+l.joined("-G")+fields,
		"fi",
		"usermod -p "+quoted(lockedPassword)+" "+quoted(l.name),
	), "\n")
}

func (l login) survey() string {
	membership := ""
	if l.group != "" {
		membership = "if id -nG " + quoted(l.name) + " 2>/dev/null | tr ' ' '\\n' | grep -qx " + quoted(l.group) + "; then member=" + quoted(l.group) + "; fi\n"
	}
	return `if entry=$(getent passwd ` + quoted(l.name) + ` 2>/dev/null); then found=0; else found=$?; fi
if [ "$found" -ne 0 ] && [ "$found" -ne ` + strconv.Itoa(getentNoSuchKey) + ` ]; then
` + unreadable(KindUser, quoted(l.name), `"getent passwd exited $found"`) + `
elif [ "$found" -eq 0 ]; then
member=''
` + membership + `line=$(getent shadow ` + quoted(l.name) + ` 2>/dev/null || true)
if [ -z "$line" ] && [ -r /etc/shadow ]; then line=$(grep ` + quoted("^"+l.name+":") + ` /etc/shadow 2>/dev/null || true); fi
password=''
if [ -n "$line" ] && [ "$(printf '%s' "$line" | cut -d: -f2)" = ` + quoted(lockedPassword) + ` ]; then password=` + quoted(lockedFact) + `
elif [ -n "$line" ]; then password=unlocked
fi
if [ -n "$password" ]; then
sum=$(printf 'shell=%s\nhome=%s\ngroup=%s\npassword=%s\n' "$(printf '%s' "$entry" | cut -d: -f7)" "$(printf '%s' "$entry" | cut -d: -f6)" "$member" "$password" | sha256sum | cut -d' ' -f1)
` + reports(quoted(KindUser), quoted(l.name), "0", quoted(l.name), `"$sum"`) + `
fi
fi`
}

type Keys struct{ Path string }

func (k Keys) named() ([]byte, error) {
	path, err := resolved(k.Path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"option %q names %s, which ocel cannot read: %s", "deployKey", path, err)
	}
	keys := authorized(raw)
	if len(keys) == 0 {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"option %q names %s, which contains no public key", "deployKey", path)
	}
	return keys, nil
}

func resolved(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"option %q names %s, and there is no home directory to resolve it: %s", "deployKey", path, err)
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	if !filepath.IsAbs(path) {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"option %q names %s, a relative path\nSpell it from / or ~/",
			"deployKey", path)
	}
	return path, nil
}

func authorized(raw []byte) []byte {
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

func (h *Host) keys(ctx context.Context) ([]byte, error) {
	h.keying.Lock()
	defer h.keying.Unlock()
	if h.keyed != nil {
		return h.keyed, nil
	}
	keys, err := h.resolve(ctx)
	if err != nil {
		return nil, err
	}
	h.keyed = keys
	return keys, nil
}

func (h *Host) resolve(ctx context.Context) ([]byte, error) {
	if h.deploy.Path != "" {
		return h.deploy.named()
	}
	live, err := h.dial(ctx)
	if err != nil {
		return nil, err
	}
	result, err := live.Stream(ctx, "cat ~/.ssh/authorized_keys 2>/dev/null", nil)
	if err != nil {
		return nil, err
	}
	keys := authorized([]byte(result.Stdout))
	if len(keys) == 0 {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s has no ~/.ssh/authorized_keys for %s to inherit\nName a public key file with the %q option",
			h.named(), deployUser, "deployKey")
	}
	return keys, nil
}
