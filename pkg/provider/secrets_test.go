package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestWithoutSecretsDropsEveryPropertyAnAppAuthenticatesWith(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		binding provider.Binding
		dropped []string
		kept    []string
	}{
		{"postgres", provider.Binding{Type: provider.BindingPostgres, Properties: map[string]string{
			provider.PropertyHost: "h", provider.PropertyPassword: "p", provider.PropertyURL: "postgres://u:p@h/d"}},
			[]string{provider.PropertyPassword, provider.PropertyURL}, []string{provider.PropertyHost}},
		{"kv", provider.Binding{Type: provider.BindingKV, Properties: map[string]string{
			provider.PropertyHost: "h", provider.PropertyPassword: "p"}},
			[]string{provider.PropertyPassword}, []string{provider.PropertyHost}},
		{"realtime", provider.Binding{Type: provider.BindingRealtime, Properties: map[string]string{
			provider.PropertyURL: "/s", provider.PropertySigningKey: "k", provider.PropertyVerifyKey: "v"}},
			[]string{provider.PropertySigningKey}, []string{provider.PropertyURL, provider.PropertyVerifyKey}},
		{"custom", provider.Binding{Type: provider.BindingCustom, Properties: map[string]string{"token": "t"}},
			[]string{"token"}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if len(test.binding.Properties) != len(test.dropped)+len(test.kept) {
				t.Fatal("test table lists a property neither dropped nor kept")
			}
			got := test.binding.WithoutSecrets()
			for _, name := range test.dropped {
				if _, ok := got.Properties[name]; ok {
					t.Errorf("WithoutSecrets() kept %q, want it dropped", name)
				}
			}
			for _, name := range test.kept {
				if got.Properties[name] == "" {
					t.Errorf("WithoutSecrets() dropped %q, want it kept", name)
				}
			}
		})
	}
}
