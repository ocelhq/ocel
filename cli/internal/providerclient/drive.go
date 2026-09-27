package providerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func Drive(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer, trust Trust, fn func(*Runner) error) error {
	return drive(ctx, cfg, stdout, stderr, trust, PinToLock, fn)
}

func DriveDry(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer, trust Trust, fn func(*Runner) error) error {
	return drive(ctx, cfg, stdout, stderr, trust, PinInMemory, fn)
}

func drive(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer, trust Trust, mode Pinning, fn func(*Runner) error) error {
	config, err := prepareLaunch(ctx, cfg, mode)
	if err != nil {
		return err
	}
	config.Stdout, config.Stderr = stdout, stderr

	return driveTrusting(ctx, trust, func() error {
		runner, err := Spawn(ctx, config)
		if err != nil {
			return fmt.Errorf("spawn provider: %w", err)
		}
		defer runner.Close()

		if err := runner.Ready(ctx); err != nil {
			return err
		}
		return fn(runner)
	})
}

func prepareLaunch(ctx context.Context, cfg *projectconfig.Config, pins Pinning) (Config, error) {
	if err := node.Ensure(cfg.Dir); err != nil {
		return Config{}, err
	}
	desc, err := cfg.RequireProvider()
	if err != nil {
		return Config{}, err
	}
	binPath, err := locateProvider(ctx, cfg.Dir, desc.ID, pins)
	if err != nil {
		return Config{}, err
	}
	env, err := workerBundleEnv(cfg.Dir)
	if err != nil {
		return Config{}, err
	}
	providerConfig, err := providerConfig(cfg, desc)
	if err != nil {
		return Config{}, err
	}
	return Config{
		BinaryPath:     binPath,
		Env:            env,
		ProviderConfig: providerConfig,
		ProviderName:   desc.ID,
	}, nil
}

func providerConfig(cfg *projectconfig.Config, desc *projectconfig.ProviderDescriptor) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{Transforms: cfg.Transforms, Slug: cfg.Slug}
	if desc == nil || len(desc.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(desc.Options, options); err != nil {
		return nil, fmt.Errorf("the config configures provider %q with options that are not an object: %w", desc.ID, err)
	}
	config.Options = options
	return config, nil
}

func workerBundleEnv(projectDir string) ([]string, error) {
	bundles, err := json.Marshal(node.WorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal worker bundles: %w", err)
	}
	store, err := json.Marshal(node.StoreWorkerBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal store worker bundles: %w", err)
	}
	isrWriter, err := json.Marshal(node.ISRWriterBundles(projectDir))
	if err != nil {
		return nil, fmt.Errorf("marshal isr writer worker bundles: %w", err)
	}
	return append(os.Environ(),
		"OCEL_WORKER_BUNDLES="+string(bundles),
		"OCEL_STORE_WORKER_BUNDLES="+string(store),
		"OCEL_ISR_WRITER_WORKER_BUNDLES="+string(isrWriter),
	), nil
}
