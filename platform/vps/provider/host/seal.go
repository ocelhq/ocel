package host

import (
	_ "embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

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
	return Item{Kind: KindSealKey, Name: SealKeyPath(tier), Mode: sealKeyMode, Owner: rootOwner, Tier: tier, Note: "seals secret values",
		script: Item.mint, survey: sealSurvey}
}

func sealSudoers(tier environment.Tier) []byte {
	allowed := make([]string, 0, 2)
	for _, verb := range []string{boxapi.SealVerbSeal, boxapi.SealVerbOpen} {
		allowed = append(allowed, LiveBinary+" "+boxapi.SealCommand+" "+string(tier)+" "+verb+" *")
	}
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + strings.Join(allowed, ", ") + "\n")
}

func (i Item) mint() string {
	name := shell.Quote(i.Name)
	return "if [ -e " + name + " ]; then chown " + rootOwner + ":" + rootOwner + " " + name +
		fmt.Sprintf(" && chmod %04o ", i.Mode) + name + "; else " +
		shell.Words([]string{LiveBinary, boxapi.SealCommand, string(i.Tier), boxapi.SealVerbInit}) + "; fi"
}

func sealSurvey(item Item) string {
	name := shell.Quote(item.Name)
	return "if [ -h " + name + " ]; then " + reports(shell.Quote(kindLink), name, "0", `''`, `"$(readlink `+name+`)"`) + `
elif [ -f ` + name + " ]; then printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' " +
		shell.Quote(KindSealKey) + " " + name +
		` "$(stat -c %a ` + name + `)" "$(stat -c %U ` + name + `)"` +
		` "$(sha256sum ` + name + ` | cut -d' ' -f1)"` +
		` "$(date -u -d @"$(stat -c %Y ` + name + `)" +%Y-%m-%dT%H:%M:%SZ)"
fi`
}
