package host

import (
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

const sudoersPins = machine.SudoersRoot + "/ocel-pins"

func pinsSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.PinCommand + " *\n")
}

func pinsSudoersItem() machine.Item {
	return machine.Item{Kind: machine.KindFile, Name: sudoersPins, Mode: 0o440, Owner: machine.RootOwner, Content: pinsSudoers(), Note: "sudo for reading your pinned certificates, never their keys"}
}
