package boxstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

type SealTransport interface {
	Seal(ctx context.Context, what string, tier environment.Tier, argv []string, stdin io.Reader) (string, error)
}

type Cipher struct{ over SealTransport }

func NewCipher(over SealTransport) *Cipher { return &Cipher{over: over} }

func (s *Cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	return s.runSealHelper(ctx, "seal", tier, bound, plaintext)
}

func (s *Cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	return s.runSealHelper(ctx, "open", tier, bound, sealed)
}

func (s *Cipher) runSealHelper(ctx context.Context, verb string, tier environment.Tier, bound seal.AssociatedData, body []byte) ([]byte, error) {
	if tier == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a value bound to %s names no tier", bound.Bytes())
	}
	argv := []string{SealHelper, string(tier), verb}
	for _, field := range bound {
		argv = append(argv, "--"+field.Name, field.Value)
	}
	fed := append([]byte(base64.StdEncoding.EncodeToString(body)), '\n')
	rendered, err := s.over.Seal(ctx, verb+" a value bound to "+string(bound.Bytes()), tier, argv, bytes.NewReader(fed))
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
