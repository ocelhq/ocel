package variables

import (
	"encoding/base64"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	valuesTable    = "ocel-variables"
	valuesTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-variables"
	variablesTier  = "production"
)

func liveStore() Store {
	return Store{KeyARN: productionVariablesKeyARN, Table: valuesTable, Tier: variablesTier}
}

func partitionOf(t *testing.T, slug string) string {
	t.Helper()
	return ValuePartition(slug, variablesTier)
}

func scopedVariable(key, folder string, class resourcesv1.VariableClass) *contractv1.ManifestVariable {
	return &contractv1.ManifestVariable{Key: key, Class: class, Folder: folder}
}

func previewOf(store Store, identity string) Store {
	store.Tier, store.Env = environment.TierPreview, identity
	return store
}

func decodeEnvelope(t *testing.T, envelope string) []byte {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("envelope is not base64: %v", err)
	}
	return key
}

func appValuesOf(app *contractv1.ManifestApp) provider.AppValues {
	plain, sensitive := map[string]string{}, map[string]string{}
	for _, v := range app.GetVariables() {
		switch v.GetClass() {
		case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
			plain[v.GetKey()] = v.GetValue()
		case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
			sensitive[v.GetKey()] = v.GetValue()
		}
	}
	return provider.AppValues{Plain: plain, Sensitive: sensitive, Folder: app.GetFolder()}
}
