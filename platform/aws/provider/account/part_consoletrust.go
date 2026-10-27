package account

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const (
	deploySessionSeconds = 14400

	outputDeployRoleARN              = "DeployRoleArn"
	outputConsoleIdentityProviderARN = "ConsoleIdentityProviderArn"
	outputTrustedIssuer              = "TrustedIssuer"
	outputTrustedAudience            = "TrustedAudience"
	outputTrustedOrganization        = "TrustedOrganization"
	outputTrustedTarget              = "TrustedTarget"
	assumeRoleWithWebIdentity        = "sts:AssumeRoleWithWebIdentity"
	identityProviderARNInTemplate    = "arn:${AWS::Partition}:iam::${AWS::AccountId}:oidc-provider/"
	identityProviderKind             = "AWS::IAM::OIDCProvider"
	identityProviderARNPrefix        = "arn:aws:iam::*:oidc-provider/"
)

var consoleTrustPart = part{
	name:        provider.ConsoleTrustPart,
	summary:     "the role a console's identity tokens assume to deploy here, holding exactly the deploy permissions",
	free:        true,
	template:    consoleTrustTemplate,
	before:      ensureTrustedIssuer,
	beforePlan:  plannedTrustedIssuer,
	release:     releaseTrustedIssuer,
	releasePlan: plannedIssuerRelease,
}

func refuseUntrusted(tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s %s part records no console to trust: log in to the console and bootstrap again", tier, provider.ConsoleTrustPart)
}

func consoleTrustTemplate(in partInputs) (partStack, error) {
	if in.trust == nil {
		return partStack{}, refuseUntrusted(in.tier)
	}
	trust, err := trustStatements(in.tier, *in.trust)
	if err != nil {
		return partStack{}, err
	}
	path := in.ns.bootstrapRolePathFor(in.tier)
	stem := in.ns.deployPolicyStem(in.tier)
	resources := make([]cfn.Resource, 0, len(in.deploy)+1)
	attached := make([]any, 0, len(in.deploy))
	for i, policy := range in.deploy {
		id := "DeployPolicy" + strconv.Itoa(i+1)
		resources = append(resources, cfn.Resource{
			ID:   id,
			Type: "AWS::IAM::ManagedPolicy",
			Properties: map[string]any{
				"ManagedPolicyName": stem + "-" + strconv.Itoa(i+1),
				"Path":              path,
				"PolicyDocument":    policy.PolicyDocument,
			},
		})
		attached = append(attached, cfn.Ref(id))
	}
	resources = append(resources, cfn.Resource{
		ID:   "DeployRole",
		Type: "AWS::IAM::Role",
		Metadata: cfn.Metadata{
			Description: "The role a deploy of the " + string(in.tier) + " tier assumes with a console's identity token; it holds what `ocel permissions deploy` prints and nothing more.",
		},
		Properties: map[string]any{
			"RoleName":                 in.ns.DeployRoleName(in.tier),
			"Path":                     path,
			"MaxSessionDuration":       deploySessionSeconds,
			"AssumeRolePolicyDocument": iam.Document(trust),
			"ManagedPolicyArns":        attached,
		},
	})
	template := cfn.Template{
		Description: "Ocel bootstrap part (" + provider.ConsoleTrustPart + ", " + string(in.tier) + ") - the role a console's identity tokens assume to deploy into this tier.",
		Resources:   resources,
		Outputs:     consoleTrustOutputs(*in.trust),
	}
	body, err := template.CompactBody()
	if err != nil {
		return partStack{}, err
	}
	if len(body) > cfn.TemplateBodyBytes {
		return partStack{}, fmt.Errorf("the %s template is %d bytes, over the %d CloudFormation takes inline", provider.ConsoleTrustPart, len(body), cfn.TemplateBodyBytes)
	}
	return partStack{template: template, body: body}, nil
}

