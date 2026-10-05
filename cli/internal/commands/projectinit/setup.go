package projectinit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func NewSetup(dependencies Dependencies) prerequisite.Setup {
	return prerequisite.Setup{
		Prompt: func(prerequisite.MissingError) string { return "Set up a project here?" },
		Run: func(ctx context.Context, policy consent.Policy, span *run.Span, missing prerequisite.MissingError) error {
			var absent project.NoConfigError
			if !errors.As(missing, &absent) {
				return missing
			}
			opts := initOptions{configPath: dependencies.ConfigPath()}
			var answered bool
			err := span.Ask(func() (err error) {
				answered, err = askProvider(ctx, terminal.NewPrompt(policy.Out, policy.In), project.DeriveSlug(filepath.Base(absent.StartDir)), &opts)
				return err
			})
			if err != nil {
				return err
			}
			if !answered {
				return prerequisite.SetupDeclinedError{Missing: missing}
			}
			_, err = runInit(ctx, dependencies, absent.StartDir, "", opts)
			return err
		},
	}
}

func askProvider(ctx context.Context, prompt terminal.Prompt, slug string, opts *initOptions) (bool, error) {
	ids := configdoc.ProviderIDs()
	options := make([]terminal.Option, 0, len(ids))
	for _, id := range ids {
		options = append(options, terminal.Option{Name: id})
	}
	provider, answered, err := prompt.Select(ctx, fmt.Sprintf("Where should %s deploy?", slug), options)
	if err != nil || !answered {
		return false, err
	}
	opts.provider = provider
	for _, required := range configdoc.RequiredProviderOptions(provider) {
		value, answered, err := prompt.Input(ctx, required.Name, required.Doc)
		if err != nil || !answered || value == "" {
			return false, err
		}
		opts.settings = append(opts.settings, providerSetting{name: required.Name, value: value})
	}
	return true, nil
}
