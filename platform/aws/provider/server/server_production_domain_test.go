package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const domainSlug = "acme-web"

type callLog struct {
	calls []string
}

func (l *callLog) note(format string, args ...any) {
	if l == nil {
		return
	}
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

type boundStack struct {
	name      string
	log       *callLog
	state     edge.StackState
	bound     []edge.DomainBinding
	unbound   []string
	bindErr   error
	hostFront func(hostname string) string
}

func (s *boundStack) State() edge.StackState { return s.state }

func (s *boundStack) Ledger() edge.Ledger { return nil }

func (s *boundStack) Promote(context.Context, edge.Promotion, string) error { return nil }

func (s *boundStack) RemovePointer(context.Context, string) (edge.PruneResult, error) {
	return edge.PruneResult{}, nil
}

func (s *boundStack) BindDomain(_ context.Context, binding edge.DomainBinding) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	s.log.note("bind %s on %s", binding.Hostname, s.name)
	s.bound = append(s.bound, binding)
	s.state.Bind(binding.Hostname)
	if s.hostFront != nil {
		s.state.PublishFront(binding.Hostname, s.hostFront(binding.Hostname))
	}
	return nil
}

func (s *boundStack) UnbindDomain(_ context.Context, hostname string) error {
	s.log.note("unbind %s from %s", hostname, s.name)
	s.unbound = append(s.unbound, hostname)
	s.state.Release(hostname)
	return nil
}

func (s *boundStack) Destroy(context.Context) error { return nil }

func newBoundStack() *boundStack {
	return &boundStack{state: edge.StackState{Slug: domainSlug}}
}

type domainWriter struct {
	log     *callLog
	written []edge.Record
	deleted []edge.Record
}

func (w *domainWriter) RecordTTL() time.Duration { return 5 * time.Minute }

func (w *domainWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	for _, rec := range records {
		w.log.note("write %s", rec)
	}
	w.written = append(w.written, records...)
	return records, nil
}

func (w *domainWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	w.deleted = append(w.deleted, records...)
	return nil
}

type domainACM struct {
	log         *callLog
	requested   [][]string
	deleted     []string
	deleteErr   error
	listed      []acmtypes.CertificateSummary
	details     map[string]*acmtypes.CertificateDetail
	described   []string
	describeErr error
	minted      int
}

func newDomainACM() *domainACM {
	return &domainACM{details: map[string]*acmtypes.CertificateDetail{}}
}

func (a *domainACM) issue(arn string, names ...string) {
	a.details[arn] = &acmtypes.CertificateDetail{
		CertificateArn:          aws.String(arn),
		DomainName:              aws.String(names[0]),
		SubjectAlternativeNames: names[1:],
		Status:                  acmtypes.CertificateStatusIssued,
		DomainValidationOptions: validationFor(names),
	}
}

func validationFor(names []string) []acmtypes.DomainValidation {
	options := make([]acmtypes.DomainValidation, 0, len(names))
	for _, name := range names {
		options = append(options, acmtypes.DomainValidation{ResourceRecord: &acmtypes.ResourceRecord{
			Name:  aws.String("_ocel." + name + "."),
			Type:  acmtypes.RecordTypeCname,
			Value: aws.String("_target.acm-validations.aws."),
		}})
	}
	return options
}

func (a *domainACM) RequestCertificate(_ context.Context, in *acm.RequestCertificateInput, _ ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	names := append([]string{aws.ToString(in.DomainName)}, in.SubjectAlternativeNames...)
	a.log.note("request certificate for %s", strings.Join(names, ", "))
	a.requested = append(a.requested, names)
	a.minted++
	arn := fmt.Sprintf("arn:aws:acm:us-east-1:111122223333:certificate/minted-%d", a.minted)
	a.issue(arn, names...)
	return &acm.RequestCertificateOutput{CertificateArn: aws.String(arn)}, nil
}

func (a *domainACM) DescribeCertificate(_ context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	a.described = append(a.described, aws.ToString(in.CertificateArn))
	if a.describeErr != nil {
		return nil, a.describeErr
	}
	detail, ok := a.details[aws.ToString(in.CertificateArn)]
	if !ok {
		return nil, fmt.Errorf("no certificate %s", aws.ToString(in.CertificateArn))
	}
	return &acm.DescribeCertificateOutput{Certificate: detail}, nil
}

