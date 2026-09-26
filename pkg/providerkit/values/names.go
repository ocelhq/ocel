package values

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	ClassWideEnvironment = "*"

	rootFolder = "/"

	versionDigits = 10
)

type Scope struct {
	Project string
	Class   edge.Class
}

type Cell struct {
	Folder string
	Key    string
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
	if c.Environment != "" && c.Environment != ClassWideEnvironment {
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
		return ClassWideEnvironment
	}
	return environment
}

func plainEnvironment(environment string) string {
	if environment == ClassWideEnvironment {
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

func Under(scope Scope, rest ...string) records.Name {
	return append(records.Name{records.RootValues, scope.Project, string(scope.Class)}, rest...)
}

func cellsName(scope Scope) records.Name { return Under(scope, "cells") }

func cellName(scope Scope, at Coordinate) records.Name {
	at = at.canonical()
	return Under(scope, "cells", records.Escape(at.Folder), records.Escape(at.Key), records.Escape(at.Environment))
}

func historyName(scope Scope, at Coordinate) records.Name {
	at = at.canonical()
	return Under(scope, "history", records.Escape(at.Folder), records.Escape(at.Key), records.Escape(at.Environment))
}

func versionName(scope Scope, at Coordinate, version int64) records.Name {
	return append(historyName(scope, at), fmt.Sprintf("%0*d", versionDigits, version))
}

func bindingsName(scope Scope) records.Name { return Under(scope, "bindings") }

func bindingName(scope Scope, binding string) records.Name {
	return Under(scope, "bindings", records.Escape(binding))
}

func bindingRecordName(scope Scope, binding, environment string) records.Name {
	return append(bindingName(scope, binding), "records", records.Escape(canonicalEnvironment(environment)))
}

func bindingValueName(scope Scope, binding, environment string) records.Name {
	return append(bindingName(scope, binding), "values", records.Escape(canonicalEnvironment(environment)))
}

func bindingOwnersName(scope Scope) records.Name { return Under(scope, "bindingowners") }

func bindingOwnerName(scope Scope, owner, environment string) records.Name {
	return Under(scope, "bindingowners", records.Escape(owner), records.Escape(canonicalEnvironment(environment)))
}

func Refs(scope Scope) records.Name {
	return records.Name{records.RootValueRefs, string(scope.Class), scope.Project}
}

func refsName(target Scope, at Coordinate) records.Name {
	at = at.canonical()
	return append(Refs(target), records.Escape(at.Folder), records.Escape(at.Key))
}

func refName(target Scope, at Coordinate, from Scope, holds Coordinate) records.Name {
	holds = holds.canonical()
	return append(refsName(target, at), from.Project, records.Escape(holds.Folder), records.Escape(holds.Key), records.Escape(holds.Environment))
}

func cellOf(name records.Name) (Coordinate, bool) {
	if len(name) < 3 {
		return Coordinate{}, false
	}
	tail := name[len(name)-3:]
	folder, key, environment := records.Unescape(tail[0]), records.Unescape(tail[1]), records.Unescape(tail[2])
	if folder == "" || key == "" || environment == "" {
		return Coordinate{}, false
	}
	return Coordinate{Cell: Cell{Folder: plainFolder(folder), Key: key}, Environment: plainEnvironment(environment)}, true
}
