package providers

import (
	"slices"
	"testing"
)

func TestAssetName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, version, goos, goarch string
		want                        string
	}{
		{"aws", "0.1.0", "linux", "amd64", "ocel-provider-aws_0.1.0_linux_amd64.tar.gz"},
		{"gcp", "0.1.0-alpha.3", "darwin", "arm64", "ocel-provider-gcp_0.1.0-alpha.3_darwin_arm64.tar.gz"},
		{"vps", "1.2.3", "windows", "amd64", "ocel-provider-vps_1.2.3_windows_amd64.zip"},
	} {
		if got := AssetName(tc.name, tc.version, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("AssetName(%q, %q, %q, %q) = %q, want %q", tc.name, tc.version, tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestAssetNameRoundTrips(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, version, goos, goarch string
	}{
		{"aws", "0.1.0", "linux", "amd64"},
		{"a-long-hyphenated-name", "0.1.0-alpha.3+build.7", "windows", "amd64"},
	} {
		asset := AssetName(tc.name, tc.version, tc.goos, tc.goarch)
		got, ok := ParseAssetName(asset)
		if !ok {
			t.Fatalf("ParseAssetName(%q) ok = false, want true", asset)
		}
		want := Asset{Name: tc.name, Version: tc.version, GOOS: tc.goos, GOARCH: tc.goarch}
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
	} {
		if got, ok := ParseAssetName(asset); ok {
			t.Errorf("ParseAssetName(%q) = %+v, ok = true, want false", asset, got)
		}
	}
}

func TestExecutableName(t *testing.T) {
	t.Parallel()

	if got := ExecutableName("aws", "linux"); got != "provider-aws" {
		t.Errorf("ExecutableName(aws, linux) = %q, want %q", got, "provider-aws")
	}
	if got := ExecutableName("aws", "windows"); got != "provider-aws.exe" {
		t.Errorf("ExecutableName(aws, windows) = %q, want %q", got, "provider-aws.exe")
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
