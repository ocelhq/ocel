package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	envFileSuffix = ".env"
	envDigestLen  = 12
)

func EnvFile(class providerkit.Class, container string) string {
	return StateDir(class) + "/" + container + envFileSuffix
}

func RenderEnvFile(env map[string]string) ([]byte, error) {
	var written bytes.Buffer
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if err := writable(key, env[key]); err != nil {
			return nil, err
		}
		written.WriteString(key + "=" + env[key] + "\n")
	}
	return written.Bytes(), nil
}

func writable(key, value string) error {
	switch {
	case key == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"a value is declared under no name at all, and a container reads its environment by name")
	case strings.ContainsAny(key, "\n\r"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the name %q breaks a line, and a container is handoff one name and one value per line", key)
	case strings.Contains(key, "="):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the name %q carries %q, which is what separates a name from its value on the line a container reads it off", key, "=")
	case strings.HasPrefix(key, "#"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the name %q begins with %q, and a line beginning that way is read as a note and never bound at all", key, "#")
	case strings.ContainsAny(value, "\n\r"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s holds a value that breaks a line: a container is handoff one value per line, so what follows the break would be read as another name and the value itself truncated", key)
	}
	return nil
}

func envDigest(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "", nil
	}
	rendered, err := RenderEnvFile(env)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])[:envDigestLen], nil
}

type handoff struct {
	path   string
	digest string
}

func (h *Host) hand(ctx context.Context, spec Container) (handoff, error) {
	digest, err := envDigest(spec.Env)
	if err != nil {
		return handoff{}, err
	}
	if digest == "" {
		return handoff{}, nil
	}
	rendered, err := RenderEnvFile(spec.Env)
	if err != nil {
		return handoff{}, err
	}
	path := EnvFile(spec.Class, spec.Name)
	if _, err := h.ran(ctx, "write the values "+spec.App+" is handoff",
		"install -m 0600 /dev/stdin "+quoted(path), bytes.NewReader(rendered), ""); err != nil {
		return handoff{}, err
	}
	return handoff{path: path, digest: digest}, nil
}

func (h *Host) forget(ctx context.Context, held handoff) error {
	if held.path == "" {
		return nil
	}
	if _, err := h.ran(ctx, "take back "+held.path, "rm -f "+quoted(held.path), nil, ""); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s is left standing on %s and holds every value this deploy resolved in plaintext, which no deploy after this one will take back: %v",
			held.path, h.named(), err)
	}
	return nil
}
