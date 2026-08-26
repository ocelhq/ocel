package host

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"

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
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + SealHelper + "\n")
}

func (i Item) mint() string {
	name := quoted(i.Name)
	return "if [ -e " + name + " ]; then chown " + rootOwner + ":" + rootOwner + " " + name +
		fmt.Sprintf(" && chmod %04o ", i.Mode) + name + "; else " +
		`command -v python3 >/dev/null 2>&1 || { echo 'this host carries no python3, and the seal helper reaches its libcrypto for the AES-256-GCM every value is sealed with' >&2; exit 1; }
` + quoted(SealHelper) + " " + quoted(string(i.Class)) + " init; fi"
}

type Sealer struct{ host *Host }

func NewSealer(h *Host) *Sealer { return &Sealer{host: h} }

func (s *Sealer) Seal(ctx context.Context, at providerkit.Coordinate, plaintext []byte) ([]byte, error) {
	return s.through(ctx, "seal", at, plaintext)
}

func (s *Sealer) Open(ctx context.Context, at providerkit.Coordinate, sealed []byte) ([]byte, error) {
	return s.through(ctx, "open", at, sealed)
}

func (s *Sealer) through(ctx context.Context, verb string, at providerkit.Coordinate, body []byte) ([]byte, error) {
	argv, err := sealArgv(verb, at)
	if err != nil {
		return nil, err
	}
	fed := append([]byte(base64.StdEncoding.EncodeToString(body)), '\n')
	rendered, err := s.host.granted(ctx, verb+" a value at "+at.Name, argv, fed)
	if err != nil {
		return nil, err
	}
	written, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"the seal helper answered a %s with %d bytes, which is no value ocel sealed", verb, len(rendered))
	}
	return written, nil
}

func sealArgv(verb string, at providerkit.Coordinate) ([]string, error) {
	if at.Class == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s names no class, and this host mints one seal key per class", at.Name)
	}
	argv := []string{SealHelper, string(at.Class), verb}
	for _, named := range [][2]string{
		{"project", at.Project},
		{"env", at.Env},
		{"folder", at.Folder},
		{"link", at.Link},
		{"name", at.Name},
	} {
		if named[1] == "" && named[0] != "link" {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"a value's coordinate names no %s, and the coordinate is what a sealed value is bound to", named[0])
		}
		argv = append(argv, "--"+named[0], named[1])
	}
	return argv, nil
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

var _ providerkit.Sealer = (*Sealer)(nil)
