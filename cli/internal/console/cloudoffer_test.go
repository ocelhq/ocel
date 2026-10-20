package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type consoleService struct {
	consolev1connect.UnimplementedConsoleServiceHandler
	describe   func() (*consolev1.DescribeResponse, error)
	issue      func(*consolev1.IssueCloudTokenRequest) (*consolev1.IssueCloudTokenResponse, error)
	described  atomic.Int64
	issued     atomic.Int64
	authorized atomic.Value
}

func (s *consoleService) Describe(context.Context, *consolev1.DescribeRequest) (*consolev1.DescribeResponse, error) {
	s.described.Add(1)
	return s.describe()
}

func (s *consoleService) IssueCloudToken(_ context.Context, req *consolev1.IssueCloudTokenRequest) (*consolev1.IssueCloudTokenResponse, error) {
	s.issued.Add(1)
	return s.issue(req)
}

func serveConsole(t *testing.T, service *consoleService) string {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewConsoleServiceHandler(service)
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.authorized.Store(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func offering(apiURL string) func() (*consolev1.DescribeResponse, error) {
	return func() (*consolev1.DescribeResponse, error) {
		return &consolev1.DescribeResponse{
			ConsoleVersion: "1.0.0",
			Capabilities:   []string{"cloud"},
			Cloud:          &consolev1.CloudOffer{ApiUrl: apiURL, ProviderUrl: apiURL + "/providers", Audience: "ocel-cloud"},
		}, nil
	}
}

func TestReadCloudOfferAnswersTheOfferAndNoOfferFromAConsoleThatMakesNone(t *testing.T) {
	t.Parallel()

	t.Run("a console that offers Ocel Cloud answers where it is, asked without a session", func(t *testing.T) {
		t.Parallel()
		service := &consoleService{describe: offering("https://cloud.example.test")}

		offer, err := New(serveConsole(t, service)).ReadCloudOffer(context.Background())
		if err != nil {
			t.Fatalf("ReadCloudOffer err = %v", err)
		}
		want := CloudOffer{APIURL: "https://cloud.example.test", ProviderURL: "https://cloud.example.test/providers", Audience: "ocel-cloud"}
		if offer == nil || *offer != want {
			t.Errorf("offer = %+v, want %+v", offer, want)
		}
		if bearer := service.authorized.Load(); bearer != "" {
			t.Errorf("Authorization = %q, want none", bearer)
		}
	})

	t.Run("a console that offers no Ocel Cloud answers no offer", func(t *testing.T) {
		t.Parallel()
		service := &consoleService{describe: func() (*consolev1.DescribeResponse, error) {
			return &consolev1.DescribeResponse{ConsoleVersion: "1.0.0"}, nil
		}}

		offer, err := New(serveConsole(t, service)).ReadCloudOffer(context.Background())
		if err != nil || offer != nil {
			t.Errorf("ReadCloudOffer = %+v, %v, want no offer", offer, err)
		}
	})

	t.Run("a console that does not serve Describe answers no offer", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)

		offer, err := New(srv.URL).ReadCloudOffer(context.Background())
		if err != nil || offer != nil {
			t.Errorf("ReadCloudOffer = %+v, %v, want no offer", offer, err)
		}
	})

	t.Run("a console that fails to describe itself is an error", func(t *testing.T) {
		t.Parallel()
		service := &consoleService{describe: func() (*consolev1.DescribeResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("down"))
		}}

		if _, err := New(serveConsole(t, service)).ReadCloudOffer(context.Background()); connect.CodeOf(err) != connect.CodeInternal {
			t.Errorf("ReadCloudOffer err = %v, want the console's failure", err)
		}
	})
}

type savedCredentials struct {
	stored Credentials
	saves  int
}

func (s *savedCredentials) load() (Credentials, error) { return s.stored, nil }

func (s *savedCredentials) save(creds Credentials) (CredentialStore, error) {
	s.saves++
	s.stored = creds
	return KeyringStore, nil
}

