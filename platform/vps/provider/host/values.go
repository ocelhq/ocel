package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	envFileSuffix = ".env"
	envDigestLen  = 12
	forgetWindow  = 30 * time.Second
	orphanMinutes = "10"
)

const registryPrefix = "registry."

func sweepCommand() string {
	return "find " + quoted(stateRoot) + " -mindepth 1 -maxdepth 2 " +
		`\( -type f -name ` + quoted("*"+envFileSuffix) + " -o -type d -name " + quoted(registryPrefix+"*") + ` \)` +
		" -mmin +" + orphanMinutes + " -prune -exec rm -rf {} + 2>/dev/null || true"
}

func (h *Host) sweep(ctx context.Context, elevation string) error {
	_, err := h.ran(ctx, "sweep what an interrupted deploy left under "+stateRoot, sweepCommand(), nil, elevation)
	return err
}

func EnvFile(class edge.Class, container string) string {
	return StateDir(class) + "/" + container + envFileSuffix
}

const (
	handedDir     = "handed"
	handedKnown   = "held"
	handedUnknown = "unknown"
)

func HandedNote(class edge.Class, container string) string {
	return StateDir(class) + "/" + handedDir + "/" + container
}

type Note struct {
	Handed []string        `json:"handed"`
	Live   json.RawMessage `json:"live,omitempty"`
}

func RenderNote(spec Container) ([]byte, error) {
	note := Note{Handed: slices.Sorted(maps.Keys(spec.Env))}
	if len(spec.Manifest) > 0 {
		note.Live = json.RawMessage(spec.Manifest)
	}
	written, err := json.Marshal(note)
	if err != nil {
		return nil, err
	}
	return append(written, '\n'), nil
}

func ParseNote(raw []byte) (Note, error) {
	var note Note
	if err := json.Unmarshal(raw, &note); err != nil {
		return Note{}, refusal.Refuse(refusal.CodeNotReady,
			"unreadable container values note: %v", err)
	}
	return note, nil
}

func (h *Host) note(ctx context.Context, spec Container) error {
	rendered, err := RenderNote(spec)
	if err != nil {
		return err
	}
	_, err = h.ran(ctx, "note the names "+spec.App+" is handed",
		"install -D -m 0600 /dev/stdin "+quoted(HandedNote(spec.Class, spec.Name)), bytes.NewReader(rendered), "")
	return err
}

func handedCommand(class edge.Class, container string) string {
	note := quoted(HandedNote(class, container))
	return "if [ -f " + note + " ]; then echo " + handedKnown + "; cat " + note + "; else echo " + handedUnknown + "; fi"
}

func (h *Host) handed(ctx context.Context, spec Container) (Note, bool, error) {
	said, err := h.ran(ctx, "ask what "+spec.Name+" was handed", handedCommand(spec.Class, spec.Name), nil, "")
	if err != nil {
		return Note{}, false, err
	}
	verdict, rest, _ := strings.Cut(said, "\n")
	if strings.TrimSpace(verdict) != handedKnown {
		return Note{}, false, nil
	}
	note, err := ParseNote([]byte(rest))
	if err != nil {
		return Note{}, false, err
	}
	return note, true, nil
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
	env := spec.delivered()
	digest, err := envDigest(spec.Name, env)
	if err != nil {
		return handoff{}, err
	}
	held := handoff{digest: digest}
	if len(env) > 0 {
		held.path = EnvFile(spec.Class, spec.Name)
	}
	return held, nil
}

func (h *Host) hand(ctx context.Context, held handoff, spec Container) error {
	if held.path == "" {
		return nil
	}
	rendered, err := RenderEnvFile(spec.delivered())
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
		return refusal.Refuse(refusal.CodeNotReady,
			"could not remove %s on %s, which holds this deploy's values in plaintext: %v",
			held.path, h.named(), err)
	}
	return nil
}
