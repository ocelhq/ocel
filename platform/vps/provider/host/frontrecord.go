package host

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const FrontRecordPath = boxapi.StateRoot + "/proxy.json"

type frontRecord struct {
	Proxy      *front.Proxy     `json:"proxy"`
	Project    string           `json:"project,omitempty"`
	Tier       environment.Tier `json:"tier"`
	MovingFrom *frontRecord     `json:"movingFrom,omitempty"`
}

func (r frontRecord) front() front.Proxy {
	if r.Proxy == nil {
		return front.Proxy{}
	}
	return *r.Proxy
}

func (r frontRecord) setter() string {
	if r.Project == "" {
		return string(r.Tier)
	}
	return r.Project + "/" + string(r.Tier)
}

func (r frontRecord) item() (machine.Item, error) {
	written, err := json.Marshal(r)
	if err != nil {
		return machine.Item{}, err
	}
	return machine.Item{
		Kind:    machine.KindFile,
		Name:    FrontRecordPath,
		Mode:    0o644,
		Owner:   machine.RootOwner,
		Content: append(written, '\n'),
		Note:    "which proxy fronts this box, and who set it",
	}, nil
}

func refuseDisagreeing(f front.Proxy, record frontRecord, tier environment.Tier) error {
	recorded := record.front()
	if f.Same(recorded) {
		return nil
	}
	remedy := "remove `\"proxy\"` from this project's vps options"
	if recorded.Adopted() {
		remedy = "add `\"proxy\": " + recorded.Front().Spelled() + "`"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"this box routes through %s (set by %s); %s\nOne proxy fronts a box: to move it to another, write the new `proxy` and `%s` moves the box",
		recorded.Front().Named(), record.setter(), remedy, provider.BootstrapCommand(tier))
}

func frontReading() string {
	at := shell.Quote(FrontRecordPath)
	return "if [ -f " + at + " ]; then cat " + at + "; fi"
}

func (r frontRecord) listStranded(to front.Proxy) []machine.Removal {
	stranded := listOwned(to)
	kept := len(stranded)
	for moved := r; moved.MovingFrom != nil; moved = *moved.MovingFrom {
		for _, taken := range (frontMove{from: moved.MovingFrom.front(), to: moved.front()}).listRemovals() {
			if !slices.ContainsFunc(stranded, func(listed machine.Removal) bool { return listed.Kind == taken.Kind && listed.Path == taken.Path }) {
				stranded = append(stranded, taken)
			}
		}
	}
	return stranded[kept:]
}

func listOwned(f front.Proxy) []machine.Removal {
	if f.Adopted() {
		return placedRemovals(f)
	}
	return frontMove{from: f}.listRemovals()
}

func (h *Host) frontRecorded(ctx context.Context, as ssh.Acting) (*frontRecord, error) {
	said, err := h.Run(ctx, as, "read which proxy fronts this box", frontReading(), nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(said) == "" {
		return nil, nil
	}
	var record frontRecord
	if err := json.Unmarshal([]byte(said), &record); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s on %s is not a record this ocel can read: %v\nRemove it and run `%s`",
			FrontRecordPath, h.Named(), err, provider.BootstrapCommand(environment.TierProduction))
	}
	return &record, nil
}

func (h *Host) RefuseDisagreeingFront(ctx context.Context, tier environment.Tier) error {
	record, err := h.frontRecorded(ctx, ssh.RootWhenAble)
	if err != nil {
		return err
	}
	if record == nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s records no proxy for %s, so this deploy cannot tell what fronts it\nRun `%s`",
			FrontRecordPath, h.Named(), provider.BootstrapCommand(tier))
	}
	if record.MovingFrom != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"this box is moving from %s to %s (set by %s), and no deploy lands until the move is finished\nRun `%s` to finish it",
			record.MovingFrom.front().Front().Named(), record.front().Front().Named(), record.setter(), provider.BootstrapCommand(tier))
	}
	return refuseDisagreeing(h.proxyOption, *record, tier)
}

func (b Bootstrap) recorded(ctx context.Context, read Reading) (Reading, error) {
	existing, err := b.host.frontRecorded(ctx, ssh.RootWhenAble)
	if err != nil {
		return Reading{}, err
	}
	option := b.host.proxyOption
	record := frontRecord{Proxy: option.Recorded(), Project: b.project, Tier: read.Tier}
	moving := record
	switch {
	case existing != nil && existing.MovingFrom != nil && option.Same(existing.front()):
		record, moving = *existing, *existing
		record.MovingFrom = nil
		read.move = &frontMove{host: b.host, from: existing.MovingFrom.front(), to: option, resumed: true, stranded: existing.MovingFrom.listStranded(option)}
	case existing != nil && option.Same(existing.front()):
		record = *existing
	case existing != nil:
		moving.MovingFrom = existing
		read.move = &frontMove{host: b.host, from: existing.front(), to: option, stranded: existing.listStranded(option)}
	case read.observed(machine.KindContainer, caddy.Container) && option.Adopted():
		moving.MovingFrom = &frontRecord{Tier: read.Tier}
		read.move = &frontMove{host: b.host, to: option}
	}
	if read.move != nil {
		if read.move.table, err = b.host.routing.ReadMovedTable(ctx); err != nil {
			return Reading{}, err
		}
		if read.move.recorded, err = moving.item(); err != nil {
			return Reading{}, err
		}
	}
	item, err := record.item()
	if err != nil {
		return Reading{}, err
	}
	read.recorded = []machine.Item{item}
	return read, nil
}
