package gcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/api/iam/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
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

	appDescriptionPrefix      = "the identity app "
	appDescriptionProject     = " of project "
	appDescriptionEnvironment = " runs as in environment "
	appDescriptionTier        = " of the "
	appDescriptionSuffix      = " tier"
)

var appGrantedRoles = []string{appRecordsRole, taskRecordsRole, appOpeningRole, appObjectsRole, appAssetsRole}

func listGrantedRoles(names Names) []string {
	return append(slices.Clone(appGrantedRoles), names.CDNPurgeRolePath())
}

func (p *Provider) ensureAppAccount(ctx context.Context, c *clients, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (string, error) {
	ref, app := spec.Ref, spec.App.App
	account := c.AppAccount(ref.Tier, ref.Project, ref.Name.Env, app)
	if err := c.createAppAccount(ctx, ref, app); err != nil {
		return "", err
	}
	email := c.AppAccountEmail(ref.Tier, ref.Project, ref.Name.Env, app)
	referenced, err := p.referencedOwners(ctx, spec)
	if err != nil {
		return "", err
	}
	want, err := c.appGrants(ctx, spec, account, "serviceAccount:"+email, declared, referenced)
	if err != nil {
		return "", err
	}
	if err := p.reconcileAppGrants(ctx, c, ref.Tier, account, want); err != nil {
		return "", err
	}
	return email, nil
}

func (c *clients) appGrants(ctx context.Context, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec, referenced []string) ([]appGrant, error) {
	var grants []appGrant
	for _, project := range append([]string{spec.Ref.Project}, referenced...) {
		grants = append(grants,
			onProject(appRecordsRole, member, projectDatabaseCondition(c, spec.Ref.Tier, project)),
			onProject(appOpeningRole, member, projectKeyCondition(c, spec.Ref.Tier, project)))
	}
	if reachesTopics(spec.App) {
		tasks, err := c.taskGrants(ctx, spec, account, member, declared)
		if err != nil {
			return nil, err
		}
		grants = append(grants, tasks...)
	}
	grants = append(grants, bucketGrants(spec, account, member)...)
	cache, err := c.cacheGrants(spec, member)
	if err != nil {
		return nil, err
	}
	grants = append(grants, cache...)
	assets, err := c.assetsGrants(spec, member)
	if err != nil {
		return nil, err
	}
	grants = append(grants, assets...)
	grants = append(grants, c.cdnPurgeGrants(spec, member)...)
	return append(grants, c.gateGrants(spec, member)...), nil
}

func (c *clients) taskGrants(ctx context.Context, spec provider.StackSpec, account, member string, declared map[string]*provider.TopicSpec) ([]appGrant, error) {
	agent, err := c.ReadServiceAgent(ctx, cloudTasksAgentDomain)
	if err != nil {
		return nil, err
	}
	grants := []appGrant{
		onProject(taskRecordsRole, member, taskDatabaseCondition(c, spec.Ref.Tier)),
		on(targetQueue, c.DelayQueuePath(c.region, spec.Ref.Tier), queueEnqueuerRole, member),
		on(targetAccount, account, runAsRole, member),
		on(targetAccount, account, runAsRole, agent),
	}
	names := taskNames(c.Names, spec.Ref)
	for _, name := range slices.Sorted(maps.Keys(declared)) {
		grants = append(grants, on(targetTopic, topics.TopicPath(c.project, names.Topic(name)), topics.PublisherRole, member))
	}
	return grants, nil
}

func (c *clients) createAppAccount(ctx context.Context, ref provider.StackRef, app string) error {
	return c.createAccount(ctx, c.AppAccount(ref.Tier, ref.Project, ref.Name.Env, app),
		clipped("ocel "+ref.Project+"/"+app+"@"+ref.Name.Env+" ("+string(ref.Tier)+")", maxDisplayNameBytes),
		appAccountDescription(ref.Tier, ref.Project, ref.Name.Env, app))
}

func (c *clients) accountExists(ctx context.Context, account string) (bool, error) {
	service, err := c.Accounts()
	if err != nil {
		return false, err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Get(accountPath(c, account)).Context(ctx).Do)
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
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+c.project, &iam.CreateServiceAccountRequest{
		AccountId:      account,
		ServiceAccount: &iam.ServiceAccount{DisplayName: displayName, Description: description},
	}).Context(ctx).Do)
	switch {
	case err == nil, taken(err):
		return nil
	case googleretry.AnsweredCode(err) == http.StatusTooManyRequests || strings.Contains(err.Error(), accountQuotaMessage):
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
	if _, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Delete(accountPath(c, id)).Context(ctx).Do); err != nil {
		if absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete the %s service account: %w", id, err)
	}
	return true, nil
}

func appAccountDescription(tier environment.Tier, project, env, app string) string {
	return appDescriptionPrefix + app + appDescriptionProject + project + appDescriptionEnvironment + env + appDescriptionTier + string(tier) + appDescriptionSuffix
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
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, waitCeiling) {
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
	return googleretry.AnsweredCode(err) == http.StatusBadRequest || status.Code(err) == codes.InvalidArgument
}

func explainMissingGrant(err error, grant string) error {
	if err == nil {
		return nil
	}
	var coded interface{ GRPCStatus() *status.Status }
	if googleretry.AnsweredCode(err) == http.StatusForbidden || errors.As(err, &coded) && coded.GRPCStatus().Code() == codes.PermissionDenied {
		return fmt.Errorf("the deploy credential lacks %s: %w", grant, err)
	}
	return err
}
