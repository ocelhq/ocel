package release

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	handedDir     = "handed"
	HandedKnown   = "known"
	HandedUnknown = "unknown"
)

func HandedNote(tier environment.Tier, container string) string {
	return boxapi.StateDir(tier) + "/" + handedDir + "/" + container
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

func (c *Containers) note(ctx context.Context, spec Container) error {
	rendered, err := RenderNote(spec)
	if err != nil {
		return err
	}
	acting, err := ssh.Deploy, c.box.Assume(ctx, ssh.Deploy)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, acting, "note the names "+spec.App+" is handed", envfile.PipedInstall("-D -m 0600", HandedNote(spec.Tier, spec.Name)), bytes.NewReader(rendered))
	return err
}

func handedCommand(tier environment.Tier, container string) string {
	note := shell.Quote(HandedNote(tier, container))
	return "if [ -f " + note + " ]; then echo " + HandedKnown + "; cat " + note + "; else echo " + HandedUnknown + "; fi"
}

func (c *Containers) handed(ctx context.Context, spec Container) (Note, bool, error) {
	acting, err := ssh.Deploy, c.box.Assume(ctx, ssh.Deploy)
	if err != nil {
		return Note{}, false, err
	}
	said, err := c.box.Run(ctx, acting, "ask what "+spec.Name+" was handed", handedCommand(spec.Tier, spec.Name), nil)
	if err != nil {
		return Note{}, false, err
	}
	verdict, rest, _ := strings.Cut(said, "\n")
	if strings.TrimSpace(verdict) != HandedKnown {
		return Note{}, false, nil
	}
	note, err := ParseNote([]byte(rest))
	if err != nil {
		return Note{}, false, err
	}
	return note, true, nil
}

func Handing(spec Container) (envfile.Delivery, error) {
	env := spec.delivered()
	digest, err := envfile.Digest(spec.Name, env)
	if err != nil {
		return envfile.Delivery{}, err
	}
	delivery := envfile.Delivery{Digest: digest}
	if len(env) > 0 {
		delivery.Path = envfile.Path(spec.Tier, spec.Name)
	}
	return delivery, nil
}

func (c *Containers) hand(ctx context.Context, delivery envfile.Delivery, spec Container) error {
	if delivery.Path == "" {
		return nil
	}
	rendered, err := envfile.Render(spec.delivered())
	if err != nil {
		return err
	}
	acting, err := ssh.Deploy, c.box.Assume(ctx, ssh.Deploy)
	if err != nil {
		return err
	}
	_, err = c.box.Run(ctx, acting, "write the values "+spec.App+" is handed", envfile.PipedInstall("-m 0600", delivery.Path), bytes.NewReader(rendered))
	return err
}

func (c *Containers) forget(ctx context.Context, delivery envfile.Delivery) error {
	if delivery.Path == "" {
		return nil
	}
	taking, stop := context.WithTimeout(context.WithoutCancel(ctx), envfile.ForgetWindow)
	defer stop()
	if _, err := c.box.Run(taking, ssh.Login, "take back "+delivery.Path, "rm -f "+shell.Quote(delivery.Path), nil); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"could not remove %s on %s, which has this deploy's values in plaintext: %v",
			delivery.Path, c.box.Named(), err)
	}
	return nil
}
