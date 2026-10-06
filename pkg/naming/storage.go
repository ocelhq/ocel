package naming

import (
	"slices"
	"strings"
)

func (c Coordinate) StoragePrefix() string {
	return path(c.Env, c.Project, c.App, c.Release.String()) + PathSeparator
}

func (c Coordinate) FunctionArtifactKey(sha string) string {
	return c.StoragePrefix() + path(string(KindFunction), Sanitize(c.Name), sha+".zip")
}

func (c Coordinate) AssetKey(assetPath string) string {
	return c.StoragePrefix() + path(AssetsSegment, strings.TrimPrefix(assetPath, PathSeparator))
}

const AssetsSegment = "assets"

const ImageConfigFile = "image-config.json"

func (c Coordinate) ImageConfigKey() string {
	return c.StoragePrefix() + ImageConfigFile
}

func (c Coordinate) ISRPrefix() string {
	return c.StoragePrefix() + "isr" + PathSeparator
}

func (c Coordinate) BytecodePrefix() string {
	return c.StoragePrefix() + "bytecode" + PathSeparator
}

func path(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, PathSeparator)
}

const isrSegment = "isr"

func ISRPrefixUnder(prefix string) (string, bool) {
	trimmed, closed := strings.CutSuffix(prefix, PathSeparator)
	if !closed {
		return "", false
	}
	segments := strings.Split(trimmed, PathSeparator)
	switch {
	case len(segments) == 5 && segments[4] == isrSegment:
		segments = segments[:4]
	case len(segments) != 4:
		return "", false
	}
	if slices.Contains(segments, "") {
		return "", false
	}
	if _, err := ParseRelease(segments[3]); err != nil {
		return "", false
	}
	return strings.Join(append(segments, isrSegment), PathSeparator), true
}
