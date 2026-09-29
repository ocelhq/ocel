package bindings

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/preflight"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const defaultBindingOwner = "cli"

type bindingsOptions struct {
	preview     bool
	environment string
	owner       string
}

func (o bindingsOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

func (o bindingsOptions) tier() environmentv1.Tier {
	if o.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func (o bindingsOptions) ownerOrDefault() string {
	if o.owner == "" {
		return defaultBindingOwner
	}
	return o.owner
}

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bindings",
		Short: "Manage the bindings this project's apps resolve",
		Long: "Manage the bindings this project's apps resolve.\n\n" +
			"A binding is one resource an app reaches — its address, its credentials and the " +
			"permissions that go with it — published under a name apps bind to. Records live in " +
			"your own provider account and are reached through the provider, never by the CLI directly.",
	}
	cmd.AddCommand(newSetCommand(invocation), newRemoveCommand(invocation), newListCommand(invocation), newGenerateCommand(invocation))
	return commands.ReserveStdout(cmd)
}

func addCoordinateFlags(cmd *cobra.Command, opts *bindingsOptions) {
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "Act on the preview bootstrap instead of production")
	cmd.Flags().StringVar(&opts.environment, "environment", "", "Address the binding this named preview environment has instead of the one bound to all environments")
}

func newSetCommand(invocation commands.Invocation) *cobra.Command {
	var opts bindingsOptions
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Publish one binding, read as JSON on stdin",
		Long: "Publish one binding, read as JSON on stdin.\n\n" +
			"The binding is a common.bindings.v1.Binding in protobuf JSON, and it includes its own name, so " +
			"there is nothing to name on the command line:\n\n" +
			"  ocel bindings set < binding.json\n\n" +
			"A name belongs to whoever published it. Publishing over a name another publisher " +
			"owns is refused rather than handing every app bound to that name another " +
			"resource's values; pass --owner to publish as that publisher.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
				return runBindingsSet(ctx, invocation, cwd, cmd.InOrStdin(), opts, cmd.OutOrStdout())
			})
		},
	}
	addCoordinateFlags(cmd, &opts)
	cmd.Flags().StringVar(&opts.owner, "owner", defaultBindingOwner, "Publish under this publisher's name")
	return cmd
}

func newRemoveCommand(invocation commands.Invocation) *cobra.Command {
	var opts bindingsOptions
	cmd := &cobra.Command{
		Use:   "rm <NAME>",
		Short: "Remove a binding, whatever published it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
				return runBindingsRemove(ctx, invocation, cwd, args[0], opts, cmd.OutOrStdout())
			})
		},
	}
	addCoordinateFlags(cmd, &opts)
	return cmd
}

func newListCommand(invocation commands.Invocation) *cobra.Command {
	var opts bindingsOptions
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the published bindings, without revealing what they contain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
				return runBindingsList(ctx, invocation, cwd, opts, cmd.OutOrStdout())
			})
		},
	}
	addCoordinateFlags(cmd, &opts)
	return cmd
}

func newGenerateCommand(invocation commands.Invocation) *cobra.Command {
	var opts bindingsOptions
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Write the transform types for the bindings published to one coordinate",
		Long: "Write the transform types for the bindings published to one coordinate.\n\n" +
			"Reads the records published to production, or to the preview coordinate --preview and " +
			"--environment name, and writes " + bindingTypesFileName + " beside your ocel config. The file " +
			"names each record and the properties it has, so `bindings.<type>.<name>.<property>` in a transform " +
			"is checked where it is written instead of at the deploy. Check it in, and run this again when " +
			"what you publish changes.\n\n" +
			"This reads the published records, so it logs in and runs the provider.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
				return runBindingsGenerate(ctx, invocation, cwd, opts, cmd.OutOrStdout())
			})
		},
	}
	addCoordinateFlags(cmd, &opts)
	return cmd
}

func withBindingCommand(cmd *cobra.Command, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	return run(cmd.Context(), cwd)
}

func withBindingProvider(ctx context.Context, invocation commands.Invocation, cwd string, opts bindingsOptions, command string, drive func(context.Context, *providerprocess.Provider, *project.Project) (string, error)) (err error) {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := invocation.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerprocess.Start(ctx, cfg, check, invocation.Questions, executables.PinToLock)
	if err != nil {
		return err
	}
	defer prov.Close()

	err = preflight.Credentials(ctx, check, prov, cfg, opts.tier(), "ocel bootstrap "+bootstrap.Name(opts.tier()))
	check.End(err)
	if err != nil {
		return err
	}
	headline, err := drive(ctx, prov, cfg)
	if err == nil && headline != "" {
		run.Succeed(headline)
	}
	return err
}

