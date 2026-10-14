package protection

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func publishedTo(t *testing.T, project clitest.FakeProject, tier environment.Tier) []edge.GatePublication {
	t.Helper()
	var published []edge.GatePublication
	for _, publication := range relayEdge(project).Publications() {
		if publication.State.Tier == tier {
			published = append(published, publication)
		}
	}
	return published
}

func TestCommandsActOnProductionUnlessGivenPreview(t *testing.T) {
	project := protectedProject(t)

	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	if production := recordedIn(t, project, environment.TierProduction); len(production.Gate.BypassSecrets) != 1 {
		t.Errorf("production holds %d bypass secrets, want the one just created", len(production.Gate.BypassSecrets))
	}
	if preview := recordedIn(t, project, environment.TierPreview); len(preview.Gate.BypassSecrets) != 0 {
		t.Errorf("preview holds %d bypass secrets, want none: no --preview was given", len(preview.Gate.BypassSecrets))
	}
	if len(publishedTo(t, project, environment.TierProduction)) == 0 || len(publishedTo(t, project, environment.TierPreview)) != 0 {
		t.Error("the change reached the wrong tier's edge stack")
	}
}

func TestPreviewTargetsThePreviewsAndNeverTouchesProduction(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	productionBefore := recordedIn(t, project, environment.TierProduction)

	if _, _, err := execute(t, project, "", "password", "rotate", "--preview"); err != nil {
		t.Fatal(err)
	}

	if after := recordedIn(t, project, environment.TierProduction); after.Password != productionBefore.Password || after.Gate.Key != productionBefore.Gate.Key {
		t.Error("rotating the preview password changed the production credentials")
	}
	if preview := recordedIn(t, project, environment.TierPreview); preview.Password == "" || preview.Password == productionBefore.Password {
		t.Errorf("the preview password is %q, want a generated one of its own", preview.Password)
	}
}

func TestEachTierHasAPasswordOfItsOwn(t *testing.T) {
	project := protectedProject(t)
	production, _, err := execute(t, project, "", "password", "rotate")
	if err != nil {
		t.Fatal(err)
	}
	preview, _, err := execute(t, project, "", "password", "rotate", "--preview")
	if err != nil {
		t.Fatal(err)
	}

	if strings.TrimSpace(production) == "" || strings.TrimSpace(production) == strings.TrimSpace(preview) {
		t.Errorf("production password %q and preview password %q, want two different ones", production, preview)
	}
	if got, _, _ := execute(t, project, "", "password"); strings.TrimSpace(got) != strings.TrimSpace(production) {
		t.Errorf("password = %q, want the production password %q", got, production)
	}
	if got, _, _ := execute(t, project, "", "password", "--preview"); strings.TrimSpace(got) != strings.TrimSpace(preview) {
		t.Errorf("password --preview = %q, want the preview password %q", got, preview)
	}
}

func TestProductionCredentialCommandsRefuseWhileTheConfigDoesNotProtectProductionAndPointAtTheSwitch(t *testing.T) {
	project := projectWith(t, `protection: { preview: true },`)

	for _, args := range [][]string{{"password"}, {"password", "rotate"}, {"bypass", "create"}, {"bypass", "ls"}, {"sync"}} {
		stdout, _, err := execute(t, project, "", args...)

		if err == nil || !strings.Contains(err.Error(), "protection.production") || !strings.Contains(err.Error(), "ocel deploy") {
			t.Errorf("%v: err = %v, want a refusal that points at protection.production and `ocel deploy`", args, err)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q, want nothing", args, stdout)
		}
	}
	if _, _, err := execute(t, project, "", "password", "rotate", "--preview"); err != nil {
		t.Errorf("rotating the preview password: %v, want it allowed: protection.preview is on", err)
	}
}

func TestTheSuccessMessageNamesTheTierItChanged(t *testing.T) {
	project := protectedProject(t)

	_, stderr, err := execute(t, project, "", "password", "rotate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Rotated the production password of "+clitest.FixtureSlug) {
		t.Errorf("stderr = %q, want it to say the production password was rotated", stderr)
	}

	_, stderr, err = execute(t, project, "", "password", "rotate", "--preview")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Rotated the preview password of "+clitest.FixtureSlug) {
		t.Errorf("stderr = %q, want it to say the preview password was rotated", stderr)
	}
}

func TestTheStatusNamesBothTiersAndWhyEachIsOnOrOff(t *testing.T) {
	project := projectWith(t, `protection: { production: true },`)

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Production of " + clitest.FixtureSlug + " is protected: protection.production is on", "Previews of " + clitest.FixtureSlug + " are not protected: protection.preview is off"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestTheStatusAsJSONNamesEachTierWithItsOwnSwitchAndRecord(t *testing.T) {
	useJSONOutput(t)
	project := projectWith(t, `protection: { production: true },`)
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	var status resultv1.ProtectionResult
	clitest.DecodeResultInto(t, stdout, &status)
	byTier := map[environmentv1.Tier]*resultv1.TierProtection{}
	for _, tier := range status.GetTiers() {
		byTier[tier.GetTier()] = tier
	}
	production, preview := byTier[environmentv1.Tier_TIER_PRODUCTION], byTier[environmentv1.Tier_TIER_PREVIEW]
	if production == nil || !production.GetEnabled() || !production.GetRecorded() || len(production.GetBypassSecretIds()) != 1 {
		t.Errorf("production = %v, want the switch on, a recorded gate and one bypass id", production)
	}
	if preview == nil || preview.GetEnabled() || preview.GetRecorded() {
		t.Errorf("preview = %v, want the switch off and nothing recorded", preview)
	}
}

func TestResultsNameTheTierTheyAreFor(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)

	stdout, _, err := execute(t, project, "", "password", "rotate", "--preview")
	if err != nil {
		t.Fatal(err)
	}

	var result resultv1.PasswordResult
	clitest.DecodeResultInto(t, stdout, &result)
	if result.GetTier() != environmentv1.Tier_TIER_PREVIEW {
		t.Errorf("tier = %v, want preview", result.GetTier())
	}
}
