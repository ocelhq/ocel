package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestPreviewAppNames(t *testing.T) {
	t.Parallel()

	t.Run("is a lowercased comma separated list", func(t *testing.T) {
		t.Parallel()
		got := previewAppNames([]*deploymentsv1.ManifestApp{{Name: "Web"}, {Name: " admin "}, {Name: ""}})
		if got != "web,admin" {
			t.Errorf("previewAppNames = %q, want web,admin", got)
		}
	})

	t.Run("no app names nothing", func(t *testing.T) {
		t.Parallel()
		if got := previewAppNames(nil); got != "" {
			t.Errorf("previewAppNames(nil) = %q, want empty", got)
		}
	})
}
