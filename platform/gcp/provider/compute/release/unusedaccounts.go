package release

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const serviceAccountListPage = 100

type AppAccount struct {
	ID      string
	Tier    environment.Tier
	Project string
	Env     string
	App     string
}

func ReadAppAccount(n names.Names, account *iam.ServiceAccount) (AppAccount, bool) {
	id, here := n.AccountID(account)
	if !here {
		return AppAccount{}, false
	}
	rest, found := strings.CutPrefix(names.UnmarkedAccountDescription(account.Description), appDescriptionPrefix)
	if !found {
		return AppAccount{}, false
	}
	if rest, found = strings.CutSuffix(rest, appDescriptionSuffix); !found {
		return AppAccount{}, false
	}
	at := strings.LastIndex(rest, appDescriptionTier)
	if at < 0 {
		return AppAccount{}, false
	}
	running, tier := rest[:at], environment.Tier(rest[at+len(appDescriptionTier):])
	at = strings.LastIndex(running, appDescriptionEnvironment)
	if at < 0 {
		return AppAccount{}, false
	}
	named, env := running[:at], running[at+len(appDescriptionEnvironment):]
	app, project, found := strings.Cut(named, appDescriptionProject)
	if !found || n.AppAccount(project, env, app) != id {
		return AppAccount{}, false
	}
	for _, each := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		if id == n.PushAccount(each) || id == n.RealtimeAccount(each) || id == n.EnvSourceSyncAccount(each) {
			return AppAccount{}, false
		}
	}
	if id == n.Connector() {
		return AppAccount{}, false
	}
	return AppAccount{ID: id, Tier: tier, Project: project, Env: env, App: app}, true
}

func isAppRecorded(stacks []stackrecords.NamedStack, env, app string) bool {
	for _, stack := range stacks {
		if !stack.Name.IsInfra() && stack.Name.Env == env && stack.Name.App == app {
			return true
		}
	}
	return false
}

func DeleteUnusedAccounts(ctx context.Context, b bootstrap.Engine, req provider.BootstrapRequest, progress progress.Log) error {
	progress = gcloud.EnsureProgress(progress)
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	var candidates []AppAccount
	for token := ""; ; {
		page, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.List("projects/"+b.Clients.Project()).
			PageSize(serviceAccountListPage).PageToken(token).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("list the service accounts of project %s: %w", b.Clients.Project(), err)
		}
		for _, account := range page.Accounts {
			if found, ok := ReadAppAccount(b.Clients.Names, account); ok && found.Tier == req.Tier {
				candidates = append(candidates, found)
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}

	var failures []error
	deleted := 0
	readProjects := map[string][]stackrecords.NamedStack{}
	for _, candidate := range candidates {
		stacks, read := readProjects[candidate.Project]
		if !read {
			var err error
			if stacks, err = stackrecords.List(ctx, b.Records, candidate.Tier, candidate.Project); err != nil {
				failures = append(failures, fmt.Errorf("check whether app %s of %s still runs: %w", candidate.App, candidate.Project, err))
				continue
			}
			readProjects[candidate.Project] = stacks
		}
		if isAppRecorded(stacks, candidate.Env, candidate.App) {
			continue
		}
		if stacks, err := stackrecords.List(ctx, b.Records, candidate.Tier, candidate.Project); err != nil {
			failures = append(failures, fmt.Errorf("check whether app %s of %s still runs: %w", candidate.App, candidate.Project, err))
			continue
		} else if isAppRecorded(stacks, candidate.Env, candidate.App) {
			continue
		}
		if _, err := grants.RevokeAll(ctx, b.Clients, b.Records, candidate.Tier, candidate.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		removed, err := b.Clients.DeleteAccount(ctx, candidate.Tier, candidate.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if removed {
			deleted++
			progress.Debug("Deleted the " + candidate.ID + " service account app " + candidate.App + " of " + candidate.Project + " ran as in environment " + candidate.Env + " of the " + string(candidate.Tier) + " tier")
		}
	}
	if deleted > 0 {
		progress.Say(fmt.Sprintf("Deleted %d of the %s tier's app service accounts: no environment runs their apps any more", deleted, req.Tier))
	} else if len(failures) == 0 {
		progress.Debug("No app service account of the " + string(req.Tier) + " tier is unused")
	}
	return errors.Join(failures...)
}
