package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

func accountWithNoBootstrap(t *testing.T) aws.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>ValidationError</Code><Message>Stack with id ocel does not exist</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
	}))
	t.Cleanup(server.Close)
	return aws.Config{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
}

func TestAnEnvironmentInATierWithNoBootstrapHasNothingToRetire(t *testing.T) {
	t.Parallel()

	p := NewProvider(control.Options{}, nil, accountWithNoBootstrap(t), defaultNamespace)
	live := provider.LiveApps{Slug: "shop", Tier: environment.TierPreview, Env: "pr-1"}

	plan, err := p.PlanRetirement(context.Background(), live, progress.Discard())
	if err != nil || len(plan.Groups) != 0 {
		t.Errorf("PlanRetirement() = %v, %v, want nothing to retire where the tier holds no bootstrap and so no deploy", plan.Groups, err)
	}
	if err := p.Retire(context.Background(), live, progress.Discard()); err != nil {
		t.Errorf("Retire() = %v, want a pass over a tier no deploy could have reached", err)
	}
}
