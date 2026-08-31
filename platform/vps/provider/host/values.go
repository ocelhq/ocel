package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	envFileSuffix = ".env"
	envDigestLen  = 12
	forgetWindow  = 30 * time.Second
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
			"the name %q breaks a line, and a container is handed one name and one value per line", key)
	case strings.Contains(key, "="):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the name %q carries %q, which is what separates a name from its value on the line a container reads it off", key, "=")
	case strings.HasPrefix(key, "#"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"the name %q begins with %q, and a line beginning that way is read as a note and never bound at all", key, "#")
	case strings.ContainsAny(value, "\n\r"):
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s holds a value that breaks a line: a container is handed one value per line, so what follows the break would be read as another name and the value itself truncated", key)
	}
	return nil
}

func envDigest(container string, env map[string]string) (string, error) {
	rendered, err := RenderEnvFile(env)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(container+"\n"), rendered...))
	return hex.EncodeToString(sum[:])[:envDigestLen], nil
}

type handoff struct {
	path   string
	digest string
}

func handing(spec Container) (handoff, error) {
	digest, err := envDigest(spec.Name, spec.Env)
	if err != nil {
		return handoff{}, err
	}
	held := handoff{digest: digest}
	if len(spec.Env) > 0 {
		held.path = EnvFile(spec.Class, spec.Name)
	}
	return held, nil
}

func (h *Host) hand(ctx context.Context, held handoff, spec Container) error {
	if held.path == "" {
		return nil
	}
	rendered, err := RenderEnvFile(spec.Env)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "write the values "+spec.App+" is handed",
		"install -m 0600 /dev/stdin "+quoted(held.path), bytes.NewReader(rendered), "")
	return err
}

func (h *Host) forget(ctx context.Context, held handoff) error {
	if held.path == "" {
		return nil
	}
	taking, stop := context.WithTimeout(context.WithoutCancel(ctx), forgetWindow)
	defer stop()
	if _, err := h.ran(taking, "take back "+held.path, "rm -f "+quoted(held.path), nil, ""); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s is left standing on %s and holds every value this deploy resolved in plaintext, which no deploy after this one will take back: %v",
			held.path, h.named(), err)
	}
	return nil
}
