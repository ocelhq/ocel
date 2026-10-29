package aws

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func TestAProviderSessionConfiguredWithAWebIdentityIsServedAndSoIsTheNext(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_SESSION_NAME", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(name, "")
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("a-console-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := structpb.NewStruct(map[string]any{"region": "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"first", "second"} {
		server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{Version: "test", New: New}))
		t.Cleanup(server.Close)
		client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
		_, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{
			ProjectDir: t.TempDir(),
			Options:    options,
			WebIdentity: &contractv1.WebIdentity{
				TokenFile:        token,
				IdentityProvider: "arn:aws:iam::123456789012:oidc-provider/console.example/oidc",
				Principal:        "arn:aws:iam::123456789012:role/ocel-production-deploy",
				SessionName:      "ocel-deploy-" + session,
			},
		}})
		if err != nil {
			t.Errorf("the %s Configure with a web identity = %v, want it served: the provider takes the identity into its own credentials and names none in the environment", session, err)
		}
	}
}