func (a *domainACM) DeleteCertificate(_ context.Context, in *acm.DeleteCertificateInput, _ ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	if a.deleteErr != nil {
		return nil, a.deleteErr
	}
	a.deleted = append(a.deleted, aws.ToString(in.CertificateArn))
	return &acm.DeleteCertificateOutput{}, nil
}

func (a *domainACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return &acm.ListCertificatesOutput{CertificateSummaryList: a.listed}, nil
}

type domainFixture struct {
	session *domainSession
	stack   *boundStack
	writer  *domainWriter
	acm     *domainACM
	ssm     *stateSSM
	log     *callLog
	said    []string
	asked   []edge.Record
}

func (f *domainFixture) say(message string) { f.said = append(f.said, message) }

func (f *domainFixture) askedFor(want edge.Record) bool {
	return slices.Contains(f.asked, want)
}

func (f *domainFixture) spoke(want string) bool {
	return slices.ContainsFunc(f.said, func(line string) bool { return strings.Contains(line, want) })
}

func (f *domainFixture) recorded(t *testing.T) domains.Settlement {
	t.Helper()
	record, err := bootstrap.ReadStackRecordFor(t.Context(), f.ssm, bootstrap.ClassProduction, domainSlug)
	if err != nil {
		t.Fatalf("ReadStackRecordFor: %v", err)
	}
	return record.Production
}

func (f *domainFixture) surfaces(t *testing.T) []string {
	t.Helper()
	record, err := bootstrap.ReadStackRecordFor(t.Context(), f.ssm, bootstrap.ClassProduction, domainSlug)
	if err != nil {
		t.Fatalf("ReadStackRecordFor: %v", err)
	}
	return record.Edge.Bound
}

type domainFixtureOptions struct {
	edge       edge.EdgeStack
	configured []string
	host       string
	pins       map[string]string
	prior      domains.Settlement
	certified  bool
	kind       edge.Kind
	priorEdge  *boundStack
	stack      *boundStack
	writer     *domainWriter
	acm        *domainACM
	probe      string
}

func newDomainFixture(opts domainFixtureOptions) *domainFixture {
	if opts.stack == nil {
		opts.stack = newBoundStack()
	}
	if opts.acm == nil {
		opts.acm = newDomainACM()
	}
	kind := opts.kind
	if kind == "" {
		kind = cloudflare.Kind
	}
	probe := opts.probe
	if probe == "" {
		probe = string(kind)
	}
	if opts.stack.name == "" {
		opts.stack.name = string(kind)
	}
	log := &callLog{}
	opts.stack.log, opts.acm.log = log, log
	if opts.writer != nil {
		opts.writer.log = log
	}
	if opts.priorEdge != nil {
		opts.priorEdge.log = log
	}
	f := &domainFixture{stack: opts.stack, writer: opts.writer, acm: opts.acm, ssm: &stateSSM{params: map[string]string{}}, log: log}
	opened := edge.EdgeStack(opts.stack)
	if opts.edge != nil {
		opened = opts.edge
	}
	held := &stackWriter{ssm: f.ssm, slug: domainSlug, stack: opened, settled: opts.prior}
	front := edge.EdgeStack(persisting(opened, held.write))

	var writer edge.DNSWriter
	if opts.writer != nil {
		writer = opts.writer
	}
	engine := domains.Engine{
		Kind:          kind,
		ServesUnbound: registeredEdge(kind).Facts().ServesUnbound,
		Writer:        writer,
		Poller: dns.Poller{
			Lookup:   func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil },
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
		},
		Prober: certs.Prober{
			Get: func(_ context.Context, target string) (http.Header, error) {
				log.note("probe %s", target)
				header := http.Header{}
				header.Set(edge.HeaderEdge, probe)
				return header, nil
			},
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
			Now:      func() time.Time { return time.Unix(1755500000, 0).UTC() },
			Jitter:   func() float64 { return 0.5 },
		},
		Discarder: func(certs.Certificate) certs.Issuer {
			return certs.Issuer{API: opts.acm, Region: certs.CloudFrontRegion}
		},
		Store: held,
		Unbind: func(ctx context.Context, hostname string) error {
			return front.UnbindDomain(ctx, hostname)
		},
		Ask: func(_ string, records []edge.Record, _ ...string) {
			f.asked = append(f.asked, records...)
		},
	}
	if opts.priorEdge != nil {
		engine.Open = func(edge.Kind) (edge.EdgeStack, error) { return opts.priorEdge, nil }
	}
	switch {
	case opts.kind != "":
		engine.Issuer = fakeIssuer(opts.kind, opts.acm)
	case opts.certified:
		engine.Issuer = certs.Issuer{
			API:      opts.acm,
			Region:   certs.CloudFrontRegion,
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 2,
			Every:    time.Millisecond,
		}
	}
	f.session = &domainSession{
		engine:     engine,
		stack:      front,
		recorded:   opts.prior,
		configured: opts.configured,
		host:       opts.host,
		pins:       opts.pins,
	}
	return f
}

