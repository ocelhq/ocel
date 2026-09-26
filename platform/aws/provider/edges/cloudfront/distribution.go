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
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	assetOriginID     = "assets"
	containerOriginID = "containers"

	containerOriginReadTimeout      = 60
	containerOriginKeepaliveTimeout = 5

	maxDistributionNameLen = 128
)

type front struct {
	id         string
	domainName string
}

type distributionPlan struct {
	name          string
	assetOrigin   string
	function      string
	emptyBody     string
	cachePolicy   string
	headersPolicy string
	oac           string
	front         awsports.ContainerFront
}

func (p distributionPlan) keeping(current *cftypes.DistributionConfig) distributionPlan {
	if p.front.VPCOrigin == "" {
		p.front = containerFrontOf(current)
	}
	return p
}

func containerFrontOf(config *cftypes.DistributionConfig) awsports.ContainerFront {
	if config == nil || config.Origins == nil {
		return awsports.ContainerFront{}
	}
	for _, origin := range config.Origins.Items {
		if aws.ToString(origin.Id) != containerOriginID || origin.VpcOriginConfig == nil {
			continue
		}
		return awsports.ContainerFront{VPCOrigin: aws.ToString(origin.VpcOriginConfig.VpcOriginId), Host: aws.ToString(origin.DomainName)}
	}
	return awsports.ContainerFront{}
}

func (p distributionPlan) origins() *cftypes.Origins {
	origins := []cftypes.Origin{{
		Id:                    aws.String(assetOriginID),
		DomainName:            aws.String(p.assetOrigin),
		OriginAccessControlId: aws.String(p.oac),
		S3OriginConfig:        &cftypes.S3OriginConfig{OriginAccessIdentity: aws.String("")},
		OriginPath:            aws.String(""),
		CustomHeaders:         &cftypes.CustomHeaders{Quantity: ptr(int32(0))},
		ConnectionAttempts:    ptr(int32(3)),
		ConnectionTimeout:     ptr(int32(10)),
		OriginShield:          &cftypes.OriginShield{Enabled: ptr(false)},
	}}
	if p.front.VPCOrigin != "" {
		origins = append(origins, cftypes.Origin{
			Id:         aws.String(containerOriginID),
			DomainName: aws.String(p.front.Host),
			VpcOriginConfig: &cftypes.VpcOriginConfig{
				VpcOriginId:            aws.String(p.front.VPCOrigin),
				OriginReadTimeout:      ptr(int32(containerOriginReadTimeout)),
				OriginKeepaliveTimeout: ptr(int32(containerOriginKeepaliveTimeout)),
			},
			OriginPath:         aws.String(""),
			CustomHeaders:      &cftypes.CustomHeaders{Quantity: ptr(int32(0))},
			ConnectionAttempts: ptr(int32(3)),
			ConnectionTimeout:  ptr(int32(10)),
			OriginShield:       &cftypes.OriginShield{Enabled: ptr(false)},
		})
	}
	return &cftypes.Origins{Quantity: ptr(int32(len(origins))), Items: origins}
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
		"empty-body function":     p.emptyBody,
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
		CallerReference:              aws.String(p.name),
		Comment:                      aws.String(p.name),
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
		Origins:              p.origins(),
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
				Quantity: ptr(int32(2)),
				Items: []cftypes.FunctionAssociation{
					{
						EventType:   cftypes.EventTypeViewerRequest,
						FunctionARN: aws.String(p.function),
					},
					{
						EventType:   cftypes.EventTypeViewerResponse,
						FunctionARN: aws.String(p.emptyBody),
					},
				},
			},
		},
		ViewerCertificate: viewerCertificate(certificate),
	}
	return config
}

