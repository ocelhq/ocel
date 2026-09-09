package gcp

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	certmanager "google.golang.org/api/certificatemanager/v1"
)

type certServer struct {
	mu sync.Mutex

	authorizations map[string]*certmanager.DnsAuthorization
	certificates   map[string]*certmanager.Certificate

	provisioning int
	deleted      []string
	failure      string
}

func newCertServer() *certServer {
	return &certServer{
		authorizations: map[string]*certmanager.DnsAuthorization{},
		certificates:   map[string]*certmanager.Certificate{},
	}
}

func (s *certServer) open(t *testing.T) *Provider {
	t.Helper()
	server := httptest.NewServer(s.serve(t))
	t.Cleanup(server.Close)
	return pushing(t, server.URL)
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
			writeBody(w, &certmanager.Operation{Name: name, Done: true})
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
	asked := readBody[certmanager.DnsAuthorization](w, r)
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
	writeBody(w, &certmanager.Operation{Name: "operations/authorize", Done: true})
}

func (s *certServer) certify(w http.ResponseWriter, r *http.Request, parent string) {
	asked := readBody[certmanager.Certificate](w, r)
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
	writeBody(w, &certmanager.Operation{Name: "operations/certify", Done: true})
}

func (s *certServer) drop(w http.ResponseWriter, name string) {
	s.deleted = append(s.deleted, name)
	delete(s.authorizations, name)
	delete(s.certificates, name)
	writeBody(w, &certmanager.Operation{Name: "operations/drop", Done: true})
}

func (s *certServer) readAuthorization(w http.ResponseWriter, name string) {
	held, standing := s.authorizations[name]
	if !standing {
		missing(w)
		return
	}
	writeBody(w, held)
}

func (s *certServer) readCertificate(w http.ResponseWriter, name string) {
	held, standing := s.certificates[name]
	if !standing {
		missing(w)
		return
	}
	if s.failure != "" {
		held.Managed.State = certificateFailed
		held.Managed.ProvisioningIssue = &certmanager.ProvisioningIssue{Details: s.failure}
		writeBody(w, held)
		return
	}
	if s.provisioning > 0 {
		s.provisioning--
		provisioning := *held
		managed := *held.Managed
		managed.State = certificateProvisioning
		provisioning.Managed = &managed
		writeBody(w, &provisioning)
		return
	}
	writeBody(w, held)
}

func missing(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
}

func (s *certServer) certified() map[string]*certmanager.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := make(map[string]*certmanager.Certificate, len(s.certificates))
	for name, cert := range s.certificates {
		held[name] = cert
	}
	return held
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
