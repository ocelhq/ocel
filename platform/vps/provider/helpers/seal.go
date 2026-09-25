package helpers

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type SealTransport interface {
	Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error)
}

type Sealer struct{ over SealTransport }

func SealerOver(over SealTransport) *Sealer { return &Sealer{over: over} }

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
	rendered, err := s.over.Seal(ctx, verb+" a value at "+at.Name, argv, bytes.NewReader(fed))
	if err != nil {
		return nil, err
	}
	written, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"the seal helper answered a %s with %d unreadable bytes", verb, len(rendered))
	}
	return written, nil
}

func sealArgv(verb string, at providerkit.Coordinate) ([]string, error) {
	if at.Class == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s names no class", at.Name)
	}
	argv := []string{SealPath, string(at.Class), verb}
	for _, named := range [][2]string{
		{"project", at.Project},
		{"env", at.Env},
		{"folder", at.Folder},
		{"binding", at.Binding},
		{"name", at.Name},
	} {
		if named[1] == "" && named[0] != "binding" {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"a value's coordinate names no %s", named[0])
		}
		argv = append(argv, "--"+named[0], named[1])
	}
	return argv, nil
}

var _ providerkit.Sealer = (*Sealer)(nil)
