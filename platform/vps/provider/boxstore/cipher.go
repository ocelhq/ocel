package boxstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/tracing/content"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Cipher struct{ box *ssh.Box }

func NewCipher(box *ssh.Box) *Cipher { return &Cipher{box: box} }

func (s *Cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	return s.run(ctx, boxapi.SealVerbSeal, tier, bound, plaintext)
}

func (s *Cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	return s.run(content.Omit(ctx), boxapi.SealVerbOpen, tier, bound, sealed)
}

func SealCommand(tier environment.Tier, verb string, bound seal.AssociatedData) ssh.LiveCommand {
	return ssh.LiveCommand{
		Fixed: []string{boxapi.LiveBinary, boxapi.SealCommand, string(tier), verb},
		Rest:  []string{string(bound.Bytes())},
	}
}

func (s *Cipher) run(ctx context.Context, verb string, tier environment.Tier, bound seal.AssociatedData, body []byte) ([]byte, error) {
	if tier == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a value bound to %s names no tier", bound.Bytes())
	}
	fed := append([]byte(base64.StdEncoding.EncodeToString(body)), '\n')
	rendered, err := s.box.RunLive(ctx, ssh.Granted, verb+" a value bound to "+string(bound.Bytes()), SealCommand(tier, verb, bound), bytes.NewReader(fed))
	if err != nil {
		return nil, err
	}
	written, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied,
			"the box answered a %s with %d unreadable bytes", verb, len(rendered))
	}
	return written, nil
}

var _ seal.Cipher = (*Cipher)(nil)
