package apigateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTeardownRefusesWhileAProjectStillHasARestAPI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	err = e.Teardown(ctx, edge.ClassProduction)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Teardown = %v, want a refusal while %s still has a REST API", err, conformanceSlug)
	}
	if !strings.Contains(err.Error(), conformanceSlug) || !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("refusal = %q, want it to name the project and the command that clears it", err)
	}
	if err := e.Teardown(ctx, edge.ClassPreview); err != nil {
		t.Errorf("Teardown(preview) = %v, want nil: the standing API belongs to the production class", err)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown after the project's destroy = %v, want nil", err)
	}
}

func TestTeardownIgnoresARestAPIAnotherBootstrapFronts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := w.gateway.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String(apiName("fronted-elsewhere", edge.ClassProduction, "")),
	}); err != nil {
		t.Fatalf("CreateRestApi: %v", err)
	}

	if err := e.Teardown(ctx, edge.ClassProduction); err != nil {
		t.Errorf("Teardown = %v, want nil: this bootstrap's ledger names no project behind that REST API", err)
	}
}
