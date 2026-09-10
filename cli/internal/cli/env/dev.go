package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/envstore"
	"github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type devStore struct {
	client    *envstore.Client
	token     string
	projectID string
}

func unlinked(key string) error {
	if key == "" {
		return fmt.Errorf("this project is not linked to a console, so nothing holds its dev values. For `ocel dev`, put KEY=VALUE lines in %s; to share values with your team, run `ocel link`.", //nolint:staticcheck // ST1005: prose addressed to a person, ending in a sentence rather than wrapped by a caller
			dotenv.FileName)
	}
	return fmt.Errorf("this project is not linked to a console, so nothing holds %s. For `ocel dev`, put %s=<VALUE> in %s; to share values with your team, run `ocel link`.", //nolint:staticcheck // ST1005: prose addressed to a person, ending in a sentence rather than wrapped by a caller
		key, key, dotenv.FileName)
}

func withDevStore(ctx context.Context, deps cmddeps.Deps, cwd, key string, opts envOptions, stderr io.Writer, run func(*devStore, *projectconfig.Config) error) error {
	if err := opts.checkDev(); err != nil {
		return err
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	creds, credsErr := deps.LoadCredentials()
	apiURL := console.EffectiveBaseURL(creds.APIURL)

	record, err := link.Read(cfg.Dir, apiURL)
	if err != nil {
		return err
	}
	if record == nil {
		return unlinked(key)
	}
	if credsErr != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &exitsig.ExitError{Code: 1}
	}

	return run(&devStore{client: envstore.New(apiURL), token: creds.AccessToken, projectID: record.ProjectID}, cfg)
}

func runEnvSetDevPairs(ctx context.Context, deps cmddeps.Deps, cwd string, pairs []envSetPair, opts envOptions, stdout, stderr io.Writer) error {
	key := ""
	if len(pairs) > 0 {
		key = pairs[0].key
	}
	return withDevStore(ctx, deps, cwd, key, opts, stderr, func(store *devStore, cfg *projectconfig.Config) error {
		definitions, err := declaredVariables(ctx, deps, cfg, nil, key, opts, stderr)
		if err != nil {
			return err
		}
		for _, pair := range pairs {
			if err := checkDevWritable(definitions, pair.key); err != nil {
				return err
			}
		}
		for _, pair := range pairs {
			if err := store.client.Set(ctx, store.token, store.projectID, pair.key, pair.value); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Set %s for `ocel dev`. Every developer linked to this project resolves it; a deploy resolves none of it.\n", pair.key)
		}
		return nil
	})
}

func checkDevWritable(definitions []*resourcesv1.VariableDefinition, key string) error {
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) == 0 {
			return nil
		}
		return fmt.Errorf("%s is scoped to %s, and a dev value carries no folder scope — `ocel dev` runs one child for the whole project. Put %s=<VALUE> in %s under the folder that needs it, or drop the scope where %s is declared so one dev value serves the project%s",
			key, strings.Join(scope, " and "), key, dotenv.FileName, key, descriptionLine(definition.GetDescription()))
	}
	return envgate.CheckWritable(definitions, key, "")
}

func runEnvLsDev(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withDevStore(ctx, deps, cwd, "", opts, stderr, func(store *devStore, cfg *projectconfig.Config) error {
		definitions, err := declaredVariables(ctx, deps, cfg, nil, "", opts, stderr)
		if err != nil {
			return err
		}
		values, err := store.client.List(ctx, store.token, store.projectID)
		if err != nil {
			return err
		}
		renderDevValues(stdout, values, descriptions(definitions))
		return nil
	})
}

func runEnvGetDev(ctx context.Context, deps cmddeps.Deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withDevStore(ctx, deps, cwd, key, opts, stderr, func(store *devStore, cfg *projectconfig.Config) error {
		definitions, err := declaredVariables(ctx, deps, cfg, nil, key, opts, stderr)
		if err != nil {
			return err
		}
		held, err := store.client.Get(ctx, store.token, store.projectID, key)
		if errors.Is(err, envstore.ErrNoValue) {
			return fmt.Errorf("no value is set for %s in dev; set one with `ocel env set %s=<VALUE> --dev`%s", key, key, descriptionLine(descriptions(definitions)[key]))
		}
		if err != nil {
			return err
		}
		if opts.reveal {
			fmt.Fprintln(stdout, held.Value)
			return nil
		}
		fmt.Fprintf(stdout, "%s for `ocel dev` — %d bytes, updated %s\n", key, len(held.Value), runui.EpochDate(held.UpdatedAt/1000))
		fmt.Fprintln(stdout, "Pass --reveal to print the value.")
		return nil
	})
}

func runEnvRmDev(ctx context.Context, deps cmddeps.Deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return withDevStore(ctx, deps, cwd, key, opts, stderr, func(store *devStore, _ *projectconfig.Config) error {
		deleted, err := store.client.Delete(ctx, store.token, store.projectID, key)
		if err != nil {
			return err
		}
		if !deleted {
			fmt.Fprintf(stdout, "No value was set for %s in dev.\n", key)
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s from dev. A run already under way keeps what it resolved.\n", key)
		return nil
	})
}

func renderDevValues(stdout io.Writer, values []envstore.Value, descriptions map[string]string) {
	if len(values) == 0 {
		fmt.Fprintln(stdout, "No dev values set. Set one with `ocel env set <KEY>=<VALUE> --dev`.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tDESCRIPTION\tBYTES\tUPDATED")
	for _, value := range values {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", value.Key, descriptions[value.Key], len(value.Value), runui.EpochDate(value.UpdatedAt/1000))
	}
	_ = tw.Flush()
}

type devValues struct{}

func (devValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (devValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return map[envgate.Cell]string{}, nil
}
