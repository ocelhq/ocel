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

func TestEveryTopicOrTaskTheStacksSuiteProvisionsCarriesTheConfigADeployHandsIt(t *testing.T) {
	for _, resource := range declared([]provider.BindingType{provider.BindingTopic, provider.BindingTask, provider.BindingKV}) {
		queued := resource.Type == provider.BindingTopic || resource.Type == provider.BindingTask
		if queued && resource.Topic == nil {
			t.Errorf("declared() handed %s %s no topic config, and a deploy hands every topic and task one: a provider refusing it fails conformance for a resource no deploy sends", resource.Type, resource.Name)
		}
		if !queued && resource.Topic != nil {
			t.Errorf("declared() handed %s %s a topic config, and only a topic or a task takes one", resource.Type, resource.Name)
		}
		if resource.Type == provider.BindingTask && resource.Topic != nil {
			if consumers := resource.Topic.Consumers; len(consumers) != 1 || !consumers[0].Exclusive {
				t.Errorf("declared() handed task %s consumers %v, want the one exclusive consumer every task runs on", resource.Name, consumers)
			}
		}
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

func TestAProviderRunningWorkersButServingNoTopicOrTaskBindingFails(t *testing.T) {
	facts := provider.Facts{
		Vendor:         "nowhere",
		Computes:       []provider.Compute{provider.ComputeServerless},
		Bindings:       []provider.BindingType{provider.BindingTopic},
		WorkerCeilings: []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: time.Minute}},
	}
	if found := workerFaults(facts); len(found) == 0 {
		t.Error("workerFaults() = none, want a provider that lifts the refusal yet provisions no task flagged, since every task deploy would then fail at provisioning")
	}
}

func TestAWorkerCeilingOnAComputeTheProviderRunsPasses(t *testing.T) {
	facts := provider.Facts{
		Vendor:         "nowhere",
		Computes:       []provider.Compute{provider.ComputeServerless},
		Bindings:       []provider.BindingType{provider.BindingTopic, provider.BindingTask},
		WorkerCeilings: []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}},
	}
	if found := workerFaults(facts); len(found) != 0 {
		t.Errorf("workerFaults() = %v, want none", found)
	}
}
