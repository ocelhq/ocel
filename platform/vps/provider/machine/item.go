package machine

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
	stampFile   = "stamp.json"
	SealKeyFile = "seal.key"

	SudoersRoot        = "/etc/sudoers.d"
	SudoersSealPrefix  = SudoersRoot + "/ocel-seal-"
	SudoersCaddyReload = SudoersRoot + "/ocel-caddy-reload"
)

const RootOwner = "root"

func StampPath(tier environment.Tier) string { return boxapi.TierDir(tier) + "/" + stampFile }

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

	Script    func(Item) string
	Survey    func(Item) string
	Rewritten bool
}

func Dir(name string, mode fs.FileMode, owner string, note string) Item {
	return Item{Kind: KindDir, Name: name, Mode: mode, Owner: owner, Note: note}
}

func (i Item) ID() string { return i.Kind + " " + i.Name }

func (i Item) Phrase() string { return Phrase(i.Kind, i.Name) }

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

func Phrase(kind, name string) string {
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

func Capitalized(phrase string) string {
	first, size := utf8.DecodeRuneInString(phrase)
	return string(unicode.ToUpper(first)) + phrase[size:]
}

func (i Item) Stdin() io.Reader {
	if i.Kind == KindFile {
		return bytes.NewReader(i.Content)
	}
	return nil
}

func (i Item) Digest() string {
	return DigestOf(i.Kind, i.Name, i.Mode, i.Owner, i.Sum())
}

func (i Item) Sum() string {
	if i.Rewritten {
		return ""
	}
	return ContentSum(i.Content)
}

func (i Item) Command() string {
	switch {
	case i.Script != nil:
		return i.Script(i)
	case i.Kind == KindDir:
		return fmt.Sprintf("install -d -m %04o -o %s -g %s %s", i.Mode, i.Owner, i.Owner, shell.Quote(i.Name))
	default:
		return fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", i.Mode, i.Owner, i.Owner, shell.Quote(i.Name))
	}
}

func (i Item) Probe() string {
	if i.Survey == nil {
		return ""
	}
	return i.Survey(i)
}

func SystemdUnit(item Item) Item {
	item.Kind, item.Script, item.Survey = KindUnit, UnitCommand, UnitProbe
	return item
}

func DigestOf(kind, name string, mode fs.FileMode, owner, content string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%04o\n%s\n%s\n", kind, name, mode, owner, content))
	return hex.EncodeToString(sum[:])
}

func ContentSum(content []byte) string {
	if content == nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func DigestsOf(items []Item) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.ID()] = item.Digest()
	}
	return out
}

func ParseMode(written string) (fs.FileMode, error) {
	parsed, err := strconv.ParseUint(written, 8, 32)
	if err != nil {
		return 0, err
	}
	return fs.FileMode(parsed), nil
}
