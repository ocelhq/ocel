package gcp

import "testing"

func TestCloudRunDeclaresNoFunctionSizeBudget(t *testing.T) {
	if got := pushing(t, "").Facts().MaxFunctionBytes; got != 0 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want none: a Cloud Run function is a container image, which no unzipped-code limit splits", got)
	}
}

func TestCloudRunSaysItShipsANextServerRuntime(t *testing.T) {
	if pushing(t, "").Hooks().ReadNextServerRuntime == nil {
		t.Error("Hooks().ReadNextServerRuntime = nil, want the Next server runtime a Cloud Run Next container loads its cache handlers from")
	}
}

func TestCloudRunAnswersThePreviewLoginAnEdgeHandsItAndGatesAPreviewWhenNothingIsInFrontOfIt(t *testing.T) {
	facts := pushing(t, "").Facts()

	if !facts.AnswersPreviewLogin {
		t.Error("Facts().AnswersPreviewLogin = false, and a gated preview behind the load balancer posts its password to the service, because the plugin reads no body")
	}
	if !facts.GatesPreviewsAtOrigin {
		t.Error("Facts().GatesPreviewsAtOrigin = false, and a preview served on its own run.app url would be refused a gate")
	}
}
