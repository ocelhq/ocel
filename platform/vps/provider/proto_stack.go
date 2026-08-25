package vps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	stackName   = "core"
	deployUser  = "ocel-deploy"
	deployHome  = "/var/lib/ocel"
	helperDir   = "/usr/local/lib/ocel"
	sealHelper  = helperDir + "/seal"
	sudoersFile = "/etc/sudoers.d/ocel-deploy-seal"
)

func etcDir(class providerkit.Class) string   { return "/etc/ocel/" + string(class) }
func stateDir(class providerkit.Class) string { return deployHome + "/state/" + string(class) }
func stampPath(class providerkit.Class) string {
	return etcDir(class) + "/stamp.json"
}
func sealKeyPath(class providerkit.Class) string { return etcDir(class) + "/seal.key" }

type item struct {
	kind      string
	name      string
	singleton bool
	data      bool
	reason    string
	body      string
	present   func(ctx context.Context, h host) (bool, error)
	create    func(ctx context.Context, h host) error
	remove    func(ctx context.Context, h host) error
}

func coreItems(class providerkit.Class) []item {
	return []item{
		dirItem("fs:dir", etcDir(class), "root", "0755", false),
		userItem(),
		dirItem("fs:dir", helperDir, "root", "0755", true),
		fileItem("fs:file", sealHelper, sealHelperBody, "root", "0755", true),
		fileItem("fs:file", sudoersFile, sudoersBody, "root", "0440", true),
		dirItem("fs:dir", deployHome, deployUser, "0750", true),
		dataDir(dirItem("fs:dir", stateDir(class), deployUser, "0750", false), "holds this class's records"),
		sealKeyItem(class),
		dockerItem(),
	}
}

func dataDir(each item, why string) item {
	each.data, each.reason = true, why
	return each
}

func dirItem(kind, path, owner, mode string, singleton bool) item {
	return item{
		kind: kind, name: path, singleton: singleton,
		body: fmt.Sprintf("dir %s %s %s", path, owner, mode),
		present: func(ctx context.Context, h host) (bool, error) {
			return h.ok(ctx, h.sudo(shellQuote("test", "-d", path))), nil
		},
		create: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(shellQuote("install", "-d", "-o", owner, "-g", owner, "-m", mode, path)))
		},
		remove: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(shellQuote("rm", "-rf", path)))
		},
	}
}

func fileItem(kind, path, body, owner, mode string, singleton bool) item {
	return item{
		kind: kind, name: path, singleton: singleton, body: body + "\x00" + owner + mode,
		present: func(ctx context.Context, h host) (bool, error) {
			got, err := h.check(ctx, h.sudo(shellQuote("sha256sum", path))+" 2>/dev/null || true")
			if err != nil {
				return false, err
			}
			return strings.HasPrefix(got, sha256hex(body)), nil
		},
		create: func(ctx context.Context, h host) error {
			return h.write(ctx, path, body, owner, mode)
		},
		remove: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(shellQuote("rm", "-f", path)))
		},
	}
}

func userItem() item {
	return item{
		kind: "linux:user", name: deployUser, singleton: true,
		body: "user " + deployUser + " shell=/bin/sh home=" + deployHome + " groups=docker",
		present: func(ctx context.Context, h host) (bool, error) {
			return h.ok(ctx, shellQuote("id", "-u", deployUser)), nil
		},
		create: func(ctx context.Context, h host) error {
			steps := []string{
				h.sudo(shellQuote("useradd", "--system", "--home-dir", deployHome, "--shell", "/bin/sh", "--create-home", "--skel", "/dev/null", deployUser)),
				h.sudo(shellQuote("usermod", "-p", "*", deployUser)),
				h.sudo(shellQuote("install", "-d", "-o", deployUser, "-g", deployUser, "-m", "0700", deployHome+"/.ssh")),
				h.sudo(fmt.Sprintf("cp ~/.ssh/authorized_keys %s/.ssh/authorized_keys", deployHome)),
				h.sudo(shellQuote("chown", deployUser+":"+deployUser, deployHome+"/.ssh/authorized_keys")),
				h.sudo(shellQuote("chmod", "0600", deployHome+"/.ssh/authorized_keys")),
			}
			for _, step := range steps {
				if err := h.must(ctx, step); err != nil {
					return err
				}
			}
			return nil
		},
		remove: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(shellQuote("userdel", "-r", deployUser))+" 2>/dev/null || true")
		},
	}
}

