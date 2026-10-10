package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const (
	appRecordsRole = "roles/datastore.viewer"
	appOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	appObjectsRole = "roles/storage.objectUser"
	appAssetsRole  = "roles/storage.objectViewer"

	appAccountVisibleAttempts = 20
	maxDisplayNameBytes       = 100
	accountQuotaMessage       = "Maximum number of service accounts"

	appDescriptionPrefix = "the identity app "
	appDescriptionRuns   = " runs as in the "
	appDescriptionSuffix = " tier"
)

var appGrantedRoles = []string{appRecordsRole, taskRecordsRole, appOpeningRole, appObjectsRole, appAssetsRole}

func listGrantedRoles(names Names) []string {
	return append(slices.Clone(appGrantedRoles), names.CDNPurgeRolePath())
}

func keyCondition(c *clients, tier environment.Tier) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " key",
		Expression: fmt.Sprintf("resource.name == %q", keyPath(c, string(tier))),
	}
}

func (p *Provider) ensureAppAccount(ctx context.Context, c *clients, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (string, error) {
	tier, project, app := spec.Ref.Tier, spec.Ref.Project, spec.App.App
	email := c.AppAccountEmail(tier, project, app)
	if err := c.createAppAccount(ctx, tier, project, app); err != nil {
		return "", err
	}
	member := "serviceAccount:" + email
	grants := []func() error{
		func() error {
			return c.bindProjectRole(ctx, member, appRecordsRole, databaseCondition(c.project, c.Namespace()), true)
		},
		func() error { return c.bindProjectRole(ctx, member, appOpeningRole, keyCondition(c, tier), true) },
	}
	if reachesTopics(spec.App) {
		grants = append(grants, c.taskGrants(ctx, spec, member, declared)...)
	}
	grants = append(grants, c.bucketGrants(ctx, spec, member)...)
	if refreshesByTask(spec.App.Framework, spec.App.Compute, factsOf(spec.Edge), p.servesBehindIAP(spec)) {
		grants = append(grants, c.refreshGrants(ctx, tier, member)...)
	}
	for _, grant := range grants {
		if err := untilVisible(ctx, grant); err != nil {
			return "", err
		}
	}
	if err := p.revokeDroppedBuckets(ctx, c, spec, member); err != nil {
		return "", err
	}
	return email, nil
}

func (c *clients) taskGrants(ctx context.Context, spec provider.StackSpec, member string, declared map[string]*provider.TopicSpec) []func() error {
	tier := spec.Ref.Tier
	return []func() error{
		func() error {
			return c.bindProjectRole(ctx, member, taskRecordsRole, taskDatabaseCondition(c, tier), true)
		},
		func() error {
			return explainMissingGrant(c.bindQueueRoles(ctx, tier, member, queueRoles, queueRoles), queueAdminGrant(c.DelayQueuePath(c.region, tier)))
		},
		func() error {
			own := c.AppAccount(tier, spec.Ref.Project, spec.App.App)
			if err := c.bindAccountRole(ctx, own, runAsRole, member, true); err != nil {
				return wrapAccountGrantError(err, c.AppAccountsRolePath())
			}
			agent, err := c.ReadServiceAgent(ctx, cloudTasksAgentDomain)
			if err != nil {
				return err
			}
			return wrapAccountGrantError(c.bindAccountRole(ctx, own, runAsRole, agent, true), c.AppAccountsRolePath())
		},
		func() error {
			return topics.Topology{Names: taskNames(c.Names, spec.Ref), Topics: declared, Publisher: member}.GrantPublisher(ctx, c.Workload())
		},
	}
}

func (c *clients) refreshGrants(ctx context.Context, tier environment.Tier, member string) []func() error {
	return []func() error{
		func() error {
			err := c.bindAccountRole(ctx, c.RefreshAccount(tier), runAsRole, member, true)
			if absent(err) {
				return refusal.Refuse(refusal.CodeNotReady,
					"the %s tier's bootstrap predates the account Cloud Tasks signs a Next app's page refreshes as.\nRun `%s %s`, then deploy again",
					tier, provider.BootstrapFeaturesCommand(tier), tasksFeature)
			}
			return explainMissingGrant(err, c.AppAccountsRolePath())
		},
		func() error {
			return explainMissingGrant(c.bindQueueRoles(ctx, tier, member, queueRoles, queueRoles),
				queueAdminGrant(c.DelayQueuePath(c.region, tier)))
		},
	}
}

func (c *clients) createAppAccount(ctx context.Context, tier environment.Tier, project, app string) error {
	return c.createAccount(ctx, c.AppAccount(tier, project, app),
		clipped("ocel "+project+"/"+app+" ("+string(tier)+")", maxDisplayNameBytes), appAccountDescription(tier, project, app))
}

func (c *clients) accountExists(ctx context.Context, account string) (bool, error) {
	service, err := c.Accounts()
	if err != nil {
		return false, err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Get(accountPath(c, account)).Context(ctx).Do)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the %s service account: %w", account, err)
	}
	return true, nil
}

