package gcp

import "testing"

func TestCloudRunDeclaresNoFunctionSizeBudget(t *testing.T) {
	if got := pushing(t, "").Facts().MaxFunctionBytes; got != 0 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want none: a Cloud Run function is a container image, which no unzipped-code limit splits", got)
	}
}

func TestCloudRunDeclaresItsNextFunctionsKeepWorkingAfterTheResponse(t *testing.T) {
	if pushing(t, "").Facts().NextRefreshesByRequest {
		t.Error("Facts().NextRefreshesByRequest = true, want false: a Next service on Cloud Run is billed per instance and keeps its CPU once the response ends")
	}
}

func TestCloudRunSaysItShipsANextServerRuntime(t *testing.T) {
	if pushing(t, "").Hooks().ReadNextServerRuntime == nil {
		t.Error("Hooks().ReadNextServerRuntime = nil, want the Next server runtime a Cloud Run Next container loads its cache handlers from")
	}
}
