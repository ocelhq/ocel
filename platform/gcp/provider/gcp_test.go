package gcp

import "testing"

func TestCloudRunDeclaresNoFunctionSizeBudget(t *testing.T) {
	if got := pushing(t, "").Facts().MaxFunctionBytes; got != 0 {
		t.Errorf("Facts().MaxFunctionBytes = %d, want none: a Cloud Run function is a container image, which no unzipped-code limit splits", got)
	}
}

func TestCloudRunDeclaresItsNextFunctionsRefreshByRequest(t *testing.T) {
	if !pushing(t, "").Facts().NextRefreshesByRequest {
		t.Error("Facts().NextRefreshesByRequest = false, want true: a Cloud Run function billed per request stops its work when the response ends")
	}
}