func (c *clients) createAccount(ctx context.Context, account, displayName, description string) error {
	found, err := c.accountExists(ctx, account)
	if found || err != nil {
		return explainMissingGrant(err, c.AppAccountsRolePath())
	}
	service, err := c.Accounts()
	if err != nil {
		return err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+c.project, &iam.CreateServiceAccountRequest{
		AccountId:      account,
		ServiceAccount: &iam.ServiceAccount{DisplayName: displayName, Description: description},
	}).Context(ctx).Do)
	switch {
	case err == nil, taken(err):
		return nil
	case answeredCode(err) == http.StatusTooManyRequests || strings.Contains(err.Error(), accountQuotaMessage):
		return refusal.Refuse(refusal.CodeNotReady,
			"project %s has as many service accounts as its Service Account Count quota allows, and every app ocel deploys runs as one of its own.\n"+
				"Delete accounts nothing uses or raise the quota in the console, then deploy again", c.project)
	}
	return explainMissingGrant(fmt.Errorf("create the %s service account: %w", account, err), c.AppAccountsRolePath())
}

func (c *clients) deleteAccount(ctx context.Context, id string) (bool, error) {
	service, err := c.Accounts()
	if err != nil {
		return false, err
	}
	if _, err := attempted(ctx, service.Projects.ServiceAccounts.Delete(accountPath(c, id)).Context(ctx).Do); err != nil {
		if absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete the %s service account: %w", id, err)
	}
	return true, nil
}

func appAccountDescription(tier environment.Tier, project, app string) string {
	return appDescriptionPrefix + app + " of project " + project + appDescriptionRuns + string(tier) + appDescriptionSuffix
}

func clipped(text string, maxBytes int) string {
	for len(text) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

func untilVisible(ctx context.Context, grant func() error) error {
	var err error
	for attempt := range appAccountVisibleAttempts {
		if attempt > 0 && !waitedFor(ctx, attempt, waitCeiling) {
			return ctx.Err()
		}
		if err = grant(); !isUnseenAccount(err) {
			return err
		}
	}
	return err
}

var errUnseenAccount = errors.New("the service account is not readable yet")

func wrapAccountGrantError(err error, grant string) error {
	if absent(err) {
		return fmt.Errorf("%w: %w", errUnseenAccount, err)
	}
	return explainMissingGrant(err, grant)
}

func isUnseenAccount(err error) bool {
	if errors.Is(err, errUnseenAccount) {
		return true
	}
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		return false
	}
	return answeredCode(err) == http.StatusBadRequest || status.Code(err) == codes.InvalidArgument
}

func explainMissingGrant(err error, grant string) error {
	if err == nil {
		return nil
	}
	var coded interface{ GRPCStatus() *status.Status }
	if answeredCode(err) == http.StatusForbidden || errors.As(err, &coded) && coded.GRPCStatus().Code() == codes.PermissionDenied {
		return fmt.Errorf("the deploy credential lacks %s: %w", grant, err)
	}
	return err
}
