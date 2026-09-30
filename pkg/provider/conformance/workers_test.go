package conformance

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProviderThatRunsNoWorkersPassesOnlyByRefusingEveryDeclarationAsUnsupported(t *testing.T) {
	facts := provider.Facts{Vendor: "nowhere", Computes: []provider.Compute{provider.ComputeServerless}}
	if found := workerFaults(facts); len(found) != 0 {
		t.Errorf("workerFaults() = %v, want none from a provider that refuses topics, tasks and workers", found)
	}
}

func TestAWorkerCeilingOnAComputeTheProviderDoesNotRunFails(t *testing.T) {
	facts := provider.Facts{
		Vendor:         "nowhere",
		Computes:       []provider.Compute{provider.ComputeServerless},
		WorkerCeilings: []provider.WorkerCeiling{{Compute: provider.ComputeContainer, Unbounded: true}},
	}
	if found := workerFaults(facts); len(found) == 0 {
		t.Error("workerFaults() = none, want a ceiling on a compute the provider does not run flagged")
	}
}

func TestAComputeWithTwoWorkerCeilingsFails(t *testing.T) {
	facts := provider.Facts{
		Vendor:   "nowhere",
		Computes: []provider.Compute{provider.ComputeServerless},
		WorkerCeilings: []provider.WorkerCeiling{
			{Compute: provider.ComputeServerless, MaxDuration: time.Minute},
			{Compute: provider.ComputeServerless, MaxDuration: time.Hour},
		},
	}
	if found := workerFaults(facts); len(found) == 0 {
		t.Error("workerFaults() = none, want a compute named twice flagged, since the build could read either ceiling")
	}
}

func TestABoundedWorkerCeilingOfNoTimeFails(t *testing.T) {
	facts := provider.Facts{
		Vendor:         "nowhere",
		Computes:       []provider.Compute{provider.ComputeServerless},
		WorkerCeilings: []provider.WorkerCeiling{{Compute: provider.ComputeServerless}},
	}
	if found := workerFaults(facts); len(found) == 0 {
		t.Error("workerFaults() = none, want a ceiling of zero flagged, since it refuses every maxDuration")
	}
}

func TestAWorkerCeilingOnAComputeTheProviderRunsPasses(t *testing.T) {
	facts := provider.Facts{
		Vendor:         "nowhere",
		Computes:       []provider.Compute{provider.ComputeServerless},
		WorkerCeilings: []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}},
	}
	if found := workerFaults(facts); len(found) != 0 {
		t.Errorf("workerFaults() = %v, want none", found)
	}
}
