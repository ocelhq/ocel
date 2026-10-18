package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

const (
	KindDir     = "fs:dir"
	KindFile    = "fs:file"
	KindUser    = "linux:user"
	KindSealKey = "ocel:seal-key"
)

const (
	tierRoot     = boxapi.TierRoot
	stateRoot    = boxapi.StateRoot
	releasesRoot = stateRoot + "/releases"

	releasesHelper = boxapi.LibDir + "/releases"

	stampFile   = "stamp.json"
	sealKeyFile = "seal.key"

	sudoersRoot        = "/etc/sudoers.d"
	sudoersSealPrefix  = sudoersRoot + "/ocel-seal-"
	sudoersCaddyReload = sudoersRoot + "/ocel-caddy-reload"
)

const rootOwner = "root"

const stateOwner = deployUser

func TierDir(tier environment.Tier) string { return tierRoot + "/" + string(tier) }

func StampPath(tier environment.Tier) string { return TierDir(tier) + "/" + stampFile }

func SealKeyPath(tier environment.Tier) string { return TierDir(tier) + "/" + sealKeyFile }

func sudoersSeal(tier environment.Tier) string { return sudoersSealPrefix + string(tier) }

func StateDir(tier environment.Tier) string { return stateRoot + "/" + string(tier) }

func ReleasesDir() string { return releasesRoot }

func KeyValuesDir(tier environment.Tier) string { return StateDir(tier) + "/keyvalues" }

type Item struct {
	Kind    string
	Name    string
	Mode    fs.FileMode
	Owner   string
	Content []byte
	Tier    environment.Tier
	Watch   []string
	Slow    bool
	Note    string

	script    func(Item) string
	survey    func(Item) string
	rewritten bool
}

func TierItems(tier environment.Tier) []Item {
	return []Item{
		dir(tierRoot, 0o755, rootOwner, ""),
		dir(TierDir(tier), 0o755, rootOwner, ""),
	}
}

func StorageItems(tier environment.Tier, keys []byte, arch string) []Item {
	return []Item{
		dir(boxapi.LibDir, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: releasesHelper, Mode: 0o755, Owner: rootOwner, Content: releasesScript, Note: "release window"},
		newAgentItem(liveAgent(arch)),
		principal(),
		{Kind: KindFile, Name: sudoersSeal(tier), Mode: 0o440, Owner: rootOwner, Content: sealSudoers(tier), Note: "sudo for sealing and opening this tier's values"},
		dir(stateRoot, 0o711, rootOwner, ""),
		dir(releasesRoot, 0o750, stateOwner, ""),
		dir(sshDir, 0o700, stateOwner, ""),
		{Kind: KindFile, Name: authorizedKeys, Mode: 0o600, Owner: stateOwner, Content: keys, Note: "keys allowed to deploy"},
		dir(StateDir(tier), 0o750, stateOwner, ""),
		dir(KeyValuesDir(tier), 0o750, stateOwner, ""),
		sealKey(tier),
	}
}

func Items(tier environment.Tier, keys []byte, arch string, front Front) []Item {
	return Reading{Tier: tier, Keys: keys, Arch: arch, Front: front}.Items()
}

func dir(name string, mode fs.FileMode, owner string, note string) Item {
	return Item{Kind: KindDir, Name: name, Mode: mode, Owner: owner, Note: note}
}

func (i Item) ID() string { return i.Kind + " " + i.Name }

func (i Item) phrase() string { return phrase(i.Kind, i.Name) }

var kindNouns = map[string]string{
	KindDir:             "directory",
	KindFile:            "file",
	KindUser:            "user",
	KindSealKey:         "seal key",
	KindUnit:            "systemd unit",
	KindLiveRestore:     "Docker's live-restore setting in",
	KindNetwork:         "Docker network",
	KindContainer:       "container",
	KindProxyConfig:     "proxy config",
	KindRoutingTable:    "routing table",
	KindApps:            "app containers labelled",
	KindAppNetworks:     "app networks labelled",
	KindResourceVolumes: "resource volumes labelled",
}

func phrase(kind, name string) string {
	switch kind {
	case KindEngine:
		return "the Docker engine"
	case KindApps, KindAppNetworks, KindResourceVolumes:
		return "the " + kindNouns[kind] + " " + name
	}
	noun, known := kindNouns[kind]
	if !known {
		noun = kind
	}
	return noun + " " + name
}

func capitalized(phrase string) string {
	first, size := utf8.DecodeRuneInString(phrase)
	return string(unicode.ToUpper(first)) + phrase[size:]
}

func (i Item) stdin() io.Reader {
	if i.Kind == KindFile {
		return bytes.NewReader(i.Content)
	}
	return nil
}

func (i Item) Digest() string {
	return digest(i.Kind, i.Name, i.Mode, i.Owner, i.sum())
}

func (i Item) sum() string {
	if i.rewritten {
		return ""
	}
	return contentSum(i.Content)
}

func (i Item) command() string {
	switch {
	case i.script != nil:
		return i.script(i)
	case i.Kind == KindDir:
		return fmt.Sprintf("install -d -m %04o -o %s -g %s %s", i.Mode, i.Owner, i.Owner, shell.Quote(i.Name))
	default:
		return fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", i.Mode, i.Owner, i.Owner, shell.Quote(i.Name))
	}
}

func (i Item) probe() string {
	if i.survey == nil {
		return ""
	}
	return i.survey(i)
}

func systemdUnit(item Item) Item {
	item.Kind, item.script, item.survey = KindUnit, unitCommand, unitProbe
	return item
}

func digest(kind, name string, mode fs.FileMode, owner, content string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%04o\n%s\n%s\n", kind, name, mode, owner, content))
	return hex.EncodeToString(sum[:])
}

func contentSum(content []byte) string {
	if content == nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func digests(items []Item) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.ID()] = item.Digest()
	}
	return out
}

func mode(written string) (fs.FileMode, error) {
	parsed, err := strconv.ParseUint(written, 8, 32)
	if err != nil {
		return 0, err
	}
	return fs.FileMode(parsed), nil
}
