package envfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	Suffix        = ".env"
	DigestLen     = 12
	ForgetWindow  = 30 * time.Second
	OrphanMinutes = "10"
)

func SweepCommand() string {
	return "find " + shell.Quote(boxapi.StateDir(environment.TierProduction)) + " " + shell.Quote(boxapi.StateDir(environment.TierPreview)) +
		" -mindepth 1 -maxdepth 1 -type f -name " + shell.Quote("*"+Suffix) +
		" -mmin +" + OrphanMinutes + " -exec rm -f {} + 2>/dev/null || true"
}

func Sweep(ctx context.Context, box *ssh.Box, elevation ssh.Acting) error {
	_, err := box.Run(ctx, elevation, "sweep what an interrupted deploy left under "+boxapi.StateRoot, SweepCommand(), nil)
	return err
}

func Path(tier environment.Tier, container string) string {
	return boxapi.StateDir(tier) + "/" + container + Suffix
}

func PipedInstall(flags, path string) string {
	return "cat | install " + flags + " /dev/stdin " + shell.Quote(path)
}

func Render(env map[string]string) ([]byte, error) {
	var written bytes.Buffer
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if err := refuseUnwritableLine(key, env[key]); err != nil {
			return nil, err
		}
		written.WriteString(key + "=" + env[key] + "\n")
	}
	return written.Bytes(), nil
}

func refuseUnwritableLine(key, value string) error {
	switch {
	case key == "":
		return refusal.Refuse(refusal.CodeInvalid,
			"a value is declared with an empty name")
	case strings.ContainsAny(key, "\n\r"):
		return refusal.Refuse(refusal.CodeInvalid,
			"the name %q contains a line break", key)
	case strings.Contains(key, "="):
		return refusal.Refuse(refusal.CodeInvalid,
			"the name %q contains %q", key, "=")
	case strings.HasPrefix(key, "#"):
		return refusal.Refuse(refusal.CodeInvalid,
			"the name %q begins with %q", key, "#")
	case strings.ContainsAny(value, "\n\r"):
		return refusal.Refuse(refusal.CodeInvalid,
			"the value of %s contains a line break", key)
	}
	return nil
}

func Digest(container string, env map[string]string) (string, error) {
	rendered, err := Render(env)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(container+"\n"), rendered...))
	return hex.EncodeToString(sum[:])[:DigestLen], nil
}

type Delivery struct {
	Path   string
	Digest string
}
