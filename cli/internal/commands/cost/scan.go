package cost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/source"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	command = "ocel cost scan"

	envProduction = "production"
	envPreview    = "preview"

	stdinPath = "-"

	unbuiltAssumption = "nothing is built, so each serverless app is priced as one function; run `ocel build` first to price the functions the build produces"
)

func profileNames() []string {
	names := make([]string, 0, len(pricing.Profiles()))
	for _, profile := range pricing.Profiles() {
		names = append(names, pricing.ProfileName(profile))
	}
	return names
}

type Options struct {
	Env     string
	Profile string
	Usage   string
	From    string
	Against string
	Diff    bool
	Offline bool
}

type scanned struct {
	label       string
	proposed    *costv1.ResourceSet
	current     *costv1.ResourceSet
	assumptions []string
}

func (s scanned) compares() bool { return s.current != nil }

func Run(ctx context.Context, dependencies Dependencies, cwd string, opts Options, stdin io.Reader, stdout io.Writer) (err error) {
	if err := refuseConflictingFlags(opts); err != nil {
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
	var current *costv1.ResourceSet
	var documentAssumptions []string
	if opts.Against != "" {
		against, err := readDocument(opts.Against, stdin)
		if err != nil {
			return err
		}
		current, documentAssumptions = against.Set, against.Assumptions
	}

	if opts.From == "" {
		env, err := environmentOf(opts.Env)
		if err != nil {
			return err
		}
		cfg, err := dependencies.LoadProject(ctx, cwd)
		if err != nil {
			return err
		}
		return dependencies.WithProvider(ctx, cfg, command, commands.OpenOptions{}, func(ctx context.Context, p commands.ProviderRun) error {
			p.Check.End(nil)
			if !p.Provider.Facts().GetTakesInventory() {
				return fmt.Errorf("%s takes no inventory; pass --from a plan or state file", p.Provider.Name())
			}
			ctx, pricingSpan := p.Run.Phase(progressv1.Phase_PHASE_PLAN).Child(ctx, cfg.Slug, progress.Pricing.Title("what a deploy would provision"))
			s := scanned{label: cfg.Slug, current: current}
			var err error
			s.proposed, s.assumptions, err = projectInventory(ctx, dependencies, p.Provider, cfg, env, pricingSpan.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED))
			s.assumptions = append(s.assumptions, documentAssumptions...)
			if err == nil {
				err = report(ctx, dependencies, s, opts, profile, overrides, stdout)
			}
			pricingSpan.End(err)
			return err
		})
	}

	from, err := readDocument(opts.From, stdin)
	if err != nil {
		return err
	}
	s := scanned{label: documentLabel(opts.From), proposed: from.Set, current: current, assumptions: mergeNotes(from.Assumptions, documentAssumptions)}
	if opts.Diff {
		if from.Prior == nil {
			return fmt.Errorf("%s carries no prior state to compare with; pass --against the current state", s.label)
		}
		s.current = from.Prior
	}
	ctx, running, err := dependencies.Events.Begin(ctx, command, "")
	if err != nil {
		return err
	}
	defer running.End(&err)
	ctx, pricingSpan := running.Phase(progressv1.Phase_PHASE_PLAN).Child(ctx, s.label, progress.Pricing.Title(s.label))
	err = report(ctx, dependencies, s, opts, profile, overrides, stdout)
	pricingSpan.End(err)
	return err
}

func refuseConflictingFlags(opts Options) error {
	switch {
	case opts.From != "" && opts.Env != "":
		return errors.New("--env picks the project environment to price, so it cannot go with --from")
	case opts.Diff && opts.Against != "":
		return errors.New("--diff compares with the prior state in --from, so it cannot go with --against")
	case opts.Diff && opts.From == "":
		return errors.New("--diff compares with the prior state a plan carries; pass --from the plan")
	case opts.From == stdinPath && opts.Against == stdinPath:
		return errors.New("only one of --from and --against can read stdin")
	}
	return nil
}

func readDocument(path string, stdin io.Reader) (source.Document, error) {
	var raw []byte
	var err error
	if path == stdinPath {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return source.Document{}, fmt.Errorf("read %s: %w", documentLabel(path), err)
	}
	document, err := source.Read(raw)
	if err != nil {
		return source.Document{}, fmt.Errorf("read %s: %w", documentLabel(path), err)
	}
	return document, nil
}

func documentLabel(path string) string {
	if path == stdinPath {
		return "stdin"
	}
	return filepath.Base(path)
}

