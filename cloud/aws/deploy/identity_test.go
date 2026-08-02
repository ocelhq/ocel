package deploy

import (
	"reflect"
	"testing"
)

// An identity assembled field-by-field skips the constructor's checks, so it can
// render a token that parses back as some other Deployment. Callers outside this
// package must have no way to assemble one.
func TestDeploymentIdentity_CannotBeForgedFromOutsideThePackage(t *testing.T) {
	typ := reflect.TypeOf(DeploymentIdentity{})
	for i := range typ.NumField() {
		if f := typ.Field(i); f.IsExported() {
			t.Errorf("field %s is exported: an identity can be forged by struct literal", f.Name)
		}
	}
}

func TestNewDeploymentIdentity_NoFingerprintIsTheBuildIDVerbatim(t *testing.T) {
	id, err := NewDeploymentIdentity("WEB1", "")
	if err != nil {
		t.Fatalf("NewDeploymentIdentity: %v", err)
	}
	if got := id.String(); got != "WEB1" {
		t.Errorf("String() = %q, want the bare build id %q", got, "WEB1")
	}
}

func TestNewDeploymentIdentity_FingerprintIsCarriedAlongsideTheBuildID(t *testing.T) {
	id, err := NewDeploymentIdentity("WEB1", "abc123")
	if err != nil {
		t.Fatalf("NewDeploymentIdentity: %v", err)
	}
	if id.BuildID() != "WEB1" || id.Fingerprint() != "abc123" {
		t.Fatalf("identity = %+v, want build id WEB1 and fingerprint abc123", id)
	}
	if id.String() == "WEB1" {
		t.Error("String() = the bare build id; a fingerprinted identity must be distinguishable from the build id alone")
	}
}

// The whole point of the identity: a rotation reuses the build output, so two
// Deployments share a build id and are told apart only by their fingerprint.
func TestDeploymentIdentity_SameBuildDifferentFingerprintsNeverCollide(t *testing.T) {
	a, _ := NewDeploymentIdentity("WEB1", "aaa")
	b, _ := NewDeploymentIdentity("WEB1", "bbb")
	plain, _ := NewDeploymentIdentity("WEB1", "")
	for _, pair := range [][2]DeploymentIdentity{{a, b}, {a, plain}, {b, plain}} {
		if pair[0].String() == pair[1].String() {
			t.Errorf("%+v and %+v render the same identity %q", pair[0], pair[1], pair[0].String())
		}
	}
}

func TestParseDeploymentIdentity_RoundTripsBothShapes(t *testing.T) {
	for _, want := range []DeploymentIdentity{
		buildOnly("WEB1"),
		fingerprinted("WEB1", "abc123"),
	} {
		got, err := ParseDeploymentIdentity(want.String())
		if err != nil {
			t.Fatalf("ParseDeploymentIdentity(%q): %v", want.String(), err)
		}
		if got != want {
			t.Errorf("ParseDeploymentIdentity(%q) = %+v, want %+v", want.String(), got, want)
		}
	}
}

// A build id carrying the reserved separator would make the rendered identity
// ambiguous to parse, so it is refused where build ids enter the system rather
// than mis-split later.
func TestNewDeploymentIdentity_RejectsUnusableParts(t *testing.T) {
	for _, c := range []struct{ buildID, fingerprint string }{
		{"", ""},
		{"", "abc"},
		{"WEB" + identitySeparator + "1", ""},
		{"WEB1", "ab" + identitySeparator + "c"},
	} {
		if _, err := NewDeploymentIdentity(c.buildID, c.fingerprint); err == nil {
			t.Errorf("NewDeploymentIdentity(%q, %q) err = nil, want an error", c.buildID, c.fingerprint)
		}
	}
}

func TestParseDeploymentIdentity_RejectsMalformed(t *testing.T) {
	for _, s := range []string{"", identitySeparator, "WEB1" + identitySeparator, identitySeparator + "abc"} {
		if _, err := ParseDeploymentIdentity(s); err == nil {
			t.Errorf("ParseDeploymentIdentity(%q) err = nil, want an error", s)
		}
	}
}
