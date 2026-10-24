package certs

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	certmanager "google.golang.org/api/certificatemanager/v1"
)

type certServer struct {
	mu sync.Mutex

	authorizations map[string]*certmanager.DnsAuthorization
	certificates   map[string]*certmanager.Certificate

	provisioning          int
	deleted               []string
	failure               string
	refusedAuthorizations int
}

func newCertServer() *certServer {
	return &certServer{
		authorizations: map[string]*certmanager.DnsAuthorization{},
		certificates:   map[string]*certmanager.Certificate{},
	}
}

func (s *certServer) open(t *testing.T) Certificates {
	t.Helper()
	server := httptest.NewServer(s.serve(t))
	t.Cleanup(server.Close)
	return New(googletest.Opener(googletest.Clients(server.URL)))
}

func (s *certServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		switch {
		case strings.Contains(name, "/operations/"):
			googletest.WriteBody(w, &certmanager.Operation{Name: name, Done: true})
		case r.Method == http.MethodPost && strings.HasSuffix(name, "/dnsAuthorizations"):
			s.authorize(w, r, name)
		case r.Method == http.MethodPost && strings.HasSuffix(name, "/certificates"):
			s.certify(w, r, name)
		case r.Method == http.MethodDelete:
			s.drop(w, name)
		case r.Method == http.MethodGet && strings.Contains(name, "/dnsAuthorizations/"):
			s.readAuthorization(w, name)
		case r.Method == http.MethodGet && strings.Contains(name, "/certificates/"):
			s.readCertificate(w, name)
		default:
			t.Errorf("the certifier called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *certServer) authorize(w http.ResponseWriter, r *http.Request, parent string) {
	asked := googletest.ReadBody[certmanager.DnsAuthorization](w, r)
	if asked == nil {
		return
	}
	name := parent + "/" + r.URL.Query().Get("dnsAuthorizationId")
	asked.Name = name
	asked.DnsResourceRecord = &certmanager.DnsResourceRecord{
		Name: "_acme-challenge." + asked.Domain + ".",
		Type: "CNAME",
		Data: "abc123.authorize.certificatemanager.goog.",
	}
	s.authorizations[name] = asked
	googletest.WriteBody(w, &certmanager.Operation{Name: "operations/authorize", Done: true})
}

func (s *certServer) certify(w http.ResponseWriter, r *http.Request, parent string) {
	asked := googletest.ReadBody[certmanager.Certificate](w, r)
	if asked == nil {
		return
	}
	name := parent + "/" + r.URL.Query().Get("certificateId")
	asked.Name = name
	asked.ExpireTime = "2027-01-02T15:04:05Z"
	if asked.Managed != nil {
		asked.Managed.State = certificateActive
		asked.SanDnsnames = slices.Clone(asked.Managed.Domains)
	}
	s.certificates[name] = asked
	googletest.WriteBody(w, &certmanager.Operation{Name: "operations/certify", Done: true})
}

func (s *certServer) drop(w http.ResponseWriter, name string) {
	_, authorized := s.authorizations[name]
	_, certified := s.certificates[name]
	if !authorized && !certified {
		googletest.WriteNotFound(w)
		return
	}
	if authorized && s.refusedAuthorizations > 0 {
		s.refusedAuthorizations--
		googletest.WriteBody(w, &certmanager.Operation{Name: "operations/drop", Done: true,
			Error: &certmanager.Status{Message: "the service is unavailable"}})
		return
	}
	s.deleted = append(s.deleted, name)
	delete(s.authorizations, name)
	delete(s.certificates, name)
	googletest.WriteBody(w, &certmanager.Operation{Name: "operations/drop", Done: true})
}

func (s *certServer) readAuthorization(w http.ResponseWriter, name string) {
	authorization, present := s.authorizations[name]
	if !present {
		googletest.WriteNotFound(w)
		return
	}
	googletest.WriteBody(w, authorization)
}

func (s *certServer) readCertificate(w http.ResponseWriter, name string) {
	certificate, present := s.certificates[name]
	if !present {
		googletest.WriteNotFound(w)
		return
	}
	if s.failure != "" {
		certificate.Managed.State = certificateFailed
		certificate.Managed.ProvisioningIssue = &certmanager.ProvisioningIssue{Details: s.failure}
		googletest.WriteBody(w, certificate)
		return
	}
	if s.provisioning > 0 {
		s.provisioning--
		provisioning := *certificate
		managed := *certificate.Managed
		managed.State = certificateProvisioning
		provisioning.Managed = &managed
		googletest.WriteBody(w, &provisioning)
		return
	}
	googletest.WriteBody(w, certificate)
}

func (s *certServer) refuseDeletingAuthorizations(times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusedAuthorizations = times
}

func (s *certServer) certified() map[string]*certmanager.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	certified := make(map[string]*certmanager.Certificate, len(s.certificates))
	for name, cert := range s.certificates {
		certified[name] = cert
	}
	return certified
}

func (s *certServer) authorized() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.authorizations))
	for name := range s.authorizations {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (s *certServer) dropped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.deleted)
}
