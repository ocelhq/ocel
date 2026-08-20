package deploy

import (
	"sync"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const appConcurrency = 4

func runAppStacks(apps []*contractv1.ManifestApp, run func(i int, app *contractv1.ManifestApp)) {
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