func TestAddDomain(t *testing.T) {
	t.Parallel()

	t.Run("with no argument it settles every configured host", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			writer:     &domainWriter{},
			certified:  true,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}

		if len(f.acm.requested) != 1 || !slices.Equal(f.acm.requested[0], []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("requested = %v, want one SAN certificate over both hosts", f.acm.requested)
		}
		if len(f.stack.bound) != 2 || f.stack.bound[0].Hostname != "shop.app.com" || f.stack.bound[1].Hostname != "www.app.com" {
			t.Errorf("bound = %+v, want both hosts bound", f.stack.bound)
		}
		if f.stack.bound[0].Certificate == "" {
			t.Error("the binding carries no certificate")
		}

		recorded := f.recorded(t)
		if !slices.Equal(recorded.Hostnames(), []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("recorded hosts = %v", recorded.Hostnames())
		}
		if !recorded.Ready("shop.app.com", cloudflare.Kind) || !recorded.Ready("www.app.com", cloudflare.Kind) {
			t.Errorf("recorded = %+v, want both hosts probed live", recorded.Hosts)
		}
		if len(recorded.Host("shop.app.com").Records.Written) != 1 {
			t.Errorf("recorded records = %+v, want the front record ocel wrote", recorded.Host("shop.app.com").Records.Written)
		}
		if recorded.Certificate.ARN == "" || recorded.Certificate.Adopted {
			t.Errorf("recorded certificate = %+v, want the one ocel requested", recorded.Certificate)
		}
		if !slices.Equal(f.surfaces(t), []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("surfaces = %v, want both recorded on the stack state", f.surfaces(t))
		}
	})

	t.Run("an argument settles only that host", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			host:       "www.app.com",
			writer:     &domainWriter{},
			certified:  true,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(f.stack.bound) != 1 || f.stack.bound[0].Hostname != "www.app.com" {
			t.Errorf("bound = %+v, want only the named host", f.stack.bound)
		}
		if !slices.Equal(f.acm.requested[0], []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("requested = %v, want the certificate to cover everything configured", f.acm.requested)
		}
	})

	t.Run("a host the config does not declare is refused", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{configured: []string{"shop.app.com"}, host: "other.app.com"})
		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want a refusal: the config is the declaration")
		}
		if !strings.Contains(err.Error(), "domains.production") {
			t.Errorf("err = %v, want it to name where the hostname is declared", err)
		}
	})

	t.Run("a project declaring nothing is refused", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{})
		if err := f.session.add(t.Context(), f.say); err == nil {
			t.Fatal("add err = nil, want a refusal with nothing declared")
		}
	})

	t.Run("a host set that changed swaps the certificate", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue("arn:aws:acm:us-east-1:111122223333:certificate/old", "shop.app.com")
		prior := domains.Settlement{
			Certificate: certs.Certificate{
				ARN:     "arn:aws:acm:us-east-1:111122223333:certificate/old",
				Region:  certs.CloudFrontRegion,
				Status:  certs.StatusIssued,
				Domains: []string{"shop.app.com"},
			},
			Hosts: []domains.Host{{
				Hostname:    "shop.app.com",
				Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/old",
				Probe:       certs.Probe{At: time.Unix(1, 0), Edge: cloudflare.Kind, OK: true},
			}},
		}
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			writer:     &domainWriter{},
			certified:  true,
			acm:        api,
			prior:      prior,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(api.requested) != 1 || !slices.Equal(api.requested[0], []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("requested = %v, want a fresh certificate over the new host set", api.requested)
		}
		recorded := f.recorded(t)
		if recorded.Certificate.ARN == prior.Certificate.ARN || recorded.Certificate.ARN == "" {
			t.Fatalf("recorded certificate = %+v, want the fresh one", recorded.Certificate)
		}
		for _, host := range []string{"shop.app.com", "www.app.com"} {
			if got := recorded.Host(host).Certificate; got != recorded.Certificate.ARN {
				t.Errorf("%s holds certificate %q, want the settled %q", host, got, recorded.Certificate.ARN)
			}
			if !slices.ContainsFunc(f.stack.bound, func(b edge.DomainBinding) bool {
				return b.Hostname == host && b.Certificate == recorded.Certificate.ARN
			}) {
				t.Errorf("bound = %+v, want %s re-bound behind the new certificate", f.stack.bound, host)
			}
		}
		if !slices.Equal(api.deleted, []string{prior.Certificate.ARN}) {
			t.Errorf("deleted = %v, want the superseded certificate discarded once nothing holds it", api.deleted)
		}
	})

	t.Run("discarding the superseded certificate keeps the validation records the new one renews through", func(t *testing.T) {
		t.Parallel()

		shopValidation := edge.Record{Name: "_ocel.shop.app.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
		api := newDomainACM()
		api.issue("arn:aws:acm:us-east-1:111122223333:certificate/old", "shop.app.com")
		prior := domains.Settlement{
			Certificate: certs.Certificate{
				ARN:     "arn:aws:acm:us-east-1:111122223333:certificate/old",
				Region:  certs.CloudFrontRegion,
				Status:  certs.StatusIssued,
				Domains: []string{"shop.app.com"},
			},
			Validation: domains.Records{Written: []edge.Record{shopValidation}},
			Hosts: []domains.Host{{
				Hostname:    "shop.app.com",
				Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/old",
				Probe:       certs.Probe{At: time.Unix(1, 0), Edge: cloudflare.Kind, OK: true},
			}},
		}
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			writer:     &domainWriter{},
			certified:  true,
			acm:        api,
			prior:      prior,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if slices.Contains(f.writer.deleted, shopValidation) {
			t.Errorf("deleted = %+v, want the validation record the new certificate renews through left standing", f.writer.deleted)
		}
		if !slices.Contains(f.recorded(t).Validation.Written, shopValidation) {
			t.Errorf("recorded validation = %+v, want it still written for the new certificate", f.recorded(t).Validation.Written)
		}
	})

	t.Run("a binding that fails is still recorded so rm can undo it", func(t *testing.T) {
		t.Parallel()

		stack := newBoundStack()
		stack.bindErr = errors.New("the edge refused")
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			writer:     &domainWriter{},
			stack:      stack,
		})
		if err := f.session.add(t.Context(), f.say); err == nil {
			t.Fatal("add err = nil, want the edge's refusal")
		}
		if !slices.Equal(f.recorded(t).Hostnames(), []string{"shop.app.com"}) {
			t.Errorf("recorded = %v, want the intended host on record before the edge was touched", f.recorded(t).Hostnames())
		}
	})

	t.Run("a certificate that already covers the set is reused by name", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.listed = []acmtypes.CertificateSummary{{
			CertificateArn:                  aws.String("arn:aws:acm:us-east-1:111122223333:certificate/theirs"),
			DomainName:                      aws.String("*.app.com"),
			SubjectAlternativeNameSummaries: []string{"app.com"},
		}}
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			writer:     &domainWriter{},
			certified:  true,
			acm:        api,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(api.requested) != 0 {
			t.Errorf("requested = %v, want the certificate already in ACM reused", api.requested)
		}
		recorded := f.recorded(t)
		if recorded.Certificate.ARN != "arn:aws:acm:us-east-1:111122223333:certificate/theirs" || !recorded.Certificate.Adopted {
			t.Errorf("recorded certificate = %+v, want the reused one, unclaimed", recorded.Certificate)
		}
	})

	t.Run("a pinned certificate is verified and never requested", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue("arn:aws:acm:us-east-1:111122223333:certificate/pinned", "shop.app.com")
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			pins:       map[string]string{"shop.app.com": "arn:aws:acm:us-east-1:111122223333:certificate/pinned"},
			writer:     &domainWriter{},
			certified:  true,
			acm:        api,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(api.requested) != 0 {
			t.Errorf("requested = %v, want nothing requested when every host is pinned", api.requested)
		}
		if f.stack.bound[0].Certificate != "arn:aws:acm:us-east-1:111122223333:certificate/pinned" {
			t.Errorf("binding = %+v, want the pinned certificate", f.stack.bound[0])
		}
		if f.recorded(t).Certificate.ARN != "" {
			t.Error("a pinned certificate was claimed as ocel's own")
		}
	})

	t.Run("a pinned certificate that does not cover the host is refused", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue("arn:aws:acm:us-east-1:111122223333:certificate/elsewhere", "other.example.com")
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			pins:       map[string]string{"shop.app.com": "arn:aws:acm:us-east-1:111122223333:certificate/elsewhere"},
			certified:  true,
			acm:        api,
		})
		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want a refusal: the pin covers nothing here")
		}
		for _, want := range []string{"shop.app.com", "other.example.com"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
		if len(f.stack.bound) != 0 {
			t.Errorf("bound = %+v, want nothing bound behind a certificate that covers nothing", f.stack.bound)
		}
	})

	t.Run("a pinned certificate in another region is refused", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue("arn:aws:acm:eu-west-1:111122223333:certificate/elsewhere", "shop.app.com")
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			pins:       map[string]string{"shop.app.com": "arn:aws:acm:eu-west-1:111122223333:certificate/elsewhere"},
			certified:  true,
			acm:        api,
		})
		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want a refusal: TLS never terminates in that region")
		}
		for _, want := range []string{"eu-west-1", certs.CloudFrontRegion} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("a rerun resumes from what is recorded", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.issue("arn:aws:acm:us-east-1:111122223333:certificate/half", "shop.app.com")
		prior := domains.Settlement{
			Certificate: certs.Certificate{
				ARN:     "arn:aws:acm:us-east-1:111122223333:certificate/half",
				Region:  certs.CloudFrontRegion,
				Status:  certs.StatusPendingValidation,
				Domains: []string{"shop.app.com"},
			},
		}
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			writer:     &domainWriter{},
			certified:  true,
			acm:        api,
			prior:      prior,
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(api.requested) != 0 {
			t.Errorf("requested = %v, want the half-finished certificate picked up, not a second one", api.requested)
		}
		if !f.recorded(t).Ready("shop.app.com", cloudflare.Kind) {
			t.Error("the resumed run did not finish the host")
		}
	})

	t.Run("an already-served host is left alone", func(t *testing.T) {
		t.Parallel()

		prior := domains.Settlement{Hosts: []domains.Host{{
			Hostname: "shop.app.com",
			Probe:    certs.Probe{At: time.Unix(1, 0), Edge: cloudflare.Kind, OK: true},
		}}}
		f := newDomainFixture(domainFixtureOptions{configured: []string{"shop.app.com"}, prior: prior})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(f.stack.bound) != 0 {
			t.Errorf("bound = %+v, want a host already served left alone", f.stack.bound)
		}
		if !f.spoke("already served") {
			t.Errorf("said = %v, want it to say there was nothing to do", f.said)
		}
	})

	t.Run("without a dns writer the record is handed to the user and waited on", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{configured: []string{"shop.app.com"}})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if !f.askedFor(edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}) {
			t.Errorf("asked = %+v, want the record the user has to add", f.asked)
		}
		if f.spoke("Writing ") {
			t.Errorf("said = %v, want nothing claimed as written with no writer in hand", f.said)
		}
		if owed := f.recorded(t).Host("shop.app.com").Records.Owed; len(owed) != 1 {
			t.Errorf("owed = %+v, want the record recorded as the user's", owed)
		}
	})

	t.Run("a probe that never answers as this edge fails and names what is outstanding", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{configured: []string{"shop.app.com"}, probe: "someone-else"})
		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want a bounded refusal")
		}
		for _, want := range []string{"shop.app.com", "still outstanding"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
		if f.recorded(t).Ready("shop.app.com", cloudflare.Kind) {
			t.Error("a failed probe was recorded as live")
		}
	})
}

