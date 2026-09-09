package providers

import "strings"

const assetPrefix = "ocel-provider-"

const executablePrefix = "provider-"

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

func archiveExtension(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func AssetName(name, version, goos, goarch string) string {
	return assetPrefix + name + "_" + version + "_" + goos + "_" + goarch + archiveExtension(goos)
}

func ExecutableName(name, goos string) string {
	if goos == "windows" {
		return executablePrefix + name + ".exe"
	}
	return executablePrefix + name
}

type Asset struct {
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
	name, version, goos, tail := fields[0], fields[1], fields[2], fields[3]
	if !strings.HasPrefix(name, assetPrefix) {
		return Asset{}, false
	}
	goarch, found := strings.CutSuffix(tail, archiveExtension(goos))
	if !found {
		return Asset{}, false
	}
	parsed := Asset{Name: strings.TrimPrefix(name, assetPrefix), Version: version, GOOS: goos, GOARCH: goarch}
	if parsed.Name == "" || parsed.Version == "" || parsed.GOOS == "" || parsed.GOARCH == "" {
		return Asset{}, false
	}
	return parsed, true
}
