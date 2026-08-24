package deploy

import (
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func previewAppNames(apps []*contractv1.ManifestApp) string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := strings.ToLower(strings.TrimSpace(app.GetName())); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}
