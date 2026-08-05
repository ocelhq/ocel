package deploy

import "golang.org/x/sync/errgroup"

// appConcurrency bounds how many app-deploy stacks run at once. The number
// that matters is not this one but its product with the Pulumi engine's own
// optup.Parallel(64) in upStack: each admitted app is a separate engine, so
// the AWS control plane sees appConcurrency * 64 operations in flight. Four
// targets ~256, which is deliberately of the same order as the upload paths'
// uploadConcurrency of 64 rather than a multiple of it — those talk to S3,
// a data plane built for the traffic, whereas this fans out to Lambda, IAM
// and CloudFormation, whose throttles are an order of magnitude tighter and
// whose rejections surface as a failed user deploy rather than a retry.
//
// Four also leaves the common manifest untouched: most projects deploy one
// to three apps, which never reach the cap. Past that, an app stack's wall
// time is dominated by a serial critical path — IAM propagation, then a
// Lambda create and its wait-for-active — that more engines overlap with
// sharply diminishing returns, because each engine is already 64 wide.
//
// It is a constant, not derived from runtime.NumCPU: the resource being
// rationed is a remote service's throttle budget, which has nothing to do
// with the machine running the deploy. It is not configurable either, since
// a knob here is a knob for restoring the unbounded case.
const appConcurrency = 4

// runAppStacks runs run(i) for each of the n app-deploy stacks, at most
// appConcurrency at a time.
//
// The group is deliberately not an errgroup.WithContext and run returns
// nothing: a failing app must not cancel the apps already in flight. The
// caller records each app's own error into its own slot and decides what to
// do with the set once every app has finished, so one app's failure still
// leaves the others' results recorded. Cancelling siblings would change
// which apps get deployed on a partial failure.
func runAppStacks(n int, run func(i int)) {
	var g errgroup.Group
	g.SetLimit(appConcurrency)
	for i := range n {
		g.Go(func() error {
			run(i)
			return nil
		})
	}
	_ = g.Wait() // run returns no error, so Wait cannot.
}
