package apigateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *provider) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", fmt.Errorf("the %q edge serves previews on a wildcard domain name; this reconcile names no base domain", Kind)
	}
	if spec.Certificate == "" {
		return "", fmt.Errorf("the %q edge terminates TLS for %s at API Gateway, so the domain name needs the wildcard certificate; this reconcile carries none", Kind, wildcard)
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return "", err
	}
	notFound, found, err := findAPI(ctx, c, notFoundAPIName(edge.ClassPreview))
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("the preview bootstrap has no not-found API, so nothing would answer a hostname on %s that no preview claims; run `ocel bootstrap --preview` first", wildcard)
	}
	front, err := ensurePreviewDomain(ctx, c, wildcard, spec.Certificate)
	if err != nil {
		return "", err
	}
	if err := putHostRule(ctx, c, wildcard, anyHost, notFound, catchAllPriority); err != nil {
		return "", err
	}
	return front, nil
}

func (p *provider) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	rules, _, err := routingRules(ctx, c, wildcard)
	if err != nil {
		errs = append(errs, err)
	}
	for _, rule := range rules {
		if err := deleteRule(ctx, c, wildcard, aws.ToString(rule.RoutingRuleId), ruleHost(rule)); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := c.APIGateway.DeleteDomainName(ctx, &apigateway.DeleteDomainNameInput{
		DomainName: aws.String(wildcard),
	}); err != nil && !isNotFound(err) {
		errs = append(errs, fmt.Errorf("delete the API Gateway domain name for %s: %w", wildcard, err))
	}
	return errors.Join(errs...)
}

func ensurePreviewDomain(ctx context.Context, c Clients, wildcard, certificate string) (string, error) {
	held, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{
		DomainName: aws.String(wildcard),
	})
	if err == nil {
		return convergePreviewDomain(ctx, c, wildcard, certificate, held)
	}
	if !isNotFound(err) {
		return "", fmt.Errorf("read the API Gateway domain name for %s: %w", wildcard, err)
	}
	created, err := c.APIGateway.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:             aws.String(wildcard),
		RegionalCertificateArn: aws.String(certificate),
		SecurityPolicy:         agtypes.SecurityPolicyTls12,
		RoutingMode:            agtypes.RoutingModeRoutingRuleOnly,
		EndpointConfiguration: &agtypes.EndpointConfiguration{
			Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create the API Gateway domain name for %s: %w", wildcard, err)
	}
	return aws.ToString(created.RegionalDomainName), nil
}

func convergePreviewDomain(ctx context.Context, c Clients, wildcard, certificate string, held *apigateway.GetDomainNameOutput) (string, error) {
	var patch []agtypes.PatchOperation
	if aws.ToString(held.RegionalCertificateArn) != certificate {
		patch = append(patch, agtypes.PatchOperation{
			Op:    agtypes.OpReplace,
			Path:  aws.String("/regionalCertificateArn"),
			Value: aws.String(certificate),
		})
	}
	if held.RoutingMode != agtypes.RoutingModeRoutingRuleOnly {
		patch = append(patch, agtypes.PatchOperation{
			Op:    agtypes.OpReplace,
			Path:  aws.String("/routingMode"),
			Value: aws.String(string(agtypes.RoutingModeRoutingRuleOnly)),
		})
	}
	if len(patch) == 0 {
		return aws.ToString(held.RegionalDomainName), nil
	}
	updated, err := c.APIGateway.UpdateDomainName(ctx, &apigateway.UpdateDomainNameInput{
		DomainName:      aws.String(wildcard),
		PatchOperations: patch,
	})
	if err != nil {
		return "", fmt.Errorf("move the API Gateway domain name for %s onto the certificate this bootstrap holds and the routing mode every preview rule needs: %w", wildcard, err)
	}
	return aws.ToString(updated.RegionalDomainName), nil
}
