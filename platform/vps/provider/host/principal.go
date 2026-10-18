package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	deployUser  = "ocel-deploy"
	deployShell = LoginShell

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
	password string
}

func deployLogin() login {
	return login{name: deployUser, shell: deployShell, home: stateRoot, password: lockedFact}
}

func (l login) described() []byte {
	return fmt.Appendf(nil, "shell=%s\nhome=%s\ngroups=\npassword=%s\n", l.shell, l.home, l.password)
}

func principal() Item {
	deploy := deployLogin()
	return Item{Kind: KindUser, Name: deploy.name, Owner: deploy.name, Content: deploy.described(), Note: "deploy login",
		script: func(Item) string { return deploy.command() }, survey: func(Item) string { return deploy.survey() }}
}

func (l login) command() string {
	fields := " -d " + shell.Quote(l.home) + " -s " + shell.Quote(l.shell) + " " + shell.Quote(l.name)
	return strings.Join([]string{"set -e",
		"getent group " + shell.Quote(l.name) + " >/dev/null 2>&1 || groupadd -r " + shell.Quote(l.name),
		"if getent passwd " + shell.Quote(l.name) + " >/dev/null 2>&1; then",
		"usermod -g " + shell.Quote(l.name) + " -G ''" + fields,
		"else",
		"useradd -r -M -g " + shell.Quote(l.name) + fields,
		"fi",
		"usermod -p " + shell.Quote(lockedPassword) + " " + shell.Quote(l.name),
	}, "\n")
}

func (l login) survey() string {
	return `if entry=$(getent passwd ` + shell.Quote(l.name) + ` 2>/dev/null); then found=0; else found=$?; fi
if [ "$found" -ne 0 ] && [ "$found" -ne ` + strconv.Itoa(getentNoSuchKey) + ` ]; then
` + unreadable(KindUser, shell.Quote(l.name), `"getent passwd exited $found"`) + `
elif [ "$found" -eq 0 ]; then
groups=$(id -nG ` + shell.Quote(l.name) + ` 2>/dev/null | tr ' ' '\n' | grep -vx ` + shell.Quote(l.name) + ` | LC_ALL=C sort | tr '\n' ',' | sed 's/,$//')
line=$(getent shadow ` + shell.Quote(l.name) + ` 2>/dev/null || true)
if [ -z "$line" ] && [ -r /etc/shadow ]; then line=$(grep ` + shell.Quote("^"+l.name+":") + ` /etc/shadow 2>/dev/null || true); fi
password=''
if [ -n "$line" ] && [ "$(printf '%s' "$line" | cut -d: -f2)" = ` + shell.Quote(lockedPassword) + ` ]; then password=` + shell.Quote(lockedFact) + `
elif [ -n "$line" ]; then password=unlocked
fi
if [ -n "$password" ]; then
sum=$(printf 'shell=%s\nhome=%s\ngroups=%s\npassword=%s\n' "$(printf '%s' "$entry" | cut -d: -f7)" "$(printf '%s' "$entry" | cut -d: -f6)" "$groups" "$password" | sha256sum | cut -d' ' -f1)
` + reports(shell.Quote(KindUser), shell.Quote(l.name), "0", shell.Quote(l.name), `"$sum"`) + `
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
	result, err := h.Stream(ctx, ssh.Login, "cat ~/.ssh/authorized_keys 2>/dev/null", nil)
	if err != nil {
		return nil, err
	}
	keys := authorized([]byte(result.Stdout))
	if len(keys) == 0 {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s has no ~/.ssh/authorized_keys for %s to inherit\nName a public key file with the %q option",
			h.Named(), deployUser, "deployKey")
	}
	return keys, nil
}
