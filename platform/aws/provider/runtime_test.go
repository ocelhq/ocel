package aws

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestAContainerBuiltFromAFunctionThatIsNotNextIsRefused(t *testing.T) {
	runtime := (&Provider{}).Runtime()
	node := buildoutput.Framework{Name: buildoutput.FrameworkNode}

	if _, err := runtime.FunctionBase(context.Background(), node); err == nil || !strings.Contains(err.Error(), "use a Dockerfile") {
		t.Errorf("FunctionBase(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
	if _, err := runtime.FunctionFiles(context.Background(), node); err == nil || !strings.Contains(err.Error(), "use a Dockerfile") {
		t.Errorf("FunctionFiles(node) = %v, want a refusal naming the Dockerfile that builds the container instead", err)
	}
}
