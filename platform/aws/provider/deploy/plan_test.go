package deploy

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func prodEnv() *deploymentsv1.Environment {
	return &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PRODUCTION}
}

const testDeploymentID = "d1a2b3c4"

func buildOnly(buildID string) Identity { return fingerprinted(buildID, "") }

func fingerprinted(buildID, fingerprint string) Identity {
	return deployed(testDeploymentID, buildID, fingerprint)
}

func deployed(deploymentID, buildID, fingerprint string) Identity {
	id, err := NewIdentity(deploymentID, buildID, fingerprint)
	if err != nil {
		panic(err)
	}
	return id
}

func previewEnv(lifecycle deploymentsv1.Environment_Lifecycle) *deploymentsv1.Environment {
	return &deploymentsv1.Environment{
		Class:     deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle: lifecycle,
		Identity:  "staging",
	}
}

func appStack(t *testing.T, env, app string, id Identity) naming.StackName {
	t.Helper()
	return naming.AppStack(env, app, releaseOf(id))
}

func TestEnvName(t *testing.T) {
	t.Parallel()

	t.Run("production is named, not classed", func(t *testing.T) {
		t.Parallel()
		got, err := EnvName(prodEnv())
		if err != nil {
			t.Fatalf("EnvName: %v", err)
		}
		if got != ProductionEnv {
			t.Errorf("EnvName = %q, want %q", got, ProductionEnv)
		}
	})

	t.Run("a preview environment is named by its pointer", func(t *testing.T) {
		t.Parallel()
		got, err := EnvName(previewEnv(deploymentsv1.Environment_LIFECYCLE_PERSISTENT))
		if err != nil {
			t.Fatalf("EnvName: %v", err)
		}
		if got != "staging" {
			t.Errorf("EnvName = %q, want the pointer with no class infix", got)
		}
	})

	t.Run("a preview named prod is refused", func(t *testing.T) {
		t.Parallel()
		env := &deploymentsv1.Environment{
			Class:    deploymentsv1.Environment_CLASS_PREVIEW,
			Identity: ProductionEnv,
		}
		_, err := EnvName(env)
		if err == nil {
			t.Fatal("EnvName err = nil, want a preview named after production refused")
		}
		if !strings.Contains(err.Error(), "rename the preview") {
			t.Errorf("error %q does not tell the user how to recover", err)
		}
	})

	t.Run("an unusable preview name is refused at ingest", func(t *testing.T) {
		t.Parallel()
		for _, pointer := range []string{"", "PR-7", "pr--7", "-pr7", "pr_7", "pr 7"} {
			env := &deploymentsv1.Environment{
				Class:    deploymentsv1.Environment_CLASS_PREVIEW,
				Identity: pointer,
			}
			if _, err := EnvName(env); err == nil {
				t.Errorf("EnvName(%q) err = nil, want the name refused before it reaches a stack", pointer)
			}
		}
	})

	t.Run("an unspecified class is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := EnvName(&deploymentsv1.Environment{}); err == nil {
			t.Fatal("EnvName err = nil, want an unspecified class refused")
		}
	})
}

func TestEnvScope(t *testing.T) {
	t.Parallel()

	t.Run("a preview class with no pointer scopes every preview", func(t *testing.T) {
		t.Parallel()
		got, err := EnvScope(&deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW})
		if err != nil {
			t.Fatalf("EnvScope: %v", err)
		}
		if got != EveryPreview {
			t.Errorf("EnvScope = %q, want the project-wide preview scope", got)
		}
	})

	t.Run("a pointed preview keeps its name", func(t *testing.T) {
		t.Parallel()
		got, err := EnvScope(previewEnv(deploymentsv1.Environment_LIFECYCLE_PERSISTENT))
		if err != nil {
			t.Fatalf("EnvScope: %v", err)
		}
		if got != "staging" {
			t.Errorf("EnvScope = %q, want %q", got, "staging")
		}
	})

	t.Run("an unusable preview name is still refused", func(t *testing.T) {
		t.Parallel()
		env := &deploymentsv1.Environment{
			Class:    deploymentsv1.Environment_CLASS_PREVIEW,
			Identity: ProductionEnv,
		}
		if _, err := EnvScope(env); err == nil {
			t.Fatal("EnvScope err = nil, want a preview named after production refused")
		}
	})

	t.Run("production and unspecified classes are unchanged", func(t *testing.T) {
		t.Parallel()
		got, err := EnvScope(prodEnv())
		if err != nil {
			t.Fatalf("EnvScope: %v", err)
		}
		if got != ProductionEnv {
			t.Errorf("EnvScope = %q, want %q", got, ProductionEnv)
		}
		if _, err := EnvScope(&deploymentsv1.Environment{}); err == nil {
			t.Fatal("EnvScope err = nil, want an unspecified class refused")
		}
	})
}

