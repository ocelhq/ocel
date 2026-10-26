package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func idlesCPU(patched bool) *patching {
	if !patched {
		return &patching{}
	}
	return servicePatched(map[string]any{"template": map[string]any{"containers": []any{
		map[string]any{"resources": map[string]any{"cpuIdle": true}},
	}}})
}

func TestAContainerKeepsItsCPUBetweenRequestsUnlessItsPatchIdlesIt(t *testing.T) {
	for _, patched := range []bool{false, true} {
		server := &googletest.Run{}
		p := servingTransformed(t, server, idlesCPU(patched))

		if err := provisionTransformedContainers(t, p, containerStackDeclaring(environment.TierProduction, "production", provider.AppValues{})); err != nil {
			t.Fatalf("ProvisionContainers() = %v", err)
		}
		if got := server.Created[0].Template.Containers[0].Resources.CpuIdle; got != patched {
			t.Errorf("a container whose patch sets cpuIdle %v idles its CPU %v, want %v: a container is billed per instance unless its patch says otherwise", patched, got, patched)
		}
	}
}

func TestANextFunctionKeepsItsCPUBetweenRequestsUnlessItsPatchIdlesIt(t *testing.T) {
	for _, patched := range []bool{false, true} {
		server := &googletest.Run{}
		p := servingTransformed(t, server, idlesCPU(patched))

		if err := provisionTransformedFunctions(t, p, nextSpec()); err != nil {
			t.Fatalf("ProvisionFunctions() = %v", err)
		}
		if got := server.Created[0].Template.Containers[0].Resources.CpuIdle; got != patched {
			t.Errorf("a Next function whose patch sets cpuIdle %v idles its CPU %v, want %v: after() and background refreshes need the CPU kept unless the patch gives it up", patched, got, patched)
		}
	}
}

func TestANodeFunctionIdlesItsCPUBetweenRequestsUnlessItsPatchKeepsIt(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"containers": []any{
		map[string]any{"resources": map[string]any{"cpuIdle": false}},
	}}}))

	if err := provisionTransformedFunctions(t, p, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if server.Created[0].Template.Containers[0].Resources.CpuIdle {
		t.Error("a node function whose patch sets cpuIdle false idles its CPU between requests, want it kept")
	}
}