func runBindingsSet(ctx context.Context, invocation commands.Invocation, cwd string, stdin io.Reader, opts bindingsOptions, stdout io.Writer) error {
	binding, err := decodeBinding(stdin)
	if err != nil {
		return err
	}
	owner := opts.ownerOrDefault()
	if owner == naming.InlineRecordOwner {
		return fmt.Errorf("publisher %q is the one ocel writes an inline binding's record as, at deploy, from the config; publish as your own tool with --owner", owner)
	}
	return withBindingProvider(ctx, invocation, cwd, opts, "ocel bindings set", func(ctx context.Context, prov *providerprocess.Provider, cfg *project.Project) (string, error) {
		client, err := prov.Vars()
		if err != nil {
			return "", err
		}
		resp, err := client.SetBinding(ctx, &envvarsv1.SetBindingRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Binding:     binding,
			Owner:       owner,
		})
		if err != nil {
			return "", err
		}
		headline := fmt.Sprintf("Published %s as %s (version %d)", describeBinding(binding.GetName(), opts), owner, resp.GetVersion())
		if invocation.Presentation(stdout).Format == terminal.FormatJSON {
			return headline, writeBindingJSON(stdout, bindingSetReport{Name: binding.GetName(), Owner: owner, Version: resp.GetVersion()})
		}
		return headline, nil
	})
}

func decodeBinding(stdin io.Reader) (*bindingsv1.Binding, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read the binding on stdin: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("nothing came in on stdin; `ocel bindings set` reads one binding as protobuf JSON, so pipe it in: `ocel bindings set < binding.json`")
	}
	var shape any
	if err := json.Unmarshal(raw, &shape); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			return nil, fmt.Errorf("the binding on stdin is not JSON: it breaks at line %d", bytes.Count(raw[:syntax.Offset], []byte("\n"))+1)
		}
		return nil, errors.New("the binding on stdin is not JSON")
	}
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(raw, binding); err != nil {
		if at := mismatchedField(binding.ProtoReflect().Descriptor(), shape, ""); at != "" {
			return nil, fmt.Errorf("the binding on stdin does not have the shape of a binding: %s", at)
		}
		return nil, errors.New("the binding on stdin does not have the shape of a binding")
	}
	return binding, nil
}

func mismatchedField(message protoreflect.MessageDescriptor, value any, path string) string {
	if message.FullName().Parent() == "google.protobuf" {
		return ""
	}
	object, ok := value.(map[string]any)
	if !ok {
		return cmp.Or(path, "the binding") + " is not an object"
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		field := message.Fields().ByJSONName(key)
		if field == nil {
			field = message.Fields().ByTextName(key)
		}
		at := strings.TrimPrefix(path+"."+key, ".")
		switch {
		case field == nil:
			return fmt.Sprintf("%s is not a field of %s", at, message.Name())
		case field.IsMap() || object[key] == nil:
			continue
		case field.IsList():
			items, ok := object[key].([]any)
			if !ok {
				return at + " is not a list"
			}
			for i, item := range items {
				if problem := mismatchedValue(field, item, fmt.Sprintf("%s[%d]", at, i)); problem != "" {
					return problem
				}
			}
		default:
			if problem := mismatchedValue(field, object[key], at); problem != "" {
				return problem
			}
		}
	}
	return ""
}

func mismatchedValue(field protoreflect.FieldDescriptor, value any, at string) string {
	switch field.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return mismatchedField(field.Message(), value, at)
	case protoreflect.StringKind, protoreflect.BytesKind:
		if _, ok := value.(string); !ok {
			return at + " is not a string"
		}
	case protoreflect.BoolKind:
		if _, ok := value.(bool); !ok {
			return at + " is not true or false"
		}
	case protoreflect.EnumKind:
		return ""
	default:
		switch number := value.(type) {
		case float64:
			return ""
		case string:
			if _, err := strconv.ParseFloat(number, 64); err == nil {
				return ""
			}
		}
		return at + " is not a number"
	}
	return ""
}

