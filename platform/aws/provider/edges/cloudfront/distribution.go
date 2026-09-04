package cloudfront

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const (
	assetOriginID = "assets"

	maxDistributionNameLen = 128
)

func capDistributionName(name string) string {
	if len(name) <= maxDistributionNameLen {
		return name
	}
	return strings.ToValidUTF8(name[:maxDistributionNameLen], "")
}

type front struct {
	id         string
	domainName string
}

type distributionPlan struct {
	name          string
	assetOrigin   string
	function      string
	cachePolicy   string
	headersPolicy string
	oac           string
}

type distributionSummary struct {
	id         string
	domainName string
	comment    string
	aliases    map[string]bool
}

func (p distributionPlan) ready() error {
	missing := []string{}
	for name, value := range map[string]string{
		"asset bucket":            p.assetOrigin,
		"resolver function":       p.function,
		"cache policy":            p.cachePolicy,
		"response headers policy": p.headersPolicy,
		"origin access control":   p.oac,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return fmt.Errorf("the stack for %s names no %s, so there is nothing to build a distribution out of; reconcile it before promoting into it", p.name, strings.Join(missing, ", no "))
}

func (p distributionPlan) config(aliases []string, certificate string) *cftypes.DistributionConfig {
	slices.Sort(aliases)
	config := &cftypes.DistributionConfig{
		CallerReference:              aws.String(capDistributionName(p.name)),
		Comment:                      aws.String(capDistributionName(p.name)),
		Enabled:                      ptr(true),
		HttpVersion:                  cftypes.HttpVersionHttp2and3,
		IsIPV6Enabled:                ptr(true),
		PriceClass:                   cftypes.PriceClassPriceClassAll,
		CacheTagConfig:               &cftypes.CacheTagConfig{HeaderName: aws.String(bootstrap.EdgeCacheTagHeader)},
		DefaultRootObject:            aws.String(""),
		WebACLId:                     aws.String(""),
		ContinuousDeploymentPolicyId: aws.String(""),
		Staging:                      ptr(false),
		Logging: &cftypes.LoggingConfig{
			Enabled:        ptr(false),
			IncludeCookies: ptr(false),
			Bucket:         aws.String(""),
			Prefix:         aws.String(""),
		},
		Restrictions: &cftypes.Restrictions{
			GeoRestriction: &cftypes.GeoRestriction{
				RestrictionType: cftypes.GeoRestrictionTypeNone,
				Quantity:        ptr(int32(0)),
				Items:           []string{},
			},
		},
		CustomErrorResponses: &cftypes.CustomErrorResponses{Quantity: ptr(int32(0))},
		CacheBehaviors:       &cftypes.CacheBehaviors{Quantity: ptr(int32(0))},
		OriginGroups:         &cftypes.OriginGroups{Quantity: ptr(int32(0))},
		Aliases:              &cftypes.Aliases{Quantity: quantity(aliases), Items: aliases},
		Origins: &cftypes.Origins{
			Quantity: ptr(int32(1)),
			Items: []cftypes.Origin{{
				Id:                    aws.String(assetOriginID),
				DomainName:            aws.String(p.assetOrigin),
				OriginAccessControlId: aws.String(p.oac),
				S3OriginConfig:        &cftypes.S3OriginConfig{OriginAccessIdentity: aws.String("")},
				OriginPath:            aws.String(""),
				CustomHeaders:         &cftypes.CustomHeaders{Quantity: ptr(int32(0))},
				ConnectionAttempts:    ptr(int32(3)),
				ConnectionTimeout:     ptr(int32(10)),
				OriginShield:          &cftypes.OriginShield{Enabled: ptr(false)},
			}},
		},
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:             aws.String(assetOriginID),
			ViewerProtocolPolicy:       cftypes.ViewerProtocolPolicyRedirectToHttps,
			Compress:                   ptr(true),
			CachePolicyId:              aws.String(p.cachePolicy),
			OriginRequestPolicyId:      aws.String(allViewerExceptHostPolicyID),
			ResponseHeadersPolicyId:    aws.String(p.headersPolicy),
			FieldLevelEncryptionId:     aws.String(""),
			SmoothStreaming:            ptr(false),
			TrustedSigners:             &cftypes.TrustedSigners{Enabled: ptr(false), Quantity: ptr(int32(0))},
			TrustedKeyGroups:           &cftypes.TrustedKeyGroups{Enabled: ptr(false), Quantity: ptr(int32(0))},
			LambdaFunctionAssociations: &cftypes.LambdaFunctionAssociations{Quantity: ptr(int32(0))},
			AllowedMethods: &cftypes.AllowedMethods{
				Quantity: ptr(int32(7)),
				Items: []cftypes.Method{
					cftypes.MethodGet, cftypes.MethodHead, cftypes.MethodOptions,
					cftypes.MethodPut, cftypes.MethodPost, cftypes.MethodPatch, cftypes.MethodDelete,
				},
				CachedMethods: &cftypes.CachedMethods{
					Quantity: ptr(int32(2)),
					Items:    []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead},
				},
			},
			FunctionAssociations: &cftypes.FunctionAssociations{
				Quantity: ptr(int32(1)),
				Items: []cftypes.FunctionAssociation{{
					EventType:   cftypes.EventTypeViewerRequest,
					FunctionARN: aws.String(p.function),
				}},
			},
		},
		ViewerCertificate: viewerCertificate(certificate),
	}
	return config
}