func TestAddDomainOnCloudflare(t *testing.T) {
	t.Run("the cloudflare edge binds the worker route the host is served by", func(t *testing.T) {
		front := cloudflareFront(t)
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			writer:     &domainWriter{},
			edge:       front.stack(t),
		})
		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}
		if len(front.routes) != 1 || front.routes[0]["pattern"] != "shop.app.com/*" || front.routes[0]["script"] != cloudflareEntryScript {
			t.Errorf("routes = %v, want shop.app.com/* on %s", front.routes, cloudflareEntryScript)
		}
	})

	t.Run("a host the cloudflare edge terminates no TLS for is refused before DNS", func(t *testing.T) {
		front := cloudflareFront(t)
		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"eu.shop.app.com"},
			writer:     &domainWriter{},
			edge:       front.stack(t),
		})
		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want the edge's refusal surfaced")
		}
		if !strings.Contains(err.Error(), "Advanced Certificate") {
			t.Errorf("err = %v, want the certificate-pack message the edge gave", err)
		}
		if len(front.routes) != 0 {
			t.Errorf("routes = %v, want nothing bound for a host nothing terminates TLS for", front.routes)
		}
		if len(f.writer.written) != 0 {
			t.Errorf("written = %+v, want no record for a host nothing terminates TLS for", f.writer.written)
		}
	})

}

