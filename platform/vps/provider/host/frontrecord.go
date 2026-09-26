package host

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

const FrontRecordPath = live.StateRoot + "/proxy.json"

type frontRecord struct {
	Proxy   *Front     `json:"proxy"`
	Project string     `json:"project,omitempty"`
	Class   edge.Class `json:"class"`
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
	switch {
	case f.Traefik != nil:
		return runBy(f.Traefik.Preset, "Traefik")
	case f.Caddy != nil:
		return runBy(f.Caddy.Preset, "Caddy")
	case f.Manual != nil:
		return "a proxy you route yourself"
	default:
		return "ocel's own proxy"
	}
}

func runBy(preset, proxy string) string {
	if tool, ok := hostTools[preset]; ok {
		return tool.name + "'s " + proxy
	}
	return "your " + proxy
}

func (f Front) spelled() string {
	switch {
	case f.Traefik != nil:
		return `{ "traefik": ` + f.Traefik.spelled().object() + ` }`
	case f.Caddy != nil:
		return `{ "caddy": ` + f.Caddy.spelled().object() + ` }`
	case f.Manual == nil:
		return ""
	}
	var fields spelling
	fields.number("port", f.Manual.Port, manual.DefaultPort)
	fields.text("network", f.Manual.Network, "")
	if len(fields) == 0 {
		return `"manual"`
	}
	return `{ "manual": ` + fields.object() + ` }`
}

func (t TraefikFront) spelled() spelling {
	base := TraefikFront{Preset: t.Preset}.Filled()
	var fields, entrypoints spelling
	fields.text("preset", t.Preset, "")
	fields.text("directory", t.Directory, base.Directory)
	fields.text("resolver", t.Resolver, base.Resolver)
	fields.text("previewResolver", t.PreviewResolver, base.PreviewResolver)
	entrypoints.text("http", t.Entrypoints.HTTP, base.Entrypoints.HTTP)
	entrypoints.text("https", t.Entrypoints.HTTPS, base.Entrypoints.HTTPS)
	if len(entrypoints) > 0 {
		fields = append(fields, strconv.Quote("entrypoints")+": "+entrypoints.object())
	}
	fields.text("network", t.Network, base.Network)
	fields.number("port", t.Port, base.Port)
	return fields
}

func (c CaddyFront) spelled() spelling {
	base := CaddyFront{Preset: c.Preset}.Filled()
	var fields spelling
	fields.text("preset", c.Preset, "")
	fields.text("directory", c.Directory, base.Directory)
	fields.text("container", c.Container, base.Container)
	fields.text("config", c.Config, base.Config)
	fields.text("network", c.Network, base.Network)
	fields.number("port", c.Port, base.Port)
	return fields
}

type spelling []string

func (s *spelling) text(key, value, base string) {
	if value != "" && value != base {
		*s = append(*s, strconv.Quote(key)+": "+strconv.Quote(value))
	}
}

func (s *spelling) number(key string, value, base int) {
	if value != 0 && value != base {
		*s = append(*s, strconv.Quote(key)+": "+strconv.Itoa(value))
	}
}

func (s spelling) object() string { return "{ " + strings.Join(s, ", ") + " }" }

func (f Front) same(other Front) bool { return reflect.DeepEqual(f.unlabelled(), other.unlabelled()) }

func (f Front) unlabelled() Front {
	if f.Traefik != nil {
		unset := *f.Traefik
		unset.Preset = ""
		f.Traefik = &unset
	}
	if f.Caddy != nil {
		unset := *f.Caddy
		unset.Preset = ""
		f.Caddy = &unset
	}
	return f
}

func (f Front) agrees(held Front, setter string) error {
	if f.same(held) {
		return nil
	}
	remedy := "remove `\"proxy\"` from this project's vps options"
	if held.adopted() {
		remedy = "add `\"proxy\": " + held.spelled() + "`"
	}
	return refusal.Refuse(refusal.CodeInvalid,
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
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s on %s is not a record this ocel can read: %v\nRemove it and run `%s`",
			FrontRecordPath, h.named(), err, provider.BootstrapCommand(edge.ClassProduction))
	}
	return &record, nil
}

func (h *Host) FrontAgrees(ctx context.Context) error {
	record, err := h.frontRecorded(ctx, h.reach)
	if err != nil {
		return err
	}
	if record == nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s records no proxy for %s, so this deploy cannot tell what fronts it\nRun `%s`",
			FrontRecordPath, h.named(), provider.BootstrapCommand(edge.ClassProduction))
	}
	return h.proxyOption.agrees(record.front(), record.setter())
}

const unrecordedSetter = "a bootstrap that left no record"

func (b Bootstrap) recorded(ctx context.Context, read Reading) (Reading, error) {
	held, err := b.host.frontRecorded(ctx, b.host.reach)
	if err != nil {
		return Reading{}, err
	}
	record := frontRecord{Proxy: b.host.proxyOption.recorded(), Project: b.project, Class: read.Class}
	switch {
	case held != nil:
		if err := b.host.proxyOption.agrees(held.front(), held.setter()); err != nil {
			return Reading{}, err
		}
		record = *held
	case read.standing(KindContainer, caddy.Container):
		if err := b.host.proxyOption.agrees(Front{}, unrecordedSetter); err != nil {
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
