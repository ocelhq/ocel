package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPreviewBaseDomainArg(t *testing.T) {
	t.Parallel()

	t.Run("normalizes the domain", func(t *testing.T) {
		t.Parallel()
		got, err := previewBaseDomainArg("  Preview.Acme.com. ")
		if err != nil {
			t.Fatalf("previewBaseDomainArg: %v", err)
		}
		if got != "preview.acme.com" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("refuses a wildcard and names the domain to pass", func(t *testing.T) {
		t.Parallel()
		_, err := previewBaseDomainArg("*.preview.acme.com")
		if err == nil {
			t.Fatal("expected a wildcard to be refused")
		}
		if !strings.Contains(err.Error(), "preview.acme.com") {
			t.Errorf("error must name the domain to pass instead, got %q", err)
		}
	})

	t.Run("refuses what is not a domain", func(t *testing.T) {
		t.Parallel()
		for _, in := range []string{"", "acme", "https://preview.acme.com"} {
			if _, err := previewBaseDomainArg(in); err == nil {
				t.Errorf("expected %q to be refused", in)
			}
		}
	})
}

func TestPreviewWildcard(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", Scope: "acct", GrammarMin: 1, GrammarMax: 1}

	t.Run("is nil when nothing is recorded", func(t *testing.T) {
		t.Parallel()
		owner := func(context.Context, string) (string, error) { return "", nil }
		if got := previewWildcard(context.Background(), owner, bootstrap.PreviewDomain{}); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("reports the route the shared entry actually holds", func(t *testing.T) {
		t.Parallel()
		var asked string
		owner := func(_ context.Context, hostname string) (string, error) {
			asked = hostname
			return edge.PreviewEntryOwner, nil
		}
		got := previewWildcard(context.Background(), owner, recorded)
		if asked != "*.preview.acme.com" {
			t.Errorf("asked %q, want the wildcard hostname", asked)
		}
		if !got.GetRouteInstalled() {
			t.Error("RouteInstalled = false, want true")
		}
		if got.GetBaseDomain() != "preview.acme.com" || got.GetEdgeScope() != "acct" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("carries the certificate, the records and the last probe", func(t *testing.T) {
		t.Parallel()
		owner := func(context.Context, string) (string, error) { return edge.PreviewEntryOwner, nil }
		full := recorded
		full.Settlement = domains.Settlement{
			Certificate: certs.Certificate{ARN: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", Status: certs.StatusIssued},
			Validation:  domains.Records{Owed: []edge.Record{{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}}},
			Hosts: []domains.Host{{
				Hostname: "*.preview.acme.com",
				Records:  domains.Records{Written: []edge.Record{{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}}},
				Probe:    certs.Probe{At: time.Unix(1755500000, 0).UTC(), Edge: cloudflare.Kind, OK: true},
			}},
		}

		got := previewWildcard(context.Background(), owner, full)
		if got.GetCertificate().GetCertificateId() != full.Settlement.Certificate.ARN || got.GetCertificate().GetCertificateStatus() != certs.StatusIssued {
			t.Errorf("certificate = %q %q", got.GetCertificate().GetCertificateId(), got.GetCertificate().GetCertificateStatus())
		}
		if len(got.GetCertificate().GetRecordsWritten()) != 1 || !strings.Contains(got.GetCertificate().GetRecordsWritten()[0], "*.preview.acme.com") {
			t.Errorf("records written = %v", got.GetCertificate().GetRecordsWritten())
		}
		if len(got.GetCertificate().GetRecordsOwed()) != 1 || !strings.Contains(got.GetCertificate().GetRecordsOwed()[0], "acm-validations") {
			t.Errorf("records owed = %v", got.GetCertificate().GetRecordsOwed())
		}
		if !got.GetCertificate().GetLastProbeOk() || got.GetCertificate().GetLastProbeAt() != 1755500000 || got.GetCertificate().GetLastProbeEdge() != "cloudflare" {
			t.Errorf("probe = %v %d %q", got.GetCertificate().GetLastProbeOk(), got.GetCertificate().GetLastProbeAt(), got.GetCertificate().GetLastProbeEdge())
		}
		if previewWildcard(context.Background(), owner, recorded).GetCertificate().GetLastProbeAt() != 0 {
			t.Error("an unprobed domain carries a probe timestamp")
		}
	})

	t.Run("a foreign or missing owner is not installed", func(t *testing.T) {
		t.Parallel()
		for _, owner := range []routeOwnerFunc{
			func(context.Context, string) (string, error) { return "", nil },
			func(context.Context, string) (string, error) { return "someone-elses-worker", nil },
			func(context.Context, string) (string, error) { return "", errors.New("no zone") },
		} {
			if previewWildcard(context.Background(), owner, recorded).GetRouteInstalled() {
				t.Error("RouteInstalled = true, want false")
			}
		}
	})
}

type stateSSM struct {
	params map[string]string
	puts   int
}

func (s *stateSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	raw, ok := s.params[aws.ToString(in.Name)]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(raw)}}, nil
}

func (s *stateSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	s.puts++
	s.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
	return &ssm.PutParameterOutput{}, nil
}

func (s *stateSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	out := &ssm.GetParametersByPathOutput{}
	for name := range s.params {
		if strings.HasPrefix(name, aws.ToString(in.Path)) {
			out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
		}
	}
	return out, nil
}

func (s *stateSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	delete(s.params, aws.ToString(in.Name))
	return &ssm.DeleteParameterOutput{}, nil
}

type releaseEdge struct {
	edge.Edge
	destroyed []string
}

func (e *releaseEdge) Kind() edge.Kind { return cloudflare.Kind }

func (e *releaseEdge) DestroyPreviewWildcard(_ context.Context, baseDomain string) error {
	e.destroyed = append(e.destroyed, baseDomain)
	return nil
}

type releaseWriter struct {
	deleted []edge.Record
}

func (w *releaseWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	return records, nil
}

func (w *releaseWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	w.deleted = append(w.deleted, records...)
	return nil
}

type releaseACM struct {
	deleted []string
}

func (a *releaseACM) RequestCertificate(context.Context, *acm.RequestCertificateInput, ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	return nil, errors.New("releasing a domain never requests a certificate")
}

func (a *releaseACM) DescribeCertificate(context.Context, *acm.DescribeCertificateInput, ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	return nil, errors.New("releasing a domain never describes a certificate")
}

func (a *releaseACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return nil, errors.New("releasing a domain never lists certificates")
}

func (a *releaseACM) DeleteCertificate(_ context.Context, in *acm.DeleteCertificateInput, _ ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.CertificateArn))
	return &acm.DeleteCertificateOutput{}, nil
}

func TestRemovePreviewWildcard(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	validation := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	front := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	recorded := bootstrap.PreviewDomain{
		BaseDomain: "preview.acme.com",
		Settlement: domains.Settlement{
			Certificate: certs.Certificate{ARN: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", Region: "us-east-1", Status: certs.StatusIssued, Validation: []edge.Record{validation}},
			Validation:  domains.Records{Written: []edge.Record{validation}},
			Hosts:       []domains.Host{{Hostname: "*.preview.acme.com", Records: domains.Records{Written: []edge.Record{front}}}},
		},
	}

	ssmc := &stateSSM{params: map[string]string{}}
	if err := bootstrap.WritePreviewDomain(ctx, ssmc, bootstrap.ClassPreview, recorded); err != nil {
		t.Fatalf("WritePreviewDomain: %v", err)
	}
	front1 := &releaseEdge{}
	writer := &releaseWriter{}
	api := &releaseACM{}

	if err := removePreviewWildcard(ctx, removalDeps{
		ssm:    ssmc,
		edge:   front1,
		writer: writer,
		issuer: certs.Issuer{API: api, Region: recorded.Settlement.Certificate.Region},
	}, recorded, func(string) {}); err != nil {
		t.Fatalf("removePreviewWildcard: %v", err)
	}

	if len(front1.destroyed) != 1 || front1.destroyed[0] != recorded.BaseDomain {
		t.Errorf("destroyed = %v, want the shared entry on %s removed", front1.destroyed, recorded.BaseDomain)
	}
	if len(writer.deleted) != 2 {
		t.Errorf("deleted = %+v, want every record ocel wrote removed, the validation record included", writer.deleted)
	}
	if len(api.deleted) != 1 || api.deleted[0] != recorded.Settlement.Certificate.ARN {
		t.Errorf("deleted certificates = %v, want %q discarded in the region it was issued in", api.deleted, recorded.Settlement.Certificate.ARN)
	}
	got, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain: %v", err)
	}
	if got.BaseDomain != "" {
		t.Errorf("recorded domain = %+v, want it deleted", got)
	}
}

func TestGlobalPreviewProjects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	for slug, state := range map[string]edge.StackState{
		"ambient":     {Slug: "ambient", GlobalPreview: "preview.acme.com"},
		"own-domain":  {Slug: "own-domain"},
		"other-usage": {Slug: "other-usage", GlobalPreview: "preview.old.com"},
	} {
		if err := bootstrap.WriteStackRecordFor(ctx, ssmc, bootstrap.ClassPreview, slug, bootstrap.StackRecord{Edge: state}); err != nil {
			t.Fatalf("WriteStackRecordFor(%s): %v", slug, err)
		}
	}

	served, err := globalPreviewProjects(ctx, ssmc, []string{"ambient", "own-domain", "other-usage", "never-deployed"}, "preview.acme.com")
	if err != nil {
		t.Fatalf("globalPreviewProjects: %v", err)
	}
	if len(served) != 1 || served[0] != "ambient" {
		t.Errorf("served = %v, want [ambient]: a project on its own preview domain is not served by the bootstrap's", served)
	}
}

func TestPreviewWildcardOwner(t *testing.T) {
	t.Parallel()

	const baseDomain = "preview.acme.com"

	t.Run("a project on another edge still sees the wildcard the recording edge holds", func(t *testing.T) {
		t.Parallel()

		recorded := bootstrap.PreviewDomain{BaseDomain: baseDomain, Edge: cloudflare.Kind}
		var asked []edge.Kind
		owner := previewWildcardOwner(recorded, func(kind edge.Kind) routeOwnerFunc {
			asked = append(asked, kind)
			return func(context.Context, string) (string, error) {
				if kind != cloudflare.Kind {
					return "", nil
				}
				return edge.PreviewEntryOwner, nil
			}
		})
		if !slices.Equal(asked, []edge.Kind{cloudflare.Kind}) {
			t.Errorf("edges asked = %v, want only the %s edge that stood the wildcard up", asked, cloudflare.Kind)
		}
		if !sharedEntryRouteInstalled(context.Background(), owner, baseDomain) {
			t.Error("RouteInstalled = false, want true: the wildcard is account-global, and a sibling project on another edge must not be told it is gone")
		}
	})

	t.Run("a record nothing names an edge for probes no edge and reads as not installed", func(t *testing.T) {
		t.Parallel()

		owner := previewWildcardOwner(bootstrap.PreviewDomain{BaseDomain: baseDomain}, func(edge.Kind) routeOwnerFunc {
			t.Error("an edge was probed for a wildcard whose owner is unknown")
			return nil
		})
		if owner != nil {
			t.Fatal("previewWildcardOwner returned an owner for a wildcard no edge is known to hold")
		}
		if sharedEntryRouteInstalled(context.Background(), owner, baseDomain) {
			t.Error("RouteInstalled = true, want false: an unknown holder cannot vouch for the route")
		}
	})
}

func TestPreviewWildcardHolder(t *testing.T) {
	t.Parallel()

	const baseDomain = "preview.acme.com"

	t.Run("the recorded edge holds it, whoever is asking", func(t *testing.T) {
		t.Parallel()

		got, err := previewWildcardHolder(bootstrap.PreviewDomain{BaseDomain: baseDomain, Edge: cloudflare.Kind})
		if err != nil {
			t.Fatalf("previewWildcardHolder: %v", err)
		}
		if got != cloudflare.Kind {
			t.Errorf("holder = %q, want %q", got, cloudflare.Kind)
		}
	})

	t.Run("an unknown holder is refused with a remedy that runs", func(t *testing.T) {
		t.Parallel()

		_, err := previewWildcardHolder(bootstrap.PreviewDomain{BaseDomain: baseDomain})
		if err == nil {
			t.Fatal("previewWildcardHolder err = nil, want a refusal rather than a teardown through a guessed edge")
		}
		for _, want := range []string{"*.preview.acme.com", "ocel domain use '*.preview.acme.com' --preview"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestPreviewWildcardEdge(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", Edge: cloudflare.Kind}
	held := &releaseEdge{}
	var asked []edge.Kind
	got, err := previewWildcardEdge(recorded, func(kind edge.Kind) (edge.Edge, error) {
		asked = append(asked, kind)
		return held, nil
	})
	if err != nil {
		t.Fatalf("previewWildcardEdge: %v", err)
	}
	if !slices.Equal(asked, []edge.Kind{cloudflare.Kind}) {
		t.Errorf("edges asked = %v, want only the recorded %s edge, never the requesting project's", asked, cloudflare.Kind)
	}
	if got != edge.Edge(held) {
		t.Error("previewWildcardEdge returned an edge other than the one it resolved")
	}

	if _, err := previewWildcardEdge(bootstrap.PreviewDomain{BaseDomain: "preview.acme.com"}, func(edge.Kind) (edge.Edge, error) {
		t.Error("an edge was opened for a wildcard whose holder is unknown")
		return nil, nil
	}); err == nil {
		t.Error("previewWildcardEdge err = nil, want the teardown refused rather than aimed at a guess")
	}
}

func TestRefuseRehomingPreviewWildcard(t *testing.T) {
	t.Parallel()

	const baseDomain = "preview.acme.com"

	t.Run("another edge's wildcard is not re-homed", func(t *testing.T) {
		t.Parallel()

		err := refuseRehomingPreviewWildcard(bootstrap.PreviewDomain{BaseDomain: baseDomain, Edge: cloudflare.Kind}, apigateway.Kind)
		if err == nil {
			t.Fatal("refuseRehomingPreviewWildcard = nil, want the Cloudflare entry protected from a project on another edge")
		}
		for _, want := range []string{"*.preview.acme.com", "cloudflare", "api-gateway", "ocel domain release --preview"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("the holding edge reconciles its own wildcard", func(t *testing.T) {
		t.Parallel()

		if err := refuseRehomingPreviewWildcard(bootstrap.PreviewDomain{BaseDomain: baseDomain, Edge: cloudflare.Kind}, cloudflare.Kind); err != nil {
			t.Fatalf("refuseRehomingPreviewWildcard = %v, want a rerun from the holding edge allowed", err)
		}
	})

	t.Run("an unknown holder is recorded rather than refused", func(t *testing.T) {
		t.Parallel()

		if err := refuseRehomingPreviewWildcard(bootstrap.PreviewDomain{BaseDomain: baseDomain}, apigateway.Kind); err != nil {
			t.Fatalf("refuseRehomingPreviewWildcard = %v, want the one command that can write the edge down allowed to run", err)
		}
	})

	t.Run("nothing recorded is nothing to re-home", func(t *testing.T) {
		t.Parallel()

		if err := refuseRehomingPreviewWildcard(bootstrap.PreviewDomain{}, apigateway.Kind); err != nil {
			t.Fatalf("refuseRehomingPreviewWildcard = %v, want a first use allowed", err)
		}
	})
}

func previewTestEngine(edgeFront edge.Edge, issuer certs.Issuer, writer edge.DNSWriter, prober certs.Prober, ssmc *stateSSM, baseDomain string) domains.Engine {
	return domains.Engine{
		Kind:          edgeFront.Kind(),
		ServesUnbound: edgeFront.Facts().ServesUnbound,
		Issuer:        issuer,
		Writer:        writer,
		Prober:        prober,
		Store: previewStore{
			ssm: ssmc,
			domain: bootstrap.PreviewDomain{
				BaseDomain: baseDomain,
				Edge:       edgeFront.Kind(),
				Scope:      edgeFront.Facts().CredentialScope,
				GrammarMin: edge.PreviewGrammarMin,
				GrammarMax: edge.PreviewGrammarMax,
			},
		},
	}
}

func TestUsePreviewWildcardRecordsTheEdgeHoldingIt(t *testing.T) {
	t.Parallel()

	const baseDomain = "preview.acme.com"

	ctx := context.Background()
	validation := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	api := &issuingACM{arn: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", domain: edge.PreviewWildcard(baseDomain), validation: validation}
	writer := &releaseWriter{}
	ssmc := &stateSSM{params: map[string]string{}}
	front := &wildcardEdge{front: "d-wild.execute-api.us-east-1.amazonaws.com"}
	prober := certs.Prober{Attempts: 1, Now: time.Now, Get: func(context.Context, string) (http.Header, error) {
		answer := http.Header{}
		answer.Set(edge.HeaderEdge, string(cloudfront.Kind))
		return answer, nil
	}}
	engine := previewTestEngine(front, certs.Issuer{API: api, Region: "us-east-1", Attempts: 1}, writer, prober, ssmc, baseDomain)

	if err := usePreviewWildcard(ctx, engine, front, edge.PreviewWildcardSpec{BaseDomain: baseDomain}, domains.Settlement{}, string(edge.PreviewWildcard(baseDomain)), func(string) {}); err != nil {
		t.Fatalf("usePreviewWildcard: %v", err)
	}
	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain: %v", err)
	}
	if recorded.Edge != cloudfront.Kind {
		t.Errorf("Edge = %q, want %q: every later lookup of this account-global wildcard asks the edge that raised it", recorded.Edge, cloudfront.Kind)
	}
	if recorded.Scope != "" {
		t.Errorf("Scope = %q, want nothing: the %s edge is not bound to a credential scope", recorded.Scope, cloudfront.Kind)
	}
}

func TestUsePreviewWildcardPointsAnUnboundEdgeAtItsProxy(t *testing.T) {
	t.Parallel()

	const baseDomain = "preview.acme.com"

	ctx := context.Background()
	writer := &fakeDNSWriter{}
	ssmc := &stateSSM{params: map[string]string{}}
	front := &unboundWildcardEdge{}
	prober := certs.Prober{Attempts: 1, Now: time.Now, Get: func(context.Context, string) (http.Header, error) {
		answer := http.Header{}
		answer.Set(edge.HeaderEdge, string(cloudflare.Kind))
		return answer, nil
	}}
	engine := previewTestEngine(front, certs.Issuer{}, writer, prober, ssmc, baseDomain)

	if err := usePreviewWildcard(ctx, engine, front, edge.PreviewWildcardSpec{BaseDomain: baseDomain}, domains.Settlement{}, string(edge.PreviewWildcard(baseDomain)), func(string) {}); err != nil {
		t.Fatalf("usePreviewWildcard: %v", err)
	}
	want := edge.Record{Name: edge.PreviewWildcard(baseDomain), Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	if len(writer.written) != 1 || writer.written[0] != want {
		t.Fatalf("written = %v, want %v: an edge that serves every hostname publishes no front to CNAME to", writer.written, want)
	}
	recorded, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain: %v", err)
	}
	if recorded.Scope != "cf-ambient" {
		t.Errorf("Scope = %q, want the scope the holding edge answered for itself", recorded.Scope)
	}
}

func TestGlobalPreviewScopeMismatch(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", Edge: cloudflare.Kind, Scope: "recorded-acct"}

	t.Run("a match passes", func(t *testing.T) {
		t.Parallel()
		if err := globalPreviewScopeMismatch(recorded, &scopedEdge{scope: "recorded-acct"}); err != nil {
			t.Fatalf("globalPreviewScopeMismatch: %v", err)
		}
	})

	t.Run("a mismatch refuses and names both accounts", func(t *testing.T) {
		t.Parallel()
		err := globalPreviewScopeMismatch(recorded, &scopedEdge{scope: "ambient-acct"})
		if err == nil {
			t.Fatal("expected a mismatched scope to be refused")
		}
		for _, want := range []string{"recorded-acct", "ambient-acct", "preview.acme.com", "cloudflare", "ocel domain release"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q, got %q", want, err)
			}
		}
	})

	t.Run("an edge bound to no scope is nothing to compare", func(t *testing.T) {
		t.Parallel()
		if err := globalPreviewScopeMismatch(recorded, &scopedEdge{}); err != nil {
			t.Fatalf("globalPreviewScopeMismatch: %v", err)
		}
	})

	t.Run("nothing recorded is nothing to compare", func(t *testing.T) {
		t.Parallel()
		if err := globalPreviewScopeMismatch(bootstrap.PreviewDomain{}, &scopedEdge{scope: "ambient-acct"}); err != nil {
			t.Fatalf("globalPreviewScopeMismatch: %v", err)
		}
	})
}

type scopedEdge struct {
	edge.Edge
	scope string
}

func (e *scopedEdge) Kind() edge.Kind { return cloudflare.Kind }

func (e *scopedEdge) Facts() edge.Facts { return edge.Facts{CredentialScope: e.scope} }

type wildcardEdge struct {
	edge.Edge
	front string
	specs []edge.PreviewWildcardSpec
}

func (e *wildcardEdge) Kind() edge.Kind { return cloudfront.Kind }

func (e *wildcardEdge) Facts() edge.Facts { return edge.Facts{} }

func (e *wildcardEdge) ReconcilePreviewWildcard(_ context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	e.specs = append(e.specs, spec)
	return e.front, nil
}

type unboundWildcardEdge struct {
	edge.Edge
}

func (e *unboundWildcardEdge) Facts() edge.Facts {
	return edge.Facts{ServesUnbound: true, CredentialScope: "cf-ambient"}
}

func (e *unboundWildcardEdge) Kind() edge.Kind { return cloudflare.Kind }

func (e *unboundWildcardEdge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", nil
}

type issuingACM struct {
	arn        string
	domain     string
	validation edge.Record
	requested  int
}

func (a *issuingACM) RequestCertificate(context.Context, *acm.RequestCertificateInput, ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	a.requested++
	return &acm.RequestCertificateOutput{CertificateArn: aws.String(a.arn)}, nil
}

func (a *issuingACM) DescribeCertificate(context.Context, *acm.DescribeCertificateInput, ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	return &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{
		CertificateArn: aws.String(a.arn),
		DomainName:     aws.String(a.domain),
		Status:         acmtypes.CertificateStatusIssued,
		DomainValidationOptions: []acmtypes.DomainValidation{{
			ResourceRecord: &acmtypes.ResourceRecord{
				Name:  aws.String(a.validation.Name),
				Type:  acmtypes.RecordType(a.validation.Type),
				Value: aws.String(a.validation.Value),
			},
		}},
	}}, nil
}

func (a *issuingACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return &acm.ListCertificatesOutput{}, nil
}

func (a *issuingACM) DeleteCertificate(context.Context, *acm.DeleteCertificateInput, ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	return nil, errors.New("using a domain never discards a certificate")
}

func TestUsePreviewWildcardResumed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const baseDomain = "preview.acme.com"
	validation := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	api := &issuingACM{arn: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", domain: edge.PreviewWildcard(baseDomain), validation: validation}
	front := &wildcardEdge{front: "d-wild.execute-api.us-east-1.amazonaws.com"}
	writer := &releaseWriter{}
	ssmc := &stateSSM{params: map[string]string{}}
	prober := certs.Prober{Attempts: 1, Now: time.Now, Get: func(context.Context, string) (http.Header, error) {
		answer := http.Header{}
		answer.Set(edge.HeaderEdge, string(cloudfront.Kind))
		return answer, nil
	}}
	engine := previewTestEngine(front, certs.Issuer{API: api, Region: "us-east-1", Attempts: 1}, writer, prober, ssmc, baseDomain)

	if err := usePreviewWildcard(ctx, engine, front, edge.PreviewWildcardSpec{BaseDomain: baseDomain}, domains.Settlement{}, string(edge.PreviewWildcard(baseDomain)), func(string) {}); err != nil {
		t.Fatalf("usePreviewWildcard: %v", err)
	}
	first, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain: %v", err)
	}
	if !slices.Contains(first.Settlement.WrittenRecords(), validation) {
		t.Fatalf("records = %+v, want the validation record among them", first.Settlement.WrittenRecords())
	}

	if err := usePreviewWildcard(ctx, engine, front, edge.PreviewWildcardSpec{BaseDomain: baseDomain}, first.Settlement, string(edge.PreviewWildcard(baseDomain)), func(string) {}); err != nil {
		t.Fatalf("usePreviewWildcard again: %v", err)
	}
	second, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain again: %v", err)
	}
	if !slices.Contains(second.Settlement.WrittenRecords(), validation) {
		t.Errorf("records = %+v, want the validation record to survive a resumed run: `ocel domain release` deletes only what is recorded here, and ACM renews the certificate through it", second.Settlement.WrittenRecords())
	}
	if api.requested != 1 {
		t.Errorf("requested = %d, want the certificate already issued to be reused", api.requested)
	}
	if len(front.specs) != 2 || front.specs[1].Certificate != api.arn {
		t.Errorf("specs = %+v, want the wildcard reconciled onto the issued certificate on both runs", front.specs)
	}
}
