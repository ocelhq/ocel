package boxstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type SealTransport interface {
	Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error)
}

type Cipher struct{ over SealTransport }

func NewCipher(over SealTransport) *Cipher { return &Cipher{over: over} }

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
	if at.Tier == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s names no tier", at.Name)
	}
	argv := []string{SealHelper, string(at.Tier), verb}
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

var _ records.Cipher = (*Cipher)(nil)