func runBindingsRemove(ctx context.Context, invocation commands.Invocation, cwd, name string, opts bindingsOptions, stdout io.Writer) error {
	if naming.IsInlineRecord(name) {
		return fmt.Errorf("%s is the record ocel keeps for a binding written inline in `bindings`, and the next deploy writes it again: remove that binding from the config, and the deploy after removes the record", name)
	}
	return withBindingProvider(ctx, invocation, cwd, opts, "ocel bindings rm", func(ctx context.Context, prov *providerprocess.Provider, cfg *project.Project) (string, error) {
		client, err := prov.Vars()
		if err != nil {
			return "", err
		}
		resp, err := client.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Name:        name,
		})
		if err != nil {
			return "", err
		}
		headline := "Removed " + describeBinding(name, opts)
		if !resp.GetRemoved() {
			headline = fmt.Sprintf("No binding named %s is published", describeBinding(name, opts))
		}
		if invocation.Presentation(stdout).Format == terminal.FormatJSON {
			return headline, writeBindingJSON(stdout, bindingRemoveReport{Name: name, Removed: resp.GetRemoved()})
		}
		return headline, nil
	})
}

func runBindingsList(ctx context.Context, invocation commands.Invocation, cwd string, opts bindingsOptions, stdout io.Writer) error {
	return withBindingProvider(ctx, invocation, cwd, opts, "ocel bindings ls", func(ctx context.Context, prov *providerprocess.Provider, cfg *project.Project) (string, error) {
		client, err := prov.Vars()
		if err != nil {
			return "", err
		}
		resp, err := client.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return "", err
		}
		if invocation.Presentation(stdout).Format == terminal.FormatJSON {
			return "", writeBindingJSON(stdout, bindingListReport{Bindings: bindingReports(resp.GetBindings())})
		}
		renderBindings(stdout, resp.GetBindings())
		return "", nil
	})
}

func runBindingsGenerate(ctx context.Context, invocation commands.Invocation, cwd string, opts bindingsOptions, stdout io.Writer) error {
	return withBindingProvider(ctx, invocation, cwd, opts, "ocel bindings generate", func(ctx context.Context, prov *providerprocess.Provider, cfg *project.Project) (string, error) {
		client, err := prov.Vars()
		if err != nil {
			return "", err
		}
		resp, err := client.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return "", err
		}

		path := filepath.Join(cfg.Dir, bindingTypesFileName)
		if err := os.WriteFile(path, []byte(renderBindingTypes(describeBindingCoordinate(opts), cfg.BindingsFor(opts.tier()), resp.GetBindings())), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", bindingTypesFileName, err)
		}

		headline := fmt.Sprintf("Wrote %s from the %d bindings published to %s", path, len(resp.GetBindings()), describeBindingCoordinate(opts))
		if len(resp.GetBindings()) == 0 {
			headline = fmt.Sprintf("Nothing is published to %s; wrote %s, which names no record and so leaves no binding name open", describeBindingCoordinate(opts), path)
		}
		if invocation.Presentation(stdout).Format == terminal.FormatJSON {
			return headline, writeBindingJSON(stdout, bindingGenerateReport{Path: path, Bindings: bindingReports(resp.GetBindings())})
		}
		return headline, nil
	})
}

func describeBindingCoordinate(opts bindingsOptions) string {
	if !opts.preview {
		return "production"
	}
	if opts.environment == "" {
		return "preview"
	}
	return "the preview environment " + opts.environment
}

type bindingGenerateReport struct {
	Path     string          `json:"path"`
	Bindings []bindingReport `json:"bindings"`
}

type bindingSetReport struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

type bindingRemoveReport struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

type bindingListReport struct {
	Bindings []bindingReport `json:"bindings"`
}

type bindingReport struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Source  string `json:"source"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

func bindingReports(bindings []*envvarsv1.BindingSummary) []bindingReport {
	out := make([]bindingReport, 0, len(bindings))
	for _, l := range bindings {
		out = append(out, bindingReport{
			Name:    l.GetName(),
			Type:    bindingTypeName(l.GetType()),
			Source:  l.GetSource(),
			Owner:   l.GetOwner(),
			Version: l.GetVersion(),
		})
	}
	return out
}

func writeBindingJSON(stdout io.Writer, report any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderBindings(stdout io.Writer, bindings []*envvarsv1.BindingSummary) {
	if len(bindings) == 0 {
		fmt.Fprintln(stdout, "No bindings published. Publish one with `ocel bindings set < binding.json`.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSOURCE\tOWNER\tVERSION")
	for _, l := range bindings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			l.GetName(), bindingTypeName(l.GetType()), sourceOrDash(l.GetSource()), l.GetOwner(), l.GetVersion())
	}
	_ = tw.Flush()
}

func bindingTypeName(t bindingsv1.BindingType) string {
	return strings.ToLower(naming.EnvFragment(t))
}

func sourceOrDash(source string) string {
	if source == "" {
		return "—"
	}
	return source
}

func describeBinding(name string, opts bindingsOptions) string {
	if opts.environment != "" {
		return name + " for " + opts.environment
	}
	return name
}
