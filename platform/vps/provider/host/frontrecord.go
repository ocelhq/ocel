package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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

func frontRecordItem(front Front, project string, class providerkit.Class) (Item, error) {
	written, err := json.Marshal(frontRecord{Proxy: front.recorded(), Project: project, Class: class})
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

func (f Front) agrees(record frontRecord) error {
	held := record.front()
	if f.same(held) {
		return nil
	}
	remedy := "remove `\"proxy\"` from this project's vps options"
	if held.adopted() {
		remedy = "add `\"proxy\": " + held.spelled() + "`"
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"this box routes through %s (set by %s); %s\nOne proxy fronts a box, and moving a box to another is not supported yet",
		held.named(), record.setter(), remedy)
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
	return h.fronts.agrees(*record)
}

type fronting struct {
	item    Item
	present bool
}

func (b Bootstrapper) fronted(ctx context.Context, class providerkit.Class) (fronting, error) {
	item, err := frontRecordItem(b.host.fronts, b.project, class)
	if err != nil {
		return fronting{}, err
	}
	record, err := b.host.frontRecorded(ctx, b.host.reach)
	if err != nil || record == nil {
		return fronting{item: item}, err
	}
	return fronting{item: item, present: true}, b.host.fronts.agrees(*record)
}

func (f fronting) change() providerkit.Change {
	change := providerkit.Change{Kind: f.item.Kind, Name: f.item.Name, Action: providerkit.ActionCreate, Reason: f.item.Note}
	if f.present {
		change.Action, change.Reason = providerkit.ActionKeep, "set already, and this project agrees"
	}
	return change
}

func (f fronting) write(ctx context.Context, h *Host, report providerkit.Reporter) error {
	if f.present {
		say(report, f.item.ID()+": "+reasonStanding)
		return nil
	}
	if err := h.Install(ctx, f.item); err != nil {
		return err
	}
	say(report, "wrote "+f.item.ID())
	return nil
}
