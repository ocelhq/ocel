package deploy

import (
	"reflect"
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

	t.Run("same build different fingerprints never collide", func(t *testing.T) {
		t.Parallel()

		a := deployed("dep1", "WEB1", "aaa")
		b := deployed("dep1", "WEB1", "bbb")
		plain := deployed("dep1", "WEB1", "")
		for _, pair := range [][2]Identity{{a, b}, {a, plain}, {b, plain}} {
			if pair[0].String() == pair[1].String() {
				t.Errorf("%+v and %+v render the same identity %q", pair[0], pair[1], pair[0].String())
			}
		}
	})

	t.Run("two deployments of one build never collide", func(t *testing.T) {
		t.Parallel()

		a := deployed("dep1", "build-TfctsWXpff2fKS", "abc")
		b := deployed("dep2", "build-TfctsWXpff2fKS", "abc")
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

	t.Run("leads with the deployment id", func(t *testing.T) {
		t.Parallel()

		id, err := NewIdentity("dep1", "WEB1", "")
		if err != nil {
			t.Fatalf("NewIdentity: %v", err)
		}
		if got := id.String(); got != "dep1"+identitySeparator+"WEB1" {
			t.Errorf("String() = %q, want the deployment id ahead of the build id", got)
		}
	})

	t.Run("a fingerprint is carried alongside the build ID", func(t *testing.T) {
		t.Parallel()

		id, err := NewIdentity("dep1", "WEB1", "abc123")
		if err != nil {
			t.Fatalf("NewIdentity: %v", err)
		}
		if id.DeploymentID() != "dep1" || id.BuildID() != "WEB1" || id.Fingerprint() != "abc123" {
			t.Fatalf("identity = %+v, want deployment dep1, build id WEB1 and fingerprint abc123", id)
		}
		if id.String() == deployed("dep1", "WEB1", "").String() {
			t.Error("String() = the unfingerprinted identity; a fingerprinted identity must be distinguishable")
		}
	})

	t.Run("rejects unusable parts", func(t *testing.T) {
		t.Parallel()

		for _, c := range []struct{ deploymentID, buildID, fingerprint string }{
			{"", "", ""},
			{"", "WEB1", ""},
			{"dep1", "", ""},
			{"dep1", "", "abc"},
			{"dep" + identitySeparator + "1", "WEB1", ""},
			{"dep1", "WEB" + identitySeparator + "1", ""},
			{"dep1", "WEB1", "ab" + identitySeparator + "c"},
		} {
			if _, err := NewIdentity(c.deploymentID, c.buildID, c.fingerprint); err == nil {
				t.Errorf("NewIdentity(%q, %q, %q) err = nil, want an error", c.deploymentID, c.buildID, c.fingerprint)
			}
		}
	})
}

func TestParseIdentity(t *testing.T) {
	t.Parallel()

	t.Run("round trips both shapes", func(t *testing.T) {
		t.Parallel()

		for _, want := range []Identity{
			buildOnly("WEB1"),
			fingerprinted("WEB1", "abc123"),
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
			"WEB1",
			identitySeparator,
			"dep1" + identitySeparator,
			identitySeparator + "WEB1",
			"dep1" + identitySeparator + "WEB1" + identitySeparator,
		} {
			if _, err := ParseIdentity(s); err == nil {
				t.Errorf("ParseIdentity(%q) err = nil, want an error", s)
			}
		}
	})
}
