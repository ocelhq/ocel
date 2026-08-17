package deploy

import (
	"reflect"
	"strings"
	"testing"
)

func TestIdentity(t *testing.T) {
	t.Parallel()

	t.Run("cannot be forged from outside the package", func(t *testing.T) {
		t.Parallel()

		typ := reflect.TypeOf(Identity{})
		for i := range typ.NumField() {
			if f := typ.Field(i); f.IsExported() {
				t.Errorf("field %s is exported: an identity can be forged by struct literal", f.Name)
			}
		}
	})

	t.Run("same deployment different values never collide", func(t *testing.T) {
		t.Parallel()

		a, b, plain := fingerprinted("dep1", "aaa"), fingerprinted("dep1", "bbb"), deployedAs("dep1")
		for _, pair := range [][2]Identity{{a, b}, {a, plain}, {b, plain}} {
			if pair[0].String() == pair[1].String() {
				t.Errorf("%+v and %+v render the same identity %q", pair[0], pair[1], pair[0].String())
			}
		}
	})

	t.Run("one build deployed into two environments never collides", func(t *testing.T) {
		t.Parallel()

		prod := deployedInto(ProductionEnv, "dep1", "")
		preview := deployedInto("pr-7", "dep1", "")
		other := deployedInto("pr-8", "dep1", "")
		for _, pair := range [][2]Identity{{prod, preview}, {prod, other}, {preview, other}} {
			if pair[0].String() == pair[1].String() {
				t.Errorf("identities %q and %q collide across environments", pair[0], pair[1])
			}
			if releaseOf(pair[0]) == releaseOf(pair[1]) {
				t.Errorf("environments share the release %s", releaseOf(pair[0]))
			}
		}
	})

	t.Run("two deployments of one build never collide", func(t *testing.T) {
		t.Parallel()

		a, b := deployedAs("dep1"), deployedAs("dep2")
		if a.String() == b.String() {
			t.Fatalf("both deployments render the identity %q", a.String())
		}
		if releaseOf(a) == releaseOf(b) {
			t.Errorf("both deployments claim the release %s", releaseOf(a))
		}
	})
}

func TestNewIdentity(t *testing.T) {
	t.Parallel()

	t.Run("always renders a fingerprint beside the deployment id", func(t *testing.T) {
		t.Parallel()

		want := deploymentIDFor("dep1")
		id, err := NewIdentity(want, ProductionEnv, "")
		if err != nil {
			t.Fatalf("NewIdentity: %v", err)
		}
		if id.DeploymentID() != want {
			t.Errorf("DeploymentID() = %q, want %q", id.DeploymentID(), want)
		}
		if id.Fingerprint() == "" {
			t.Error("Fingerprint() is empty: the environment alone must fingerprint an identity")
		}
		if got, expected := id.String(), want+identitySeparator+id.Fingerprint(); got != expected {
			t.Errorf("String() = %q, want %q", got, expected)
		}
	})

	t.Run("is stable for the same environment and values", func(t *testing.T) {
		t.Parallel()

		if a, b := fingerprinted("dep1", "abc"), fingerprinted("dep1", "abc"); a != b {
			t.Errorf("identity is not stable: %+v then %+v", a, b)
		}
	})

	t.Run("rejects unusable parts", func(t *testing.T) {
		t.Parallel()

		for _, c := range []struct{ deploymentID, environment, values string }{
			{"", ProductionEnv, ""},
			{"", "", "abc"},
			{"dep" + identitySeparator + "1", ProductionEnv, ""},
			{deploymentIDFor("dep1"), "", ""},
			{"dep1", ProductionEnv, ""},
			{strings.ToUpper(deploymentIDFor("dep1")), ProductionEnv, ""},
			{deploymentIDFor("dep1")[:31], ProductionEnv, ""},
			{deploymentIDFor("dep1") + "0", ProductionEnv, ""},
			{deploymentIDFor("dep1") + "\n", ProductionEnv, ""},
			{"../" + deploymentIDFor("dep1"), ProductionEnv, ""},
		} {
			if _, err := NewIdentity(c.deploymentID, c.environment, c.values); err == nil {
				t.Errorf("NewIdentity(%q, %q, %q) err = nil, want an error", c.deploymentID, c.environment, c.values)
			}
		}
	})
}

func TestParseIdentity(t *testing.T) {
	t.Parallel()

	t.Run("round trips", func(t *testing.T) {
		t.Parallel()

		for _, want := range []Identity{
			deployedAs("dep1"),
			fingerprinted("dep1", "abc123"),
			deployedInto("pr-7", "dep1", "abc123"),
		} {
			got, err := ParseIdentity(want.String())
			if err != nil {
				t.Fatalf("ParseIdentity(%q): %v", want.String(), err)
			}
			if got != want {
				t.Errorf("ParseIdentity(%q) = %+v, want %+v", want.String(), got, want)
			}
		}
	})

	t.Run("rejects malformed", func(t *testing.T) {
		t.Parallel()

		for _, s := range []string{
			"",
			"dep1",
			identitySeparator,
			"dep1" + identitySeparator,
			identitySeparator + "abc",
			"dep1" + identitySeparator + "abc" + identitySeparator + "def",
		} {
			if _, err := ParseIdentity(s); err == nil {
				t.Errorf("ParseIdentity(%q) err = nil, want an error", s)
			}
		}
	})
}
