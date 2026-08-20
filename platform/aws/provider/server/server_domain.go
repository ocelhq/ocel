package server

import (
	"context"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) UseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }
	ask := func(headline string, records []edge.Record, notes ...string) {
		_ = stream.Send(dnsOwedEvent(headline, records, notes...))
	}

	if err := s.runUseDomain(ctx, req, progress, ask, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) runUseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, progress func(string), ask askDNS, logf func(string)) error {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return err
	}
	baseDomain, err := previewBaseDomainArg(req.GetBaseDomain())
	if err != nil {
		return err
	}
	clients, err := s.domainClients(ctx, "")
	if err != nil {
		return err
	}
	ssmClient := clients.ssm

	deployed, err := s.deployed(ctx, clients.cfn, "", true)
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
	isrWriter, err := bootstrap.ReadISRWriterFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	edgeValues, err := bootstrap.ReadEdgeValues(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}

	edgeFront, err := s.edge(requestedEdge(req), clients.region)
	if err != nil {
		return err
	}
	if err := refuseRehomingPreviewWildcard(recorded, edgeFront.Kind()); err != nil {
		return err
	}

	spec, err := deploy.PreviewWildcardSpecFor(deploy.PreviewWildcard{
		Edge:   edgeFront,
		Values: edgeValues,
		Worker: deploy.WorkerFacts{
			Region:             clients.region,
			StateTable:         deployed.StateTable,
			AssetBucket:        deployed.AssetBucket,
			ImageOptimizerURL:  deployed.ImageOptimizerURL,
			RevalidateQueueURL: deployed.RevalidateQueueURL,
			EdgeAccessKeyID:    creds.AccessKeyID,
			EdgeSecretKey:      creds.SecretAccessKey,
		},
		StoreScriptName:     store.ScriptName,
		ISRWriterScriptName: isrWriter.ScriptName,
	}, baseDomain, logf)
	if err != nil {
		return err
	}

	writer, err := clients.writerFor(requestedDNS(req).GetKind(), requestedDNS(req).GetZone())
	if err != nil {
		return err
	}
	wildcard := string(edge.PreviewWildcard(baseDomain))
	engine := domains.Engine{
		Kind:          edgeFront.Kind(),
		ServesUnbound: edgeFront.Facts().ServesUnbound,
		Issuer:        clients.issuerFor(edgeFront),
		Writer:        writer,
		Poller:        clients.poller,
		Prober:        clients.prober,
		Store: previewStore{
			ssm: ssmClient,
			domain: bootstrap.PreviewDomain{
				BaseDomain: baseDomain,
				Edge:       edgeFront.Kind(),
				Scope:      edgeFront.Facts().CredentialScope,
				GrammarMin: edge.PreviewGrammarMin,
				GrammarMax: edge.PreviewGrammarMax,
			},
		},
		ProveNotes: []string{fmt.Sprintf("If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard)},
		Ask:        domains.Ask(ask),
	}
	return useDomain(ctx, engine, edgeFront, spec, recorded.Settlement, wildcard, progress)
}

type askDNS func(headline string, records []edge.Record, notes ...string)

type previewStore struct {
	ssm    bootstrap.SSMAPI
	domain bootstrap.PreviewDomain
}

func (s previewStore) Save(ctx context.Context, settled domains.Settlement) error {
	next := s.domain
	next.Settlement = settled
	return bootstrap.WritePreviewDomain(ctx, s.ssm, bootstrap.ClassPreview, next)
}

func useDomain(ctx context.Context, engine domains.Engine, edgeFront edge.Edge, spec edge.PreviewWildcardSpec, recorded domains.Settlement, wildcard string, progress func(string)) error {
	target := domains.Target{
		Hostname: wildcard,
		Surface: func(ctx context.Context, certificate string, say func(string)) (edge.DNSTarget, error) {
			withCert := spec
			withCert.Certificate = certificate
			say(fmt.Sprintf("Reconciling the shared preview entry on %s", wildcard))
			front, err := edgeFront.ReconcilePreviewWildcard(ctx, withCert)
			if err != nil {
				return edge.DNSTarget{}, err
			}
			return edge.DNSTarget{Kind: edgeFront.Kind(), ServesUnbound: edgeFront.Facts().ServesUnbound, Front: front}, nil
		},
	}
	_, err := engine.Settle(ctx, recorded, []string{wildcard}, func(domains.Settlement) []domains.Target {
		return []domains.Target{target}
	}, progress)
	return err
}

func (s *Server) PlanReleaseDomain(ctx context.Context, req *deploymentsv1.PlanReleaseDomainRequest) (*deploymentsv1.PlanReleaseDomainResponse, error) {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	awscfg, err := loadAWS(ctx, "")
	if err != nil {
		return nil, err
	}
	clients, err := s.domainClients(ctx, "")
	if err != nil {
		return nil, err
	}
	ssmClient := clients.ssm

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return nil, err
	}
	if recorded.BaseDomain == "" {
		return &deploymentsv1.PlanReleaseDomainResponse{}, nil
	}
	edgeFront, err := previewWildcardEdge(recorded, func(kind edge.Kind) (edge.Edge, error) {
		return s.edge(kind, clients.region)
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	plan := releaseEdgeStackPlan(edgeFront, recorded)
	if err := refusePreviewReleaseWhileServed(ctx, ssmClient, recorded.BaseDomain, s.livePreviewStacks(awscfg)); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return &deploymentsv1.PlanReleaseDomainResponse{
		BaseDomain: recorded.BaseDomain,
		EdgeStack:  plan,
	}, nil
}

func (s *Server) livePreviewStacks(awscfg aws.Config) func(context.Context, string) (int, error) {
	return func(ctx context.Context, slug string) (int, error) {
		deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), awscfg.Region, true)
		if err != nil {
			return 0, err
		}
		index, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
		if err != nil {
			return 0, err
		}
		stacks, err := deploy.ListPreviewStacks(ctx, index, slug)
		if err != nil {
			return 0, err
		}
		return len(stacks), nil
	}
}

