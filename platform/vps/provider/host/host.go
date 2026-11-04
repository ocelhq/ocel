package host

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

type Host struct {
	*ssh.Box
	assets      Assets
	deploy      Keys
	pins        *certs.Pins
	proxyOption front.Proxy
	front       proxy.Proxy

	keysRead   sync.Mutex
	cachedKeys []byte

	described sync.Mutex
	readings  map[environment.Tier]Reading

	engine  *docker.Engine
	routing *routing.Table
	tunnel  *tunnel.Container

	images     *release.Images
	containers *release.Containers
	resources  *container.Containers
}

func New(box *ssh.Box, assets Assets, project string, deploy Keys, pins []boxapi.Pin, fronting front.Proxy) *Host {
	h := &Host{Box: box, assets: assets, engine: docker.NewEngine(box), deploy: deploy, pins: certs.NewPins(box, pins), proxyOption: fronting}
	h.front = fronting.Front().Open(frontBox{h})
	h.routing = routing.NewTable(box, h.front, project, h.pins.Verified)
	h.tunnel = tunnel.New(box, h.routing)
	h.images = release.NewImages(box)
	h.resources = container.New(box, h.engine)
	h.containers = release.NewContainers(box, h.engine, h.routing, h.front, h.images)
	return h
}

func (h *Host) Pins() *certs.Pins { return h.pins }

func (h *Host) Install(ctx context.Context, item machine.Item) error {
	_, err := h.Run(ctx, ssh.Root, "write "+item.ID(), item.Command(), item.Stdin())
	return err
}

func (h *Host) InstallChanged(ctx context.Context, items []machine.Item, current func(machine.Item) bool, progress progress.Log) error {
	for _, item := range items {
		if current(item) {
			progress.Debug(machine.Capitalized(item.Phrase()) + " is " + machine.ReasonCurrent)
			continue
		}
		if err := h.Install(ctx, item); err != nil {
			return err
		}
		progress.Say("Installed " + item.Phrase())
	}
	return nil
}

func CurrentIn(observed map[string]string) func(machine.Item) bool {
	return func(item machine.Item) bool { return observed[item.ID()] == item.Digest() }
}

func (h *Host) remove(ctx context.Context, taken machine.Removal) (bool, error) {
	switch taken.Kind {
	case machine.KindUser:
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.Kind+" "+taken.Path, "userdel -f "+shell.Quote(taken.Path), nil)
		return err == nil, err
	case machine.KindNetwork:
		rendered, err := h.Run(ctx, ssh.Root, "remove "+taken.Kind+" "+taken.Path, taken.Command(), nil)
		return strings.TrimSpace(rendered) != docker.NetworkInUse, err
	case machine.KindContainer, machine.KindApps, machine.KindResourceVolumes, machine.KindAppNetworks:
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.Kind+" "+taken.Path, taken.Command(), nil)
		return err == nil, err
	case machine.KindUnit:
		if !slices.Contains([]string{LiveService, LiveSocketUnit, BackupsTimer,
			EnvSourceSyncService(environment.TierProduction), EnvSourceSyncService(environment.TierPreview)}, taken.Path) {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%s %s is not ocel's to remove", taken.Kind, taken.Path)
		}
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.Kind+" "+taken.Path, taken.Command(), nil)
		return err == nil, err
	case machine.KindDir, machine.KindFile, machine.KindSealKey, machine.KindProxyConfig, machine.KindRoutingTable, machine.KindPlaced:
		if !strings.HasPrefix(taken.Path, "/") {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%q is not an absolute path", taken.Path)
		}
		rendered, err := h.Run(ctx, ssh.Root, "remove "+taken.Path, taken.Command(), nil)
		if taken.Kind == machine.KindDir && taken.Shared {
			return strings.TrimSpace(rendered) != machine.DirNonEmpty, err
		}
		return err == nil, err
	default:
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s %s is not ocel's to remove", taken.Kind, taken.Path)
	}
}

type Reading struct {
	Assets   Assets
	Tier     environment.Tier
	Present  bool
	Keys     []byte
	Arch     string
	Stamp    machine.Stamp
	Seal     machine.Seal
	Observed map[string]string
	Front    front.Proxy
	Engine   InstalledEngine

	recorded    []machine.Item
	move        *frontMove
	unelevated  bool
	rerendering bool
}

func (r Reading) current(item machine.Item) bool { return r.Observed[item.ID()] == item.Digest() }

func (r Reading) observed(kind, path string) bool {
	_, seen := r.Observed[kind+" "+path]
	return seen
}

