package deploy

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

const (
	productionVariablesKeyARN = "arn:aws:kms:us-east-1:1234:key/prod-key"
	previewVariablesKeyARN    = "arn:aws:kms:us-east-1:1234:key/preview-key"

	stateTable     = "ocel-state"
	valuesTable    = "ocel-variables"
	valuesTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-variables"
	variablesTier  = "production"
)

func liveConfig() Config {
	return Config{
		VariablesKeyARN:   productionVariablesKeyARN,
		StateTable:        stateTable,
		StateTableARN:     stateTableARN,
		VariablesTable:    valuesTable,
		VariablesTableARN: valuesTableARN,
		Tier:              variablesTier,
	}
}

func TestAppExecutionRoleLiveValues(t *testing.T) {
	t.Run("names the owners behind the app's own live values and no others", func(t *testing.T) {
		t.Parallel()
		bundle := variables.Bundle{Live: []byte(`{"slug":"shop"}`), Referenced: []string{"identity", "platform"}}
		role := appExecutionRole(liveConfig(), "web", nil, nil, bundle, nil, nil, false, nil)
		if !slices.Equal(role.Variables.Referenced, bundle.Referenced) {
			t.Errorf("role referenced = %v, want the app's own owners %v", role.Variables.Referenced, bundle.Referenced)
		}
		other := appExecutionRole(liveConfig(), "admin", nil, nil, variables.Bundle{Live: []byte(`{"slug":"shop"}`)}, nil, nil, false, nil)
		if len(other.Variables.Referenced) != 0 {
			t.Errorf("an app reading no reference took %v, want no partition but its own", other.Variables.Referenced)
		}
	})
	t.Run("takes the table only for an app with live values", func(t *testing.T) {
		t.Parallel()
		cfg := liveConfig()
		cfg.Slug = "shop"
		withLive := appExecutionRole(cfg, "web", nil, nil, variables.Bundle{Live: []byte(`{"slug":"shop"}`)}, nil, nil, false, nil)
		if withLive.Variables.TableARN != valuesTableARN {
			t.Errorf("ValuesTableARN = %q, want the table the values live in", withLive.Variables.TableARN)
		}
		if withLive.Variables.Slug != "shop" || withLive.Variables.Tier != variablesTier {
			t.Errorf("role = %+v, want the partition it may read named", withLive)
		}

		withoutLive := appExecutionRole(cfg, "admin", nil, nil, variables.Bundle{}, nil, nil, false, nil)
		if withoutLive.Variables.TableARN != "" {
			t.Errorf("ValuesTableARN = %q, want no table grant for an app with no live values", withoutLive.Variables.TableARN)
		}
		if withoutLive.Variables.KeyARN != productionVariablesKeyARN {
			t.Errorf("VariablesKeyARN = %q, want the decrypt grant every app keeps", withoutLive.Variables.KeyARN)
		}
	})
}
