package gcp_test

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
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func TestAProviderSessionConfiguredWithAWebIdentityIsServedAndSoIsTheNext(t *testing.T) {
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GHA_CREDS_PATH", "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "GOOGLE_CREDENTIALS", "GOOGLE_CLOUD_KEYFILE_JSON", "GCLOUD_KEYFILE_JSON", "GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT"} {
		t.Setenv(name, "")
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("a-console-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := structpb.NewStruct(map[string]any{"project": "acme-prod", "region": "europe-west1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"first", "second"} {
		server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{Version: "test", New: gcp.New}))
		t.Cleanup(server.Close)
		client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
		_, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{
			ProjectDir: t.TempDir(),
			Options:    options,
			WebIdentity: &contractv1.WebIdentity{
				TokenFile:        token,
				IdentityProvider: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/ocel/providers/console",
				Principal:        "ocel-deploy-production@acme-prod.iam.gserviceaccount.com",
				SessionName:      "ocel-deploy-" + session,
			},
		}})
		if err != nil {
			t.Errorf("the %s Configure with a web identity = %v, want it served: the provider takes the identity into its own credentials and names none in the environment", session, err)
		}
	}
}
