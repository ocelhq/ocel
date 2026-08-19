package cloudfront

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const previewSweepPage = 50

func previewWildcardName(baseDomain string) string {
	return naming.Join(naming.FieldSeparator, namespace, string(edge.ClassPreview), baseDomain)
}

func (p *provider) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", fmt.Errorf("the %q edge serves every preview from one wildcard distribution; this reconcile names no base domain", Kind)
	}
	if spec.Certificate == "" {
		return "", fmt.Errorf("the %q edge terminates TLS for %s at CloudFront, so the distribution needs the wildcard certificate; this reconcile carries none", Kind, wildcard)
	}
	if err := cloudFrontCertificate(wildcard, spec.Certificate); err != nil {
		return "", err
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return "", err
	}
	plan, deployed, err := p.previewWildcardPlan(ctx, c, spec.BaseDomain)
	if err != nil {
		return "", err
	}
	held, err := reconcileWildcardDistribution(ctx, c, plan, wildcard, spec.Certificate)
	if err != nil {
		return "", err
	}
	if err := substrateLedger(c, edge.ClassPreview, deployed).NoteInvalidationTarget(ctx, held.id); err != nil {
		return "", err
	}
	return held.domainName, nil
}

func reconcileWildcardDistribution(ctx context.Context, c Clients, plan distributionPlan, wildcard, certificate string) (front, error) {
	held, found, err := findDistribution(ctx, c, plan.name)
	if err != nil {
		return front{}, err
	}
	if !found {
		created, createErr := createDistribution(ctx, c, plan, []string{wildcard}, certificate)
		if createErr == nil {
			return created, nil
		}
		if !distributionTaken(createErr) {
			return front{}, createErr
		}
		raced, racedFound, findErr := findDistribution(ctx, c, plan.name)
		if findErr != nil {
			return front{}, findErr
		}
		if !racedFound {
			return front{}, createErr
		}
		held = raced
	}
	if err := convergeWildcard(ctx, c, plan, held.id, wildcard, certificate); err != nil {
		return front{}, err
	}
	return held, nil
}

func substrateLedger(c Clients, class edge.Class, deployed bootstrap.Deployed) *edgeledger.Ledger {
	return &edgeledger.Ledger{
		Dynamo: c.Dynamo,
		Table:  deployed.StateTable,
		Scope:  edgeledger.Scope(class, ""),
	}
}

func cloudFrontCertificate(wildcard, certificate string) error {
	fields := strings.SplitN(certificate, ":", 6)
	if len(fields) < 6 || fields[0] != "arn" {
		return fmt.Errorf("the %q edge terminates TLS for %s at CloudFront, which takes an ACM certificate ARN; this reconcile carries %q, which is not one", Kind, wildcard, certificate)
	}
	if fields[3] == certs.CloudFrontRegion {
		return nil
	}
	return fmt.Errorf("the %q edge terminates TLS for %s at CloudFront, and CloudFront reads certificates only from %s; this reconcile carries one issued in %s, which CloudFront will not attach. Run `ocel domain use --preview %s` against this account to issue the wildcard certificate where CloudFront can read it", Kind, wildcard, certs.CloudFrontRegion, fields[3], strings.TrimPrefix(wildcard, "*."))
}

func convergeWildcard(ctx context.Context, c Clients, plan distributionPlan, id, wildcard, certificate string) error {
	if err := plan.ready(); err != nil {
		return err
	}
	held, etag, err := configOf(ctx, c, id)
	if err != nil {
		return err
	}
	carries := slices.ContainsFunc(aliasesOf(held), func(alias string) bool {
		return strings.EqualFold(alias, wildcard)
	})
	if carries && certificateOf(held) == certificate {
		return nil
	}
	return putConfig(ctx, c, id, etag, plan.config([]string{wildcard}, certificate))
}

func (p *provider) previewWildcardPlan(ctx context.Context, c Clients, baseDomain string) (distributionPlan, bootstrap.Deployed, error) {
	deployed, err := p.substrate(ctx, c, edge.ClassPreview)
	if err != nil {
		return distributionPlan{}, bootstrap.Deployed{}, err
	}
	if !deployed.Present {
		return distributionPlan{}, bootstrap.Deployed{}, fmt.Errorf("the preview substrate is not bootstrapped, so nothing would answer a hostname on %s; run `ocel bootstrap --preview` first", edge.PreviewWildcard(baseDomain))
	}
	set, err := findEdgeSet(ctx, c, edge.ClassPreview, edgeSet{})
	if err != nil {
		return distributionPlan{}, bootstrap.Deployed{}, err
	}
	return distributionPlan{
		name:          previewWildcardName(baseDomain),
		assetOrigin:   assetOriginDomain(deployed.AssetBucket, c.Region),
		function:      set.functionARN,
		cachePolicy:   set.cachePolicy,
		headersPolicy: set.headersPolicy,
		oac:           set.originAccessControl,
	}, deployed, nil
}

func (p *provider) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	if edge.PreviewWildcard(baseDomain) == "" {
		return nil
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	if err := sweepPreviewRoutes(ctx, c, baseDomain); err != nil {
		errs = append(errs, err)
	}
	held, found, err := findDistribution(ctx, c, previewWildcardName(baseDomain))
	switch {
	case err != nil:
		errs = append(errs, err)
	case found:
		if err := p.deleteDistribution(ctx, c, kindWildcardDistribution, held.id); err != nil {
			errs = append(errs, err)
		} else if err := p.forgetPreviewWildcardTarget(ctx, c, held.id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *provider) forgetPreviewWildcardTarget(ctx context.Context, c Clients, distribution string) error {
	deployed, err := p.substrate(ctx, c, edge.ClassPreview)
	if err != nil {
		return err
	}
	if !deployed.Present {
		return nil
	}
	return substrateLedger(c, edge.ClassPreview, deployed).ForgetInvalidationTarget(ctx, distribution)
}

func sweepPreviewRoutes(ctx context.Context, c Clients, baseDomain string) error {
	store, err := c.CloudFront.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{
		Name: aws.String(keyValueStoreName(edge.ClassPreview)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("read the key value store the %q edge routes previews with: %w", Kind, err)
	}
	arn := aws.ToString(store.KeyValueStore.ARN)
	suffix := "." + routeKey(baseDomain)

	var (
		hosts []string
		token *string
	)
	for page := 0; page < listPageCeiling; page++ {
		out, err := c.KeyValueStore.ListKeys(ctx, &cloudfrontkeyvaluestore.ListKeysInput{
			KvsARN:     aws.String(arn),
			MaxResults: ptr(int32(previewSweepPage)),
			NextToken:  token,
		})
		if err != nil {
			return fmt.Errorf("read the hostnames the %q edge answers previews on: %w", Kind, err)
		}
		for _, item := range out.Items {
			if key := routeKey(aws.ToString(item.Key)); strings.HasSuffix(key, suffix) {
				hosts = append(hosts, key)
			}
		}
		if token = out.NextToken; aws.ToString(token) == "" {
			return dropRoutes(ctx, c, arn, hosts)
		}
	}
	return pagedForever("key value store keys")
}

func dropRoutes(ctx context.Context, c Clients, arn string, hosts []string) error {
	writer := routeWriter{clients: c, arn: arn}
	for batch := range slices.Chunk(hosts, previewSweepPage) {
		if err := writer.apply(ctx, nil, batch); err != nil {
			return err
		}
	}
	return nil
}