func TestRemoveDomain(t *testing.T) {
	t.Parallel()

	written := edge.Record{Name: "www.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	owed := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	prior := func() domains.Settlement {
		return domains.Settlement{
			Certificate: certs.Certificate{
				ARN:     "arn:aws:acm:us-east-1:111122223333:certificate/ours",
				Region:  certs.CloudFrontRegion,
				Status:  certs.StatusIssued,
				Domains: []string{"shop.app.com", "www.app.com"},
			},
			Hosts: []domains.Host{
				{Hostname: "shop.app.com", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/ours", Records: domains.Records{Owed: []edge.Record{owed}}},
				{Hostname: "www.app.com", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/ours", Records: domains.Records{Written: []edge.Record{written}}},
			},
		}
	}

	t.Run("with no argument it removes every host the config dropped", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com"},
			writer:     &domainWriter{},
			prior:      prior(),
		})
		f.stack.state.Bind("shop.app.com")
		f.stack.state.Bind("www.app.com")

		if err := f.session.remove(t.Context(), f.say); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if !slices.Equal(f.stack.unbound, []string{"www.app.com"}) {
			t.Errorf("unbound = %v, want only the host the config dropped", f.stack.unbound)
		}
		if len(f.writer.deleted) != 1 || f.writer.deleted[0] != written {
			t.Errorf("deleted = %+v, want only the record ocel wrote", f.writer.deleted)
		}
		if !slices.Equal(f.recorded(t).Hostnames(), []string{"shop.app.com"}) {
			t.Errorf("recorded = %v, want the removed host gone from state", f.recorded(t).Hostnames())
		}
		if !slices.Equal(f.surfaces(t), []string{"shop.app.com"}) {
			t.Errorf("surfaces = %v, want the removed surface gone", f.surfaces(t))
		}
		if len(f.acm.deleted) != 0 {
			t.Errorf("deleted certificates = %v, want the certificate a remaining host holds kept", f.acm.deleted)
		}
	})

	t.Run("removing the last host discards the certificate ocel requested", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{writer: &domainWriter{}, prior: prior()})
		if err := f.session.remove(t.Context(), f.say); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if !slices.Equal(f.acm.deleted, []string{"arn:aws:acm:us-east-1:111122223333:certificate/ours"}) {
			t.Errorf("deleted certificates = %v, want the one ocel requested discarded", f.acm.deleted)
		}
		recorded := f.recorded(t)
		if len(recorded.Hosts) != 0 || recorded.Certificate.ARN != "" {
			t.Errorf("recorded = %+v, want nothing left", recorded)
		}
	})

	t.Run("a certificate ACM refuses to delete is not forgotten", func(t *testing.T) {
		t.Parallel()

		api := newDomainACM()
		api.deleteErr = errors.New("ResourceInUseException")
		f := newDomainFixture(domainFixtureOptions{writer: &domainWriter{}, acm: api, prior: prior()})
		if err := f.session.remove(t.Context(), f.say); err == nil {
			t.Fatal("remove err = nil, want the ACM refusal surfaced rather than swallowed")
		}
		if f.recorded(t).Certificate.ARN == "" {
			t.Error("the certificate was forgotten even though ACM still holds it")
		}
	})

	t.Run("a record the user owns and a certificate ocel never requested are left standing", func(t *testing.T) {
		t.Parallel()

		state := prior()
		state.Certificate.Adopted = true
		f := newDomainFixture(domainFixtureOptions{writer: &domainWriter{}, prior: state})

		if err := f.session.remove(t.Context(), f.say); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if slices.Contains(f.writer.deleted, owed) {
			t.Errorf("deleted = %+v, want the record ocel never wrote left alone", f.writer.deleted)
		}
		if len(f.acm.deleted) != 0 {
			t.Errorf("deleted certificates = %v, want a certificate ocel never requested left standing", f.acm.deleted)
		}
		if !f.spoke("did not request it") {
			t.Errorf("said = %v, want it to say why the certificate stays", f.said)
		}
	})

	t.Run("an argument removes that host even while it is still declared", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			configured: []string{"shop.app.com", "www.app.com"},
			host:       "www.app.com",
			writer:     &domainWriter{},
			prior:      prior(),
		})
		if err := f.session.remove(t.Context(), f.say); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if !slices.Equal(f.stack.unbound, []string{"www.app.com"}) {
			t.Errorf("unbound = %v", f.stack.unbound)
		}
	})

	t.Run("a host this project never served is refused", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{host: "nope.app.com", prior: prior()})
		err := f.session.remove(t.Context(), f.say)
		if err == nil {
			t.Fatal("remove err = nil, want a refusal naming what is served")
		}
		if !strings.Contains(err.Error(), "shop.app.com") {
			t.Errorf("err = %v, want it to name what this project does serve", err)
		}
	})

	t.Run("nothing dropped is nothing to do", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{configured: []string{"shop.app.com", "www.app.com"}, prior: prior()})
		if err := f.session.remove(t.Context(), f.say); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if len(f.stack.unbound) != 0 {
			t.Errorf("unbound = %v, want nothing removed while the config still declares it", f.stack.unbound)
		}
	})
}

