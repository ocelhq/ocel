package gcp

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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const serviceAccountListPage = 100

type appAccount struct {
	id      string
	tier    environment.Tier
	project string
	env     string
	app     string
}

func readAppAccount(n names.Names, account *iam.ServiceAccount) (appAccount, bool) {
	id, here := n.AccountID(account)
	if !here {
		return appAccount{}, false
	}
	rest, found := strings.CutPrefix(account.Description, appDescriptionPrefix)
	if !found {
		return appAccount{}, false
	}
	if rest, found = strings.CutSuffix(rest, appDescriptionSuffix); !found {
		return appAccount{}, false
	}
	at := strings.LastIndex(rest, appDescriptionTier)
	if at < 0 {
		return appAccount{}, false
	}
	running, tier := rest[:at], environment.Tier(rest[at+len(appDescriptionTier):])
	at = strings.LastIndex(running, appDescriptionEnvironment)
	if at < 0 {
		return appAccount{}, false
	}
	named, env := running[:at], running[at+len(appDescriptionEnvironment):]
	app, project, found := strings.Cut(named, appDescriptionProject)
	if !found || n.AppAccount(tier, project, env, app) != id {
		return appAccount{}, false
	}
	for _, each := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		if id == n.PushAccount(each) || id == n.RealtimeAccount(each) || id == n.EnvSourceSyncAccount(each) {
			return appAccount{}, false
		}
	}
	if id == n.Connector() {
		return appAccount{}, false
	}
	return appAccount{id: id, tier: tier, project: project, env: env, app: app}, true
}

func isAppRecorded(stacks []stackrecords.NamedStack, env, app string) bool {
	for _, stack := range stacks {
		if !stack.Name.IsInfra() && stack.Name.Env == env && stack.Name.App == app {
			return true
		}
	}
	return false
}

func (b bootstrap) deleteUnusedAccounts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	progress = gcloud.EnsureProgress(progress)
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	var candidates []appAccount
	for token := ""; ; {
		page, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.List("projects/"+b.clients.Project()).
			PageSize(serviceAccountListPage).PageToken(token).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("list the service accounts of project %s: %w", b.clients.Project(), err)
		}
		for _, account := range page.Accounts {
			if found, ok := readAppAccount(b.clients.Names, account); ok && found.tier == req.Tier {
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
		stacks, read := readProjects[candidate.project]
		if !read {
			var err error
			if stacks, err = stackrecords.List(ctx, b.records, candidate.tier, candidate.project); err != nil {
				failures = append(failures, fmt.Errorf("check whether app %s of %s still runs: %w", candidate.app, candidate.project, err))
				continue
			}
			readProjects[candidate.project] = stacks
		}
		if isAppRecorded(stacks, candidate.env, candidate.app) {
			continue
		}
		if stacks, err := stackrecords.List(ctx, b.records, candidate.tier, candidate.project); err != nil {
			failures = append(failures, fmt.Errorf("check whether app %s of %s still runs: %w", candidate.app, candidate.project, err))
			continue
		} else if isAppRecorded(stacks, candidate.env, candidate.app) {
			continue
		}
		if _, err := grants.RevokeAll(ctx, b.clients, b.records, candidate.tier, candidate.id); err != nil {
			failures = append(failures, err)
			continue
		}
		removed, err := b.clients.DeleteAccount(ctx, candidate.id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if removed {
			deleted++
			progress.Debug("Deleted the " + candidate.id + " service account app " + candidate.app + " of " + candidate.project + " ran as in environment " + candidate.env + " of the " + string(candidate.tier) + " tier")
		}
	}
	if deleted > 0 {
		progress.Say(fmt.Sprintf("Deleted %d of the %s tier's app service accounts: no environment runs their apps any more", deleted, req.Tier))
	} else if len(failures) == 0 {
		progress.Debug("No app service account of the " + string(req.Tier) + " tier is unused")
	}
	return errors.Join(failures...)
}
