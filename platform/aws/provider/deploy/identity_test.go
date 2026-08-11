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

		a, _ := NewIdentity("WEB1", "aaa")
		b, _ := NewIdentity("WEB1", "bbb")
		plain, _ := NewIdentity("WEB1", "")
		for _, pair := range [][2]Identity{{a, b}, {a, plain}, {b, plain}} {
			if pair[0].String() == pair[1].String() {
				t.Errorf("%+v and %+v render the same identity %q", pair[0], pair[1], pair[0].String())
			}
		}
	})
}

func TestNewIdentity(t *testing.T) {
	t.Parallel()

	t.Run("no fingerprint is the build ID verbatim", func(t *testing.T) {
		t.Parallel()

		id, err := NewIdentity("WEB1", "")
		if err != nil {
			t.Fatalf("NewIdentity: %v", err)
		}
		if got := id.String(); got != "WEB1" {
			t.Errorf("String() = %q, want the bare build id %q", got, "WEB1")
		}
	})

	t.Run("a fingerprint is carried alongside the build ID", func(t *testing.T) {
		t.Parallel()

		id, err := NewIdentity("WEB1", "abc123")
		if err != nil {
			t.Fatalf("NewIdentity: %v", err)
		}
		if id.BuildID() != "WEB1" || id.Fingerprint() != "abc123" {
			t.Fatalf("identity = %+v, want build id WEB1 and fingerprint abc123", id)
		}
		if id.String() == "WEB1" {
			t.Error("String() = the bare build id; a fingerprinted identity must be distinguishable from the build id alone")
		}
	})

	t.Run("rejects unusable parts", func(t *testing.T) {
		t.Parallel()

		for _, c := range []struct{ buildID, fingerprint string }{
			{"", ""},
			{"", "abc"},
			{"WEB" + identitySeparator + "1", ""},
			{"WEB1", "ab" + identitySeparator + "c"},
		} {
			if _, err := NewIdentity(c.buildID, c.fingerprint); err == nil {
				t.Errorf("NewIdentity(%q, %q) err = nil, want an error", c.buildID, c.fingerprint)
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

		for _, s := range []string{"", identitySeparator, "WEB1" + identitySeparator, identitySeparator + "abc"} {
			if _, err := ParseIdentity(s); err == nil {
				t.Errorf("ParseIdentity(%q) err = nil, want an error", s)
			}
		}
	})
}
