package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func prodEnv() *deploymentsv1.Environment {
	return &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PRODUCTION}
}

func TestAppDeployStackName_UniquePerDeploy(t *testing.T) {
	a := AppDeployStackName("proj", "web", "build1")
	b := AppDeployStackName("proj", "web", "build2")
	if a == b {
		t.Fatalf("stack names for different build ids collided: %q", a)
	}
	if got := AppDeployStackName("proj", "web", "build1"); got != a {
		t.Errorf("AppDeployStackName is not deterministic: got %q, want %q", got, a)
	}
}

func TestAppDeployStackName_UniquePerApp(t *testing.T) {
	a := AppDeployStackName("proj", "web", "build1")
	b := AppDeployStackName("proj", "api", "build1")
	if a == b {
		t.Fatalf("stack names for different apps collided: %q", a)
	}
}

// A naive "projectID-app-buildID" join would let a hyphen inside one segment
// shift where the next segment starts, colliding two distinct triples onto
// the same name.
func TestAppDeployStackName_NoCollisionAcrossHyphenatedSegments(t *testing.T) {
	a := AppDeployStackName("proj", "web-x", "1")
	b := AppDeployStackName("proj-web", "x", "1")
	if a == b {
		t.Fatalf("distinct (project, app, build id) triples collided: %q", a)
	}
}

func TestInfraStackName_StableAcrossDeploys(t *testing.T) {
	if got, want := InfraStackName("proj"), InfraStackName("proj"); got != want {
		t.Errorf("InfraStackName is not deterministic: got %q, want %q", got, want)
	}
	if InfraStackName("proj") == AppDeployStackName("proj", "web", "build1") {
		t.Error("infra stack name collides with an app-deploy stack name")
	}
}

func TestBuildPlan_HappyPath(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps: []*deploymentsv1.ManifestApp{
			{Name: "web"},
			{Name: "api"},
		},
	}
	builds := BuildIDs{"web": "buildW", "api": "buildA"}

	plan, err := BuildPlan(manifest, prodEnv(), "promo1", builds)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if plan.InfraStack != InfraStackName("proj") {
		t.Errorf("InfraStack = %q, want %q", plan.InfraStack, InfraStackName("proj"))
	}

	wantWeb := AppDeployStackName("proj", "web", "buildW")
	wantAPI := AppDeployStackName("proj", "api", "buildA")
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
}

func TestBuildPlan_MissingBuildIDErrors(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}},
	}
	builds := BuildIDs{"web": "buildW"} // api missing

	if _, err := BuildPlan(manifest, prodEnv(), "promo1", builds); err == nil {
		t.Fatal("BuildPlan with a missing app build id should error, got nil")
	}
}

func TestBuildPlan_RejectsUnspecifiedClass(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web"}},
	}
	env := &deploymentsv1.Environment{} // CLASS_UNSPECIFIED

	if _, err := BuildPlan(manifest, env, "promo1", BuildIDs{"web": "b"}); err == nil {
		t.Fatal("BuildPlan for an unspecified class should error, got nil")
	}
}

func previewEnv(lifecycle deploymentsv1.Environment_Lifecycle) *deploymentsv1.Environment {
	return &deploymentsv1.Environment{
		Class:     deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle: lifecycle,
		Identity:  "staging",
	}
}

func TestBuildPlan_PersistentPreviewHasPerNameInfraStack(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web"}},
	}

	plan, err := BuildPlan(manifest, previewEnv(deploymentsv1.Environment_LIFECYCLE_PERSISTENT), "promo1", BuildIDs{"web": "b"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.InfraStack != PreviewInfraStackName("proj", "staging") {
		t.Errorf("InfraStack = %q, want %q", plan.InfraStack, PreviewInfraStackName("proj", "staging"))
	}
	// A persistent preview's infra stack must not collide with production's.
	if plan.InfraStack == InfraStackName("proj") {
		t.Error("persistent preview infra stack collides with production infra stack")
	}
	// Preview app-deploy stacks must not collide with production's.
	if plan.AppStacks["web"] == AppDeployStackName("proj", "web", "b") {
		t.Error("preview app-deploy stack collides with the production one for the same build")
	}
}

func TestBuildPlan_EphemeralPreviewHasNoInfraStack(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web"}},
	}

	plan, err := BuildPlan(manifest, previewEnv(deploymentsv1.Environment_LIFECYCLE_EPHEMERAL), "promo1", BuildIDs{"web": "b"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.InfraStack != "" {
		t.Errorf("ephemeral preview InfraStack = %q, want empty (infra stack skipped)", plan.InfraStack)
	}
	if plan.AppStacks["web"] == "" {
		t.Error("ephemeral preview must still plan an app-deploy stack so the URL serves")
	}
}

func TestBuildPlan_PreviewRequiresIdentity(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web"}},
	}
	env := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW} // no identity

	if _, err := BuildPlan(manifest, env, "promo1", BuildIDs{"web": "b"}); err == nil {
		t.Fatal("BuildPlan for a preview with no identity should error, got nil")
	}
}

func TestBuildPlan_TwoPersistentPreviewsDoNotCollide(t *testing.T) {
	manifest := &deploymentsv1.Manifest{ProjectId: "proj", Apps: []*deploymentsv1.ManifestApp{{Name: "web"}}}
	staging := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: "staging"}
	demo := &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT, Identity: "demo"}

	a, _ := BuildPlan(manifest, staging, "p", BuildIDs{"web": "b"})
	b, _ := BuildPlan(manifest, demo, "p", BuildIDs{"web": "b"})
	if a.InfraStack == b.InfraStack {
		t.Errorf("two persistent previews share an infra stack: %q", a.InfraStack)
	}
	if a.AppStacks["web"] == b.AppStacks["web"] {
		t.Errorf("two persistent previews share an app-deploy stack: %q", a.AppStacks["web"])
	}
}

func TestBuildPlan_NoAppsYieldsEmptyPlan(t *testing.T) {
	manifest := &deploymentsv1.Manifest{ProjectId: "proj"}

	plan, err := BuildPlan(manifest, prodEnv(), "promo1", BuildIDs{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.AppStacks) != 0 || len(plan.Promotion.Builds) != 0 {
		t.Errorf("expected empty plan for a manifest with no apps, got %+v", plan)
	}
}