func TestResolveCloudOfferDescribesTheConsoleAtMostOncePerHourForAStoredLogin(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	t.Run("a cached offer younger than an hour is answered without asking the console", func(t *testing.T) {
		service := &consoleService{describe: offering("https://new.example.test")}
		url := serveConsole(t, service)
		t.Setenv(URLEnvVar, "")
		cached := &CloudOffer{APIURL: "https://cached.example.test", Audience: "ocel-cloud"}
		creds := Credentials{AccessToken: "tok", APIURL: url, Store: KeyringStore, Cloud: cached, CloudFetchedAt: now.Add(-59 * time.Minute)}
		saved := &savedCredentials{stored: creds}
		offers := NewCloudOffers(saved.load, saved.save)
		offers.now = func() time.Time { return now }

		offer, err := offers.Resolve(context.Background(), creds)
		if err != nil || offer != cached {
			t.Errorf("Resolve = %+v, %v, want the cached offer", offer, err)
		}
		if service.described.Load() != 0 || saved.saves != 0 {
			t.Errorf("described %d times and saved %d times, want neither", service.described.Load(), saved.saves)
		}
	})

	t.Run("an offer an hour old is asked for again and saved with the login", func(t *testing.T) {
		service := &consoleService{describe: offering("https://new.example.test")}
		url := serveConsole(t, service)
		t.Setenv(URLEnvVar, "")
		creds := Credentials{AccessToken: "tok", APIURL: url, Email: "ada@example.com", Store: KeyringStore, CloudFetchedAt: now.Add(-time.Hour)}
		saved := &savedCredentials{stored: creds}
		offers := NewCloudOffers(saved.load, saved.save)
		offers.now = func() time.Time { return now }

		offer, err := offers.Resolve(context.Background(), creds)
		if err != nil || offer == nil || offer.APIURL != "https://new.example.test" {
			t.Fatalf("Resolve = %+v, %v, want the console's offer", offer, err)
		}
		if saved.saves != 1 || saved.stored.Cloud == nil || *saved.stored.Cloud != *offer || !saved.stored.CloudFetchedAt.Equal(now) {
			t.Errorf("saved %d times as %+v, want the offer saved once, fetched now", saved.saves, saved.stored)
		}
		if saved.stored.AccessToken != "tok" || saved.stored.Email != "ada@example.com" {
			t.Errorf("saved %+v, want the rest of the login kept", saved.stored)
		}
	})

	t.Run("a login replaced while the console was asked is not overwritten", func(t *testing.T) {
		service := &consoleService{describe: offering("https://new.example.test")}
		url := serveConsole(t, service)
		t.Setenv(URLEnvVar, "")
		creds := Credentials{AccessToken: "old", APIURL: url, Store: KeyringStore}
		saved := &savedCredentials{stored: Credentials{AccessToken: "new", APIURL: url}}
		offers := NewCloudOffers(saved.load, saved.save)

		if _, err := offers.Resolve(context.Background(), creds); err != nil {
			t.Fatalf("Resolve err = %v", err)
		}
		if saved.saves != 0 {
			t.Errorf("saved %d times, want the newer login left alone", saved.saves)
		}
	})
}

func TestResolveCloudOfferDescribesOncePerRunAndSavesNothingForAnEnvironmentToken(t *testing.T) {
	service := &consoleService{describe: offering("https://cloud.example.test")}
	url := serveConsole(t, service)
	t.Setenv(URLEnvVar, url)
	saved := &savedCredentials{}
	offers := NewCloudOffers(saved.load, saved.save)
	creds := Credentials{AccessToken: "env", APIURL: url, Store: EnvironmentStore}

	for range 2 {
		offer, err := offers.Resolve(context.Background(), creds)
		if err != nil || offer == nil || offer.APIURL != "https://cloud.example.test" {
			t.Fatalf("Resolve = %+v, %v, want the console's offer", offer, err)
		}
	}
	if service.described.Load() != 1 {
		t.Errorf("described %d times, want once per run", service.described.Load())
	}
	if saved.saves != 0 {
		t.Errorf("saved %d times, want an environment token never saved", saved.saves)
	}
}
