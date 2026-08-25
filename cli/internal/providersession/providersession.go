package providersession

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

var ReadyTimeout time.Duration

type Locator func(ctx context.Context, projectDir, providerPackage string) (string, error)

func Drive(ctx context.Context, locate Locator, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, stdout, stderr io.Writer, drive func(*providerrunner.Runner) error) error {
	binPath, err := locate(ctx, cfg.Dir, provider.Package)
	if err != nil {
		return fmt.Errorf("locate provider binary: %w", err)
	}

	env, err := workerBundleEnv(cfg.Dir)
	if err != nil {
		return err
	}

	sessionConfig, err := providerConfig(provider)
	if err != nil {
		return err
	}

	runner, err := providerrunner.Spawn(ctx, providerrunner.Config{
		BinaryPath:      binPath,
		Stdout:          stdout,
		Stderr:          stderr,
		Env:             env,
		Provider:        sessionConfig,
		ProviderPackage: provider.Package,
		ReadyTimeout:    ReadyTimeout,
	})
	if err != nil {
		return fmt.Errorf("spawn provider: %w", err)
	}
	defer runner.Close()

	if err := runner.Ready(ctx); err != nil {
		return err
	}
	return drive(runner)
}

func Fail(ctx context.Context, ui *deployui.Session, err error) error {
	if ctx.Err() != nil {
		ui.Cancel()
		return &exitsig.ExitError{Code: exitsig.InterruptCode}
	}
	ui.Fail(err)
	return &exitsig.ExitError{Code: 1}
}

func providerConfig(provider *projectconfig.ProviderDescriptor) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{}
	if provider == nil || len(provider.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(provider.Options, options); err != nil {
		return nil, fmt.Errorf("%s configures %s with options that are not a JSON object: %w", projectconfig.ConfigFileName, provider.Package, err)
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
