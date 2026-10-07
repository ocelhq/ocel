package provider_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	buildID      = "0123456789abcdef0123456789abcdef"
	otherBuildID = "fedcba9876543210fedcba9876543210"
	promotionID  = "p1"
)

func released(t *testing.T, buildID, environment, values string) provider.Release {
	t.Helper()
	return releasedUnder(t, promotionID, buildID, environment, values)
}

func releasedUnder(t *testing.T, promotionID, buildID, environment, values string) provider.Release {
	t.Helper()
	release, err := provider.NewRelease(buildID, promotionID, environment, values)
	if err != nil {
		t.Fatalf("NewRelease(%q, %q, %q, %q) = %v", buildID, promotionID, environment, values, err)
	}
	return release
}

func TestReleaseRoundTripsThroughItsRenderedForm(t *testing.T) {
	t.Parallel()

	for _, release := range []provider.Release{
		released(t, buildID, "prod", ""),
		released(t, buildID, "prod", "v1"),
		released(t, buildID, "pr-7", "v1"),
	} {
		parsed, err := provider.ParseRelease(release.String())
		if err != nil {
			t.Fatalf("ParseRelease(%q) = %v", release, err)
		}
		if parsed != release {
			t.Errorf("ParseRelease(%q) = %v, want the release it rendered", release, parsed)
		}
		if parsed.Token() != release.Token() {
			t.Errorf("the parsed release is named by token %s, want %s", parsed.Token(), release.Token())
		}
	}
}

func TestAReleaseRendersItsBuildIDBesideAFingerprint(t *testing.T) {
	t.Parallel()

	release := released(t, buildID, "prod", "")
	if release.BuildID() != buildID {
		t.Errorf("BuildID() = %q, want %q", release.BuildID(), buildID)
	}
	if release.Fingerprint() == "" {
		t.Error("Fingerprint() is empty: the environment alone must fingerprint a release")
	}
	if got, want := release.String(), buildID+"~"+release.Fingerprint(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestAReleaseCannotBeForgedByStructLiteral(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[provider.Release]()
	for i := range typ.NumField() {
		if field := typ.Field(i); field.IsExported() {
			t.Errorf("field %s is exported: a release can be forged by struct literal", field.Name)
		}
	}
}

func TestTheSameBuildPromotionEnvironmentAndValuesAreTheSameRelease(t *testing.T) {
	t.Parallel()

	if a, b := released(t, buildID, "prod", "abc"), released(t, buildID, "prod", "abc"); a != b {
		t.Errorf("a release is not stable: %v then %v", a, b)
	}
}

func TestReleasesThatDifferInValuesEnvironmentBuildOrPromotionNeverShareAToken(t *testing.T) {
	t.Parallel()

	for name, pair := range map[string][2]provider.Release{
		"values against other values": {released(t, buildID, "prod", "aaa"), released(t, buildID, "prod", "bbb")},
		"values against none":         {released(t, buildID, "prod", "aaa"), released(t, buildID, "prod", "")},
		"production against preview":  {released(t, buildID, "prod", ""), released(t, buildID, "pr-7", "")},
		"preview against preview":     {released(t, buildID, "pr-7", ""), released(t, buildID, "pr-8", "")},
		"build against another":       {released(t, buildID, "prod", ""), released(t, otherBuildID, "prod", "")},
		"promotion against another":   {releasedUnder(t, "p1", buildID, "prod", ""), releasedUnder(t, "p2", buildID, "prod", "")},
	} {
		if pair[0].String() == pair[1].String() {
			t.Errorf("%s: both render the release %q", name, pair[0])
		}
		if pair[0].Token() == pair[1].Token() {
			t.Errorf("%s: both claim the token %s", name, pair[0].Token())
		}
	}
}

func TestNewReleaseRefusesPartsNothingCanName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ buildID, environment, values string }{
		{"", "prod", ""},
		{"", "", "abc"},
		{"not-a-build-id", "prod", ""},
		{"bld~1", "prod", ""},
		{buildID, "", ""},
		{strings.ToUpper(buildID), "prod", ""},
		{buildID[:31], "prod", ""},
		{buildID + "0", "prod", ""},
		{buildID + "\n", "prod", ""},
		{"../" + buildID, "prod", ""},
	} {
		if _, err := provider.NewRelease(c.buildID, promotionID, c.environment, c.values); err == nil {
			t.Errorf("NewRelease(%q, %q, %q) succeeded, want a refusal", c.buildID, c.environment, c.values)
		}
	}
}

func TestNewReleaseRefusesAReleaseThatNamesNoPromotion(t *testing.T) {
	t.Parallel()

	if release, err := provider.NewRelease(buildID, "", "prod", ""); err == nil {
		t.Errorf("NewRelease() with no promotion ID = %s, want a refusal: every deploy of that output would claim the one release and its stack", release)
	}
}

func TestParseReleaseRefusesARenderingThatIsNotOneIdAndOneFingerprint(t *testing.T) {
	t.Parallel()

	for _, rendered := range []string{
		"",
		buildID,
		"~",
		buildID + "~",
		"~abc",
		buildID + "~abc~def",
	} {
		if _, err := provider.ParseRelease(rendered); err == nil {
			t.Errorf("ParseRelease(%q) succeeded, want an error", rendered)
		}
	}
}
