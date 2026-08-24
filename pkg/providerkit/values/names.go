package values

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	ClassWideEnvironment = "*"

	rootFolder = "/"

	versionDigits = 10
)

type Scope struct {
	Project string
	Class   ports.Class
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

func Under(scope Scope, rest ...string) ports.RecordName {
	return append(ports.RecordName{ports.RootValues, scope.Project, string(scope.Class)}, rest...)
}

func cellsName(scope Scope) ports.RecordName { return Under(scope, "cells") }

func cellName(scope Scope, at Coordinate) ports.RecordName {
	at = at.canonical()
	return Under(scope, "cells", escape(at.Folder), escape(at.Key), escape(at.Environment))
}

func historyName(scope Scope, at Coordinate) ports.RecordName {
	at = at.canonical()
	return Under(scope, "history", escape(at.Folder), escape(at.Key), escape(at.Environment))
}

func versionName(scope Scope, at Coordinate, version int64) ports.RecordName {
	return append(historyName(scope, at), fmt.Sprintf("%0*d", versionDigits, version))
}

func linksName(scope Scope) ports.RecordName { return Under(scope, "links") }

func linkName(scope Scope, link string) ports.RecordName {
	return Under(scope, "links", escape(link))
}

func linkRecordName(scope Scope, link, environment string) ports.RecordName {
	return append(linkName(scope, link), "records", escape(canonicalEnvironment(environment)))
}

func linkValueName(scope Scope, link, environment string) ports.RecordName {
	return append(linkName(scope, link), "values", escape(canonicalEnvironment(environment)))
}

func linkOwnersName(scope Scope) ports.RecordName { return Under(scope, "linkowners") }

func linkOwnerName(scope Scope, owner, environment string) ports.RecordName {
	return Under(scope, "linkowners", escape(owner), escape(canonicalEnvironment(environment)))
}

func Refs(scope Scope) ports.RecordName {
	return ports.RecordName{ports.RootValueRefs, string(scope.Class), scope.Project}
}

func refsName(target Scope, at Coordinate) ports.RecordName {
	at = at.canonical()
	return append(Refs(target), escape(at.Folder), escape(at.Key))
}

func refName(target Scope, at Coordinate, from Scope, holds Coordinate) ports.RecordName {
	holds = holds.canonical()
	return append(refsName(target, at), from.Project, escape(holds.Folder), escape(holds.Key), escape(holds.Environment))
}

func cellOf(name ports.RecordName) (Coordinate, bool) {
	if len(name) < 3 {
		return Coordinate{}, false
	}
	tail := name[len(name)-3:]
	folder, key, environment := unescape(tail[0]), unescape(tail[1]), unescape(tail[2])
	if folder == "" || key == "" || environment == "" {
		return Coordinate{}, false
	}
	return Coordinate{Cell: Cell{Folder: plainFolder(folder), Key: key}, Environment: plainEnvironment(environment)}, true
}

func escape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), "/", "%2F")
}

func unescape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%2F", "/"), "%25", "%")
}