func report(ctx context.Context, dependencies Dependencies, s scanned, opts Options, profile costv1.Profile, overrides map[string]*structpb.Struct, stdout io.Writer) error {
	if s.compares() && s.current.GetSource() != s.proposed.GetSource() {
		return fmt.Errorf("the current set comes from %s and the proposed one from %s, whose resource ids never match; compare two documents from the same tool", s.current.GetSource(), s.proposed.GetSource())
	}
	catalog, err := dependencies.OpenPriceCatalog(opts.Offline)
	if err != nil {
		return err
	}
	sets := []*costv1.ResourceSet{s.proposed}
	if s.compares() {
		sets = append(sets, s.current)
	}
	estimates, err := estimateSets(ctx, catalog, dependencies.Prices.Table, overrides, opts.Offline, sets...)
	if err != nil {
		return err
	}
	isJSON := dependencies.Presentation(stdout).Format == terminal.FormatJSON
	if !s.compares() {
		if isJSON {
			return terminal.WriteResultJSON(stdout, &resultv1.CostScanResult{Resources: s.proposed, Estimate: estimates[0][profile], Assumptions: s.assumptions})
		}
		return render(stdout, s.label, s.proposed, estimates[0], profile, s.assumptions)
	}
	diffs := make(map[costv1.Profile]*costv1.EstimateDiff, len(pricing.Profiles()))
	for _, each := range pricing.Profiles() {
		diffs[each] = pricing.Diff(s.current, s.proposed, estimates[1][each], estimates[0][each])
	}
	if isJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.CostDiffResult{Current: s.current, Proposed: s.proposed, Diff: diffs[profile], Assumptions: s.assumptions})
	}
	return renderDiff(stdout, s.label, s.current, s.proposed, diffs, profile, s.assumptions)
}

func projectInventory(ctx context.Context, dependencies Dependencies, provider *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment, out io.Writer) (*costv1.ResourceSet, []string, error) {
	resolved, err := readiness.ResolveComputes(provider, cfg)
	if err != nil {
		return nil, nil, err
	}
	release, err := workdir.LockBuildOutput(ctx, resolved.Dir, func() {
		_, _ = fmt.Fprintln(out, "Waiting for another ocel run in "+resolved.Dir+" to finish with "+buildoutput.Dir)
	})
	if err != nil {
		return nil, nil, err
	}
	defer release()
	assembled, assumptions, err := scanManifest(ctx, dependencies, resolved, env, out)
	if err != nil {
		return nil, nil, err
	}
	set, err := provider.Inventory(ctx, resolved, assembled, env)
	if err != nil {
		return nil, nil, err
	}
	return set, assumptions, nil
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

func profileOf(name string) (costv1.Profile, error) {
	if name == "" {
		return pricing.DefaultProfile, nil
	}
	for _, profile := range pricing.Profiles() {
		if pricing.ProfileName(profile) == name {
			return profile, nil
		}
	}
	return costv1.Profile_PROFILE_UNSPECIFIED, fmt.Errorf("the usage profile is one of %s, not %q", strings.Join(profileNames(), ", "), name)
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
		usage, err := structpb.NewStruct(quantities)
		if err != nil {
			return nil, fmt.Errorf("the usage file %s sets %s to something that is not a quantity: %w", path, id, err)
		}
		overrides[id] = usage
	}
	return overrides, nil
}

type unread struct{}

func (unread) List(context.Context) ([]variables.ValueMetadata, error) { return nil, nil }

func (unread) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return map[variables.Coordinate]string{}, nil
}

const unbuiltDigest = "0000000000000000000000000000000000000000000000000000000000000000"

func scanManifest(ctx context.Context, dependencies Dependencies, cfg *project.Project, env *environmentv1.Environment, out io.Writer) (*contractv1.Manifest, []string, error) {
	declarations := variables.NewDeclarations(unread{}, variables.ScopeOf(cfg, env.GetTier(), ""))
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, out, out)
	if err != nil {
		return nil, nil, err
	}
	built, assumptions, err := scannedOutput(dependencies, cfg)
	if err != nil {
		return nil, nil, err
	}
	assembled, err := manifest.Assemble(manifest.Input{
		Project:   cfg,
		Tier:      env.GetTier(),
		Resources: resources,
		Built:     built,
	})
	if err != nil {
		return nil, nil, err
	}
	return assembled, assumptions, nil
}

func scannedOutput(dependencies Dependencies, cfg *project.Project) (build.Output, []string, error) {
	images := make(map[string]string, len(cfg.Apps))
	for _, a := range cfg.Apps {
		if a.IsBuiltAsImage() {
			images[a.Name] = cfg.Slug + "/" + a.Name + "@sha256:" + unbuiltDigest
		}
	}
	functions, err := dependencies.ReadFunctions(cfg.Dir)
	if errors.Is(err, build.ErrNoBuildOutput) {
		return build.Output{Functions: unbuiltFunctions(cfg), Images: images}, []string{unbuiltAssumption}, nil
	}
	if err != nil {
		return build.Output{}, nil, err
	}
	return build.Output{Functions: functions, Images: images}, nil, nil
}

func unbuiltFunctions(cfg *project.Project) []build.Function {
	functions := make([]build.Function, 0, len(cfg.Apps))
	for _, a := range build.FunctionApps(cfg.Apps) {
		functions = append(functions, build.Function{Route: a.Name, App: a.Name, Framework: buildoutput.Framework{Name: a.Framework, Arch: a.Arch}})
	}
	return functions
}
