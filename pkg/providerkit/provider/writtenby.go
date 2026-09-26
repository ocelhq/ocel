package provider

import (
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

const (
	devWriter     = "dev"
	unknownWriter = "unknown"
)

type WrittenBy string

func WrittenByVersion(version string) WrittenBy {
	return writerFor(version, vcsRevision())
}

func writerFor(version, revision string) WrittenBy {
	w := WrittenBy(strings.TrimSpace(version))
	if w.Release() {
		return w
	}
	if revision == "" {
		return devWriter
	}
	return WrittenBy(devWriter + "+" + revision)
}

func (w WrittenBy) Release() bool {
	_, ok := w.release()
	return ok
}

func (w WrittenBy) Development() bool {
	return w.String() != unknownWriter && !w.Release()
}

func (w WrittenBy) Newer(than WrittenBy) bool {
	mine, ok := w.release()
	if !ok {
		return false
	}
	theirs, ok := than.release()
	if !ok {
		return false
	}
	return slices.Compare(mine.core[:], theirs.core[:]) > 0 ||
		(mine.core == theirs.core && mine.pre == "" && theirs.pre != "")
}

type releaseVersion struct {
	core [3]uint64
	pre  string
}

func (w WrittenBy) release() (releaseVersion, bool) {
	core, _, _ := strings.Cut(string(w), "+")
	core, pre, _ := strings.Cut(core, "-")
	parts := strings.Split(strings.TrimPrefix(core, "v"), ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	var out releaseVersion
	out.pre = pre
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return releaseVersion{}, false
		}
		out.core[i] = n
	}
	return out, true
}

func (w WrittenBy) String() string {
	if w == "" {
		return unknownWriter
	}
	return string(w)
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}
