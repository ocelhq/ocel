package naming

import (
	"strings"
	"testing"
)

func TestIsResourceNameAcceptsHyphenatedLowercaseWordsOfAtMostSixtyThreeCharacters(t *testing.T) {
	for name, want := range map[string]bool{
		"orders":                true,
		"order-emails":          true,
		"v2":                    true,
		strings.Repeat("a", 63): true,
		strings.Repeat("a", 64): false,
		"":                      false,
		"Orders":                false,
		"order--emails":         false,
		"-orders":               false,
		"orders-":               false,
		"order_emails":          false,
	} {
		if got := IsResourceName(name); got != want {
			t.Errorf("IsResourceName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSanitizeKeepsTheAlphabet(t *testing.T) {
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
		Release: NewReleaseToken("build-1", ""),
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
	base := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Release: NewReleaseToken("b1", "")}
	long := strings.Repeat("route-segment-", 6)

	a, b := base, base
	a.Name = long + "one"
	b.Name = long + "two"

	if a.PhysicalName(64) == b.PhysicalName(64) {
		t.Fatalf("distinct routes collided on %q", a.PhysicalName(64))
	}
}

func TestFitIsStableWhenItFits(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "api-users", Release: NewReleaseToken("build-1", "")}
	want := "shop-prod-web-api-users-" + c.Release.String()
	if got := c.PhysicalName(64); got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
}

func TestFitOmitsEmptySegments(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Release: NewReleaseToken("build-1", "")}
	want := "shop-prod-web-" + c.Release.String()
	if got := c.PhysicalName(64); got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
	}
	if got := Fit(64, WordSeparator, Fixed("shop"), Compressible(""), Fixed("prod")); got != "shop-prod" {
		t.Errorf("Fit = %q, want %q", got, "shop-prod")
	}
}

func TestReleaseSeparatesBuildFromFingerprint(t *testing.T) {
	if NewReleaseToken("build-1", "fp-a") == NewReleaseToken("build-1", "fp-b") {
		t.Error("a changed value fingerprint must mint a new release")
	}
	if NewReleaseToken("build-1", "") == NewReleaseToken("build-2", "") {
		t.Error("a changed build must mint a new release")
	}
	if _, err := ParseReleaseToken(NewReleaseToken("build-1", "fp").String()); err != nil {
		t.Errorf("ParseRelease rejected a minted release: %v", err)
	}
}

