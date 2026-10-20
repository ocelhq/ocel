package naming_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestHashTagIsTheHeadOfTheDigestOverTheJoinedFields(t *testing.T) {
	if got, want := naming.HashTag("ocel", "acme", "prod", "", "db", "main"), "a65fde"; got != want {
		t.Errorf("HashTag = %q, want %q", got, want)
	}
}

func TestHashDigitsAreAsManyHexDigitsOfTheSameDigestAsTheHashTagHeads(t *testing.T) {
	if got, want := naming.HashDigits(10, "ocel", "acme", "prod", "", "db", "main"), "a65fdeceb0"; got != want {
		t.Errorf("HashDigits(10) = %q, want %q", got, want)
	}
}

func TestCoordinateHashTagCoversNamespaceProjectEnvAppKindAndName(t *testing.T) {
	c := naming.Coordinate{Project: "acme", Env: "prod", App: "web", Kind: naming.KindFunction, Name: "api", Release: naming.NewReleaseToken("b1", "")}
	if got, want := c.HashTag("ocel"), "e0a849"; got != want {
		t.Errorf("HashTag = %q, want %q", got, want)
	}
}

func TestCoordinateHashTagOfAnEnvLevelResourceLeavesTheAppFieldEmpty(t *testing.T) {
	c := naming.Coordinate{Project: "acme", Env: "prod", App: naming.InfraApp, Kind: naming.KindDatabase, Name: "main"}
	if got, want := c.HashTag("ocel"), "a65fde"; got != want {
		t.Errorf("HashTag = %q, want %q", got, want)
	}
}

func TestCoordinateHashTagIsTheSameForEveryRelease(t *testing.T) {
	one := naming.Coordinate{Project: "acme", Env: "prod", App: "web", Kind: naming.KindFunction, Name: "api", Release: naming.NewReleaseToken("b1", "")}
	two := one
	two.Release = naming.NewReleaseToken("b2", "")
	if one.HashTag("ocel") != two.HashTag("ocel") {
		t.Errorf("two releases of one resource hash to %q and %q, want one tag", one.HashTag("ocel"), two.HashTag("ocel"))
	}
}

func TestCoordinateHashTagTellsApartFieldsThatReadTheSameJoinedByDashes(t *testing.T) {
	one := naming.Coordinate{Project: "acme-pr", Env: "42", App: naming.InfraApp, Kind: naming.KindDatabase, Name: "main"}
	two := naming.Coordinate{Project: "acme", Env: "pr-42", App: naming.InfraApp, Kind: naming.KindDatabase, Name: "main"}
	if one.HashTag("ocel") == two.HashTag("ocel") {
		t.Errorf("project %q env %q and project %q env %q share the tag %q", one.Project, one.Env, two.Project, two.Env, one.HashTag("ocel"))
	}
}

func TestNewRandomTagIsSixLowercaseLettersOrDigits(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		tag := naming.NewRandomTag()
		if !naming.IsRandomTag(tag) {
			t.Fatalf("NewRandomTag() = %q, which IsRandomTag refuses", tag)
		}
		seen[tag] = true
	}
	if len(seen) < 999 {
		t.Errorf("1000 random tags had %d distinct values", len(seen))
	}
}

func TestIsRandomTagRefusesAnythingButSixLowercaseLettersOrDigits(t *testing.T) {
	for _, given := range []string{"", "abc12", "abc1234", "ABC123", "abc-12", "abc_12", "r1a2b3c4d"} {
		if naming.IsRandomTag(given) {
			t.Errorf("IsRandomTag(%q) = true, want false", given)
		}
	}
	for _, given := range []string{"a1b2c3", "000000", "zzzzzz"} {
		if !naming.IsRandomTag(given) {
			t.Errorf("IsRandomTag(%q) = false, want true", given)
		}
	}
}