func TestReleaseProductionDomains(t *testing.T) {
	t.Parallel()

	front := edge.Record{Name: "shop.app.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	validation := edge.Record{Name: "_ocel.shop.app.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	recorded := domains.Settlement{
		Certificate: certs.Certificate{ARN: "arn:aws:acm:us-east-1:111122223333:certificate/ours", Region: certs.CloudFrontRegion, Status: certs.StatusIssued},
		Validation:  domains.Records{Written: []edge.Record{validation}},
		Hosts:       []domains.Host{{Hostname: "shop.app.com", Records: domains.Records{Written: []edge.Record{front}}}},
	}
	writer := &domainWriter{}
	api := newDomainACM()
	discarder := func(certs.Certificate) certs.Issuer {
		return certs.Issuer{API: api, Region: certs.CloudFrontRegion}
	}
	if err := releaseProductionDomains(t.Context(), recorded, writer, discarder, func(string) {}); err != nil {
		t.Fatalf("releaseProductionDomains: %v", err)
	}
	if !slices.Equal(writer.deleted, []edge.Record{front, validation}) {
		t.Errorf("deleted = %+v, want the front record and the certificate's validation record released", writer.deleted)
	}
	if !slices.Equal(api.deleted, []string{recorded.Certificate.ARN}) {
		t.Errorf("deleted certificates = %v, want the one ocel requested discarded on teardown", api.deleted)
	}
}

func TestProductionHost(t *testing.T) {
	t.Parallel()

	got, err := productionHost("  Shop.App.com. ")
	if err != nil {
		t.Fatalf("productionHost: %v", err)
	}
	if got != "shop.app.com" {
		t.Errorf("got %q", got)
	}
	for _, in := range []string{"*.app.com", "https://app.com", "app"} {
		if _, err := productionHost(in); err == nil {
			t.Errorf("expected %q to be refused", in)
		}
	}
}

const cloudflareEntryScript = "ocel-acme-web-prod"

type cloudflareEdge struct {
	zoneID string
	routes []map[string]any
}

func cloudflareFront(t *testing.T) *cloudflareEdge {
	t.Helper()
	return &cloudflareEdge{zoneID: "zone1"}
}

func (c *cloudflareEdge) stack(t *testing.T) edge.EdgeStack {
	t.Helper()
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Setenv("CLOUDFLARE_API_TOKEN", "test")

	write := func(w http.ResponseWriter, result any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "errors": []any{}, "messages": []any{}, "result": result,
			"result_info": map[string]any{"page": 1, "per_page": 100, "count": 1, "total_count": 1},
		})
	}
	firstPage := func(r *http.Request) bool {
		page := r.URL.Query().Get("page")
		return page == "" || page == "1"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if !firstPage(r) {
			write(w, []any{})
			return
		}
		write(w, []map[string]any{{"id": c.zoneID, "name": "app.com"}})
	})
	mux.HandleFunc("GET /zones/"+c.zoneID+"/ssl/certificate_packs", func(w http.ResponseWriter, _ *http.Request) {
		write(w, []any{})
	})
	mux.HandleFunc("/zones/"+c.zoneID+"/workers/routes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			c.routes = append(c.routes, body)
			write(w, map[string]any{"id": "route-1", "pattern": body["pattern"], "script": body["script"]})
			return
		}
		write(w, c.routes)
	})
	mux.HandleFunc("/zones/"+c.zoneID+"/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		write(w, []any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	stack, err := cloudflare.NewAt(srv.URL + "/").Open(edge.StackState{
		Slug:    domainSlug,
		Adapter: edge.Own(map[string][]string{"entryWorkers": {cloudflareEntryScript}}),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return stack
}

