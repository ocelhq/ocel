package projectconfig

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func TestResolveLeavesEveryTierOnItsDefaultEnvSource(t *testing.T) {
	for _, config := range []string{
		`{"slug":"acme"}`,
		`{"slug":"acme","envSource":{"production":"builtin","preview":"builtin","dev":"dotenv"}}`,
	} {
		cfg := mustResolveJSON(t, config)
		if !reflect.DeepEqual(cfg.EnvSource, envsource.DefaultTiers()) {
			t.Errorf("envSource from %s = %+v, want every tier on its default", config, cfg.EnvSource)
		}
	}
}

func TestResolveReadsEachTiersEnvSourceFromTheConfig(t *testing.T) {
	cfg := mustResolveJSON(t, `{"slug":"acme","envSource":{
		"preview":{"infisical":{"project":"p-1","environment":"staging","auth":{"identity":{"identityId":"ident"}}}},
		"dev":{"exec":{"command":["op","run"],"format":"json"}}
	}}`)

	preview := cfg.EnvSource.Preview.Infisical
	if cfg.EnvSource.Production.Kind != envsource.Builtin || preview == nil || preview.Project != "p-1" || preview.Environment != "staging" || cfg.EnvSource.Dev.Kind != envsource.Exec {
		t.Fatalf("envSource = %+v, want production on builtin, preview on its Infisical project and dev on exec", cfg.EnvSource)
	}
}
