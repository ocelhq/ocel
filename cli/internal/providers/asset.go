package providers

import (
	"fmt"
	"regexp"
	"strings"
)

var providerName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func checkName(name string) error {
	if !providerName.MatchString(name) {
		return fmt.Errorf("%q is not a provider name", name)
	}
	return nil
}

type Kind string

const (
	KindProvider  Kind = "provider"
	KindConnector Kind = "connector"
)

var Kinds = []Kind{KindProvider, KindConnector}

func (k Kind) assetPrefix() string { return "ocel-" + string(k) + "-" }

func (k Kind) PlatformsFor(name string) []Platform {
	if k == KindConnector {
		return connectorPlatforms[name]
	}
	return Platforms
}

type Platform struct {
	GOOS   string
	GOARCH string
}

func (p Platform) Dir() string { return p.GOOS + "-" + p.GOARCH }

var Platforms = []Platform{
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
}

var connectorPlatforms = map[string][]Platform{
	"aws": {{GOOS: "linux", GOARCH: "arm64"}},
	"gcp": {{GOOS: "linux", GOARCH: "amd64"}},
	"vps": {{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "linux", GOARCH: "arm64"}},
}

func archiveExtension(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func AssetName(kind Kind, name, version, goos, goarch string) string {
	return kind.assetPrefix() + name + "_" + version + "_" + goos + "_" + goarch + archiveExtension(goos)
}

func ExecutableName(kind Kind, name, goos string) string {
	if goos == "windows" {
		return string(kind) + "-" + name + ".exe"
	}
	return string(kind) + "-" + name
}

type Asset struct {
	Kind    Kind
	Name    string
	Version string
	GOOS    string
	GOARCH  string
}

func ParseAssetName(asset string) (Asset, bool) {
	fields := strings.Split(asset, "_")
	if len(fields) != 4 {
		return Asset{}, false
	}
	head, version, goos, tail := fields[0], fields[1], fields[2], fields[3]
	for _, kind := range Kinds {
		name, prefixed := strings.CutPrefix(head, kind.assetPrefix())
		if !prefixed {
			continue
		}
		goarch, found := strings.CutSuffix(tail, archiveExtension(goos))
		if !found {
			return Asset{}, false
		}
		parsed := Asset{Kind: kind, Name: name, Version: version, GOOS: goos, GOARCH: goarch}
		if parsed.Name == "" || parsed.Version == "" || parsed.GOOS == "" || parsed.GOARCH == "" {
			return Asset{}, false
		}
		return parsed, true
	}
	return Asset{}, false
}
