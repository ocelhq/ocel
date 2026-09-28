package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	KindDir     = "fs:dir"
	KindFile    = "fs:file"
	KindUser    = "linux:user"
	KindSealKey = "ocel:seal-key"
)

const (
	tierRoot     = live.TierRoot
	stateRoot    = live.StateRoot
	releasesRoot = stateRoot + "/releases"

	releasesHelper = boxstore.Dir + "/releases"

	stampFile   = "stamp.json"
	sealKeyFile = "seal.key"

	sudoersRoot       = "/etc/sudoers.d"
	sudoersSealPrefix = sudoersRoot + "/ocel-seal-"
)

const rootOwner = "root"

const stateOwner = deployUser

func TierDir(tier environment.Tier) string { return tierRoot + "/" + string(tier) }

func StampPath(tier environment.Tier) string { return TierDir(tier) + "/" + stampFile }

func SealKeyPath(tier environment.Tier) string { return TierDir(tier) + "/" + sealKeyFile }

func sudoersSeal(tier environment.Tier) string { return sudoersSealPrefix + string(tier) }

func StateDir(tier environment.Tier) string { return stateRoot + "/" + string(tier) }

func ReleasesDir() string { return releasesRoot }

func KeyValuesDir(tier environment.Tier) string { return StateDir(tier) + "/records" }

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
	box     *boxContainer

	rendered *Item
}

func TierItems(tier environment.Tier) []Item {
	return []Item{
		dir(tierRoot, 0o755, rootOwner, ""),
		dir(TierDir(tier), 0o755, rootOwner, ""),
	}
}

func StorageItems(tier environment.Tier, keys []byte) []Item {
	return []Item{
		dir(boxstore.Dir, 0o755, rootOwner, ""),
		{Kind: KindFile, Name: boxstore.KeyValuesHelper, Mode: 0o755, Owner: rootOwner, Content: keyValuesScript, Note: "deploy records"},
		{Kind: KindFile, Name: releasesHelper, Mode: 0o755, Owner: rootOwner, Content: releasesScript, Note: "release window"},
		{Kind: KindFile, Name: boxstore.SealHelper, Mode: 0o755, Owner: rootOwner, Content: sealScript, Note: "seals secret values"},
		principal(),
		{Kind: KindFile, Name: sudoersSeal(tier), Mode: 0o440, Owner: rootOwner, Content: sealSudoers(tier), Note: "sudo for the seal helper"},
		dir(stateRoot, 0o750, stateOwner, ""),
		dir(releasesRoot, 0o750, stateOwner, ""),
		dir(sshDir, 0o700, stateOwner, ""),
		{Kind: KindFile, Name: authorizedKeys, Mode: 0o600, Owner: stateOwner, Content: keys, Note: "keys allowed to deploy"},
		dir(StateDir(tier), 0o750, stateOwner, ""),
		dir(KeyValuesDir(tier), 0o750, stateOwner, ""),
		sealKey(tier),
	}
}

func Items(tier environment.Tier, keys []byte, arch string, front Front) []Item {
	return slices.Concat(TierItems(tier), StorageItems(tier, keys), EngineItems(), LiveItems(arch), EnvSourceSyncItems(tier, arch), ProxyItems(arch, front), BackupItems())
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
	if rewrittenByDeploys(i) {
		return ""
	}
	return contentSum(i.Content)
}

func (i Item) command() string {
	switch i.Kind {
	case KindUser:
		return deployLogin().command()
	case KindSealKey:
		return i.mint()
	case KindEngine:
		return engineCommand()
	case KindUnit:
		return unitCommand(i)
	case KindNetwork:
		return networkCommand()
	case KindContainer:
		return i.box.writing(containerRising)
	case KindProxyConfig:
		return seedingRouting(routingTableItem())
	case KindRoutingTable:
		return seedingRouting(i)
	case KindDir:
		return fmt.Sprintf("install -d -m %04o -o %s -g %s %s", i.Mode, i.Owner, i.Owner, quoted(i.Name))
	default:
		return fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", i.Mode, i.Owner, i.Owner, quoted(i.Name))
	}
}

func (i Item) probe() string {
	switch i.Kind {
	case KindUser:
		return deployLogin().survey()
	case KindSealKey:
		return sealSurvey(i)
	case KindEngine:
		return engineProbe()
	case KindUnit:
		return unitProbe(i)
	case KindNetwork:
		return networkProbe()
	case KindContainer:
		return i.box.probe()
	case KindProxyConfig, KindRoutingTable:
		return seededProbe(i)
	default:
		return ""
	}
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

func quoted(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func mode(written string) (fs.FileMode, error) {
	parsed, err := strconv.ParseUint(written, 8, 32)
	if err != nil {
		return 0, err
	}
	return fs.FileMode(parsed), nil
}
