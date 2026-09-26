package host

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func sealKey(class edge.Class) Item {
	return Item{Kind: KindSealKey, Name: SealKeyPath(class), Mode: sealKeyMode, Owner: rootOwner, Class: class, Note: "seals secret values"}
}

func sealSudoers(class edge.Class) []byte {
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

type SealTransport interface {
	Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error)
}

type Cipher struct{ over SealTransport }

func NewCipher(h *Host) *Cipher { return &Cipher{over: sshSeal{host: h}} }

func CipherOver(over SealTransport) *Cipher { return &Cipher{over: over} }

type sshSeal struct{ host *Host }

func (s sshSeal) Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	return s.host.granted(ctx, what, argv, stdin)
}

func (s *Cipher) Seal(ctx context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	return s.through(ctx, "seal", at, plaintext)
}

func (s *Cipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	return s.through(ctx, "open", at, sealed)
}

func (s *Cipher) through(ctx context.Context, verb string, at records.SealScope, body []byte) ([]byte, error) {
	argv, err := sealArgv(verb, at)
	if err != nil {
		return nil, err
	}
	fed := append([]byte(base64.StdEncoding.EncodeToString(body)), '\n')
	rendered, err := s.over.Seal(ctx, verb+" a value at "+at.Name, argv, bytes.NewReader(fed))
	if err != nil {
		return nil, err
	}
	written, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied,
			"the seal helper answered a %s with %d unreadable bytes", verb, len(rendered))
	}
	return written, nil
}

func sealArgv(verb string, at records.SealScope) ([]string, error) {
	if at.Class == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s names no class", at.Name)
	}
	argv := []string{SealHelper, string(at.Class), verb}
	for _, named := range [][2]string{
		{"project", at.Project},
		{"env", at.Env},
		{"folder", at.Folder},
		{"binding", at.Binding},
		{"name", at.Name},
	} {
		if named[1] == "" && named[0] != "binding" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a value's coordinate names no %s", named[0])
		}
		argv = append(argv, "--"+named[0], named[1])
	}
	return argv, nil
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

var _ records.Cipher = (*Cipher)(nil)
