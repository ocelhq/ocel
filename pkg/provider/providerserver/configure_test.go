package providerserver_test

import (
	"context"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func TestConfigureHandsTheProviderTheTransformModulesTheProjectLists(t *testing.T) {
	var seen provider.Settings
	config := providerserver.Config{
		Version: "test",
		New: func(_ context.Context, settings provider.Settings) (provider.Provider, error) {
			seen = settings
			return fake.NewProvider(fake.Options{}), nil
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	modules := []string{"./transforms/network.transform.ts", "./transforms/tags.transform.ts"}
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{Transforms: modules, ProjectDir: t.TempDir()},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	if !slices.Equal(seen.Transforms, modules) {
		t.Errorf("Transforms = %v, want %v", seen.Transforms, modules)
	}
}

func TestConfigureHandsTheProviderTheProjectItServes(t *testing.T) {
	var seen provider.Settings
	config := providerserver.Config{
		Version: "test",
		New: func(_ context.Context, settings provider.Settings) (provider.Provider, error) {
			seen = settings
			return fake.NewProvider(fake.Options{}), nil
		},
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{Slug: "shop", ProjectDir: t.TempDir()},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	if seen.Slug != "shop" {
		t.Errorf("Slug = %q, want shop", seen.Slug)
	}
}

func workingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func workingOutputRoot(t *testing.T) string {
	t.Helper()
	root, err := buildoutput.Root(workingDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func configureInWorkingDir(t *testing.T) *contractv1.ConfigureRequest {
	t.Helper()
	return &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{ProjectDir: workingDir(t)}}
}

func TestConfigureRefusesAProjectDirectoryThatIsNotAbsolute(t *testing.T) {
	for name, dir := range map[string]string{"none": "", "relative": "shop"} {
		t.Run(name, func(t *testing.T) {
			built := false
			server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
				Version: "test",
				New: func(context.Context, provider.Settings) (provider.Provider, error) {
					built = true
					return fake.NewProvider(fake.Options{}), nil
				},
			}))
			t.Cleanup(server.Close)
			client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

			_, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{ProjectDir: dir}})
			if err == nil || !strings.Contains(err.Error(), "project") {
				t.Fatalf("Configure(%q) = %v, want the project directory refused", dir, err)
			}
			if built {
				t.Error("the provider was built for a project directory the server refused")
			}
		})
	}
}
