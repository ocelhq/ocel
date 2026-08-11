package deploy

import (
	"sync"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const appConcurrency = 4

func runAppStacks(apps []*deploymentsv1.ManifestApp, run func(i int, app *deploymentsv1.ManifestApp)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, appConcurrency)
	for i, app := range apps {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			run(i, app)
		}()
	}
	wg.Wait()
}