func (r Reading) unfinished() bool { return r.Present && r.Stamp.State != machine.StateComplete }

func (r Reading) Items() []machine.Item { return partItems(new(Host).parts(), r) }

func (r Reading) upToDate() bool {
	items := r.Items()
	if !r.Present || r.Stamp.State != machine.StateComplete || !r.Stamp.Records(items) {
		return false
	}
	if r.unelevated {
		return true
	}
	if r.Seal.Fingerprint == "" || r.Seal.Fingerprint != r.Stamp.Seal.Fingerprint {
		return false
	}
	for _, item := range items {
		if !r.current(item) {
			return false
		}
	}
	return true
}

func (h *Host) rememberReading(read Reading) {
	h.described.Lock()
	defer h.described.Unlock()
	if h.readings == nil {
		h.readings = map[environment.Tier]Reading{}
	}
	h.readings[read.Tier] = read
}

func (h *Host) recallReading(tier environment.Tier) (Reading, bool) {
	h.described.Lock()
	defer h.described.Unlock()
	read, remembered := h.readings[tier]
	return read, remembered
}

func (h *Host) forgetReading(tier environment.Tier) {
	h.described.Lock()
	defer h.described.Unlock()
	delete(h.readings, tier)
}

func (h *Host) Read(ctx context.Context, tier environment.Tier) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.read(ctx, tier, keys, ssh.Root)
}

func (h *Host) Observe(ctx context.Context, tier environment.Tier) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	denied := h.Elevation(ctx)
	read, err := h.read(ctx, tier, keys, ssh.RootWhenAble)
	if err != nil {
		return Reading{}, err
	}
	read.unelevated = denied != nil
	return read, nil
}

func (h *Host) Survey(ctx context.Context, tier environment.Tier) (Reading, error) {
	return h.observing(ctx, tier, nil, ssh.Root)
}

func (h *Host) observing(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		arch = ssh.AMD64
	}
	return h.surveyed(ctx, tier, keys, arch, as)
}

func (h *Host) observe(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.surveyed(ctx, tier, keys, arch, as)
}

func (h *Host) surveyed(ctx context.Context, tier environment.Tier, keys []byte, arch string, as ssh.Acting) (Reading, error) {
	surveying := Items(h.assets, tier, keys, arch, h.proxyOption)
	if h.proxyOption.Adopted() {
		surveying = append(surveying, containerItem(frontProxy(), ""))
	}
	rendered, err := h.Run(ctx, as, "survey what "+string(tier)+" has installed", machine.SurveyScript(surveying, machine.StampPath(tier), FrontRecordPath, boxapi.ImagesLock), nil)
	if err != nil {
		return Reading{}, err
	}
	observed, seal, err := machine.ReadSurvey(rendered)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Assets: h.assets, Tier: tier, Keys: keys, Arch: arch, Seal: seal, Observed: observed, Front: h.proxyOption, Engine: readEngine(rendered)}, nil
}

func (h *Host) read(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	read, err := h.observe(ctx, tier, keys, as)
	if err != nil {
		return Reading{}, err
	}
	if _, stamped := read.Observed[machine.KindFile+" "+machine.StampPath(tier)]; !stamped {
		return read, nil
	}
	stamp, err := h.readStamp(ctx, tier, as)
	if err != nil {
		return Reading{}, err
	}
	read.Present, read.Stamp = true, stamp
	return read, nil
}

func (h *Host) readStamp(ctx context.Context, tier environment.Tier, as ssh.Acting) (machine.Stamp, error) {
	rendered, err := h.Run(ctx, as, "read the stamp", "cat "+shell.Quote(machine.StampPath(tier)), nil)
	if err != nil {
		return machine.Stamp{}, err
	}
	var stamp machine.Stamp
	if err := json.Unmarshal([]byte(rendered), &stamp); err != nil {
		return machine.Stamp{}, refusal.Refuse(refusal.CodeInvalid,
			"%s is not a stamp this ocel can read: %s", machine.StampPath(tier), err)
	}
	return stamp, nil
}

func (h *Host) Stamp(ctx context.Context, tier environment.Tier, stamp machine.Stamp) error {
	item, err := stamp.Item(tier)
	if err != nil {
		return err
	}
	return h.Install(ctx, item)
}

func (h *Host) Routing() *routing.Table { return h.routing }

func (h *Host) Tunnel() *tunnel.Container { return h.tunnel }

func (h *Host) Images() *release.Images { return h.images }

func (h *Host) Containers() *release.Containers { return h.containers }

func (h *Host) Resources() *container.Containers { return h.resources }
