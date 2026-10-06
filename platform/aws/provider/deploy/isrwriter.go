package deploy

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func checkISRWriterAgrees(tier environment.Tier, stores ObjectStores, w cloudflare.ISRWriter) error {
	adopted, writer := isrEntriesAdopted(stores), w.IsConfigured()
	switch {
	case adopted && !writer:
		return fmt.Errorf("this bootstrap adopted an edge cache store but no ISR writer to write into it, so this build could not revalidate anything it cached; re-run `%s`", provider.BootstrapCommand(tier))
	case !adopted && writer:
		return fmt.Errorf("this bootstrap adopted an ISR writer but no edge cache store, so entries would be written where nothing reads them; re-run `%s`", provider.BootstrapCommand(tier))
	}
	return nil
}

func seedISRWriter(ctx context.Context, w cloudflare.ISRWriter, app string, cache *isrConfig) error {
	if err := w.Initialize(ctx, cache.Prefix); err != nil {
		return fmt.Errorf("seed the isr writer for %s: %w", app, err)
	}
	return nil
}