func completeFrom(want, held *cftypes.DistributionConfig) *cftypes.DistributionConfig {
	if want == nil || held == nil {
		return want
	}
	if want.Aliases == nil {
		want.Aliases = held.Aliases
	}
	if want.AnycastIpListId == nil {
		want.AnycastIpListId = held.AnycastIpListId
	}
	if want.CacheBehaviors == nil {
		want.CacheBehaviors = held.CacheBehaviors
	}
	if want.CacheTagConfig == nil {
		want.CacheTagConfig = held.CacheTagConfig
	}
	if want.CallerReference == nil {
		want.CallerReference = held.CallerReference
	}
	if want.Comment == nil {
		want.Comment = held.Comment
	}
	if want.ConnectionFunctionAssociation == nil {
		want.ConnectionFunctionAssociation = held.ConnectionFunctionAssociation
	}
	if want.ContinuousDeploymentPolicyId == nil {
		want.ContinuousDeploymentPolicyId = held.ContinuousDeploymentPolicyId
	}
	if want.CustomErrorResponses == nil {
		want.CustomErrorResponses = held.CustomErrorResponses
	}
	if want.DefaultCacheBehavior == nil {
		want.DefaultCacheBehavior = held.DefaultCacheBehavior
	}
	if want.DefaultRootObject == nil {
		want.DefaultRootObject = held.DefaultRootObject
	}
	if want.Enabled == nil {
		want.Enabled = held.Enabled
	}
	if want.IsIPV6Enabled == nil {
		want.IsIPV6Enabled = held.IsIPV6Enabled
	}
	if want.Logging == nil {
		want.Logging = held.Logging
	}
	if want.OriginGroups == nil {
		want.OriginGroups = held.OriginGroups
	}
	if want.Origins == nil {
		want.Origins = held.Origins
	}
	if want.Restrictions == nil {
		want.Restrictions = held.Restrictions
	}
	if want.Staging == nil {
		want.Staging = held.Staging
	}
	if want.TenantConfig == nil {
		want.TenantConfig = held.TenantConfig
	}
	if want.ViewerCertificate == nil {
		want.ViewerCertificate = held.ViewerCertificate
	}
	if want.ViewerMtlsConfig == nil {
		want.ViewerMtlsConfig = held.ViewerMtlsConfig
	}
	if want.WebACLId == nil {
		want.WebACLId = held.WebACLId
	}
	if want.ConnectionMode == "" {
		want.ConnectionMode = held.ConnectionMode
	}
	if want.HttpVersion == "" {
		want.HttpVersion = held.HttpVersion
	}
	if want.PriceClass == "" {
		want.PriceClass = held.PriceClass
	}
	return want
}

func viewerCertificate(certificate string) *cftypes.ViewerCertificate {
	if certificate == "" {
		return &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: ptr(true)}
	}
	return &cftypes.ViewerCertificate{
		ACMCertificateArn:      aws.String(certificate),
		SSLSupportMethod:       cftypes.SSLSupportMethodSniOnly,
		MinimumProtocolVersion: cftypes.MinimumProtocolVersionTLSv122021,
	}
}

func listDistributions(ctx context.Context, c Clients) ([]distributionSummary, error) {
	var (
		summaries []distributionSummary
		marker    *string
	)
	for page := 0; page < listPageCeiling; page++ {
		out, err := c.CloudFront.ListDistributions(ctx, &cloudfront.ListDistributionsInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("read the CloudFront distributions this account already serves: %w", err)
		}
		if out.DistributionList == nil {
			return summaries, nil
		}
		for _, item := range out.DistributionList.Items {
			summary := distributionSummary{
				id:         aws.ToString(item.Id),
				domainName: aws.ToString(item.DomainName),
				comment:    aws.ToString(item.Comment),
				aliases:    map[string]bool{},
			}
			if item.Aliases != nil {
				for _, alias := range item.Aliases.Items {
					summary.aliases[strings.ToLower(alias)] = true
				}
			}
			summaries = append(summaries, summary)
		}
		if marker = out.DistributionList.NextMarker; aws.ToString(marker) == "" {
			return summaries, nil
		}
	}
	return nil, pagedForever("CloudFront distributions")
}

func findDistribution(ctx context.Context, c Clients, name string) (front, bool, error) {
	summaries, err := listDistributions(ctx, c)
	if err != nil {
		return front{}, false, err
	}
	held := capDistributionName(name)
	for _, summary := range summaries {
		if summary.comment == held {
			return front{id: summary.id, domainName: summary.domainName}, true, nil
		}
	}
	return front{}, false, nil
}

