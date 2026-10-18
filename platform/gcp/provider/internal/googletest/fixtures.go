package googletest

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func ProductionRef(t *testing.T) provider.StackRef {
	t.Helper()
	stack, err := naming.ParseStackName("prod--infra")
	if err != nil {
		t.Fatal(err)
	}
	return provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack}
}

func JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

var BothTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func IdsOf[T interface{ ID() string }](items []T) []string {
	ids := make([]string, 0, len(items))
	for _, each := range items {
		ids = append(ids, each.ID())
	}
	return ids
}
