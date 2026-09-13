package providers

import (
	"slices"
	"testing"
)

func TestAssetName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind                        Kind
		name, version, goos, goarch string
		want                        string
	}{
		{KindProvider, "aws", "0.1.0", "linux", "amd64", "ocel-provider-aws_0.1.0_linux_amd64.tar.gz"},
		{KindProvider, "gcp", "0.1.0-alpha.3", "darwin", "arm64", "ocel-provider-gcp_0.1.0-alpha.3_darwin_arm64.tar.gz"},
		{KindProvider, "vps", "1.2.3", "windows", "amd64", "ocel-provider-vps_1.2.3_windows_amd64.zip"},
		{KindConnector, "vps", "0.1.0", "linux", "arm64", "ocel-connector-vps_0.1.0_linux_arm64.tar.gz"},
	} {
		if got := AssetName(tc.kind, tc.name, tc.version, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("AssetName(%s, %q, %q, %q, %q) = %q, want %q", tc.kind, tc.name, tc.version, tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestAssetNameRoundTrips(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind                        Kind
		name, version, goos, goarch string
	}{
		{KindProvider, "aws", "0.1.0", "linux", "amd64"},
		{KindProvider, "a-long-hyphenated-name", "0.1.0-alpha.3+build.7", "windows", "amd64"},
		{KindConnector, "vps", "0.1.0", "linux", "amd64"},
	} {
		asset := AssetName(tc.kind, tc.name, tc.version, tc.goos, tc.goarch)
		got, ok := ParseAssetName(asset)
		if !ok {
			t.Fatalf("ParseAssetName(%q) ok = false, want true", asset)
		}
		want := Asset{Kind: tc.kind, Name: tc.name, Version: tc.version, GOOS: tc.goos, GOARCH: tc.goarch}
		if got != want {
			t.Errorf("ParseAssetName(%q) = %+v, want %+v", asset, got, want)
		}
	}
}

func TestParseAssetNameRefusesWhatIsNotAProviderArchive(t *testing.T) {
	t.Parallel()

	for _, asset := range []string{
		"",
		"checksums.txt",
		"ocel_0.1.0_linux_amd64.tar.gz",
		"ocel-provider-aws_0.1.0_linux_amd64",
		"ocel-provider-aws_0.1.0_linux.tar.gz",
		"ocel-provider-_0.1.0_linux_amd64.tar.gz",
		"ocel-provider-aws__linux_amd64.tar.gz",
		"ocel-provider-aws_0.1.0_linux_amd64_extra.tar.gz",
		"ocel-connector-_0.1.0_linux_amd64.tar.gz",
		"ocel-thing-vps_0.1.0_linux_amd64.tar.gz",
	} {
		if got, ok := ParseAssetName(asset); ok {
			t.Errorf("ParseAssetName(%q) = %+v, ok = true, want false", asset, got)
		}
	}
}

func TestExecutableName(t *testing.T) {
	t.Parallel()

	if got := ExecutableName(KindProvider, "aws", "linux"); got != "provider-aws" {
		t.Errorf("ExecutableName(provider, aws, linux) = %q, want %q", got, "provider-aws")
	}
	if got := ExecutableName(KindProvider, "aws", "windows"); got != "provider-aws.exe" {
		t.Errorf("ExecutableName(provider, aws, windows) = %q, want %q", got, "provider-aws.exe")
	}
	if got := ExecutableName(KindConnector, "vps", "linux"); got != "connector-vps" {
		t.Errorf("ExecutableName(connector, vps, linux) = %q, want %q", got, "connector-vps")
	}
}

func TestPlatformsAreTheFiveTheReleaseShips(t *testing.T) {
	t.Parallel()

	want := []Platform{
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
		{GOOS: "windows", GOARCH: "amd64"},
	}
	if !slices.Equal(Platforms, want) {
		t.Fatalf("Platforms = %+v, want %+v", Platforms, want)
	}
	if got := Platforms[2].Dir(); got != "linux-amd64" {
		t.Fatalf("Platform.Dir() = %q, want %q", got, "linux-amd64")
	}
}

func TestAConnectorShipsForTheTwoLinuxArchesABoxRuns(t *testing.T) {
	t.Parallel()

	want := []Platform{
		{GOOS: "linux", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "arm64"},
	}
	if !slices.Equal(KindConnector.Platforms(), want) {
		t.Fatalf("KindConnector.Platforms() = %+v, want %+v", KindConnector.Platforms(), want)
	}
	if !slices.Equal(KindProvider.Platforms(), Platforms) {
		t.Fatal("a provider ships for every platform the CLI runs on, and this says otherwise")
	}
}
