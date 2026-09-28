package stackrecords_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestARecordNamesTheTierItBelongsTo(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name records.Name
		want environment.Tier
	}{
		{name: records.Name{records.RootEnvSources, "preview", "shop"}, want: environment.TierPreview},
		{name: records.Name{records.RootEnvSourceStatus, "production", "0f3a"}, want: environment.TierProduction},
		{name: records.Name{records.RootEnvSourceDigestKey, "preview"}, want: environment.TierPreview},
		{name: records.Name{records.RootValues, "shop", "preview", "cells"}, want: environment.TierPreview},
	} {
		tier, named := stackrecords.TierOf(c.name)
		if !named || tier != c.want {
			t.Errorf("TierOf(%s) = %q, %v, want %q", c.name, tier, named, c.want)
		}
	}
}