func dockerItem() item {
	return item{
		kind: "docker:engine", name: "docker", singleton: true, body: "docker engine",
		reason: "installed from get.docker.com",
		present: func(ctx context.Context, h host) (bool, error) {
			return h.ok(ctx, "command -v docker >/dev/null"), nil
		},
		create: func(ctx context.Context, h host) error {
			steps := []string{
				h.sudo("curl -fsSL --connect-timeout 10 --max-time 120 https://get.docker.com -o /tmp/ocel-get-docker.sh"),
				h.sudo("sh /tmp/ocel-get-docker.sh"),
				h.sudo(shellQuote("rm", "-f", "/tmp/ocel-get-docker.sh")),
				h.sudo(shellQuote("usermod", "-aG", "docker", deployUser)),
			}
			for _, step := range steps {
				if err := h.must(ctx, step); err != nil {
					return err
				}
			}
			return nil
		},
		remove: nil,
	}
}

func sealKeyItem(class providerkit.Class) item {
	path := sealKeyPath(class)
	return item{
		kind: "fs:file", name: path, data: true,
		reason: "holds the sealing key for this class",
		body:   "seal key " + string(class),
		present: func(ctx context.Context, h host) (bool, error) {
			return h.ok(ctx, h.sudo(shellQuote("test", "-s", path))), nil
		},
		create: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(fmt.Sprintf(
				"sh -c 'umask 077; head -c 32 /dev/urandom > %s; chown root:root %s; chmod 0400 %s'", path, path, path)))
		},
		remove: func(ctx context.Context, h host) error {
			return h.must(ctx, h.sudo(shellQuote("rm", "-f", path)))
		},
	}
}

const sealHelperBody = `#!/bin/sh
set -eu
op=${1:?seal|open}
class=""
while [ $# -gt 1 ]; do
    shift
    case $1 in
    --class) shift; class=$1 ;;
    esac
done
key=/etc/ocel/$class/seal.key
[ -r "$key" ] || { echo "seal: no key for class '$class'" >&2; exit 1; }
echo "seal: $op with $(sha256sum "$key" | cut -c1-16) (PROTOTYPE: no crypto)" >&2
cat
`

const sudoersBody = "ocel-deploy ALL=(root) NOPASSWD: " + sealHelper + "\n"

func (h host) must(ctx context.Context, command string) error {
	if _, err := h.check(ctx, command); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady, "%v", err)
	}
	return nil
}

func (h host) write(ctx context.Context, path, body, owner, mode string) error {
	command := h.sudo(shellQuote("install", "-o", owner, "-g", owner, "-m", mode, "/dev/stdin", path))
	if _, errOut, err := h.run(ctx, []byte(body), command); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady, "writing %s: %v: %s", path, err, strings.TrimSpace(errOut))
	}
	return nil
}

func shellQuote(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && strings.IndexFunc(part, unsafeInShell) < 0 {
			quoted = append(quoted, part)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(part, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}

func unsafeInShell(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case strings.ContainsRune("_@%+=:,./-", r):
		return false
	}
	return true
}

func sha256hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func wouldWriteDigest(items []item) string {
	bodies := make([]string, 0, len(items))
	for _, each := range items {
		bodies = append(bodies, each.kind+" "+each.name+" "+each.body)
	}
	sort.Strings(bodies)
	return sha256hex(strings.Join(bodies, "\n"))
}
