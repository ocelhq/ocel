package envvars

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
)

const (
	TierWideEnvironment = "*"

	rootFolder = "/"

	versionDigits = 10
)

type Scope struct {
	Project string
	Tier    environment.Tier
}

type Cell struct {
	Folder string
	Key    string
}

func (c Cell) Compare(other Cell) int {
	return cmp.Or(cmp.Compare(c.Folder, other.Folder), cmp.Compare(c.Key, other.Key))
}

type Coordinate struct {
	Cell
	Environment string
}

func (c Coordinate) String() string {
	out := c.Key
	if c.Folder != "" && c.Folder != rootFolder {
		out += " in " + c.Folder
	}
	if c.Environment != "" && c.Environment != TierWideEnvironment {
		out += " for " + c.Environment
	}
	return out
}

func (c Coordinate) canonical() Coordinate {
	if c.Folder == "" {
		c.Folder = rootFolder
	}
	c.Environment = canonicalEnvironment(c.Environment)
	return c
}

func canonicalEnvironment(environment string) string {
	if environment == "" {
		return TierWideEnvironment
	}
	return environment
}

func plainEnvironment(environment string) string {
	if environment == TierWideEnvironment {
		return ""
	}
	return environment
}

func plainFolder(folder string) string {
	if folder == rootFolder {
		return ""
	}
	return folder
}

func ValuesPartition(scope Scope) keyvalue.Partition {
	return keyvalue.Partition{Tier: scope.Tier, Root: keyvalue.RootValues, Path: []string{scope.Project}}
}

func cellsPrefix(scope Scope) keyvalue.Key { return ValuesPartition(scope).Key("cells") }

func cellKey(scope Scope, at Coordinate) keyvalue.Key {
	at = at.canonical()
	return ValuesPartition(scope).Key("cells", at.Folder, at.Key, at.Environment)
}

func historyPrefix(scope Scope, at Coordinate) keyvalue.Key {
	at = at.canonical()
	return ValuesPartition(scope).Key("history", at.Folder, at.Key, at.Environment)
}

func versionKey(scope Scope, at Coordinate, version int64) keyvalue.Key {
	history := historyPrefix(scope, at)
	return history.Partition.Key(append(history.Path, fmt.Sprintf("%0*d", versionDigits, version))...)
}

func bindingsPrefix(scope Scope) keyvalue.Key { return ValuesPartition(scope).Key("bindings") }

func bindingPrefix(scope Scope, binding string) keyvalue.Key {
	return ValuesPartition(scope).Key("bindings", binding)
}

func bindingRecordAt(scope Scope, binding, environment string) keyvalue.Key {
	return ValuesPartition(scope).Key("bindings", binding, "records", canonicalEnvironment(environment))
}

func bindingValueAt(scope Scope, binding, environment string) keyvalue.Key {
	return ValuesPartition(scope).Key("bindings", binding, "values", canonicalEnvironment(environment))
}

func bindingOwnersPrefix(scope Scope) keyvalue.Key {
	return ValuesPartition(scope).Key("bindingowners")
}

func bindingOwnerKey(scope Scope, owner, environment string) keyvalue.Key {
	return ValuesPartition(scope).Key("bindingowners", owner, canonicalEnvironment(environment))
}

func ReferencesPartition(scope Scope) keyvalue.Partition {
	return keyvalue.Partition{Tier: scope.Tier, Root: keyvalue.RootValueRefs, Path: []string{scope.Project}}
}

func refsPrefix(target Scope, at Coordinate) keyvalue.Key {
	at = at.canonical()
	return ReferencesPartition(target).Key(at.Folder, at.Key)
}

func refKey(target Scope, at Coordinate, from Scope, sourceAt Coordinate) keyvalue.Key {
	at, sourceAt = at.canonical(), sourceAt.canonical()
	return ReferencesPartition(target).Key(at.Folder, at.Key, from.Project, sourceAt.Folder, sourceAt.Key, sourceAt.Environment)
}

func (s Store) listUnder(ctx context.Context, prefix keyvalue.Key) ([]keyvalue.Entry, error) {
	return s.KeyValues.List(ctx, prefix.Partition, prefix.Path...)
}

func cellOf(key keyvalue.Key) (Coordinate, bool) {
	if len(key.Path) < 3 {
		return Coordinate{}, false
	}
	tail := key.Path[len(key.Path)-3:]
	folder, name, environment := tail[0], tail[1], tail[2]
	if folder == "" || name == "" || environment == "" {
		return Coordinate{}, false
	}
	return Coordinate{Cell: Cell{Folder: plainFolder(folder), Key: name}, Environment: plainEnvironment(environment)}, true
}
