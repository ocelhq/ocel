package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/manifestwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	envProduction = "production"
	envPreview    = "preview"

	profileLight    = "light"
	profileModerate = "moderate"
	profileHeavy    = "heavy"
)

var profiles = []string{profileLight, profileModerate, profileHeavy}

type Options struct {
	Env     string
	Profile string
	Usage   string
}

func Run(ctx context.Context, deps cmddeps.Deps, cwd string, opts Options, stdout, stderr io.Writer) error {
	env, err := environmentOf(opts.Env)
	if err != nil {
		return err
	}
	profile, err := profileOf(opts.Profile)
	if err != nil {
		return err
	}
	overrides, err := usageFile(opts.Usage)
	if err != nil {
		return err
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		manifest, err := scanManifest(ctx, deps, cfg, env, stderr)
		if err != nil {
			return err
		}
		client, err := runner.Client()
		if err != nil {
			return err
		}
		set, err := client.Shape(ctx, &contractv1.ShapeRequest{
			Manifest:    manifest,
			Environment: env,
			Edge:        edgewire.Selection(cfg),
		})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return predates(runner.Name())
			}
			return err
		}
		pricer, err := runner.Cost()
		if err != nil {
			return err
		}
		estimates := make(map[string]*costv1.Estimate, len(profiles))
		for _, held := range profiles {
			estimate, err := pricer.Price(ctx, &costv1.PriceRequest{
				Resources: set,
				Usage:     &costv1.Usage{Profile: held, Resources: overrides},
			})
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					return predates(runner.Name())
				}
				return err
			}
			estimates[held] = estimate
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeJSON(stdout, set, estimates[profile])
		}
		return render(stdout, cfg.Slug, set, estimates, profile)
	})
}

func predates(pkg string) error {
	return fmt.Errorf("%s cannot say what a deploy would cost; it predates the estimate. Upgrade the provider pinned in this project and try again", pkg)
}

func environmentOf(name string) (*environmentv1.Environment, error) {
	switch name {
	case "", envProduction:
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}, nil
	case envPreview:
		return &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			Identity:  envPreview,
		}, nil
	}
	return nil, fmt.Errorf("the environment to price is %s or %s, not %q", envProduction, envPreview, name)
}

func profileOf(name string) (string, error) {
	if name == "" {
		return profileModerate, nil
	}
	for _, held := range profiles {
		if held == name {
			return held, nil
		}
	}
	return "", fmt.Errorf("the usage profile is %s, %s or %s, not %q", profileLight, profileModerate, profileHeavy, name)
}

func usageFile(path string) (map[string]*structpb.Struct, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the usage file: %w", err)
	}
	var byResource map[string]map[string]any
	if err := yaml.Unmarshal(raw, &byResource); err != nil {
		return nil, fmt.Errorf("the usage file %s is neither YAML nor JSON keyed by resource id: %w", path, err)
	}
	overrides := make(map[string]*structpb.Struct, len(byResource))
	for id, quantities := range byResource {
		held, err := structpb.NewStruct(quantities)
		if err != nil {
			return nil, fmt.Errorf("the usage file %s sets %s to something that is not a quantity: %w", path, id, err)
		}
		overrides[id] = held
	}
	return overrides, nil
}

type unread struct{}

func (unread) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (unread) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return map[envgate.Cell]string{}, nil
}

const unbuiltDigest = "0000000000000000000000000000000000000000000000000000000000000000"

func scanManifest(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, env *environmentv1.Environment, out io.Writer) (*contractv1.Manifest, error) {
	gate := envgate.New(unread{}, envwire.Scope(cfg, env.GetTier() == environmentv1.Tier_TIER_PREVIEW, ""))
	resources, err := deps.CollectDeclarations(ctx, cfg, gate, out, out)
	if err != nil {
		return nil, err
	}
	functions, err := deps.CollectAppFunctions(cfg.Dir)
	if errors.Is(err, appbuilder.ErrNoBuildOutput) {
		functions = unbuiltFunctions(cfg)
	} else if err != nil {
		return nil, err
	}
	return manifestbuilder.Build(cfg.Slug, cfg.Domains, scannedApps(cfg), string(providerkit.ComputeServerless), manifestwire.Declarations(cfg.Dir, resources), manifestwire.Bindings(cfg.Bindings), functions, nil)
}

func scannedApps(cfg *projectconfig.Config) []manifestbuilder.App {
	apps := make([]manifestbuilder.App, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		app := manifestbuilder.App{
			Name:    a.Name,
			Runtime: manifestwire.Runtime(a.Runtime),
			Compute: a.Compute,
			Domains: a.Domains,
			Folder:  a.Folder,
		}
		if a.Compute == string(providerkit.ComputeContainer) {
			app.Image = cfg.Slug + "/" + a.Name + "@sha256:" + unbuiltDigest
		}
		apps = append(apps, app)
	}
	return apps
}

func unbuiltFunctions(cfg *projectconfig.Config) []manifestbuilder.Function {
	if len(cfg.Apps) == 0 {
		return []manifestbuilder.Function{{Route: cfg.Slug, App: cfg.Slug}}
	}
	functions := make([]manifestbuilder.Function, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		if a.Compute == string(providerkit.ComputeContainer) {
			continue
		}
		functions = append(functions, manifestbuilder.Function{Route: a.Name, App: a.Name, Runtime: manifestwire.Runtime(a.Runtime)})
	}
	return functions
}

func writeJSON(stdout io.Writer, set *costv1.ResourceSet, estimate *costv1.Estimate) error {
	resources, err := protojson.Marshal(set)
	if err != nil {
		return err
	}
	priced, err := protojson.Marshal(estimate)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(map[string]json.RawMessage{"resources": resources, "estimate": priced})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	return err
}