func frontRecords(written []edge.Record) []edge.Record {
	return slices.DeleteFunc(slices.Clone(written), func(rec edge.Record) bool {
		return strings.HasPrefix(rec.Name, "_")
	})
}

func TestAddDomainOnAnEdgeThatIsNotCloudflare(t *testing.T) {
	t.Parallel()

	t.Run("the API Gateway edge points each host at the front its binding published", func(t *testing.T) {
		t.Parallel()

		stack := newBoundStack()
		stack.hostFront = func(hostname string) string {
			return "d-" + strings.ReplaceAll(hostname, ".", "-") + ".execute-api.eu-west-1.amazonaws.com"
		}
		writer := &domainWriter{}
		f := newDomainFixture(domainFixtureOptions{
			kind:       apigateway.Kind,
			stack:      stack,
			configured: []string{"shop.app.com", "www.app.com"},
			writer:     writer,
		})

		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}

		want := []edge.Record{
			{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "d-shop-app-com.execute-api.eu-west-1.amazonaws.com"},
			{Name: "www.app.com", Type: edge.RecordTypeCNAME, Value: "d-www-app-com.execute-api.eu-west-1.amazonaws.com"},
		}
		if front := frontRecords(writer.written); !slices.Equal(front, want) {
			t.Errorf("written = %v, want %v", front, want)
		}
	})

	t.Run("the CloudFront edge points every host at the one distribution fronting them", func(t *testing.T) {
		t.Parallel()

		stack := newBoundStack()
		stack.state.Front = "d123.cloudfront.net"
		writer := &domainWriter{}
		f := newDomainFixture(domainFixtureOptions{
			kind:       cloudfront.Kind,
			stack:      stack,
			configured: []string{"shop.app.com"},
			writer:     writer,
		})

		if err := f.session.add(t.Context(), f.say); err != nil {
			t.Fatalf("add: %v", err)
		}

		want := []edge.Record{{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "d123.cloudfront.net"}}
		if front := frontRecords(writer.written); !slices.Equal(front, want) {
			t.Errorf("written = %v, want %v", front, want)
		}
	})

	t.Run("an edge that published no front for the host is refused by name", func(t *testing.T) {
		t.Parallel()

		f := newDomainFixture(domainFixtureOptions{
			kind:       apigateway.Kind,
			configured: []string{"shop.app.com"},
			writer:     &domainWriter{},
		})

		err := f.session.add(t.Context(), f.say)
		if err == nil {
			t.Fatal("add err = nil, want a refusal: the binding published nothing to point DNS at")
		}
		if !strings.Contains(err.Error(), "shop.app.com") {
			t.Errorf("err = %v, want it to name the host with nothing to point at", err)
		}
	})
}

