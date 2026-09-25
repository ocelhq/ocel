package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

const FrontRecordPath = live.StateRoot + "/proxy.json"

type frontRecord struct {
	Proxy   *Front            `json:"proxy"`
	Project string            `json:"project,omitempty"`
	Class   providerkit.Class `json:"class"`
}

func (f Front) recorded() *Front {
	if !f.adopted() {
		return nil
	}
	return &f
}

func (r frontRecord) front() Front {
	if r.Proxy == nil {
		return Front{}
	}
	return *r.Proxy
}

func (r frontRecord) setter() string {
	if r.Project == "" {
		return string(r.Class)
	}
	return r.Project + "/" + string(r.Class)
}

func (r frontRecord) item() (Item, error) {
	written, err := json.Marshal(r)
	if err != nil {
		return Item{}, err
	}
	return Item{
		Kind:    KindFile,
		Name:    FrontRecordPath,
		Mode:    0o644,
		Owner:   rootOwner,
		Content: append(written, '\n'),
		Note:    "which proxy fronts this box, and who set it",
	}, nil
}

func (f Front) named() string {
	if f.adopted() {
		return "a proxy you route yourself"
	}
	return "ocel's own proxy"
}

func (f Front) spelled() string {
	switch {
	case f.Manual == nil:
		return ""
	case f.Manual.Port == manual.DefaultPort:
		return `"manual"`
	default:
		return fmt.Sprintf(`{ "manual": { "port": %d } }`, f.Manual.Port)
	}
}

func (f Front) same(other Front) bool {
	if f.adopted() != other.adopted() {
		return false
	}
	return !f.adopted() || f.Manual.Port == other.Manual.Port
}

func (f Front) agrees(held Front, setter string) error {
	if f.same(held) {
		return nil
	}
	remedy := "remove `\"proxy\"` from this project's vps options"
	if held.adopted() {
		remedy = "add `\"proxy\": " + held.spelled() + "`"
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"this box routes through %s (set by %s); %s\nOne proxy fronts a box, and moving a box to another is not supported yet",
		held.named(), setter, remedy)
}

func frontReading() string {
	at := quoted(FrontRecordPath)
	return "if [ -f " + at + " ]; then cat " + at + "; fi"
}

func (h *Host) frontRecorded(ctx context.Context, ask asking) (*frontRecord, error) {
	said, err := ask(ctx, "read which proxy fronts this box", frontReading(), nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(said) == "" {
		return nil, nil
	}
	var record frontRecord
	if err := json.Unmarshal([]byte(said), &record); err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s on %s is not a record this ocel can read: %v\nRemove it and run `%s`",
			FrontRecordPath, h.named(), err, providerkit.BootstrapCommand(providerkit.ClassProduction))
	}
	return &record, nil
}

func (h *Host) FrontAgrees(ctx context.Context) error {
	record, err := h.frontRecorded(ctx, h.reach)
	if err != nil {
		return err
	}
	if record == nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s records no proxy for %s, so this deploy cannot tell what fronts it\nRun `%s`",
			FrontRecordPath, h.named(), providerkit.BootstrapCommand(providerkit.ClassProduction))
	}
	return h.fronts.agrees(record.front(), record.setter())
}

const unrecordedSetter = "a bootstrap that left no record"

func (b Bootstrapper) recorded(ctx context.Context, read Reading) (Reading, error) {
	held, err := b.host.frontRecorded(ctx, b.host.reach)
	if err != nil {
		return Reading{}, err
	}
	record := frontRecord{Proxy: b.host.fronts.recorded(), Project: b.project, Class: read.Class}
	switch {
	case held != nil:
		if err := b.host.fronts.agrees(held.front(), held.setter()); err != nil {
			return Reading{}, err
		}
		record = *held
	case read.standing(KindContainer, caddy.Container):
		if err := b.host.fronts.agrees(Front{}, unrecordedSetter); err != nil {
			return Reading{}, err
		}
	}
	item, err := record.item()
	if err != nil {
		return Reading{}, err
	}
	read.recorded = []Item{item}
	return read, nil
}
