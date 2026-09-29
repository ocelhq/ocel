package provider_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	deploymentID      = "0123456789abcdef0123456789abcdef"
	otherDeploymentID = "fedcba9876543210fedcba9876543210"
	promotionID       = "p1"
)

func built(t *testing.T, deploymentID, environment, values string) provider.Build {
	t.Helper()
	return builtUnder(t, promotionID, deploymentID, environment, values)
}

func builtUnder(t *testing.T, promotionID, deploymentID, environment, values string) provider.Build {
	t.Helper()
	build, err := provider.NewBuild(deploymentID, promotionID, environment, values)
	if err != nil {
		t.Fatalf("NewBuild(%q, %q, %q, %q) = %v", deploymentID, promotionID, environment, values, err)
	}
	return build
}

func TestBuildRoundTripsThroughItsRenderedForm(t *testing.T) {
	t.Parallel()

	for _, build := range []provider.Build{
		built(t, deploymentID, "prod", ""),
		built(t, deploymentID, "prod", "v1"),
		built(t, deploymentID, "pr-7", "v1"),
	} {
		parsed, err := provider.ParseBuild(build.String())
		if err != nil {
			t.Fatalf("ParseBuild(%q) = %v", build, err)
		}
		if parsed != build {
			t.Errorf("ParseBuild(%q) = %v, want the build it rendered", build, parsed)
		}
		if parsed.Release() != build.Release() {
			t.Errorf("the parsed build releases to %s, want %s", parsed.Release(), build.Release())
		}
	}
}

func TestABuildRendersItsDeploymentIdBesideAFingerprint(t *testing.T) {
	t.Parallel()

	build := built(t, deploymentID, "prod", "")
	if build.DeploymentID() != deploymentID {
		t.Errorf("DeploymentID() = %q, want %q", build.DeploymentID(), deploymentID)
	}
	if build.Fingerprint() == "" {
		t.Error("Fingerprint() is empty: the environment alone must fingerprint a build")
	}
	if got, want := build.String(), deploymentID+"~"+build.Fingerprint(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestABuildCannotBeForgedByStructLiteral(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[provider.Build]()
	for i := range typ.NumField() {
		if field := typ.Field(i); field.IsExported() {
			t.Errorf("field %s is exported: a build can be forged by struct literal", field.Name)
		}
	}
}

func TestTheSameDeploymentPromotionEnvironmentAndValuesAreTheSameBuild(t *testing.T) {
	t.Parallel()

	if a, b := built(t, deploymentID, "prod", "abc"), built(t, deploymentID, "prod", "abc"); a != b {
		t.Errorf("a build is not stable: %v then %v", a, b)
	}
}

func TestBuildsThatDifferInValuesEnvironmentDeploymentOrPromotionNeverShareARelease(t *testing.T) {
	t.Parallel()

	for name, pair := range map[string][2]provider.Build{
		"values against other values": {built(t, deploymentID, "prod", "aaa"), built(t, deploymentID, "prod", "bbb")},
		"values against none":         {built(t, deploymentID, "prod", "aaa"), built(t, deploymentID, "prod", "")},
		"production against preview":  {built(t, deploymentID, "prod", ""), built(t, deploymentID, "pr-7", "")},
		"preview against preview":     {built(t, deploymentID, "pr-7", ""), built(t, deploymentID, "pr-8", "")},
		"deployment against another":  {built(t, deploymentID, "prod", ""), built(t, otherDeploymentID, "prod", "")},
		"promotion against another":   {builtUnder(t, "p1", deploymentID, "prod", ""), builtUnder(t, "p2", deploymentID, "prod", "")},
	} {
		if pair[0].String() == pair[1].String() {
			t.Errorf("%s: both render the build %q", name, pair[0])
		}
		if pair[0].Release() == pair[1].Release() {
			t.Errorf("%s: both claim the release %s", name, pair[0].Release())
		}
	}
}

func TestNewBuildRefusesPartsNothingCanName(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ deploymentID, environment, values string }{
		{"", "prod", ""},
		{"", "", "abc"},
		{"not-a-deployment-id", "prod", ""},
		{"dep~1", "prod", ""},
		{deploymentID, "", ""},
		{strings.ToUpper(deploymentID), "prod", ""},
		{deploymentID[:31], "prod", ""},
		{deploymentID + "0", "prod", ""},
		{deploymentID + "\n", "prod", ""},
		{"../" + deploymentID, "prod", ""},
	} {
		if _, err := provider.NewBuild(c.deploymentID, promotionID, c.environment, c.values); err == nil {
			t.Errorf("NewBuild(%q, %q, %q) succeeded, want a refusal", c.deploymentID, c.environment, c.values)
		}
	}
}

func TestParseBuildRefusesARenderingThatIsNotOneIdAndOneFingerprint(t *testing.T) {
	t.Parallel()

	for _, rendered := range []string{
		"",
		deploymentID,
		"~",
		deploymentID + "~",
		"~abc",
		deploymentID + "~abc~def",
	} {
		if _, err := provider.ParseBuild(rendered); err == nil {
			t.Errorf("ParseBuild(%q) succeeded, want an error", rendered)
		}
	}
}