func completeFrom(want, current *cftypes.DistributionConfig) *cftypes.DistributionConfig {
	if want == nil || current == nil {
		return want
	}
	if want.Aliases == nil {
		want.Aliases = current.Aliases
	}
	if want.AnycastIpListId == nil {
		want.AnycastIpListId = current.AnycastIpListId
	}
	if want.CacheBehaviors == nil {
		want.CacheBehaviors = current.CacheBehaviors
	}
	if want.CacheTagConfig == nil {
		want.CacheTagConfig = current.CacheTagConfig
	}
	if want.CallerReference == nil {
		want.CallerReference = current.CallerReference
	}
	if want.Comment == nil {
		want.Comment = current.Comment
	}
	if want.ConnectionFunctionAssociation == nil {
		want.ConnectionFunctionAssociation = current.ConnectionFunctionAssociation
	}
	if want.ContinuousDeploymentPolicyId == nil {
		want.ContinuousDeploymentPolicyId = current.ContinuousDeploymentPolicyId
	}
	if want.CustomErrorResponses == nil {
		want.CustomErrorResponses = current.CustomErrorResponses
	}
	if want.DefaultCacheBehavior == nil {
		want.DefaultCacheBehavior = current.DefaultCacheBehavior
	}
	if want.DefaultRootObject == nil {
		want.DefaultRootObject = current.DefaultRootObject
	}
	if want.Enabled == nil {
		want.Enabled = current.Enabled
	}
	if want.IsIPV6Enabled == nil {
		want.IsIPV6Enabled = current.IsIPV6Enabled
	}
	if want.Logging == nil {
		want.Logging = current.Logging
	}
	if want.OriginGroups == nil {
		want.OriginGroups = current.OriginGroups
	}
	if want.Origins == nil {
		want.Origins = current.Origins
	}
	if want.Restrictions == nil {
		want.Restrictions = current.Restrictions
	}
	if want.Staging == nil {
		want.Staging = current.Staging
	}
	if want.TenantConfig == nil {
		want.TenantConfig = current.TenantConfig
	}
	if want.ViewerCertificate == nil {
		want.ViewerCertificate = current.ViewerCertificate
	}
	if want.ViewerMtlsConfig == nil {
		want.ViewerMtlsConfig = current.ViewerMtlsConfig
	}
	if want.WebACLId == nil {
		want.WebACLId = current.WebACLId
	}
	if want.ConnectionMode == "" {
		want.ConnectionMode = current.ConnectionMode
	}
	if want.HttpVersion == "" {
		want.HttpVersion = current.HttpVersion
	}
	if want.PriceClass == "" {
		want.PriceClass = current.PriceClass
	}
	return want
}

func viewerCertificate(certificate string) *cftypes.ViewerCertificate {
	if certificate == "" {
		return &cftypes.ViewerCertificate{
			CloudFrontDefaultCertificate: ptr(true),
			MinimumProtocolVersion:       cftypes.MinimumProtocolVersionTLSv1,
		}
	}
	return &cftypes.ViewerCertificate{
		CloudFrontDefaultCertificate: ptr(false),
		ACMCertificateArn:            aws.String(certificate),
		SSLSupportMethod:             cftypes.SSLSupportMethodSniOnly,
		MinimumProtocolVersion:       cftypes.MinimumProtocolVersionTLSv122021,
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
	for _, summary := range summaries {
		if summary.comment == name {
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
	current, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	aliases, certificate := aliasesOf(current), certificateOf(current)
	return putConfig(ctx, c, id, etag, completeFrom(plan.keeping(current).config(aliases, certificate), current))
}

func (p *cloudFront) declareContainerFront(ctx context.Context, c Clients, plan distributionPlan, kind, id string, front awsports.ContainerFront) error {
	if err := plan.ready(); err != nil {
		return err
	}
	current, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	if containerFrontOf(current) == front {
		return nil
	}
	plan.front = front
	if err := putConfig(ctx, c, id, etag, completeFrom(plan.config(aliasesOf(current), certificateOf(current)), current)); err != nil {
		return fmt.Errorf("declare the container front as an origin of %s %s: %w", kind, id, err)
	}
	return p.rollout().awaitDeployed(ctx, kind, id, containerFrontRollingOut, distributionStatus(c, id))
}

func distributionStatus(c Clients, id string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		out, err := c.CloudFront.GetDistribution(ctx, &cloudfront.GetDistributionInput{Id: aws.String(id)})
		if err != nil {
			return "", fmt.Errorf("read the rollout status of distribution %s: %w", id, err)
		}
		return aws.ToString(out.Distribution.Status), nil
	}
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
	current, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	aliases := aliasesOf(current)
	if slices.ContainsFunc(aliases, func(alias string) bool { return strings.EqualFold(alias, hostname) }) {
		return nil
	}
	if certificate == "" {
		certificate = certificateOf(current)
	}
	aliases = append(aliases, hostname)
	if err := putConfig(ctx, c, id, etag, completeFrom(plan.keeping(current).config(aliases, certificate), current)); err != nil {
		return aliasError(hostname, id, err)
	}
	return nil
}

func dropAlias(ctx context.Context, c Clients, plan distributionPlan, id, hostname string) error {
	current, etag, err := configOf(ctx, c, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	aliases := slices.DeleteFunc(aliasesOf(current), func(alias string) bool {
		return strings.EqualFold(alias, hostname)
	})
	if len(aliases) == len(aliasesOf(current)) {
		return nil
	}
	certificate := certificateOf(current)
	if len(aliases) == 0 {
		certificate = ""
	}
	return putConfig(ctx, c, id, etag, completeFrom(plan.keeping(current).config(aliases, certificate), current))
}

func (p *cloudFront) deleteDistribution(ctx context.Context, c Clients, kind, id string) error {
	current, etag, err := configOf(ctx, c, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	if aws.ToBool(current.Enabled) {
		current.Enabled = ptr(false)
		if err := putConfig(ctx, c, id, etag, current); err != nil {
			return err
		}
		if err := p.rollout().waitInterval(ctx); err != nil {
			return err
		}
	}
	if err := p.rollout().awaitDeployed(ctx, kind, id, disableRollingOut, distributionStatus(c, id)); err != nil {
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