func consoleSubject(tier environment.Tier, trust provider.WebIdentityTrust) string {
	return "org:" + trust.OrganizationID + ":target:" + trust.TargetID + ":tier:" + string(tier) + ":project:*"
}

func trustStatements(tier environment.Tier, trust provider.WebIdentityTrust) ([]iam.Statement, error) {
	host, err := issuerHost(trust.Issuer)
	if err != nil {
		return nil, err
	}
	if trust.Audience == "" || trust.OrganizationID == "" || trust.TargetID == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the console's trust names no audience, organization or target, so no token could be matched to it")
	}
	return []iam.Statement{{
		Principal: iam.Principal{Federated: identityProviderInTemplate(host)},
		Actions:   []string{assumeRoleWithWebIdentity},
		Condition: map[string]any{
			"StringEquals": map[string]any{host + ":aud": trust.Audience},
			"StringLike":   map[string]any{host + ":sub": consoleSubject(tier, trust)},
		},
	}}, nil
}

func issuerHost(issuer string) (string, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", refusal.Refuse(refusal.CodeInvalid, "console issuer %q is not an https URL", issuer)
	}
	return parsed.Host + strings.TrimRight(parsed.EscapedPath(), "/"), nil
}

func identityProviderInTemplate(host string) cfn.Intrinsic {
	return cfn.Sub(identityProviderARNInTemplate + host)
}

func consoleTrustOutputs(trust provider.WebIdentityTrust) []cfn.Output {
	host, _ := issuerHost(trust.Issuer)
	return []cfn.Output{
		{Name: outputDeployRoleARN, Description: "The role a console's identity token assumes to deploy into this tier.", Value: cfn.GetAtt("DeployRole", "Arn")},
		{Name: outputConsoleIdentityProviderARN, Description: "The identity provider that role trusts for the console's tokens.", Value: identityProviderInTemplate(host)},
		{Name: outputTrustedIssuer, Description: "The console issuer this tier trusts.", Value: trust.Issuer},
		{Name: outputTrustedAudience, Description: "The audience a trusted token carries.", Value: trust.Audience},
		{Name: outputTrustedOrganization, Description: "The console organization whose tokens this tier trusts.", Value: trust.OrganizationID},
		{Name: outputTrustedTarget, Description: "The console target whose tokens this tier trusts.", Value: trust.TargetID},
	}
}

func recordedTrust(outputs map[string]string) *provider.WebIdentityTrust {
	if outputs[outputTrustedIssuer] == "" {
		return nil
	}
	return &provider.WebIdentityTrust{
		Issuer:         outputs[outputTrustedIssuer],
		Audience:       outputs[outputTrustedAudience],
		OrganizationID: outputs[outputTrustedOrganization],
		TargetID:       outputs[outputTrustedTarget],
	}
}

func (d Deployed) Identity() *provider.TierIdentity {
	if !d.Parts.Has(provider.ConsoleTrustPart) {
		return nil
	}
	role, issuer := d.Outputs[outputDeployRoleARN], d.Outputs[outputConsoleIdentityProviderARN]
	if role == "" || issuer == "" {
		return nil
	}
	return &provider.TierIdentity{IdentityProvider: issuer, Principal: role}
}

func (d Deployed) Trust() *provider.WebIdentityTrust {
	if !d.Parts.Has(provider.ConsoleTrustPart) {
		return nil
	}
	return recordedTrust(d.Outputs)
}

func readTrustToRender(ctx context.Context, stacks cfn.StacksAPI, ns Namespace, tier environment.Tier, req Request) (*provider.WebIdentityTrust, error) {
	if req.Trust != nil || !slices.Contains(req.Parts, provider.ConsoleTrustPart) {
		return req.Trust, nil
	}
	outputs, err := cfn.StackOutputs(ctx, stacks, ns.partStackName(provider.ConsoleTrustPart, tier))
	if err != nil {
		return nil, err
	}
	return recordedTrust(outputs), nil
}
