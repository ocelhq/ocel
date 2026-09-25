package host

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/helpers"
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

func sealKey(class providerkit.Class) Item {
	return Item{Kind: KindSealKey, Name: SealKeyPath(class), Mode: sealKeyMode, Owner: rootOwner, Class: class, Note: "seals secret values"}
}

func sealSudoers(class providerkit.Class) []byte {
	allowed := make([]string, 0, 2)
	for _, verb := range []string{"seal", "open"} {
		allowed = append(allowed, SealHelper+" "+string(class)+" "+verb+" *")
	}
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + strings.Join(allowed, ", ") + "\n")
}

func (i Item) mint() string {
	name := quoted(i.Name)
	return "if [ -e " + name + " ]; then chown " + rootOwner + ":" + rootOwner + " " + name +
		fmt.Sprintf(" && chmod %04o ", i.Mode) + name + "; else " +
		`command -v python3 >/dev/null 2>&1 || { echo 'the seal helper needs python3' >&2; exit 1; }
` + quoted(SealHelper) + " " + quoted(string(i.Class)) + " init; fi"
}

func NewSealer(h *Host) *helpers.Sealer { return helpers.SealerOver(sshSeal{host: h}) }

type sshSeal struct{ host *Host }

func (s sshSeal) Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	return s.host.granted(ctx, what, argv, stdin)
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
