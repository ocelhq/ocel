package deploy

import (
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// appConcurrency bounds how many app-deploy stacks run at once. What AWS sees
// is its product with the Pulumi engine's own optup.Parallel(64) in upStack —
// each admitted app is a separate engine — so four targets ~256 operations in
// flight. That is 4x the upload paths' uploadConcurrency of 64 against a
// tighter budget, not a comparable one: those talk to S3, a data plane built
// for the traffic, while this fans out to Lambda, IAM and CloudFormation,
// whose throttles are far lower and whose rejections fail the user's deploy
// instead of retrying. Four also leaves the common one-to-three-app manifest
// at today's fully concurrent behaviour.
//
// It is a constant, not runtime.NumCPU: the rationed resource is a remote
// service's throttle budget, which has nothing to do with the machine running
// the deploy. It is not configurable either, since a knob here is a knob for
// restoring the unbounded case.
//
// The cap does not make concurrent app deploys safe. pulumiEnv sets no
// PULUMI_HOME, so every LocalWorkspace roots at ~/.pulumi and four concurrent
// creations race the plugin directory and the credentials file exactly as ten
// did. That race is ocelhq-i68t.
const appConcurrency = 4

// runAppStacks runs run for each app, at most appConcurrency at a time.
//
// The group is deliberately not an errgroup.WithContext and run returns
// nothing: a failing app must not cancel the apps already in flight. The
// caller records each app's error in its own slot and decides once every app
// has finished, so a partial failure deploys the same set it does today.
func runAppStacks(apps []*deploymentsv1.ManifestApp, run func(i int, app *deploymentsv1.ManifestApp)) {
	var g errgroup.Group
	g.SetLimit(appConcurrency)
	for i, app := range apps {
		g.Go(func() error {
			run(i, app)
			return nil
		})
	}
	_ = g.Wait() // run returns no error, so Wait cannot.
}
