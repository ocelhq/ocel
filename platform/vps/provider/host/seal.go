package host

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

//go:embed seal.py
var sealScript []byte

const SealAlgorithm = "aes-256-gcm"

const (
	sealKeyBytes             = 32
	sealKeyMode  fs.FileMode = 0o400
)

type Seal struct {
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	CreatedAt   string `json:"createdAt"`
}

func sealKey(tier environment.Tier) Item {
	return Item{Kind: KindSealKey, Name: SealKeyPath(tier), Mode: sealKeyMode, Owner: rootOwner, Tier: tier, Note: "seals secret values"}
}

func sealSudoers(tier environment.Tier) []byte {
	allowed := make([]string, 0, 2)
	for _, verb := range []string{"seal", "open"} {
		allowed = append(allowed, boxstore.SealHelper+" "+string(tier)+" "+verb+" *")
	}
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + strings.Join(allowed, ", ") + "\n")
}

func (i Item) mint() string {
	name := quoted(i.Name)
	return "if [ -e " + name + " ]; then chown " + rootOwner + ":" + rootOwner + " " + name +
		fmt.Sprintf(" && chmod %04o ", i.Mode) + name + "; else " +
		`command -v python3 >/dev/null 2>&1 || { echo 'the seal helper needs python3' >&2; exit 1; }
` + quoted(boxstore.SealHelper) + " " + quoted(string(i.Tier)) + " init; fi"
}

func NewCipher(h *Host) *boxstore.Cipher { return boxstore.NewCipher(sshSeal{host: h}) }

type sshSeal struct{ host *Host }

func (s sshSeal) Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	rendered, err := s.host.granted(ctx, what, argv, stdin)
	if err == nil || len(argv) < 2 {
		return rendered, err
	}
	installed, readErr := s.host.reach(ctx, "read the seal helper", "cat "+quoted(boxstore.SealHelper), nil)
	if readErr != nil || installed == string(sealScript) {
		return "", err
	}
	tier := environment.Tier(argv[1])
	return "", refusal.Refuse(refusal.CodeNotReady,
		"%s\nThe seal helper on this box is not the one this ocel installs.\nRun `%s` to update it, then try again",
		err, provider.BootstrapCommand(tier))
}

func sealSurvey(item Item) string {
	name := quoted(item.Name)
	return "if [ -h " + name + " ]; then " + reports(quoted(kindLink), name, "0", `''`, `"$(readlink `+name+`)"`) + `
elif [ -f ` + name + " ]; then printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' " +
		quoted(KindSealKey) + " " + name +
		` "$(stat -c %a ` + name + `)" "$(stat -c %U ` + name + `)"` +
		` "$(sha256sum ` + name + ` | cut -d' ' -f1)"` +
		` "$(date -u -d @"$(stat -c %Y ` + name + `)" +%Y-%m-%dT%H:%M:%SZ)"
fi`
}
