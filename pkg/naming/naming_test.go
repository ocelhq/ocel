package naming

import (
	"strings"
	"testing"
)

func TestSanitizeHoldsTheAlphabet(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Web/API/Users", "web-api-users"},
		{"web_api_users", "web-api-users"},
		{"--leading--and--trailing--", "leading-and-trailing"},
		{"", "x"},
		{"...", "x"},
	} {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFitKeepsFixedFieldsAndMarksTruncation(t *testing.T) {
	c := Coordinate{
		Project: "a-fairly-long-project-slug",
		Env:     "preview-pr-7",
		App:     "web",
		Kind:    KindFunction,
		Name:    "api-users-with-a-very-long-route-identifier",
		Release: NewRelease("build-1", ""),
	}
	got := c.PhysicalName(64)
	if len(got) > 64 {
		t.Fatalf("PhysicalName = %q (%d chars), want <= 64", got, len(got))
	}
	for _, field := range []string{c.Project, c.Env, c.App, c.Release.String()} {
		if !strings.Contains(got, field) {
			t.Errorf("PhysicalName = %q, want it to preserve %q", got, field)
		}
	}
	if !strings.Contains(got, truncationMarker) {
		t.Errorf("PhysicalName = %q, want a visible truncation marker", got)
	}
}

func TestFitStaysCollisionSafe(t *testing.T) {
	base := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Release: NewRelease("b1", "")}
	long := strings.Repeat("route-segment-", 6)

	a, b := base, base
	a.Name = long + "one"
	b.Name = long + "two"

	if a.PhysicalName(64) == b.PhysicalName(64) {
		t.Fatalf("distinct routes collided on %q", a.PhysicalName(64))
	}
}

func TestFitIsStableWhenItFits(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "api-users", Release: NewRelease("build-1", "")}
	want := "shop-prod-web-api-users-" + c.Release.String()
	if got := c.PhysicalName(64); got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
}

func TestFitOmitsEmptySegments(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Release: NewRelease("build-1", "")}
	want := "shop-prod-web-" + c.Release.String()
	if got := c.PhysicalName(64); got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
	if got := Fit(64, WordSeparator, Fixed("shop"), Compressible(""), Fixed("prod")); got != "shop-prod" {
		t.Errorf("Fit = %q, want %q", got, "shop-prod")
	}
}

func TestReleaseSeparatesBuildFromFingerprint(t *testing.T) {
	if NewRelease("build-1", "fp-a") == NewRelease("build-1", "fp-b") {
		t.Error("a changed value fingerprint must mint a new release")
	}
	if NewRelease("build-1", "") == NewRelease("build-2", "") {
		t.Error("a changed build must mint a new release")
	}
	if _, err := ParseRelease(NewRelease("build-1", "fp").String()); err != nil {
		t.Errorf("ParseRelease rejected a minted release: %v", err)
	}
}

func TestStackNamesRoundTrip(t *testing.T) {
	release := NewRelease("build-1", "fp")
	for _, want := range []StackName{
		InfraStack("prod"),
		InfraStack("pr-7"),
		AppStack("prod", "web", release),
		AppStack("pr-7", "web", release),
	} {
		got, err := ParseStackName(want.String())
		if err != nil {
			t.Fatalf("ParseStackName(%q): %v", want.String(), err)
		}
		if got != want {
			t.Errorf("ParseStackName(%q) = %+v, want %+v", want.String(), got, want)
		}
	}
}

func TestStackNamesHaveFixedArity(t *testing.T) {
	if got := AppStack("prod", "web", NewRelease("b", "")).String(); strings.Count(got, FieldSeparator) != 2 {
		t.Errorf("app stack %q must always carry env, app and release", got)
	}
	for _, bad := range []string{"prod", "prod--web", "prod--web--nothex", "prod--infra--r00000000", "prod--web--r1--extra"} {
		if _, err := ParseStackName(bad); err == nil {
			t.Errorf("ParseStackName(%q) = nil error, want a rejection", bad)
		}
	}
}

func TestValidateRejectsTheFieldSeparator(t *testing.T) {
	for _, bad := range []string{"my--project", "-leading", "trailing-", "Upper", ""} {
		if err := Validate("project", bad); err == nil {
			t.Errorf("Validate(%q) = nil error, want a rejection", bad)
		}
	}
	if err := Validate("project", "shop-front-2"); err != nil {
		t.Errorf("Validate rejected a legal slug: %v", err)
	}
}

func TestStorageKeysShareOnePrefix(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "api-users", Release: NewRelease("b", "")}
	prefix := c.StoragePrefix()
	for _, key := range []string{
		c.FunctionArtifactKey("deadbeef"),
		c.AssetKey("/static/app.js"),
		c.ImageConfigKey(),
		c.ISRPrefix(),
	} {
		if !strings.HasPrefix(key, prefix) {
			t.Errorf("key %q does not live under the release prefix %q", key, prefix)
		}
	}
	if want := "prod/shop/web/" + c.Release.String() + "/"; prefix != want {
		t.Errorf("StoragePrefix = %q, want %q", prefix, want)
	}
}

func TestDynamoKeysLeadWithTheProject(t *testing.T) {
	stack := AppStack("prod", "web", NewRelease("b", ""))
	for _, key := range []string{
		ProjectKey("shop"),
		StackKey("shop", stack),
		VarsKey("shop", "production"),
		ISRTagKey("shop", stack, "products"),
	} {
		project, err := ProjectOf(key)
		if err != nil {
			t.Fatalf("ProjectOf(%q): %v", key, err)
		}
		if project != "shop" {
			t.Errorf("ProjectOf(%q) = %q, want %q", key, project, "shop")
		}
	}
}

func TestStackKeysRoundTrip(t *testing.T) {
	want := AppStack("pr-7", "web", NewRelease("build-1", "fp"))
	project, got, err := ParseStackKey(StackKey("shop", want))
	if err != nil {
		t.Fatalf("ParseStackKey: %v", err)
	}
	if project != "shop" || got != want {
		t.Errorf("ParseStackKey = (%q, %+v), want (%q, %+v)", project, got, "shop", want)
	}
	if _, _, err := ParseStackKey(ProjectKey("shop")); err == nil {
		t.Error("a project key names no stack and must be rejected")
	}
	if _, _, err := ParseStackKey(VarsKey("shop", "production")); err == nil {
		t.Error("a vars key names no stack and must be rejected")
	}
}

func TestResourceIDsReadAsEnglish(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{ResourceID(KindFunction, "index"), "fn-index"},
		{ResourceID(KindFunction, "index", "url"), "fn-index-url"},
		{ResourceID(KindBucket, "uploads", "public-access-block"), "bucket-uploads-public-access-block"},
		{ResourceID(KindDatabase, "main", "security-group"), "db-main-security-group"},
	} {
		if tc.got != tc.want {
			t.Errorf("ResourceID = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestTagsDropEmptyFacts(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "index", Release: NewRelease("b", "")}
	tags := c.Tags(Facts{ManagedBy: "ocel-cli/1.2.3", EnvClass: "production", BuildID: "b"})
	if _, ok := tags["ocel:expires-at"]; ok {
		t.Error("an absent fact must not become an empty tag")
	}
	if tags["ocel:component"] != "function" {
		t.Errorf("ocel:component = %q, want %q", tags["ocel:component"], "function")
	}
	if tags["ocel:stack"] != c.Stack().String() {
		t.Errorf("ocel:stack = %q, want %q", tags["ocel:stack"], c.Stack().String())
	}
}
