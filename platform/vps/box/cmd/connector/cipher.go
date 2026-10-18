package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

type agentCipher struct{ argv []string }

func newAgentCipher() agentCipher {
	return agentCipher{argv: []string{"sudo", "-n", boxapi.LiveBinary, boxapi.SealCommand}}
}

func (c agentCipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	return c.run(ctx, boxapi.SealVerbSeal, tier, bound, plaintext)
}

func (c agentCipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	return c.run(ctx, boxapi.SealVerbOpen, tier, bound, sealed)
}

func (c agentCipher) run(ctx context.Context, verb string, tier environment.Tier, bound seal.AssociatedData, body []byte) ([]byte, error) {
	if tier == "" {
		return nil, fmt.Errorf("a value bound to %s names no tier", bound.Bytes())
	}
	argv := append(append([]string{}, c.argv...), string(tier), verb, string(bound.Bytes()))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(body) + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		said := strings.TrimSpace(stderr.String())
		if said == "" {
			said = err.Error()
		}
		return nil, fmt.Errorf("%s a value bound to %s: %s", verb, bound.Bytes(), said)
	}
	written, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil {
		return nil, fmt.Errorf("the box agent answered a %s with %d unreadable bytes", verb, stdout.Len())
	}
	return written, nil
}

var _ seal.Cipher = agentCipher{}
