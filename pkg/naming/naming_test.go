package naming

import (
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

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

func TestOcelTagsNameTheNamespaceThatManagesAResourceAndTheTierItServes(t *testing.T) {
	got := OcelTags("acme", environment.TierPreview)
	want := map[string]string{"ocel:managed-by": "acme", "ocel:env-tier": "preview"}
	if !maps.Equal(got, want) {
		t.Errorf("OcelTags(acme, preview) = %v, want %v: every credential grant on an existing resource checks both", got, want)
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
		if kind.Component() != component {
			t.Errorf("%q: component = %q, want %q", kind, kind.Component(), component)
		}
	}
}

func TestTheRealtimeAuthorizerIsAComponentOfItsOwn(t *testing.T) {
	if KindRealtimeAuthorizer.Component() != "realtime-authorizer" {
		t.Errorf("%q: component = %q, want \"realtime-authorizer\"", KindRealtimeAuthorizer, KindRealtimeAuthorizer.Component())
	}
}
