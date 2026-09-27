package stackrecords_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestARecordNamesTheClassItBelongsTo(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name records.Name
		want edge.Class
	}{
		{name: records.Name{records.RootEnvSources, "preview", "shop"}, want: edge.ClassPreview},
		{name: records.Name{records.RootEnvSourceStatus, "production", "0f3a"}, want: edge.ClassProduction},
		{name: records.Name{records.RootValues, "shop", "preview", "cells"}, want: edge.ClassPreview},
	} {
		class, named := stackrecords.ClassOf(c.name)
		if !named || class != c.want {
			t.Errorf("ClassOf(%s) = %q, %v, want %q", c.name, class, named, c.want)
		}
	}
}
