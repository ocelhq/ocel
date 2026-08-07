package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if _, err := os.Stat("/opt/ocel/node/entrypoint.mjs"); err != nil {
		fatalInit(fmt.Sprintf("node entrypoint not found: %v", err))
	}
	if _, err := os.Stat("/var/lang/bin/node"); err != nil {
		fatalInit(fmt.Sprintf("node binary not found: %v", err))
	}

	ctx := context.Background()
	start := time.Now()

	// The bytecode cache's identity depends on node's own version, which this
	// process cannot know without running node --version — so that probe, and
	// the AWS config it will need if it succeeds, are kicked off before
	// anything else in main() touches the clock. Both run on their own
	// goroutine and are only joined once bringUpWithBytecode needs the result,
	// which is what lets them overlap the live-values prefetch below and the
	// baked-var decrypts rather than adding their own time to init.
	bytecodeReady := make(chan *bytecodeResolution, 1)
	go func() { bytecodeReady <- resolveBytecodeResolution(ctx, nodeVersionFromBinary) }()

	// Live values are the one class not delivered through the child's
	// environment, so their fetch is the one that need not precede the spawn.
	// It is started first and joined last: the read and its decrypts run beside
	// the exec and node's own boot, and what they cost is only ever visible if
	// node wins that race. Building the client is not deferred with them —
	// failing to configure one is a fact about this deployment, not about the
	// store, and it should be reported as itself.
	live, err := resolveLiveValues(ctx)
	if err != nil {
		fatalInit(fmt.Sprintf("failed to read this deployment's live variables: %v", err))
	}
	prefetch := live.start(ctx)

	// Encrypted-baked values are opened here for the same reason: they travel
	// in the child's environment. Both the ciphertext and its key already rode
	// in with the deployment, so this is local decryption rather than a fetch.
	bakedEnv, err := resolveBakedVarsEnv()
	if err != nil {
		fatalInit(fmt.Sprintf("failed to open this deployment's encrypted variables: %v", err))
	}

	membrane, err := bringUpWithBytecode(ctx, startNode, live, prefetch, childEnv(bakedEnv, live), start, bytecodeReady, bytecodeRehydrate)
	if err != nil {
		// Must report init failure BEFORE we start polling the Runtime API.
		fatalInit(err.Error())
	}

	rt := newRuntimeClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	for {
		if err := handleInvocation(ctx, rt, membrane); err != nil {
			// A Runtime API failure is fatal to the loop; the sandbox is recycled.
			fmt.Fprintf(os.Stderr, "ocel: runtime loop error: %v\n", err)
			os.Exit(1)
		}
	}
}

// spawner is startNode as bringUp uses it, so the order the two are put in can
// be tested without a node binary anywhere near it.
type spawner func(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*Membrane, error)

// bringUp spawns the child and settles the prefetch that ran beside it,
// returning the message init must report if either fails.
//
// The order is the property. The spawn is started before the prefetch is
// waited on, so the fetch and the decrypts overlap the exec and node's own boot
// rather than preceding them. The join is after, which is the last moment a
// failure can still be reported as an init error: past here the Runtime API
// only hears about invocations, and a function that could not resolve a value
// it declared must not come up, or the value is read at the point of use as one
// that was never required.
//
// Failing to spawn is usually node's own fault, but for a function with live
// keys it is also what a failed prefetch looks like from here: node holds its
// import until the push arrives, so a store it cannot read surfaces as a
// startup timeout that names nothing. Where the prefetch is the reason, the
// store's error is the diagnosis and it is what gets reported.
func bringUp(spawn spawner, live *liveValues, prefetch <-chan error, env []string, budget time.Duration) (*Membrane, error) {
	membrane, err := spawn(env, budget, live.attach, live.prefetchFailed())
	if err != nil {
		if prefetchErr := live.prefetchError(); prefetchErr != nil {
			return nil, fmt.Errorf("failed to resolve this deployment's live variables: %w", prefetchErr)
		}
		return nil, fmt.Errorf("failed to start node runtime: %w", err)
	}
	membrane.live = live

	if err := live.join(prefetch); err != nil {
		return nil, fmt.Errorf("failed to resolve this deployment's live variables: %w", err)
	}
	return membrane, nil
}

// bytecodeRehydrate is bringUpWithBytecode's default rehydrate dependency,
// closing over the one piece rehydrateBytecodeCache cannot know for itself:
// where node is told to keep its compile cache.
func bytecodeRehydrate(ctx context.Context, r *bytecodeResolution) bool {
	return rehydrateBytecodeCache(ctx, r, compileCacheDir)
}

// bringUpWithBytecode wraps bringUp with the bytecode cache's two legs,
// joining and rehydrating before bringUp is called and attaching the upload
// leg after it returns.
//
// The join and the rehydrate attempt both happen before budget is computed,
// which is what carves rehydration's cost out of startupBudget rather than
// adding it on top: bringUp's budget argument is startupBudget minus
// time.Since(start), and start was captured before any of this ran, so
// whatever rehydration spent is already reflected in that subtraction by the
// time bringUp sees it.
//
// A hit disables the upload leg entirely rather than merely skipping its
// PUT: it proves the object already exists, so nothing membrane.bytecode
// would do could ever matter, and leaving it nil is what keeps
// uploadBytecodeCacheOnce from flushing, walking or archiving for an outcome
// already decided.
//
// bytecodeReady and rehydrate are dependencies for the same reason spawn is:
// the whole sequence, including its budget arithmetic, is exercisable with
// fakes and no node binary, AWS client or environment in reach.
func bringUpWithBytecode(
	ctx context.Context,
	spawn spawner,
	live *liveValues,
	prefetch <-chan error,
	env []string,
	start time.Time,
	bytecodeReady <-chan *bytecodeResolution,
	rehydrate func(context.Context, *bytecodeResolution) bool,
) (*Membrane, error) {
	bytecode := <-bytecodeReady
	var hit bool
	if bytecode != nil {
		hit = rehydrate(ctx, bytecode)
	}

	membrane, err := bringUp(spawn, live, prefetch, env, startupBudget-time.Since(start))
	if err != nil {
		return nil, err
	}

	if bytecode != nil && !hit {
		membrane.bytecode = bytecode.upload(membrane.flushCompileCache)
	}
	return membrane, nil
}

// childEnv is everything node is told at exec. The baked class travels in it as
// values; the live class travels as key names only, because a live value reaches
// node down the control socket and must never be in this process's environment
// where anything reading the environment would find it.
func childEnv(bakedEnv []string, live *liveValues) []string {
	env := make([]string, 0, len(bakedEnv)+1)
	env = append(env, bakedEnv...)
	return append(env, live.declaredEnv()...)
}

func fatalInit(msg string) {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api != "" {
		url := "http://" + api + "/2018-06-01/runtime/init/error"
		payload, _ := json.Marshal(map[string]string{
			"errorMessage": msg,
			"errorType":    "Ocel.InitError",
		})
		req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
		req.Header.Set("Lambda-Runtime-Function-Error-Type", "Ocel.InitError")
		http.DefaultClient.Do(req)
	}
	os.Exit(1)
}
