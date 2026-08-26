package host

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	deployUser  = "ocel-deploy"
	deployShell = "/bin/sh"
	dockerGroup = "docker"

	lockedPassword = "*"
)

const (
	sshDir         = stateRoot + "/.ssh"
	authorizedKeys = sshDir + "/authorized_keys"
)

func DeployUser() string { return deployUser }

func principal() Item {
	return Item{Kind: KindUser, Name: deployUser, Owner: deployUser, Content: accountFacts(deployShell, stateRoot, dockerGroup, "locked")}
}

func accountFacts(shell, home, group, password string) []byte {
	return fmt.Appendf(nil, "shell=%s\nhome=%s\ngroup=%s\npassword=%s\n", shell, home, group, password)
}

func accountCommand(name string) string {
	return strings.Join([]string{
		"set -e",
		"getent group " + quoted(dockerGroup) + " >/dev/null 2>&1 || groupadd -r " + quoted(dockerGroup),
		"getent group " + quoted(name) + " >/dev/null 2>&1 || groupadd -r " + quoted(name),
		"if getent passwd " + quoted(name) + " >/dev/null 2>&1; then",
		"usermod -g " + quoted(name) + " -G " + quoted(dockerGroup) + " -d " + quoted(stateRoot) + " -s " + quoted(deployShell) + " " + quoted(name),
		"else",
		"useradd -r -M -g " + quoted(name) + " -G " + quoted(dockerGroup) + " -d " + quoted(stateRoot) + " -s " + quoted(deployShell) + " " + quoted(name),
		"fi",
		"usermod -p " + quoted(lockedPassword) + " " + quoted(name),
	}, "\n")
}

func accountSurvey(name string) string {
	return `if entry=$(getent passwd ` + quoted(name) + ` 2>/dev/null); then
held=''
if id -nG ` + quoted(name) + ` 2>/dev/null | tr ' ' '\n' | grep -qx ` + quoted(dockerGroup) + `; then held=` + quoted(dockerGroup) + `; fi
password=unlocked
if [ "$(getent shadow ` + quoted(name) + ` 2>/dev/null | cut -d: -f2)" = ` + quoted(lockedPassword) + ` ]; then password=locked; fi
sum=$(printf 'shell=%s\nhome=%s\ngroup=%s\npassword=%s\n' "$(printf '%s' "$entry" | cut -d: -f7)" "$(printf '%s' "$entry" | cut -d: -f6)" "$held" "$password" | sha256sum | cut -d' ' -f1)
printf '%s\t%s\t%s\t%s\t%s\n' ` + quoted(KindUser) + ` ` + quoted(name) + ` 0 ` + quoted(name) + ` "$sum"
fi`
}

type Keys struct{ Path string }

func (k Keys) named() ([]byte, error) {
	raw, err := os.ReadFile(k.Path)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names %s, which ocel cannot read: %s", "deployKey", k.Path, err)
	}
	keys := authorized(raw)
	if len(keys) == 0 {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names %s, which carries no public key", "deployKey", k.Path)
	}
	return keys, nil
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
	h.holding.Lock()
	defer h.holding.Unlock()
	if h.held != nil {
		return h.held, nil
	}
	keys, err := h.resolve(ctx)
	if err != nil {
		return nil, err
	}
	h.held = keys
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
	result, err := live.Exec(ctx, "cat ~/.ssh/authorized_keys 2>/dev/null", nil)
	if err != nil {
		return nil, err
	}
	keys := authorized([]byte(result.Stdout))
	if len(keys) == 0 {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s has no ~/.ssh/authorized_keys for %s to inherit, so the deploy login would answer to nobody.\nName a public key file with the %q option and try again",
			h.named(), deployUser, "deployKey")
	}
	return keys, nil
}
