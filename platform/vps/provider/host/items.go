package host

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
)

const stateOwner = deployUser

func SealKeyPath(tier environment.Tier) string {
	return boxapi.TierDir(tier) + "/" + machine.SealKeyFile
}

func sudoersSeal(tier environment.Tier) string { return machine.SudoersSealPrefix + string(tier) }

func KeyValuesDir(tier environment.Tier) string { return boxapi.StateDir(tier) + "/keyvalues" }

func TierItems(tier environment.Tier) []machine.Item {
	return []machine.Item{
		machine.Dir(boxapi.TierRoot, 0o755, machine.RootOwner, ""),
		machine.Dir(boxapi.TierDir(tier), 0o755, machine.RootOwner, ""),
	}
}

func StorageItems(tier environment.Tier, keys []byte, arch string) []machine.Item {
	return []machine.Item{
		machine.Dir(boxapi.LibDir, 0o755, machine.RootOwner, ""),
		{Kind: machine.KindFile, Name: boxapi.ReleasesHelper, Mode: 0o755, Owner: machine.RootOwner, Content: release.HelperScript, Note: "release window"},
		newAgentItem(liveAgent(arch)),
		principal(),
		{Kind: machine.KindFile, Name: sudoersSeal(tier), Mode: 0o440, Owner: machine.RootOwner, Content: sealSudoers(tier), Note: "sudo for sealing and opening this tier's values"},
		machine.Dir(boxapi.StateRoot, 0o711, machine.RootOwner, ""),
		machine.Dir(boxapi.ReleasesDir, 0o750, stateOwner, ""),
		machine.Dir(sshDir, 0o700, stateOwner, ""),
		{Kind: machine.KindFile, Name: authorizedKeys, Mode: 0o600, Owner: stateOwner, Content: keys, Note: "keys allowed to deploy"},
		machine.Dir(boxapi.StateDir(tier), 0o750, stateOwner, ""),
		machine.Dir(KeyValuesDir(tier), 0o750, stateOwner, ""),
		sealKey(tier),
	}
}

func Items(tier environment.Tier, keys []byte, arch string, fronting front.Proxy) []machine.Item {
	return Reading{Tier: tier, Keys: keys, Arch: arch, Front: fronting}.Items()
}