func refusePreviewReleaseWhileServed(ctx context.Context, ssmClient substrateSSMAPI, baseDomain string, live func(context.Context, string) (int, error)) error {
	slugs, err := bootstrap.StackSlugsFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	recorded, err := globalPreviewProjects(ctx, ssmClient, slugs, baseDomain)
	if err != nil {
		return err
	}
	var served []string
	for _, slug := range recorded {
		previews, err := live(ctx, slug)
		if err != nil {
			return err
		}
		if previews > 0 {
			served = append(served, slug)
		}
	}
	return refusePreviewRelease(baseDomain, served)
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
	awscfg, err := loadAWS(ctx, "")
	if err != nil {
		return err
	}
	clients, err := s.domainClients(ctx, "")
	if err != nil {
		return err
	}
	ssmClient := clients.ssm

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return err
	}
	if recorded.BaseDomain == "" {
		progress("This preview substrate has no global preview domain")
		return nil
	}

	if err := refusePreviewReleaseWhileServed(ctx, ssmClient, recorded.BaseDomain, s.livePreviewStacks(awscfg)); err != nil {
		return err
	}

	edgeFront, err := previewWildcardEdge(recorded, func(kind edge.Kind) (edge.Edge, error) {
		return s.edge(kind, clients.region)
	})
	if err != nil {
		return err
	}

	writer, err := clients.writerFor(requestedDNS(req).GetKind(), requestedDNS(req).GetZone())
	if err != nil {
		return err
	}

	return releaseDomain(ctx, releaseDeps{
		ssm:    ssmClient,
		edge:   edgeFront,
		writer: writer,
		issuer: clients.discarderFor(recorded.Settlement.Certificate),
	}, recorded, progress)
}

type releaseDeps struct {
	ssm    bootstrap.SSMAPI
	edge   edge.Edge
	writer edge.DNSWriter
	issuer certs.Issuer
}

func releaseDomain(ctx context.Context, deps releaseDeps, recorded bootstrap.PreviewDomain, progress func(string)) error {
	progress(fmt.Sprintf("Removing the shared preview entry on %s", edge.PreviewWildcard(recorded.BaseDomain)))
	if err := deps.edge.DestroyPreviewWildcard(ctx, recorded.BaseDomain); err != nil {
		return err
	}
	if err := dns.Release(ctx, deps.writer, recorded.Settlement.WrittenRecords(), progress); err != nil {
		return err
	}
	if err := deps.issuer.Discard(ctx, recorded.Settlement.Certificate, progress); err != nil {
		return err
	}
	return bootstrap.DeletePreviewDomain(ctx, deps.ssm, bootstrap.ClassPreview)
}

func (s *Server) ListDomain(ctx context.Context, req *deploymentsv1.ListDomainRequest) (*deploymentsv1.ListDomainResponse, error) {
	if err := requirePreviewClass(req.GetClass()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	clients, err := s.domainClients(ctx, "")
	if err != nil {
		return nil, err
	}
	ssmClient := clients.ssm

	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return nil, err
	}
	if recorded.BaseDomain == "" {
		return &deploymentsv1.ListDomainResponse{}, nil
	}
	slugs, err := bootstrap.StackSlugsFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		return nil, err
	}
	served, err := globalPreviewProjects(ctx, ssmClient, slugs, recorded.BaseDomain)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListDomainResponse{
		Domain:   globalPreviewDomain(ctx, s.globalPreviewOwner(recorded, clients.region), recorded),
		Projects: served,
	}, nil
}

