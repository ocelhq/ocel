package host

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	KindDir  = "fs:dir"
	KindFile = "fs:file"
	KindUser = "linux:user"
)

const (
	classRoot  = "/etc/ocel"
	stateRoot  = "/var/lib/ocel"
	helperRoot = "/usr/local/lib/ocel"

	recordsHelper = helperRoot + "/records"

	stampFile = "stamp.json"
)

const rootOwner = "root"

const stateOwner = deployUser

func ClassDir(class providerkit.Class) string { return classRoot + "/" + string(class) }

func StampPath(class providerkit.Class) string { return ClassDir(class) + "/" + stampFile }

func StateDir(class providerkit.Class) string { return stateRoot + "/" + string(class) }

func RecordsDir(class providerkit.Class) string { return StateDir(class) + "/records" }

type Item struct {
	Kind    string
	Name    string
	Mode    fs.FileMode
	Owner   string
	Content []byte
}

func ClassItems(class providerkit.Class) []Item {
	return []Item{
		dir(classRoot, 0o755, rootOwner),
		dir(ClassDir(class), 0o755, rootOwner),
	}
}

func StorageItems(class providerkit.Class, keys []byte) []Item {
	return []Item{
		dir(helperRoot, 0o755, rootOwner),
		{Kind: KindFile, Name: recordsHelper, Mode: 0o755, Owner: rootOwner, Content: recordsScript},
		principal(),
		dir(stateRoot, 0o750, stateOwner),
		dir(sshDir, 0o700, stateOwner),
		{Kind: KindFile, Name: authorizedKeys, Mode: 0o600, Owner: stateOwner, Content: keys},
		dir(StateDir(class), 0o750, stateOwner),
		dir(RecordsDir(class), 0o750, stateOwner),
	}
}

func Items(class providerkit.Class, keys []byte) []Item {
	return append(ClassItems(class), StorageItems(class, keys)...)
}

func dir(name string, mode fs.FileMode, owner string) Item {
	return Item{Kind: KindDir, Name: name, Mode: mode, Owner: owner}
}

func (i Item) ID() string { return i.Kind + " " + i.Name }

func (i Item) stdin() []byte {
	if i.Kind == KindFile {
		return i.Content
	}
	return nil
}

func (i Item) Digest() string {
	return digest(i.Kind, i.Name, i.Mode, i.Owner, contentSum(i.Content))
}

func (i Item) command() string {
	switch i.Kind {
	case KindUser:
		return accountCommand(i.Name)
	case KindDir:
		return fmt.Sprintf("install -d -m %04o -o %s -g %s %s", i.Mode, i.Owner, i.Owner, quoted(i.Name))
	default:
		return fmt.Sprintf("install -m %04o -o %s -g %s /dev/stdin %s", i.Mode, i.Owner, i.Owner, quoted(i.Name))
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
