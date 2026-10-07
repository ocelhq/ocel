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

func TestConfigureNamesTheDirectoryItsFunctionsLoadNextsCacheHandlersFromWithoutCredentials(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{}).WithFacts(func(facts *provider.Facts) {
		facts.NextRuntimeDir = "/var/host/next"
	})
	vendor.Credentials().(*fake.Credentials).Ask("sign in to fake", provider.Question{Finding: "no session", Prompt: "Sign in?"})
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return vendor, nil
		},
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

	configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if got := configured.GetFacts().GetNextRuntimeDir(); got != "/var/host/next" {
		t.Errorf("Configure() facts name the Next runtime directory %q, want the one the provider declares", got)
	}
}

func TestConfigureNamesTheFunctionSizeBudgetTheProviderDeclares(t *testing.T) {
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return fake.NewProvider(fake.Options{}).WithFacts(func(facts *provider.Facts) {
				facts.MaxFunctionBytes = 200 << 20
			}), nil
		},
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

	configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if got := configured.GetFacts().GetMaxFunctionBytes(); got != 200<<20 {
		t.Errorf("Configure() facts name a function size budget of %d bytes, want the one the provider declares", got)
	}
}

func TestConfigureSaysWhetherTheProvidersNextFunctionsRefreshByRequest(t *testing.T) {
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return fake.NewProvider(fake.Options{}).WithFacts(func(facts *provider.Facts) {
				facts.NextRefreshesByRequest = true
			}), nil
		},
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

	configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	if !configured.GetFacts().GetNextRefreshesByRequest() {
		t.Error("Configure() facts say Next functions refresh in the background, want the refresh by request the provider declares")
	}
}

func TestConfigureSaysTheProviderShipsANextServerRuntimeWhenItsHookIsSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*provider.Hooks)
		want bool
	}{
		{"hook set", func(hooks *provider.Hooks) {
			hooks.ReadNextServerRuntime = func(context.Context) (map[string][]byte, error) { return nil, nil }
		}, true},
		{"hook absent", func(*provider.Hooks) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
				Version: "test",
				New: func(context.Context, provider.Settings) (provider.Provider, error) {
					return fake.NewProvider(fake.Options{}).WithHooks(tc.set), nil
				},
			}))
			t.Cleanup(server.Close)
			client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

			configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
			if err != nil {
				t.Fatalf("Configure() error = %v", err)
			}
			if got := configured.GetFacts().GetShipsNextServerRuntime(); got != tc.want {
				t.Errorf("Configure() facts ship a Next server runtime = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfigureSaysTheProviderForwardsPortsWhenItsHookIsSet(t *testing.T) {
	for _, testCase := range []struct {
		name string
		set  func(*provider.Hooks)
		want bool
	}{
		{"hook set", func(hooks *provider.Hooks) {
			hooks.ForwardPorts = func(context.Context, provider.PortForwardRequest) ([]provider.PortForward, error) { return nil, nil }
		}, true},
		{"hook absent", func(*provider.Hooks) {}, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
				Version: "test",
				New: func(context.Context, provider.Settings) (provider.Provider, error) {
					return fake.NewProvider(fake.Options{}).WithHooks(testCase.set), nil
				},
			}))
			t.Cleanup(server.Close)
			client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)

			configured, err := client.Configure(context.Background(), configureInWorkingDir(t))
			if err != nil {
				t.Fatalf("Configure() error = %v", err)
			}
			if got := configured.GetFacts().GetForwardsPorts(); got != testCase.want {
				t.Errorf("Configure() facts forward ports = %v, want %v", got, testCase.want)
			}
		})
	}
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
