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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	KindDir     = "fs:dir"
	KindFile    = "fs:file"
	KindUser    = "linux:user"
	KindSealKey = "ocel:seal-key"
)

const (
	classRoot    = "/etc/ocel"
	stateRoot    = "/var/lib/ocel"
	releasesRoot = stateRoot + "/releases"
	helperRoot   = "/usr/local/lib/ocel"

	recordsHelper  = helperRoot + "/records"
	releasesHelper = helperRoot + "/releases"
	SealHelper     = helperRoot + "/seal"

	stampFile   = "stamp.json"
	sealKeyFile = "seal.key"

	sudoersRoot = "/etc/sudoers.d"
	sudoersSeal = sudoersRoot + "/ocel-seal"
)

const rootOwner = "root"

const stateOwner = deployUser

func ClassDir(class providerkit.Class) string { return classRoot + "/" + string(class) }

func StampPath(class providerkit.Class) string { return ClassDir(class) + "/" + stampFile }

func SealKeyPath(class providerkit.Class) string { return ClassDir(class) + "/" + sealKeyFile }

func StateDir(class providerkit.Class) string { return stateRoot + "/" + string(class) }

func ReleasesDir() string { return releasesRoot }

func RecordsDir(class providerkit.Class) string { return StateDir(class) + "/records" }

type Item struct {
	Kind    string
	Name    string
	Mode    fs.FileMode
	Owner   string
	Content []byte
	Class   providerkit.Class
	Slow    bool
	Note    string
}

func ClassItems(class providerkit.Class) []Item {
	return []Item{
		dir(classRoot, 0o755, rootOwner, "ocel's config root"),
		dir(ClassDir(class), 0o755, rootOwner, "config for this class"),
	}
}

func StorageItems(class providerkit.Class, keys []byte) []Item {
	return []Item{
		dir(helperRoot, 0o755, rootOwner, "ocel's helper scripts"),
		{Kind: KindFile, Name: recordsHelper, Mode: 0o755, Owner: rootOwner, Content: recordsScript, Note: "reads and writes deploy records"},
		{Kind: KindFile, Name: releasesHelper, Mode: 0o755, Owner: rootOwner, Content: releasesScript, Note: "keeps the release window and sweeps the images no release names"},
		{Kind: KindFile, Name: SealHelper, Mode: 0o755, Owner: rootOwner, Content: sealScript, Note: "seals and opens secret values"},
		principal(),
		{Kind: KindFile, Name: sudoersSeal, Mode: 0o440, Owner: rootOwner, Content: sealSudoers(), Note: "lets " + deployUser + " run the seal helper as root"},
		dir(stateRoot, 0o750, stateOwner, "deploy state root"),
		dir(releasesRoot, 0o750, stateOwner, "the release window every class on this host keeps"),
		dir(sshDir, 0o700, stateOwner, "the deploy account's ssh login"),
		{Kind: KindFile, Name: authorizedKeys, Mode: 0o600, Owner: stateOwner, Content: keys, Note: "the keys allowed to deploy"},
		dir(StateDir(class), 0o750, stateOwner, "state for this class"),
		dir(RecordsDir(class), 0o750, stateOwner, "records of what is deployed"),
		sealKey(class),
	}
}

func Items(class providerkit.Class, keys []byte, arch string) []Item {
	return slices.Concat(ClassItems(class), StorageItems(class, keys), EngineItems(), ProxyItems(arch))
}

func dir(name string, mode fs.FileMode, owner string, note string) Item {
	return Item{Kind: KindDir, Name: name, Mode: mode, Owner: owner, Note: note}
}

func (i Item) ID() string { return i.Kind + " " + i.Name }

func (i Item) stdin() io.Reader {
	if i.Kind == KindFile || i.Kind == KindProxyConfig {
		return bytes.NewReader(i.Content)
	}
	return nil
}

func (i Item) Digest() string {
	return digest(i.Kind, i.Name, i.Mode, i.Owner, i.sum())
}

func (i Item) sum() string {
	if i.Kind == KindProxyConfig {
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
		return unitCommand()
	case KindNetwork:
		return networkCommand()
	case KindContainer:
		return containerCommand()
	case KindProxyConfig:
		return proxyConfigCommand(i)
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
		return unitProbe()
	case KindNetwork:
		return networkProbe()
	case KindContainer:
		return containerProbe()
	case KindProxyConfig:
		return proxyConfigProbe(i)
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