func createDistribution(ctx context.Context, c Clients, plan distributionPlan, aliases []string, certificate string) (front, error) {
	if err := plan.ready(); err != nil {
		return front{}, err
	}
	out, err := c.CloudFront.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{
		DistributionConfig: plan.config(aliases, certificate),
	})
	if err != nil {
		return front{}, createError("distribution", plan.name, err)
	}
	return front{
		id:         aws.ToString(out.Distribution.Id),
		domainName: aws.ToString(out.Distribution.DomainName),
	}, nil
}

func reshapeDistribution(ctx context.Context, c Clients, plan distributionPlan, id string) error {
	if err := plan.ready(); err != nil {
		return err
	}
	held, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	aliases, certificate := aliasesOf(held), certificateOf(held)
	return putConfig(ctx, c, id, etag, completeFrom(plan.config(aliases, certificate), held))
}

func configOf(ctx context.Context, c Clients, id string) (*cftypes.DistributionConfig, string, error) {
	out, err := c.CloudFront.GetDistributionConfig(ctx, &cloudfront.GetDistributionConfigInput{Id: aws.String(id)})
	if err != nil {
		return nil, "", fmt.Errorf("read the configuration of distribution %s: %w", id, err)
	}
	return out.DistributionConfig, aws.ToString(out.ETag), nil
}

func putConfig(ctx context.Context, c Clients, id, etag string, config *cftypes.DistributionConfig) error {
	if _, err := c.CloudFront.UpdateDistribution(ctx, &cloudfront.UpdateDistributionInput{
		Id:                 aws.String(id),
		IfMatch:            aws.String(etag),
		DistributionConfig: config,
	}); err != nil {
		if staleETag(err) {
			return fmt.Errorf("update distribution %s: something else changed it while this command was reading it, so this command stopped rather than overwrite that change. Re-run the same command and it will read the current configuration: %w", id, err)
		}
		return fmt.Errorf("update distribution %s: %w", id, err)
	}
	return nil
}

func aliasesOf(config *cftypes.DistributionConfig) []string {
	if config == nil || config.Aliases == nil {
		return nil
	}
	return slices.Clone(config.Aliases.Items)
}

func certificateOf(config *cftypes.DistributionConfig) string {
	if config == nil || config.ViewerCertificate == nil {
		return ""
	}
	return aws.ToString(config.ViewerCertificate.ACMCertificateArn)
}

func serveAlias(ctx context.Context, c Clients, plan distributionPlan, id, hostname, certificate string) error {
	held, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	aliases := aliasesOf(held)
	if slices.ContainsFunc(aliases, func(alias string) bool { return strings.EqualFold(alias, hostname) }) {
		return nil
	}
	if certificate == "" {
		certificate = certificateOf(held)
	}
	aliases = append(aliases, hostname)
	if err := putConfig(ctx, c, id, etag, completeFrom(plan.config(aliases, certificate), held)); err != nil {
		return aliasError(hostname, id, err)
	}
	return nil
}

func dropAlias(ctx context.Context, c Clients, plan distributionPlan, id, hostname string) error {
	held, etag, err := configOf(ctx, c, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	aliases := slices.DeleteFunc(aliasesOf(held), func(alias string) bool {
		return strings.EqualFold(alias, hostname)
	})
	if len(aliases) == len(aliasesOf(held)) {
		return nil
	}
	certificate := certificateOf(held)
	if len(aliases) == 0 {
		certificate = ""
	}
	return putConfig(ctx, c, id, etag, completeFrom(plan.config(aliases, certificate), held))
}

func (p *provider) deleteDistribution(ctx context.Context, c Clients, kind, id string) error {
	held, etag, err := configOf(ctx, c, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if aws.ToBool(held.Enabled) {
		held.Enabled = ptr(false)
		if err := putConfig(ctx, c, id, etag, held); err != nil {
			return err
		}
		if err := p.settler().hold(ctx); err != nil {
			return err
		}
	}
	if err := p.settler().settled(ctx, kind, id, func(ctx context.Context) (string, error) {
		out, err := c.CloudFront.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(id)})
		if err != nil {
			return "", fmt.Errorf("read the rollout status of distribution %s: %w", id, err)
		}
		return aws.ToString(out.Distribution.Status), nil
	}); err != nil {
		return err
	}
	_, etag, err = configOf(ctx, c, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if _, err := c.CloudFront.DeleteDistribution(ctx, &cloudfront.DeleteDistributionInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
	}); err != nil {
		if isNotFound(err) {
			return nil
		}
		if stillEnabled(err) {
			return fmt.Errorf("delete distribution %s: CloudFront still reports it as serving traffic. Re-run the same command in a few minutes and it will pick up where this one stopped: %w", id, err)
		}
		return fmt.Errorf("delete distribution %s: %w", id, err)
	}
	return nil
}
