package providerserver

import (
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func refuseContainersBuiltFromFunctions(facts provider.Facts, apps []provider.AppEntry) error {
	for _, entry := range apps {
		if !entry.IsBuiltFromFunction() {
			continue
		}
		if facts.BuildsNextContainersFromFunctions && entry.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid, "app %s has no image this provider can run: run `ocel build` again", entry.App)
	}
	return nil
}