func TestReleaseOf(t *testing.T) {
	t.Parallel()

	t.Run("fixed arity whatever the identity carries", func(t *testing.T) {
		t.Parallel()
		plain := appStack(t, "prod", "web", buildOnly("build1"))
		full := appStack(t, "prod", "web", fingerprinted("build1", "fp1"))
		for _, stack := range []naming.StackName{plain, full} {
			if got := len(strings.Split(stack.String(), naming.FieldSeparator)); got != 3 {
				t.Errorf("stack %q has %d fields, want a fixed 3", stack, got)
			}
		}
	})

	t.Run("either input forces a new stack", func(t *testing.T) {
		t.Parallel()
		base := appStack(t, "prod", "web", fingerprinted("build1", "fp1"))
		newBuild := appStack(t, "prod", "web", fingerprinted("build2", "fp1"))
		newValues := appStack(t, "prod", "web", fingerprinted("build1", "fp2"))
		noValues := appStack(t, "prod", "web", buildOnly("build1"))

		for _, other := range []naming.StackName{newBuild, newValues, noValues} {
			if base == other {
				t.Errorf("a changed identity reused the stack %q", base)
			}
		}
	})

	t.Run("no collision between a fingerprint and a hyphenated build id", func(t *testing.T) {
		t.Parallel()
		if a, b := appStack(t, "prod", "web", fingerprinted("b", "f")), appStack(t, "prod", "web", buildOnly("b-f")); a == b {
			t.Errorf("distinct identities collided on %q", a)
		}
	})

	t.Run("is deterministic", func(t *testing.T) {
		t.Parallel()
		if a, b := releaseOf(buildOnly("build1")), releaseOf(buildOnly("build1")); a != b {
			t.Errorf("releaseOf is not deterministic: %q then %q", a, b)
		}
	})
}

