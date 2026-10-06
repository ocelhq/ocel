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
)

const serviceAccountListPage = 100

type appAccount struct {
	id      string
	tier    environment.Tier
	project string
	app     string
}

func retiredWorkloadDescription(tier environment.Tier) string {
	return "the identity every app ocel deploys in the " + string(tier) + " tier runs as"
}

func (n Names) accountID(account *iam.ServiceAccount) (string, bool) {
	return strings.CutSuffix(account.Email, "@"+n.project+accountDomain)
}

func (n Names) readAppAccount(account *iam.ServiceAccount) (appAccount, bool) {
	id, here := n.accountID(account)
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
	at := strings.LastIndex(rest, appDescriptionRuns)
	if at < 0 {
		return appAccount{}, false
	}
	named, tier := rest[:at], environment.Tier(rest[at+len(appDescriptionRuns):])
	app, project, found := strings.Cut(named, " of project ")
	if !found || n.AppAccount(tier, project, app) != id {
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
	return appAccount{id: id, tier: tier, project: project, app: app}, true
}

func (n Names) isRetiredWorkloadAccount(account *iam.ServiceAccount, tier environment.Tier) bool {
	id, here := n.accountID(account)
	return here && id == string(n.namespace)+"-"+string(tier) && account.Description == retiredWorkloadDescription(tier)
}

func isAppRecorded(stacks []stackrecords.NamedStack, app string) bool {
	for _, stack := range stacks {
		if !stack.Name.IsInfra() && stack.Name.App == app {
			return true
		}
	}
	return false
}

func (b bootstrap) deleteUnusedAccounts(ctx context.Context, req provider.BootstrapRequest, progress progress.Log) error {
	if req.Repair {
		return nil
	}
	progress = ensureProgress(progress)
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	var candidates []appAccount
	var retired []string
	for token := ""; ; {
		page, err := attempted(ctx, service.Projects.ServiceAccounts.List("projects/"+b.clients.project).
			PageSize(serviceAccountListPage).PageToken(token).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("list the service accounts of project %s: %w", b.clients.project, err)
		}
		for _, account := range page.Accounts {
			if b.clients.isRetiredWorkloadAccount(account, req.Tier) {
				id, _ := b.clients.accountID(account)
				retired = append(retired, id)
			} else if found, ok := b.clients.readAppAccount(account); ok && found.tier == req.Tier {
				candidates = append(candidates, found)
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}

	var failures []error
	for _, id := range retired {
		deleted, err := b.retireWorkloadAccount(ctx, req.Tier, id)
		if err != nil {
			failures = append(failures, err)
		} else if deleted {
			progress.Say(retiredWorkloadDeleted(id, req.Tier))
		}
	}
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
		if isAppRecorded(stacks, candidate.app) {
			continue
		}
		if stacks, err := stackrecords.List(ctx, b.records, candidate.tier, candidate.project); err != nil {
			failures = append(failures, fmt.Errorf("check whether app %s of %s still runs: %w", candidate.app, candidate.project, err))
			continue
		} else if isAppRecorded(stacks, candidate.app) {
			continue
		}
		removed, err := b.deleteAccount(ctx, candidate.id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if removed {
			deleted++
			progress.Debug("Deleted the " + candidate.id + " service account app " + candidate.app + " of " + candidate.project + " ran as in the " + string(candidate.tier) + " tier")
		}
	}
	if deleted > 0 {
		progress.Say(fmt.Sprintf("Deleted %d of the %s tier's app service accounts: no environment runs their apps any more", deleted, req.Tier))
	} else if len(failures) == 0 {
		progress.Debug("No app service account of the " + string(req.Tier) + " tier is unused")
	}
	return errors.Join(failures...)
}

func retiredWorkloadDeleted(id string, tier environment.Tier) string {
	return "Deleted the " + id + " service account: every app of the " + string(tier) + " tier runs as an account of its own"
}

func (b bootstrap) retireWorkloadAccount(ctx context.Context, tier environment.Tier, id string) (bool, error) {
	member := "serviceAccount:" + id + "@" + b.clients.project + accountDomain
	if _, err := b.clients.unbindProjectMember(ctx, member); err != nil {
		return false, err
	}
	if _, err := b.clients.bindKeyRoles(ctx, tier, member, []string{appOpeningRole}, nil); err != nil {
		return false, fmt.Errorf("take back what the %s service account may do with the %s key: %w", id, tier, err)
	}
	if err := ignoreAbsent(b.clients.bindQueueRoles(ctx, tier, member, nil)); err != nil {
		return false, fmt.Errorf("take back what the %s service account may do on the %s delay queue: %w", id, tier, err)
	}
	return b.deleteAccount(ctx, id)
}

func (b bootstrap) deleteAccount(ctx context.Context, id string) (bool, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return false, err
	}
	if _, err := attempted(ctx, service.Projects.ServiceAccounts.Delete(accountPath(b.clients, id)).Context(ctx).Do); err != nil {
		if absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete the %s service account: %w", id, err)
	}
	return true, nil
}

func (b bootstrap) deleteRetiredWorkloadAccount(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	id := string(b.clients.namespace) + "-" + string(tier)
	account, err := attempted(ctx, service.Projects.ServiceAccounts.Get(accountPath(b.clients, id)).Context(ctx).Do)
	if absent(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the %s service account: %w", id, err)
	}
	if !b.clients.isRetiredWorkloadAccount(account, tier) {
		return nil
	}
	deleted, err := b.retireWorkloadAccount(ctx, tier, id)
	if deleted {
		ensureProgress(progress).Say(retiredWorkloadDeleted(id, tier))
	}
	return err
}
