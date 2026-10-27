package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var githubRemote = regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

var symbolicHead = regexp.MustCompile(`(?m)^ref: refs/heads/(\S+)\s+HEAD$`)

var errConsoleUnreachable = errors.New("no console session can reach the console")

type consoleSession struct {
	apiURL  string
	client  *console.Client
	session string
}

type consoleTrust struct {
	consoleSession
	projectID string
	trust     *contractv1.WebIdentityTrust
}

func readConsoleSession(invocation commands.Invocation) *consoleSession {
	if invocation.Console.LoadCredentials == nil {
		return nil
	}
	credentials, err := invocation.Console.LoadCredentials()
	if err != nil {
		return nil
	}
	apiURL := console.BaseURL(credentials.APIURL)
	return &consoleSession{apiURL: apiURL, client: console.New(apiURL), session: credentials.AccessToken}
}

func readConsoleTrust(ctx context.Context, invocation commands.Invocation, p commands.ProviderRun, tier environmentv1.Tier, policy consent.Policy, removing []string, recorded *contractv1.WebIdentityTrust, span *run.Span) (*consoleTrust, error) {
	taken := p.Provider.Facts().GetWebIdentity()
	if taken == nil || slices.Contains(removing, provider.ConsoleTrustPart) {
		return nil, nil
	}
	session := readConsoleSession(invocation)
	if session == nil {
		return nil, nil
	}
	link, err := console.ReadLink(p.Project.Dir, session.apiURL)
	if err != nil || link == nil {
		return nil, nil
	}
	name := readiness.TierName(tier)
	if policy.DryRun {
		if recorded == nil {
			span.Say(fmt.Sprintf("%s is added on the real run, once the console names the target.", provider.ConsoleTrustPart))
			return nil, nil
		}
		return &consoleTrust{trust: recorded}, nil
	}
	question := fmt.Sprintf("Let console %s (organization %s) deploy %s here?", hostOf(session.apiURL), session.organizationName(ctx, link.OrganizationID), name)
	if granted, err := offer(ctx, policy, span, question); err != nil || !granted {
		return nil, err
	}
	identity := p.Preflight.Response.GetIdentity()
	prepared, err := session.client.PrepareTarget(ctx, session.session, &consolev1.PrepareTargetRequest{
		Vendor:    taken.GetVendor(),
		Account:   identity.GetAccount(),
		Region:    identity.GetLocation(),
		ProjectId: link.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("the console prepared no target for account %s: %w", identity.GetAccount(), err)
	}
	trusted := &consoleTrust{consoleSession: *session, projectID: link.ProjectID, trust: &contractv1.WebIdentityTrust{
		Issuer:         prepared.GetIssuer(),
		Audience:       prepared.GetAudience(),
		OrganizationId: prepared.GetOrganizationId(),
		TargetId:       prepared.GetTargetId(),
	}}
	trusted.trust.Github, err = chooseGitHubTrust(ctx, p.Project, tier, policy, recorded.GetGithub(), span)
	return trusted, err
}

func chooseGitHubTrust(ctx context.Context, cfg *project.Project, tier environmentv1.Tier, policy consent.Policy, recorded *contractv1.GitHubTrust, span *run.Span) (*contractv1.GitHubTrust, error) {
	if !policy.IsAsking() {
		return recorded, nil
	}
	if recorded != nil {
		question := fmt.Sprintf("Stop letting GitHub Actions in %s%s assume the %s deploy identity directly?", recorded.GetRepository(), triggerOf(tier, recorded.GetBranch()), readiness.TierName(tier))
		stop, err := policy.Offer(ctx, span, question)
		if err != nil || stop {
			return nil, err
		}
		return recorded, nil
	}
	repository, branch, found := readGitHubOrigin(cfg, tier)
	if !found {
		return nil, nil
	}
	question := fmt.Sprintf("Also let GitHub Actions in %s%s assume the %s deploy identity directly while the console is unreachable, with the cloud credentials the workflow configures itself?", repository, triggerOf(tier, branch), readiness.TierName(tier))
	granted, err := policy.Offer(ctx, span, question)
	if err != nil || !granted {
		return nil, err
	}
	trust := &contractv1.GitHubTrust{Repository: repository}
	if tier == environmentv1.Tier_TIER_PRODUCTION {
		trust.Branch = branch
	}
	return trust, nil
}

func triggerOf(tier environmentv1.Tier, branch string) string {
	if tier == environmentv1.Tier_TIER_PRODUCTION {
		return " on branch " + branch
	}
	return " for pull requests"
}

func (c *consoleTrust) record(ctx context.Context, span *run.Span, p commands.ProviderRun, tier environmentv1.Tier) error {
	if c.client == nil {
		return nil
	}
	described, err := describeBootstrap(ctx, span, p.Provider, p.Project, tier)
	if err != nil {
		return err
	}
	identity := described.GetBootstrap().GetIdentity()
	if identity == nil {
		return errors.New("the bootstrap names no identity the console's trust made, so there is nothing to record in the console")
	}
	err = c.client.RecordTargetTier(ctx, c.session, &consolev1.RecordTargetTierRequest{
		TargetId:   c.trust.GetTargetId(),
		Tier:       tier,
		Identity:   &consolev1.TierIdentity{IdentityProvider: identity.GetIdentityProvider(), Principal: identity.GetPrincipal()},
		CliVersion: version.Version,
		ProjectId:  c.projectID,
	})
	if err != nil {
		return fmt.Errorf("the console recorded no %s identity: %w", readiness.TierName(tier), err)
	}
	return nil
}

func forgetConsoleTrust(ctx context.Context, invocation commands.Invocation, recorded *contractv1.WebIdentityTrust, tier environmentv1.Tier, policy consent.Policy, span *run.Span) error {
	if recorded == nil || policy.DryRun {
		return nil
	}
	err := errConsoleUnreachable
	if session := readConsoleSession(invocation); session != nil {
		err = session.client.ForgetTargetTier(ctx, session.session, &consolev1.ForgetTargetTierRequest{TargetId: recorded.GetTargetId(), Tier: tier})
	}
	if err == nil {
		return nil
	}
	name := readiness.TierName(tier)
	finding := fmt.Sprintf("The console could not be told to stop issuing %s credentials (%v), so it keeps issuing them for a deploy identity this removes.", name, err)
	if policy.Yes {
		span.Warn(finding)
		return nil
	}
	if !policy.IsAsking() {
		return fmt.Errorf("%s Log in with `ocel login` and run again, or pass --yes to remove it anyway", finding)
	}
	span.Warn(finding)
	proceed, err := policy.Offer(ctx, span, fmt.Sprintf("Remove the %s deploy identity anyway?", name))
	if err != nil {
		return err
	}
	if !proceed {
		return fmt.Errorf("the %s deploy identity stays: the console still issues credentials for it", name)
	}
	return nil
}

func offer(ctx context.Context, policy consent.Policy, span *run.Span, question string) (bool, error) {
	if !policy.Yes && !policy.IsAsking() {
		return false, nil
	}
	return policy.Offer(ctx, span, question)
}

func (s *consoleSession) organizationName(ctx context.Context, id string) string {
	organizations, err := s.client.ListOrganizations(ctx, s.session)
	if err != nil {
		return id
	}
	if found := commands.FindOrganization(organizations, func(org console.Organization) bool { return org.ID == id }); found != nil {
		return found.Name
	}
	return id
}

func hostOf(apiURL string) string {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" {
		return apiURL
	}
	return parsed.Host
}

func readGitHubOrigin(cfg *project.Project, tier environmentv1.Tier) (repository, branch string, found bool) {
	remote, err := exec.Command("git", "-C", cfg.Dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", "", false
	}
	matched := githubRemote.FindStringSubmatch(strings.TrimSpace(string(remote)))
	if matched == nil {
		return "", "", false
	}
	if tier != environmentv1.Tier_TIER_PRODUCTION {
		return matched[1], "", true
	}
	head, err := exec.Command("git", "-C", cfg.Dir, "ls-remote", "--symref", "origin", "HEAD").Output()
	if err != nil {
		return "", "", false
	}
	branch, found = defaultBranchOf(string(head))
	return matched[1], branch, found
}

func defaultBranchOf(lsRemote string) (string, bool) {
	matched := symbolicHead.FindStringSubmatch(lsRemote)
	if matched == nil {
		return "", false
	}
	return matched[1], true
}
