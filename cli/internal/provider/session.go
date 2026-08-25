package provider

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

func Drive(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer, fn func(*Runner) error) error {
	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}

	desc, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	binPath, err := Locate(ctx, cfg.Dir, desc.Package)
	if err != nil {
		return fmt.Errorf("locate provider binary: %w", err)
	}

	env, err := workerBundleEnv(cfg.Dir)
	if err != nil {
		return err
	}

	sessionConfig, err := providerConfig(desc)
	if err != nil {
		return err
	}

	runner, err := Spawn(ctx, Config{
		BinaryPath:      binPath,
		Stdout:          stdout,
		Stderr:          stderr,
		Env:             env,
		Provider:        sessionConfig,
		ProviderPackage: desc.Package,
	})
	if err != nil {
		return fmt.Errorf("spawn provider: %w", err)
	}
	defer runner.Close()

	if err := runner.Ready(ctx); err != nil {
		return err
	}
	return fn(runner)
}

func providerConfig(desc *projectconfig.ProviderDescriptor) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{}
	if desc == nil || len(desc.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(desc.Options, options); err != nil {
		return nil, fmt.Errorf("%s configures %s with options that are not a JSON object: %w", projectconfig.ConfigFileName, desc.Package, err)
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
