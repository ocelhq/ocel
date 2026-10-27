package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

const (
	identityProviderComponentTagKey   = "ocel:component"
	identityProviderComponentTagValue = provider.ConsoleTrustPart
)

type IdentityProviderAPI interface {
	GetOpenIDConnectProvider(ctx context.Context, in *iam.GetOpenIDConnectProviderInput, optFns ...func(*iam.Options)) (*iam.GetOpenIDConnectProviderOutput, error)
	CreateOpenIDConnectProvider(ctx context.Context, in *iam.CreateOpenIDConnectProviderInput, optFns ...func(*iam.Options)) (*iam.CreateOpenIDConnectProviderOutput, error)
	AddClientIDToOpenIDConnectProvider(ctx context.Context, in *iam.AddClientIDToOpenIDConnectProviderInput, optFns ...func(*iam.Options)) (*iam.AddClientIDToOpenIDConnectProviderOutput, error)
	RemoveClientIDFromOpenIDConnectProvider(ctx context.Context, in *iam.RemoveClientIDFromOpenIDConnectProviderInput, optFns ...func(*iam.Options)) (*iam.RemoveClientIDFromOpenIDConnectProviderOutput, error)
	DeleteOpenIDConnectProvider(ctx context.Context, in *iam.DeleteOpenIDConnectProviderInput, optFns ...func(*iam.Options)) (*iam.DeleteOpenIDConnectProviderOutput, error)
}

type trustedIssuer struct {
	host     string
	audience string
}

func trustedIssuerOf(trust provider.WebIdentityTrust) (trustedIssuer, error) {
	host, err := issuerHost(trust.Issuer)
	if err != nil {
		return trustedIssuer{}, err
	}
	return trustedIssuer{host: host, audience: trust.Audience}, nil
}

type recordedIssuer struct {
	named string
	trustedIssuer
}

func issuerRecordedIn(outputs map[string]string) (recordedIssuer, bool) {
	named := outputs[outputConsoleIdentityProviderARN]
	parsed, err := arn.Parse(named)
	if err != nil {
		return recordedIssuer{}, false
	}
	_, host, _ := strings.Cut(parsed.Resource, "/")
	return recordedIssuer{named: named, trustedIssuer: trustedIssuer{host: host, audience: outputs[outputTrustedAudience]}}, true
}

func identityProviderARN(deployed Deployed, host string) (string, bool) {
	parsed, err := arn.Parse(deployed.AppBoundaryARN)
	if err != nil || parsed.AccountID == "" {
		return "", false
	}
	return "arn:" + parsed.Partition + ":iam::" + parsed.AccountID + ":oidc-provider/" + host, true
}

type identityProvider struct {
	clientIDs []string
	madeBy    string
}

func readIdentityProvider(ctx context.Context, api IdentityProviderAPI, named string) (*identityProvider, error) {
	out, err := api.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(named)})
	var missing *iamtypes.NoSuchEntityException
	if errors.As(err, &missing) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read identity provider %s: %w", named, err)
	}
	found := &identityProvider{clientIDs: out.ClientIDList}
	for _, tag := range out.Tags {
		if aws.ToString(tag.Key) == naming.ManagedByTagKey {
			found.madeBy = aws.ToString(tag.Value)
		}
	}
	return found, nil
}

func ensureTrustedIssuer(ctx context.Context, d stepDeps) error {
	if d.trust == nil {
		return nil
	}
	issuer, err := trustedIssuerOf(*d.trust)
	if err != nil {
		return err
	}
	named, known := identityProviderARN(d.deployed, issuer.host)
	if !known {
		return fmt.Errorf("the %s bootstrap names no account, so identity provider %s cannot be addressed", d.tier, issuer.host)
	}
	if err := ensureIdentityProvider(ctx, d, named, issuer); err != nil {
		return err
	}
	return releaseIssuer(ctx, d, &issuer)
}

func ensureIdentityProvider(ctx context.Context, d stepDeps, named string, issuer trustedIssuer) error {
	found, err := readIdentityProvider(ctx, d.identityProviders, named)
	if err != nil {
		return err
	}
	if found == nil {
		d.progress.Say("Creating identity provider " + issuer.host)
		_, err := d.identityProviders.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
			Url:          aws.String("https://" + issuer.host),
			ClientIDList: []string{issuer.audience},
			Tags: []iamtypes.Tag{
				{Key: aws.String(naming.ManagedByTagKey), Value: aws.String(string(d.ns))},
				{Key: aws.String(identityProviderComponentTagKey), Value: aws.String(identityProviderComponentTagValue)},
			},
		})
		var exists *iamtypes.EntityAlreadyExistsException
		if !errors.As(err, &exists) {
			if err != nil {
				return fmt.Errorf("create identity provider %s: %w", issuer.host, err)
			}
			return nil
		}
		if found, err = readIdentityProvider(ctx, d.identityProviders, named); err != nil || found == nil {
			return err
		}
	}
	if slices.Contains(found.clientIDs, issuer.audience) {
		d.progress.Debug("Reused identity provider " + issuer.host)
		return nil
	}
	d.progress.Say(fmt.Sprintf("Adding audience %s to identity provider %s", issuer.audience, issuer.host))
	if _, err := d.identityProviders.AddClientIDToOpenIDConnectProvider(ctx, &iam.AddClientIDToOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(named),
		ClientID:                 aws.String(issuer.audience),
	}); err != nil {
		return fmt.Errorf("add audience %s to identity provider %s: %w", issuer.audience, issuer.host, err)
	}
	return nil
}

