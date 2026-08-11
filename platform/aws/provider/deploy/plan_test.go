package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func prodEnv() *deploymentsv1.Environment {
	return &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PRODUCTION}
}

func buildOnly(buildID string) Identity { return fingerprinted(buildID, "") }

func fingerprinted(buildID, fingerprint string) Identity {
	id, err := NewIdentity(buildID, fingerprint)
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

func TestAppDeployStackName(t *testing.T) {
	t.Parallel()

	distinct := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "unique per deploy",
			a:    AppDeployStackName("proj", "web", buildOnly("build1")),
			b:    AppDeployStackName("proj", "web", buildOnly("build2")),
		},
		{
			name: "unique per app",
			a:    AppDeployStackName("proj", "web", buildOnly("build1")),
			b:    AppDeployStackName("proj", "api", buildOnly("build1")),
		},
		{
			name: "no collision across hyphenated segments",
			a:    AppDeployStackName("proj", "web-x", buildOnly("1")),
			b:    AppDeployStackName("proj-web", "x", buildOnly("1")),
		},
		{
			name: "no collision between fingerprint and hyphenated buildID",
			a:    AppDeployStackName("proj", "web", fingerprinted("b", "f")),
			b:    AppDeployStackName("proj", "web", buildOnly("b-f")),
		},
	}
	for _, tc := range distinct {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.a == tc.b {
				t.Fatalf("distinct stack names collided: %q", tc.a)
			}
		})
	}

	t.Run("is deterministic", func(t *testing.T) {
		t.Parallel()
		a := AppDeployStackName("proj", "web", buildOnly("build1"))
		if got := AppDeployStackName("proj", "web", buildOnly("build1")); got != a {
			t.Errorf("AppDeployStackName is not deterministic: got %q, want %q", got, a)
		}
	})

	t.Run("build-only identity names the buildID's stack", func(t *testing.T) {
		t.Parallel()
		if got, want := AppDeployStackName("proj", "web", buildOnly("build1")), "proj--web--build1"; got != want {
			t.Errorf("AppDeployStackName = %q, want %q", got, want)
		}
		if got, want := PreviewAppDeployStackName("proj", "pr-1", "web", buildOnly("build1")), "proj--preview-pr-1--web--build1"; got != want {
			t.Errorf("PreviewAppDeployStackName = %q, want %q", got, want)
		}
	})

	t.Run("fingerprint separates deployments of one build", func(t *testing.T) {
		t.Parallel()
		plain := AppDeployStackName("proj", "web", buildOnly("build1"))
		a := AppDeployStackName("proj", "web", fingerprinted("build1", "aaa"))
		b := AppDeployStackName("proj", "web", fingerprinted("build1", "bbb"))
		for _, pair := range [][2]string{{plain, a}, {plain, b}, {a, b}} {
			if pair[0] == pair[1] {
				t.Errorf("stack names for distinct identities of one build collided: %q", pair[0])
			}
		}
	})
}

func TestInfraStackName(t *testing.T) {
	t.Parallel()

	t.Run("stable across deploys", func(t *testing.T) {
		t.Parallel()
		if got, want := InfraStackName("proj"), InfraStackName("proj"); got != want {
			t.Errorf("InfraStackName is not deterministic: got %q, want %q", got, want)
		}
	})

	t.Run("never collides with an app-deploy stack name", func(t *testing.T) {
		t.Parallel()
		if InfraStackName("proj") == AppDeployStackName("proj", "web", buildOnly("build1")) {
			t.Error("infra stack name collides with an app-deploy stack name")
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

		if plan.InfraStack != InfraStackName("proj") {
			t.Errorf("InfraStack = %q, want %q", plan.InfraStack, InfraStackName("proj"))
		}

		wantWeb := AppDeployStackName("proj", "web", buildOnly("buildW"))
		wantAPI := AppDeployStackName("proj", "api", buildOnly("buildA"))
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
		want := map[string]string{"web": "buildW", "api": "buildA"}
		if len(plan.Promotion.Builds) != len(want) {
			t.Fatalf("Promotion.Builds = %v, want %v", plan.Promotion.Builds, want)
		}
		for app, buildID := range want {
			if got := plan.Promotion.Builds[app]; got != buildID {
				t.Errorf("Promotion.Builds[%q] = %q, want %q", app, got, buildID)
			}
		}
	})

	t.Run("promotion carries the rendered identity", func(t *testing.T) {
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
			t.Errorf("Promotion.Builds[web] = %q, want %q", got, id.String())
		}
		if plan.AppStacks["web"] != AppDeployStackName("proj", "web", id) {
			t.Errorf("AppStacks[web] = %q, want %q", plan.AppStacks["web"], AppDeployStackName("proj", "web", id))
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

	t.Run("rejects unspecified class", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "web"}},
		}
		env := &deploymentsv1.Environment{}

		if _, err := BuildPlan(manifest, env, "promo1", Identities{"web": buildOnly("b")}); err == nil {
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
		if plan.InfraStack != PreviewInfraStackName("proj", "staging") {
			t.Errorf("InfraStack = %q, want %q", plan.InfraStack, PreviewInfraStackName("proj", "staging"))
		}
		if plan.InfraStack == InfraStackName("proj") {
			t.Error("persistent preview infra stack collides with production infra stack")
		}
		if plan.AppStacks["web"] == AppDeployStackName("proj", "web", buildOnly("b")) {
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
		if plan.InfraStack != "" {
			t.Errorf("ephemeral preview InfraStack = %q, want empty (infra stack skipped)", plan.InfraStack)
		}
		if plan.AppStacks["web"] == "" {
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
}
