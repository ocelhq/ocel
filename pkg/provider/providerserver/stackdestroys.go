package providerserver

import "golang.org/x/sync/errgroup"

const stackDestroyConcurrency = 8

func destroyAppsThenInfra[S any](apps, infra []S, destroy func(S) error) []error {
	failures := make([]error, len(apps)+len(infra))
	var group errgroup.Group
	group.SetLimit(stackDestroyConcurrency)
	for i, app := range apps {
		group.Go(func() error {
			failures[i] = destroy(app)
			return nil
		})
	}
	_ = group.Wait()
	for i, stack := range infra {
		failures[len(apps)+i] = destroy(stack)
	}
	return failures
}
