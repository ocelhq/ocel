package projectconfig

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
)

func TestResolveLeavesEveryTierOnItsDefaultSource(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.EnvSource.Production.Kind != envsource.Builtin || cfg.EnvSource.Preview.Kind != envsource.Builtin {
		t.Fatalf("deployed tiers = %+v, want builtin", cfg.EnvSource)
	}
	if cfg.EnvSource.Dev.Kind != envsource.Dotenv {
		t.Fatalf("dev = %+v, want dotenv", cfg.EnvSource.Dev)
	}
}

func TestResolveFillsAnInfisicalSourceWithItsDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme","envSource":{
		"production":{"infisical":{"project":"p-1","environment":"prod","auth":{"universal":{"clientId":{"var":"ID"},"clientSecret":{"var":"SECRET"}}}}},
		"preview":{"infisical":{"project":"p-1","environment":"staging","path":"/acme/","host":"https://infisical.example.com/","write":"missing","auth":{"aws":{"identityId":"ident"}}}},
		"dev":{"exec":{"command":["op","run","{folder}"],"format":"json"}}
	}}`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	production := cfg.EnvSource.Production
	if production.Kind != envsource.Infisical || production.Infisical == nil {
		t.Fatalf("production = %+v", production)
	}
	want := envsource.InfisicalOptions{
		Project:     "p-1",
		Environment: "prod",
		Path:        "/",
		Host:        envsource.InfisicalCloud,
		Write:       envsource.WriteNever,
		Auth:        envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: "ID", ClientSecretVar: "SECRET"},
	}
	if *production.Infisical != want {
		t.Fatalf("production infisical = %+v, want %+v", *production.Infisical, want)
	}
	preview := cfg.EnvSource.Preview.Infisical
	if preview.Path != "/acme" || preview.Host != "https://infisical.example.com" || preview.Write != envsource.WriteMissing ||
		preview.Auth != (envsource.InfisicalAuth{Method: envsource.AuthAWS, IdentityID: "ident"}) {
		t.Fatalf("preview infisical = %+v", preview)
	}
	dev := cfg.EnvSource.Dev
	if dev.Kind != envsource.Exec || dev.Exec == nil || !slices.Equal(dev.Exec.Command, []string{"op", "run", "{folder}"}) || dev.Exec.Format != envsource.FormatJSON {
		t.Fatalf("dev = %+v", dev)
	}
}