func globalPreviewProjects(ctx context.Context, ssmClient bootstrap.SSMAPI, slugs []string, baseDomain string) ([]string, error) {
	var served []string
	for _, slug := range slugs {
		record, err := bootstrap.ReadStackRecordFor(ctx, ssmClient, bootstrap.ClassPreview, slug)
		if err != nil {
			return nil, err
		}
		if record.Edge.ServedOnGlobalPreview(baseDomain) {
			served = append(served, slug)
		}
	}
	return served, nil
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
	wildcard := recorded.Wildcard()
	return &deploymentsv1.GlobalPreviewDomain{
		BaseDomain:        recorded.BaseDomain,
		EdgeScope:         recorded.Scope,
		GrammarMin:        recorded.GrammarMin,
		GrammarMax:        recorded.GrammarMax,
		RouteInstalled:    sharedEntryRouteInstalled(ctx, owner, recorded.BaseDomain),
		CertificateId:     recorded.Settlement.Certificate.ARN,
		CertificateStatus: recorded.Settlement.Certificate.Status,
		RecordsWritten:    recordLines(recorded.Settlement.WrittenRecords()),
		RecordsOwed:       recordLines(recorded.Settlement.OwedRecords()),
		LastProbeAt:       probeUnix(wildcard.Probe),
		LastProbeOk:       wildcard.Probe.OK,
		LastProbeEdge:     string(wildcard.Probe.Edge),
	}
}

func recordLines(records []edge.Record) []string {
	lines := make([]string, 0, len(records))
	for _, rec := range records {
		lines = append(lines, rec.String())
	}
	return lines
}

func probeUnix(probe certs.Probe) int64 {
	if probe.At.IsZero() {
		return 0
	}
	return probe.At.Unix()
}

func sharedEntryRouteInstalled(ctx context.Context, owner routeOwnerFunc, baseDomain string) bool {
	if owner == nil {
		return false
	}
	script, err := owner(ctx, edge.PreviewWildcard(baseDomain))
	if err != nil {
		return false
	}
	return script == edge.PreviewEntryOwner
}

func previewWildcardOwner(recorded bootstrap.PreviewDomain, ownerFor func(edge.Kind) routeOwnerFunc) routeOwnerFunc {
	kind, ok := recorded.Holder()
	if !ok {
		return nil
	}
	return ownerFor(kind)
}

func (s *Server) globalPreviewOwner(recorded bootstrap.PreviewDomain, region string) routeOwnerFunc {
	return previewWildcardOwner(recorded, func(kind edge.Kind) routeOwnerFunc {
		return s.edgeRouteOwner(kind, region)
	})
}

func previewWildcardHolder(recorded bootstrap.PreviewDomain) (edge.Kind, error) {
	kind, ok := recorded.Holder()
	if !ok {
		return "", fmt.Errorf(
			"nothing in this account records which edge holds %s, and tearing it down through a guessed edge would delete its certificate, its DNS records and the record itself while leaving the real wildcard entry standing with nothing left to name it: run `ocel domain use '%s' --preview` from the project whose edge raised it — that writes the edge down and changes nothing else — then release it",
			edge.PreviewWildcard(recorded.BaseDomain), edge.PreviewWildcard(recorded.BaseDomain),
		)
	}
	return kind, nil
}

func previewWildcardEdge(recorded bootstrap.PreviewDomain, edgeFor func(edge.Kind) (edge.Edge, error)) (edge.Edge, error) {
	kind, err := previewWildcardHolder(recorded)
	if err != nil {
		return nil, err
	}
	return edgeFor(kind)
}

func refuseRehomingPreviewWildcard(recorded bootstrap.PreviewDomain, kind edge.Kind) error {
	holder, ok := recorded.Holder()
	if !ok || kind == "" || holder == kind {
		return nil
	}
	return fmt.Errorf(
		"%s is already held by the %s edge, and this project's edge is %s: reconciling it here would raise a second wildcard at the %s edge and leave the %s one standing with nothing left to name it — release it with `ocel domain release --preview` from a project on the %s edge first, then use it again from here",
		edge.PreviewWildcard(recorded.BaseDomain), holder, kind, kind, holder, holder,
	)
}

func globalPreviewScopeMismatch(recorded bootstrap.PreviewDomain, edgeFront edge.Edge) error {
	ambient := edgeFront.Facts().CredentialScope
	if recorded.BaseDomain == "" || recorded.Scope == "" || ambient == "" || recorded.Scope == ambient {
		return nil
	}
	return fmt.Errorf(
		"the global preview domain %q was claimed under %s account %s, but this run's %s credentials are scoped to account %s: this deploy would publish previews the entry worker on %q cannot serve — re-scope the %s credentials to account %s, or release the domain with `ocel domain release --preview` and use it again from this account",
		recorded.BaseDomain, edgeFront.Kind(), recorded.Scope, edgeFront.Kind(), ambient,
		edge.PreviewWildcard(recorded.BaseDomain), edgeFront.Kind(), recorded.Scope,
	)
}