func TestPersistingStackWritesWhatACallReports(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	front := newBoundStack()
	front.log = &callLog{}
	held := &stackWriter{ssm: ssmc, slug: domainSlug, stack: front}
	stack := persisting(front, held.write)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.app.com"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	recorded, err := bootstrap.ReadStackRecordFor(ctx, ssmc, bootstrap.ClassProduction, domainSlug)
	if err != nil {
		t.Fatalf("ReadStackRecordFor: %v", err)
	}
	if !recorded.Edge.BoundTo("shop.app.com") {
		t.Fatalf("recorded = %+v, want the binding persisted without a checkpoint of its own at the call site", recorded.Edge)
	}

	writes := ssmc.puts
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.app.com"}); err != nil {
		t.Fatalf("BindDomain again: %v", err)
	}
	if ssmc.puts != writes {
		t.Errorf("writes = %d, want the %d already made: a call that changed nothing reports nothing to persist", ssmc.puts, writes)
	}

	if err := stack.UnbindDomain(ctx, "shop.app.com"); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}
	recorded, err = bootstrap.ReadStackRecordFor(ctx, ssmc, bootstrap.ClassProduction, domainSlug)
	if err != nil {
		t.Fatalf("ReadStackRecordFor after unbind: %v", err)
	}
	if recorded.Edge.BoundTo("shop.app.com") {
		t.Errorf("recorded = %+v, want the host released", recorded.Edge)
	}
}