func TestBuildPlan(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{
				{Name: "web"},
				{Name: "api"},
			},
		}
		identities := Identities{"web": buildOnly("buildW"), "api": buildOnly("buildA")}

		plan, err := BuildPlan(manifest, prodEnv(), "promo1", identities)
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}

		if want := naming.InfraStack(ProductionEnv); plan.InfraStack != want {
			t.Errorf("InfraStack = %q, want %q", plan.InfraStack, want)
		}

		wantWeb := appStack(t, ProductionEnv, "web", buildOnly("buildW"))
		wantAPI := appStack(t, ProductionEnv, "api", buildOnly("buildA"))
		if plan.AppStacks["web"] != wantWeb {
			t.Errorf("AppStacks[web] = %q, want %q", plan.AppStacks["web"], wantWeb)
		}
		if plan.AppStacks["api"] != wantAPI {
			t.Errorf("AppStacks[api] = %q, want %q", plan.AppStacks["api"], wantAPI)
		}
		if plan.AppStacks["web"] == plan.AppStacks["api"] {
			t.Error("distinct apps must not share an app-deploy stack name")
		}

		if plan.Promotion.PromotionID != "promo1" {
			t.Errorf("PromotionID = %q, want %q", plan.Promotion.PromotionID, "promo1")
		}
		want := map[string]string{"web": buildOnly("buildW").String(), "api": buildOnly("buildA").String()}
		if len(plan.Promotion.Builds) != len(want) {
			t.Fatalf("Promotion.Builds = %v, want %v", plan.Promotion.Builds, want)
		}
		for app, identity := range want {
			if got := plan.Promotion.Builds[app]; got != identity {
				t.Errorf("Promotion.Builds[%q] = %q, want %q", app, got, identity)
			}
		}
	})

	t.Run("the stack names read as env, app and release", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{Slug: "shop", Apps: []*deploymentsv1.ManifestApp{{Name: "web"}}}

		plan, err := BuildPlan(manifest, prodEnv(), "promo1", Identities{"web": buildOnly("b")})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if got, want := plan.InfraStack.String(), "prod--infra"; got != want {
			t.Errorf("InfraStack = %q, want %q — the project belongs to the Pulumi project", got, want)
		}
		release := releaseOf(buildOnly("b")).String()
		if got, want := plan.AppStacks["web"].String(), "prod--web--"+release; got != want {
			t.Errorf("AppStacks[web] = %q, want %q", got, want)
		}
	})

	t.Run("the promotion record keeps the build id recoverable", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}
		id := fingerprinted("buildW", "fp1")

		plan, err := BuildPlan(manifest, prodEnv(), "promo1", Identities{"web": id})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if got := plan.Promotion.Builds["web"]; got != id.String() {
			t.Errorf("Promotion.Builds[web] = %q, want %q — the release token is one-way", got, id.String())
		}
		recovered, err := ParseIdentity(plan.Promotion.Builds["web"])
		if err != nil {
			t.Fatalf("ParseIdentity: %v", err)
		}
		if plan.AppStacks["web"] != appStack(t, ProductionEnv, "web", recovered) {
			t.Error("the recovered identity does not name the stack it was deployed under")
		}
	})

	t.Run("missing buildID errors", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}},
		}
		identities := Identities{"web": buildOnly("buildW")}

		if _, err := BuildPlan(manifest, prodEnv(), "promo1", identities); err == nil {
			t.Fatal("BuildPlan with a missing app build id should error, got nil")
		}
	})

	t.Run("an app named infra is refused", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: naming.InfraApp}},
		}

		if _, err := BuildPlan(manifest, prodEnv(), "promo1", Identities{naming.InfraApp: buildOnly("b")}); err == nil {
			t.Fatal("BuildPlan with an app named infra should error, got nil")
		}
	})

	t.Run("rejects unspecified class", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}

		if _, err := BuildPlan(manifest, &deploymentsv1.Environment{}, "promo1", Identities{"web": buildOnly("b")}); err == nil {
			t.Fatal("BuildPlan for an unspecified class should error, got nil")
		}
	})

	t.Run("persistent preview has per-name infra stack", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}

		plan, err := BuildPlan(manifest, previewEnv(deploymentsv1.Environment_LIFECYCLE_PERSISTENT), "promo1", Identities{"web": buildOnly("b")})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if want := naming.InfraStack("staging"); plan.InfraStack != want {
			t.Errorf("InfraStack = %q, want %q", plan.InfraStack, want)
		}
		if plan.InfraStack == naming.InfraStack(ProductionEnv) {
			t.Error("persistent preview infra stack collides with production infra stack")
		}
		if plan.AppStacks["web"] == appStack(t, ProductionEnv, "web", buildOnly("b")) {
			t.Error("preview app-deploy stack collides with the production one for the same build")
		}
	})

	t.Run("ephemeral preview has no infra stack", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}

		plan, err := BuildPlan(manifest, previewEnv(deploymentsv1.Environment_LIFECYCLE_EPHEMERAL), "promo1", Identities{"web": buildOnly("b")})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if !plan.InfraStack.IsZero() {
			t.Errorf("ephemeral preview InfraStack = %q, want none (infra stack skipped)", plan.InfraStack)
		}
		if plan.AppStacks["web"].IsZero() {
			t.Error("ephemeral preview must still plan an app-deploy stack so the URL serves")
		}
	})

	t.Run("preview requires identity", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}
		env := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW}

		if _, err := BuildPlan(manifest, env, "promo1", Identities{"web": buildOnly("b")}); err == nil {
			t.Fatal("BuildPlan for a preview with no identity should error, got nil")
		}
	})

	t.Run("two persistent previews do not collide", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{Slug: "proj", Apps: []*deploymentsv1.ManifestApp{{Name: "web"}}}
		staging := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: "staging"}
		demo := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: "demo"}

		a, _ := BuildPlan(manifest, staging, "p", Identities{"web": buildOnly("b")})
		b, _ := BuildPlan(manifest, demo, "p", Identities{"web": buildOnly("b")})
		if a.InfraStack == b.InfraStack {
			t.Errorf("two persistent previews share an infra stack: %q", a.InfraStack)
		}
		if a.AppStacks["web"] == b.AppStacks["web"] {
			t.Errorf("two persistent previews share an app-deploy stack: %q", a.AppStacks["web"])
		}
	})

	t.Run("no apps yields empty plan", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{Slug: "proj"}

		plan, err := BuildPlan(manifest, prodEnv(), "promo1", Identities{})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if len(plan.AppStacks) != 0 || len(plan.Promotion.Builds) != 0 {
			t.Errorf("expected empty plan for a manifest with no apps, got %+v", plan)
		}
	})

	t.Run("every planned stack re-parses", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{Slug: "proj", Apps: []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}}

		plan, err := BuildPlan(manifest, previewEnv(deploymentsv1.Environment_LIFECYCLE_PERSISTENT), "p", Identities{
			"web": buildOnly("b1"),
			"api": fingerprinted("b2", "fp"),
		})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		stacks := []naming.StackName{plan.InfraStack, plan.AppStacks["web"], plan.AppStacks["api"]}
		for _, stack := range stacks {
			parsed, err := naming.ParseStackName(stack.String())
			if err != nil {
				t.Errorf("ParseStackName(%q): %v — the index could not store it", stack, err)
				continue
			}
			if parsed != stack {
				t.Errorf("ParseStackName(%q) = %+v, want %+v", stack, parsed, stack)
			}
		}
	})
}
