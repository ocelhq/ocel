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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func main() {
	ctx := context.Background()
	start := time.Now()

	served := readArtifact()

	var bytecodeReady chan *bytecodeResolution
	if !served.executable() {
		bytecodeReady = make(chan *bytecodeResolution, 1)
		go func() {
			resolveCtx, cancel := context.WithTimeout(ctx, bytecodeResolveBudget)
			defer cancel()
			bytecodeReady <- resolveBytecodeResolution(resolveCtx, nodeVersionFromBinary)
		}()
	}

	live, err := resolveLiveValues(ctx)
	if err != nil {
		fatalInit(fmt.Sprintf("failed to read this deployment's live variables: %v", err))
	}
	prefetch := live.start(ctx)

	bakedEnv, err := resolveBakedVarsEnv()
	if err != nil {
		fatalInit(fmt.Sprintf("failed to open this deployment's encrypted variables: %v", err))
	}

	membraneEnv, membraneServed, err := serveMembrane(ctx, live.declaredLinks(), os.Getenv(stateTableEnvVar), os.Getenv(sessionPrefixEnvVar))
	if err != nil {
		fatalInit(fmt.Sprintf("failed to serve this deployment's membrane: %v", err))
	}
	go superviseMembrane(membraneServed)

	child, err := bringUp(ctx, served, live, prefetch, childEnv(bakedEnv, live, membraneEnv), start, bytecodeReady)
	if err != nil {
		fatalInit(err.Error())
	}

	rt := newRuntimeClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	for {
		if err := handleInvocation(ctx, rt, child); err != nil {
			fmt.Fprintf(os.Stderr, "ocel: runtime loop error: %v\n", err)
			os.Exit(1)
		}
	}
}

type spawner func(extraEnv []string, budget time.Duration, onControl func(io.Writer), abandon <-chan struct{}) (*nodeChild, error)

func bringUp(ctx context.Context, served artifact, live *liveValues, prefetch <-chan error, env []string, start time.Time, bytecodeReady <-chan *bytecodeResolution) (child, error) {
	if served.executable() {
		return bringUpChild(func() (*execChild, error) {
			return startExecutable(served.Command, providerkit.InjectedPort, env, spawnBudget(start))
		}, live, prefetch)
	}
	return bringUpChildWithBytecode(ctx, startNode(entrypointPath(served)), live, prefetch, env, start, bytecodeReady, bytecodeEmbedded, bytecodeRehydrate)
}

func spawnBudget(start time.Time) time.Duration {
	budget := startupBudget - time.Since(start)
	if budget < minSpawnBudget {
		return minSpawnBudget
	}
	return budget
}

func bringUpChild[C child](start func() (C, error), live *liveValues, prefetch <-chan error) (C, error) {
	var none C
	child, err := start()
	if err != nil {
		if prefetchErr := live.prefetchError(); prefetchErr != nil {
			return none, fmt.Errorf("failed to resolve this deployment's live variables: %w", prefetchErr)
		}
		return none, err
	}
	if err := live.join(prefetch); err != nil {
		return none, fmt.Errorf("failed to resolve this deployment's live variables: %w", err)
	}
	return child, nil
}

func bringUpNode(spawn spawner, live *liveValues, prefetch <-chan error, env []string, budget time.Duration) (*nodeChild, error) {
	return bringUpChild(func() (*nodeChild, error) {
		child, err := spawn(env, budget, live.attach, live.prefetchFailed())
		if err != nil {
			return nil, fmt.Errorf("failed to start node runtime: %w", err)
		}
		child.live = live
		return child, nil
	}, live, prefetch)
}

func bytecodeRehydrate(ctx context.Context, r *bytecodeResolution) bool {
	return rehydrateBytecodeCache(ctx, r, compileCacheDir)
}

func bytecodeEmbedded(ctx context.Context, r *bytecodeResolution) bool {
	tarPath := embeddedBytecodePath(r.key)
	if tarPath == "" {
		return false
	}
	return embeddedBytecodeCache(ctx, tarPath, compileCacheDir)
}

func bringUpChildWithBytecode(
	ctx context.Context,
	spawn spawner,
	live *liveValues,
	prefetch <-chan error,
	env []string,
	start time.Time,
	bytecodeReady <-chan *bytecodeResolution,
	embedded func(context.Context, *bytecodeResolution) bool,
	rehydrate func(context.Context, *bytecodeResolution) bool,
) (*nodeChild, error) {
	joinWait := time.Until(start.Add(bytecodeResolveBudget))
	if joinWait < 0 {
		joinWait = 0
	}
	var bytecode *bytecodeResolution
	select {
	case bytecode = <-bytecodeReady:
	case <-time.After(joinWait):
		fmt.Fprintln(os.Stderr, "ocel: compile cache resolution did not finish in time; compile cache disabled")
	}

	source := bytecodeSourceNone
	if bytecode != nil {
		rehydrateCtx, cancel := context.WithTimeout(ctx, bytecodeRehydrateBudget)
		switch {
		case embedded(rehydrateCtx, bytecode):
			source = bytecodeSourceEmbedded
		case rehydrate(rehydrateCtx, bytecode):
			source = bytecodeSourceS3
		}
		cancel()
	}

	child, err := bringUpNode(spawn, live, prefetch, env, spawnBudget(start))
	if err != nil {
		return nil, err
	}

	child.bytecodeSource = source
	if bytecode != nil {
		child.bytecodeKey = bytecode.key
		if !child.bytecodeCached() {
			child.bytecode = bytecode.upload(child.flushCompileCache)
		}
	}
	return child, nil
}

func childEnv(bakedEnv []string, live *liveValues, membraneEnv []string) []string {
	env := make([]string, 0, len(bakedEnv)+len(membraneEnv)+1)
	env = append(env, bakedEnv...)
	env = append(env, live.declaredEnv()...)
	return append(env, membraneEnv...)
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
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	os.Exit(1)
}
