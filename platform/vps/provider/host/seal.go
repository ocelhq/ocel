package host

import (
	_ "embed"
	"io/fs"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	return Item{Kind: KindSealKey, Name: SealKeyPath(class), Mode: sealKeyMode, Owner: rootOwner, Class: class}
}

func sealSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + sealHelper + "\n")
}

func (i Item) mint() string {
	name := quoted(i.Name)
	return "if [ -e " + name + " ]; then chown " + rootOwner + ":" + rootOwner + " " + name +
		" && chmod " + sprintMode(i.Mode) + " " + name +
		"; else " + quoted(sealHelper) + " " + quoted(string(i.Class)) + " init; fi"
}

func sealSurvey(item Item) string {
	name := quoted(item.Name)
	return "if [ -f " + name + " ]; then printf '%s\\t%s\\t%s\\t%s\\t%s\\t%s\\n' " +
		quoted(KindSealKey) + " " + name +
		` "$(stat -c %a ` + name + `)" "$(stat -c %U ` + name + `)"` +
		` "$(sha256sum ` + name + ` | cut -d' ' -f1)"` +
		` "$(date -u -d @"$(stat -c %Y ` + name + `)" +%Y-%m-%dT%H:%M:%SZ)"
fi`
}
