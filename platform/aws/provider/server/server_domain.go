package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const cloudflareAccountEnvVar = "CLOUDFLARE_ACCOUNT_ID"

func (s *Server) UseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runUseDomain(ctx, req, progress, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) runUseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, progress, logf func(string)) error {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return err
	}
	baseDomain, err := previewBaseDomainArg(req.GetBaseDomain())
	if err != nil {
		return err
	}
	entry, err := s.sharedEntry()
	if err != nil {
		return err
	}

	awscfg, err := loadAWS(ctx, "")
	if err != nil {
		return err
	}
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), "", true)
	if err != nil {
		return err
	}
	if !deployed.Present {
		return errPreviewInfraMissing
	}

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	if recorded.BaseDomain != "" && recorded.BaseDomain != baseDomain {
		return fmt.Errorf(
			"this preview substrate already serves previews on %q: release it with `ocel domain release --preview` first, then use %q — every project on %q loses its preview hostnames the moment the substrate changes domain, so that is two deliberate commands",
			recorded.BaseDomain, baseDomain, recorded.BaseDomain,
		)
	}

	creds, err := bootstrap.ReadEdgeCredentials(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	store, err := bootstrap.ReadDeploymentsStoreFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}

	spec, err := deploy.SharedPreviewEntrySpec(deploy.Config{
		Region:             awscfg.Region,
		StateTable:         deployed.StateTable,
		AssetBucket:        deployed.AssetBucket,
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,
		EdgeAccessKeyID:    creds.AccessKeyID,
		EdgeSecretKey:      creds.SecretAccessKey,
		StoreScriptName:    store.ScriptName,
		Edge:               s.edge(),
	}, baseDomain, logf)
	if err != nil {
		return err
	}

	progress(fmt.Sprintf("Reconciling the shared preview entry on %s", edge.SharedEntryWildcard(baseDomain)))
	if err := entry.ReconcileSharedEntry(ctx, spec); err != nil {
		return err
	}

	return bootstrap.WritePreviewDomain(ctx, ssmClient, bootstrap.ClassPreview, bootstrap.PreviewDomain{
		BaseDomain:        baseDomain,
		CloudflareAccount: os.Getenv(cloudflareAccountEnvVar),
		GrammarMin:        edge.PreviewGrammarMin,
		GrammarMax:        edge.PreviewGrammarMax,
	})
}

func (s *Server) ReleaseDomain(ctx context.Context, req *deploymentsv1.ReleaseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }

	if err := s.runReleaseDomain(ctx, req, progress); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) runReleaseDomain(ctx context.Context, req *deploymentsv1.ReleaseDomainRequest, progress func(string)) error {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return err
	}
	entry, err := s.sharedEntry()
	if err != nil {
		return err
	}
	awscfg, err := loadAWS(ctx, "")
	if err != nil {
		return err
	}
	ssmClient := ssm.NewFromConfig(awscfg)

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	if recorded.BaseDomain == "" {
		progress("This preview substrate has no global preview domain")
		return nil
	}

	progress(fmt.Sprintf("Removing the shared preview entry on %s", edge.SharedEntryWildcard(recorded.BaseDomain)))
	if err := entry.DestroySharedEntry(ctx, recorded.BaseDomain); err != nil {
		return err
	}
	return bootstrap.DeletePreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
}

func (s *Server) ListDomain(ctx context.Context, req *deploymentsv1.ListDomainRequest) (*deploymentsv1.ListDomainResponse, error) {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	awscfg, err := loadAWS(ctx, "")
	if err != nil {
		return nil, err
	}
	ssmClient := ssm.NewFromConfig(awscfg)

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return nil, err
	}
	if recorded.BaseDomain == "" {
		return &deploymentsv1.ListDomainResponse{}, nil
	}
	slugs, err := bootstrap.RootStackSlugsFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListDomainResponse{
		Domain:   globalPreviewDomain(ctx, s.edgeRouteOwner(), recorded),
		Projects: slugs,
	}, nil
}

func (s *Server) sharedEntry() (edge.SharedEntry, error) {
	entry, ok := s.edge().(edge.SharedEntry)
	if !ok {
		return nil, fmt.Errorf("this edge does not serve a shared preview entry")
	}
	return entry, nil
}

func requirePreviewClass(class deploymentsv1.Environment_Class) error {
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		return nil
	}
	return fmt.Errorf("a global preview domain belongs to the preview substrate; pass --preview")
}

func previewBaseDomainArg(domain string) (string, error) {
	base := strings.TrimSpace(strings.ToLower(domain))
	base = strings.TrimSuffix(base, ".")
	switch {
	case base == "":
		return "", fmt.Errorf("a domain is required, e.g. `ocel domain use --preview preview.acme.com`")
	case strings.HasPrefix(base, "*."):
		return "", fmt.Errorf("give the domain itself, not the wildcard: every preview is served on its own subdomain of it, so pass %q", strings.TrimPrefix(base, "*."))
	case strings.ContainsAny(base, "/:*"), !strings.Contains(base, "."):
		return "", fmt.Errorf("%q is not a domain name: pass a hostname like preview.acme.com", domain)
	}
	return base, nil
}

func globalPreviewDomain(ctx context.Context, owner routeOwnerFunc, recorded bootstrap.PreviewDomain) *deploymentsv1.GlobalPreviewDomain {
	if recorded.BaseDomain == "" {
		return nil
	}
	return &deploymentsv1.GlobalPreviewDomain{
		BaseDomain:        recorded.BaseDomain,
		CloudflareAccount: recorded.CloudflareAccount,
		GrammarMin:        recorded.GrammarMin,
		GrammarMax:        recorded.GrammarMax,
		RouteInstalled:    sharedEntryRouteInstalled(ctx, owner, recorded.BaseDomain),
	}
}

func sharedEntryRouteInstalled(ctx context.Context, owner routeOwnerFunc, baseDomain string) bool {
	script, err := owner(ctx, cloudflare.RoutePattern(edge.SharedEntryWildcard(baseDomain)))
	if err != nil {
		return false
	}
	return script == edge.SharedPreviewEntryScript
}

func globalPreviewAccountMismatch(recorded bootstrap.PreviewDomain) error {
	ambient := os.Getenv(cloudflareAccountEnvVar)
	if recorded.BaseDomain == "" || recorded.CloudflareAccount == "" || ambient == "" || recorded.CloudflareAccount == ambient {
		return nil
	}
	return fmt.Errorf(
		"the global preview domain %q was claimed in Cloudflare account %s, but %s is %s: this deploy would publish previews the entry worker on %q cannot serve — point %s at %s, or release the domain with `ocel domain release --preview` and use it again from this account",
		recorded.BaseDomain, recorded.CloudflareAccount, cloudflareAccountEnvVar, ambient,
		edge.SharedEntryWildcard(recorded.BaseDomain), cloudflareAccountEnvVar, recorded.CloudflareAccount,
	)
}