func TestStackNamesRoundTrip(t *testing.T) {
	release := NewReleaseToken("build-1", "fp")
	for _, want := range []StackName{
		InfraStack("prod"),
		InfraStack("pr-7"),
		AppStack("prod", "web", release),
		AppStack("pr-7", "web", release),
		ContainerWorkersStack("prod", "web"),
		ContainerWorkersStack("pr-7", "web"),
		{Env: "prod", App: "web"},
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
	if got := AppStack("prod", "web", NewReleaseToken("b", "")).String(); strings.Count(got, FieldSeparator) != 2 {
		t.Errorf("app stack %q must always include env, app and release", got)
	}
	for _, bad := range []string{"prod", "prod--web--nothex", "prod--infra--r00000000", "prod--infra--containers", "prod--web--r1--extra"} {
		if _, err := ParseStackName(bad); err == nil {
			t.Errorf("ParseStackName(%q) = nil error, want a rejection", bad)
		}
	}
}

func TestAnAppsContainerWorkersStackIsNamedApartFromItsWorkersStackAndEveryRelease(t *testing.T) {
	containers := ContainerWorkersStack("prod", "web")
	others := []StackName{
		{Env: "prod", App: "web"},
		{Env: "prod", App: "web-containers"},
		AppStack("prod", "web", NewReleaseToken("b", "")),
		InfraStack("prod"),
	}
	for _, other := range others {
		if containers.String() == other.String() || containers == other {
			t.Errorf("the container workers stack of web is %q, the same as %+v", containers, other)
		}
	}
	if !containers.HoldsContainerWorkers() || (StackName{Env: "prod", App: "web"}).HoldsContainerWorkers() {
		t.Error("only the stack ContainerWorkersStack names holds container workers")
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
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "api-users", Release: NewReleaseToken("b", "")}
	prefix := c.StoragePrefix()
	for _, key := range []string{
		c.FunctionArtifactKey("deadbeef"),
		c.AssetKey("/static/app.js"),
		c.ImageConfigKey(),
		c.ISRPrefix(),
		c.BytecodePrefix(),
	} {
		if !strings.HasPrefix(key, prefix) {
			t.Errorf("key %q does not live under the release prefix %q", key, prefix)
		}
	}
	if want := "prod/shop/web/" + c.Release.String() + "/"; prefix != want {
		t.Errorf("StoragePrefix = %q, want %q", prefix, want)
	}
	if want := prefix + "bytecode/"; c.BytecodePrefix() != want {
		t.Errorf("BytecodePrefix = %q, want %q", c.BytecodePrefix(), want)
	}
	if c.BytecodePrefix() == c.ISRPrefix() {
		t.Errorf("BytecodePrefix and ISRPrefix share %q; the compile cache must not land in the ISR namespace", c.ISRPrefix())
	}
}

func TestSessionKeysPartitionByProjectAndEnvironment(t *testing.T) {
	prod := SessionKeyPrefix("shop", "prod")

	for _, other := range []string{SessionKeyPrefix("shop", "pr-7"), SessionKeyPrefix("bank", "prod")} {
		if strings.HasPrefix(other, prod) || strings.HasPrefix(prod, other) {
			t.Errorf("%q and %q overlap; a LeadingKeys condition on one would reach the other's sessions", prod, other)
		}
	}
	if !strings.HasSuffix(prod, KeySeparator) {
		t.Errorf("SessionKeyPrefix = %q, want it to end on %q so %q* cannot match a longer scope", prod, KeySeparator, prod)
	}
	if strings.HasPrefix(token(tokenProject, "shop")+KeySeparator, prod) {
		t.Errorf("SessionKeyPrefix = %q reaches the project's own keys", prod)
	}
}

func TestTaskKeysPartitionByProjectAndEnvironmentApartFromSessions(t *testing.T) {
	prod := TaskKeyPrefix("shop", "prod")

	for _, other := range []string{TaskKeyPrefix("shop", "pr-7"), TaskKeyPrefix("bank", "prod"), SessionKeyPrefix("shop", "prod")} {
		if strings.HasPrefix(other, prod) || strings.HasPrefix(prod, other) {
			t.Errorf("%q and %q overlap; a LeadingKeys condition on one would reach the other's keys", prod, other)
		}
	}
	if !strings.HasSuffix(prod, KeySeparator) {
		t.Errorf("TaskKeyPrefix = %q, want it to end on %q so %q* cannot match a longer scope", prod, KeySeparator, prod)
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
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "index", Release: NewReleaseToken("b", "")}
	tags := c.Tags(TagValues{ManagedBy: "ocel-cli/1.2.3", EnvTier: "production", BuildID: "b"})
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

func TestTheTierADeployServesIsTaggedAsItsEnvTier(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "pr-7", App: "web", Kind: KindFunction, Name: "index", Release: NewReleaseToken("b", "")}
	tags := c.Tags(TagValues{EnvTier: "preview"})
	if tags["ocel:env-tier"] != "preview" {
		t.Errorf("ocel:env-tier = %q, want %q: IAM conditions scope an edge's grants to one tier by this tag", tags["ocel:env-tier"], "preview")
	}
}

func TestAFunctionIsTaggedWithTheBundleItWasBuiltFrom(t *testing.T) {
	c := Coordinate{Project: "shop", Env: "prod", App: "web", Kind: KindFunction, Name: "bundle-0", Release: NewReleaseToken("b", "")}
	tags := c.Tags(TagValues{Bundle: "bundle-0"})
	if tags["ocel:bundle"] != "bundle-0" {
		t.Errorf("ocel:bundle = %q, want %q", tags["ocel:bundle"], "bundle-0")
	}
	if route, ok := tags["ocel:route"]; ok {
		t.Errorf("ocel:route = %q, want no route tag: one bundle serves many routes", route)
	}
}

func TestADigestBecomesATagUnderOneRule(t *testing.T) {
	if got, want := DigestTag("sha256:abc"), "sha256-abc"; got != want {
		t.Errorf("DigestTag() = %q, want %q", got, want)
	}
	if got, want := RepositorySegment("ocel/web"), "web"; got != want {
		t.Errorf("RepositorySegment() = %q, want %q", got, want)
	}
	if got, want := RepositorySegment("web"), "web"; got != want {
		t.Errorf("RepositorySegment() = %q, want %q", got, want)
	}
}

func TestAWorkerAndATopicAreComponentsOfTheirOwn(t *testing.T) {
	for kind, component := range map[Kind]string{KindWorker: "worker", KindTopic: "topic"} {
		if !kind.Valid() || kind.Component() != component {
			t.Errorf("%q: valid = %v, component = %q, want %q", kind, kind.Valid(), kind.Component(), component)
		}
	}
}

func TestTheRealtimeAuthorizerIsAComponentOfItsOwn(t *testing.T) {
	if !KindRealtimeAuthorizer.Valid() || KindRealtimeAuthorizer.Component() != "realtime-authorizer" {
		t.Errorf("%q: valid = %v, component = %q, want \"realtime-authorizer\"", KindRealtimeAuthorizer, KindRealtimeAuthorizer.Valid(), KindRealtimeAuthorizer.Component())
	}
}
