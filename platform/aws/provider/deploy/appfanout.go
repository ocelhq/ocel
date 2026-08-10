package deploy

import (
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const appConcurrency = 4

func runAppStacks(apps []*deploymentsv1.ManifestApp, run func(i int, app *deploymentsv1.ManifestApp)) {
	var g errgroup.Group
	g.SetLimit(appConcurrency)
	for i, app := range apps {
		g.Go(func() error {
			run(i, app)
			return nil
		})
	}
	_ = g.Wait()
}
