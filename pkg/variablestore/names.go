package variablestore

import (
	"cmp"
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

const (
	RootValues    records.Root = "values"
	RootValueRefs records.Root = "valuerefs"
)

const (
	cellsSegment          = "cells"
	historySegment        = "history"
	bindingsSegment       = "bindings"
	bindingRecordsSegment = "records"
	bindingValuesSegment  = "values"
	bindingOwnersSegment  = "bindingowners"
)

const (
	TierWideEnvironment = "*"
	EveryProject        = "*"

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

func ValuesPartition(scope Scope) records.Partition {
	return records.Partition{Tier: scope.Tier, Root: RootValues, Path: []string{scope.Project}}
}

func cellsPrefix(scope Scope) records.Key { return ValuesPartition(scope).Key(cellsSegment) }

func CellKey(scope Scope, at Coordinate) records.Key {
	at = at.canonical()
	return ValuesPartition(scope).Key(cellsSegment, at.Folder, at.Key, at.Environment)
}

func historyPrefix(scope Scope, at Coordinate) records.Key {
	at = at.canonical()
	return ValuesPartition(scope).Key(historySegment, at.Folder, at.Key, at.Environment)
}

func versionKey(scope Scope, at Coordinate, version int64) records.Key {
	history := historyPrefix(scope, at)
	return history.Partition.Key(append(history.Path, fmt.Sprintf("%0*d", versionDigits, version))...)
}

func bindingsPrefix(scope Scope) records.Key { return ValuesPartition(scope).Key(bindingsSegment) }

func bindingPrefix(scope Scope, binding string) records.Key {
	return ValuesPartition(scope).Key(bindingsSegment, binding)
}

func bindingRecordAt(scope Scope, binding, environment string) records.Key {
	return ValuesPartition(scope).Key(bindingsSegment, binding, bindingRecordsSegment, canonicalEnvironment(environment))
}

func bindingValueAt(scope Scope, binding, environment string) records.Key {
	return ValuesPartition(scope).Key(bindingsSegment, binding, bindingValuesSegment, canonicalEnvironment(environment))
}

func bindingOwnersPrefix(scope Scope) records.Key {
	return ValuesPartition(scope).Key(bindingOwnersSegment)
}

func bindingOwnerKey(scope Scope, owner, environment string) records.Key {
	return ValuesPartition(scope).Key(bindingOwnersSegment, owner, canonicalEnvironment(environment))
}

func ReferencesPartition(scope Scope) records.Partition {
	return records.Partition{Tier: scope.Tier, Root: RootValueRefs, Path: []string{scope.Project}}
}

func refsPrefix(target Scope, at Coordinate) records.Key {
	at = at.canonical()
	return ReferencesPartition(target).Key(at.Folder, at.Key)
}

func refKey(target Scope, at Coordinate, from Scope, sourceAt Coordinate) records.Key {
	at, sourceAt = at.canonical(), sourceAt.canonical()
	return ReferencesPartition(target).Key(at.Folder, at.Key, from.Project, sourceAt.Folder, sourceAt.Key, sourceAt.Environment)
}

func (s Store) listUnder(ctx context.Context, prefix records.Key) ([]records.Entry, error) {
	return s.KeyValues.List(ctx, prefix.Partition, prefix.Path...)
}

func cellOf(key records.Key) (Coordinate, bool) {
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

func OwnerOf(key records.Key) (records.Owner, bool) {
	if len(key.Partition.Path) != 1 {
		return records.Owner{}, false
	}
	project, path := key.Partition.Path[0], key.Path
	switch key.Partition.Root {
	case RootValues:
		switch {
		case len(path) == 4 && path[0] == cellsSegment:
			return ownerOfValue(project, path[3]), true
		case len(path) == 5 && path[0] == historySegment:
			return ownerOfValue(project, path[3]), true
		case len(path) == 4 && path[0] == bindingsSegment && (path[2] == bindingRecordsSegment || path[2] == bindingValuesSegment):
			return ownerOfValue(project, path[3]), true
		case len(path) == 3 && path[0] == bindingOwnersSegment:
			return ownerOfValue(project, path[2]), true
		}
	case RootValueRefs:
		if len(path) == 6 {
			return ownerOfValue(path[2], path[5]), true
		}
	}
	return records.Owner{}, false
}

func ownerOfValue(project, environment string) records.Owner {
	if project == EveryProject {
		return records.Owner{Shared: true}
	}
	return records.Owner{Project: project, Environment: plainEnvironment(environment)}
}