func plannedTrustedIssuer(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, req Request) ([]provider.Change, error) {
	if req.Trust == nil {
		return nil, nil
	}
	issuer, err := trustedIssuerOf(*req.Trust)
	if err != nil {
		return nil, err
	}
	deployed, _, _, err := readCore(ctx, apis.Stacks, ns, tier)
	if err != nil {
		return nil, err
	}
	change := provider.Change{Kind: identityProviderKind, Name: issuer.host, Action: provider.ActionCreate}
	if named, known := identityProviderARN(deployed, issuer.host); known {
		found, err := readIdentityProvider(ctx, apis.IdentityProviders, named)
		if err != nil {
			return nil, err
		}
		switch {
		case found == nil:
		case slices.Contains(found.clientIDs, issuer.audience):
			change.Action, change.Reason = provider.ActionKeep, paramCurrent
		default:
			change.Action, change.Reason = provider.ActionUpdate, "adds audience "+issuer.audience
		}
	}
	changes := []provider.Change{change}
	released, err := plannedRelease(ctx, apis, ns, tier, &issuer)
	if err != nil || released == nil {
		return changes, err
	}
	return append(changes, *released), nil
}

type issuerRelease struct {
	recordedIssuer
	whole bool
}

func findIssuerRelease(ctx context.Context, stacks cfn.StacksAPI, api IdentityProviderAPI, ns Namespace, tier environment.Tier, kept *trustedIssuer) (*issuerRelease, error) {
	current, err := cfn.StackOutputs(ctx, stacks, ns.partStackName(provider.ConsoleTrustPart, tier))
	if err != nil {
		return nil, err
	}
	recorded, found := issuerRecordedIn(current)
	if !found {
		return nil, nil
	}
	sibling, err := cfn.StackOutputs(ctx, stacks, ns.partStackName(provider.ConsoleTrustPart, tier.Sibling()))
	if err != nil {
		return nil, err
	}
	var referenced []trustedIssuer
	if kept != nil {
		referenced = append(referenced, *kept)
	}
	if siblings, found := issuerRecordedIn(sibling); found {
		referenced = append(referenced, siblings.trustedIssuer)
	}
	if slices.Contains(referenced, recorded.trustedIssuer) {
		return nil, nil
	}
	held, err := readIdentityProvider(ctx, api, recorded.named)
	if err != nil || held == nil || held.madeBy != string(ns) {
		return nil, err
	}
	whole := !slices.ContainsFunc(referenced, func(issuer trustedIssuer) bool { return issuer.host == recorded.host })
	if !whole && !slices.Contains(held.clientIDs, recorded.audience) {
		return nil, nil
	}
	return &issuerRelease{recordedIssuer: recorded, whole: whole}, nil
}

func releaseIssuer(ctx context.Context, d stepDeps, kept *trustedIssuer) error {
	release, err := findIssuerRelease(ctx, d.stacks, d.identityProviders, d.ns, d.tier, kept)
	if err != nil || release == nil {
		return err
	}
	var missing *iamtypes.NoSuchEntityException
	if release.whole {
		d.progress.Say("Deleting identity provider " + release.host)
		_, err := d.identityProviders.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(release.named)})
		if err != nil && !errors.As(err, &missing) {
			return fmt.Errorf("delete identity provider %s: %w", release.host, err)
		}
		return nil
	}
	d.progress.Say(fmt.Sprintf("Removing audience %s from identity provider %s", release.audience, release.host))
	_, err = d.identityProviders.RemoveClientIDFromOpenIDConnectProvider(ctx, &iam.RemoveClientIDFromOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(release.named),
		ClientID:                 aws.String(release.audience),
	})
	if err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("remove audience %s from identity provider %s: %w", release.audience, release.host, err)
	}
	return nil
}

func releaseTrustedIssuer(ctx context.Context, d stepDeps) error {
	return releaseIssuer(ctx, d, nil)
}

func plannedRelease(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, kept *trustedIssuer) (*provider.Change, error) {
	release, err := findIssuerRelease(ctx, apis.Stacks, apis.IdentityProviders, ns, tier, kept)
	if err != nil || release == nil {
		return nil, err
	}
	change := provider.Change{Kind: identityProviderKind, Name: release.host, Action: provider.ActionDelete}
	if !release.whole {
		change.Action, change.Reason = provider.ActionUpdate, "drops audience "+release.audience
	}
	return &change, nil
}

func plannedIssuerRelease(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier) ([]provider.Change, error) {
	released, err := plannedRelease(ctx, apis, ns, tier, nil)
	if err != nil || released == nil {
		return nil, err
	}
	return []provider.Change{*released}, nil
}
