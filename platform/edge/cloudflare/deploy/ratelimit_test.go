package cloudflare

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/option"
)

const rateLimitedBody = `{"success":false,"errors":[{"code":10500,"message":"More than 1200 requests per 300 seconds reached. Please wait and consider throttling your request speed"}],"messages":[],"result":null}`

const accountBody = `{"success":true,"errors":[],"messages":[],"result":{"id":"acct-1","name":"Acme"}}`

type answer struct {
	status int
	header map[string]string
	body   string
}

type scriptedAPI struct {
	mu       sync.Mutex
	answers  []answer
	requests int
	bodies   []string
}

func (s *scriptedAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.bodies = append(s.bodies, string(body))
	next := s.answers[min(s.requests, len(s.answers)-1)]
	s.requests++
	for k, v := range next.header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(next.status)
	_, _ = w.Write([]byte(next.body))
}

func (s *scriptedAPI) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

type fakeClock struct {
	mu     sync.Mutex
	at     time.Time
	waited []time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) wait(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waited = append(c.waited, d)
	c.at = c.at.Add(d)
	return nil
}

func (c *fakeClock) total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sum time.Duration
	for _, d := range c.waited {
		sum += d
	}
	return sum
}

func scriptedClient(t *testing.T, answers ...answer) (*cf.Client, *scriptedAPI, *fakeClock) {
	t.Helper()
	api := &scriptedAPI{answers: answers}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	clock := &fakeClock{at: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	policy := apiRetryPolicy{budget: rateLimitBudget, wait: clock.wait, jitter: func() float64 { return 1 }, now: clock.now}
	return policy.newClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIToken("test")), api, clock
}

func readAccount(client *cf.Client) error {
	_, err := client.Accounts.Get(context.Background(), accounts.AccountGetParams{AccountID: cf.F("acct-1")})
	return err
}

func TestACloudflareCallThatIsRateLimitedWaitsWhatRetryAfterAsksAndThenSucceeds(t *testing.T) {
	t.Parallel()
	client, api, clock := scriptedClient(t,
		answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "90"}, body: rateLimitedBody},
		answer{status: http.StatusOK, body: accountBody},
	)

	if err := readAccount(client); err != nil {
		t.Fatalf("Accounts.Get() error = %v, want the rate limit waited out", err)
	}
	if api.count() != 2 {
		t.Errorf("Cloudflare saw %d requests, want 2: one refused, one after the wait", api.count())
	}
	if len(clock.waited) != 1 || clock.waited[0] < 90*time.Second || clock.waited[0] > 90*time.Second+rateLimitMaxJitter {
		t.Errorf("waited %v, want one wait of the 90s Retry-After asked for plus at most %v of jitter", clock.waited, rateLimitMaxJitter)
	}
}

func TestACloudflareCallThatIsRateLimitedWithoutRetryAfterWaitsForTheWindowItsRatelimitHeaderNames(t *testing.T) {
	t.Parallel()
	client, _, clock := scriptedClient(t,
		answer{status: http.StatusTooManyRequests, header: map[string]string{"Ratelimit": `"default";r=0;t=40`}, body: rateLimitedBody},
		answer{status: http.StatusOK, body: accountBody},
	)

	if err := readAccount(client); err != nil {
		t.Fatalf("Accounts.Get() error = %v, want the rate limit waited out", err)
	}
	if len(clock.waited) != 1 || clock.waited[0] < 40*time.Second {
		t.Errorf("waited %v, want one wait of at least the 40s until the window resets", clock.waited)
	}
}

func TestACloudflareCallStillRateLimitedAfterTheBudgetSaysTheAccountIsRateLimited(t *testing.T) {
	t.Parallel()
	client, api, clock := scriptedClient(t,
		answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "60"}, body: rateLimitedBody},
	)

	err := readAccount(client)
	var limited *rateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("Accounts.Get() error = %v, want a rateLimitedError", err)
	}
	if clock.total() > rateLimitBudget {
		t.Errorf("waited %v in all, want at most the %v budget", clock.total(), rateLimitBudget)
	}
	if api.count() > int(rateLimitBudget/(60*time.Second))+1 {
		t.Errorf("Cloudflare saw %d requests, want no more than one per Retry-After within the budget", api.count())
	}
	for _, want := range []string{"rate limit", "1200 requests per 300 seconds", "fewer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestACloudflareCallAskedToWaitPastTheBudgetGivesUpWithoutWaiting(t *testing.T) {
	t.Parallel()
	client, api, clock := scriptedClient(t,
		answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "3600"}, body: rateLimitedBody},
	)

	var limited *rateLimitedError
	if err := readAccount(client); !errors.As(err, &limited) {
		t.Fatalf("Accounts.Get() error = %v, want a rateLimitedError", err)
	}
	if len(clock.waited) != 0 || api.count() != 1 {
		t.Errorf("waited %v over %d requests, want no wait for a Retry-After past the budget", clock.waited, api.count())
	}
}

func TestACloudflareCallRetriesAServerErrorAFewTimesWithBackoff(t *testing.T) {
	t.Parallel()
	client, api, clock := scriptedClient(t,
		answer{status: http.StatusBadGateway, body: `{"success":false,"errors":[{"code":10000,"message":"bad gateway"}]}`},
		answer{status: http.StatusOK, body: accountBody},
	)

	if err := readAccount(client); err != nil {
		t.Fatalf("Accounts.Get() error = %v, want the server error retried", err)
	}
	if api.count() != 2 || len(clock.waited) != 1 {
		t.Errorf("Cloudflare saw %d requests after %v of waits, want one retry", api.count(), clock.waited)
	}
}

func TestACloudflareCallStopsRetryingAServerErrorThatPersists(t *testing.T) {
	t.Parallel()
	client, api, _ := scriptedClient(t,
		answer{status: http.StatusInternalServerError, body: `{"success":false,"errors":[{"code":10000,"message":"internal"}]}`},
	)

	err := readAccount(client)
	var apiErr *cf.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Accounts.Get() error = %v, want Cloudflare's 500", err)
	}
	if api.count() != apiMaxAttempts {
		t.Errorf("Cloudflare saw %d requests, want %d", api.count(), apiMaxAttempts)
	}
}

func TestACloudflareCallDoesNotRetryARefusal(t *testing.T) {
	t.Parallel()
	client, api, _ := scriptedClient(t,
		answer{status: http.StatusForbidden, body: `{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`},
	)

	if err := readAccount(client); err == nil {
		t.Fatal("Accounts.Get() error = nil, want the 403")
	}
	if api.count() != 1 {
		t.Errorf("Cloudflare saw %d requests, want 1: a refusal never succeeds on retry", api.count())
	}
}

func TestACloudflareCallThatIsRateLimitedSendsItsBodyAgainWhenItRetries(t *testing.T) {
	t.Parallel()
	client, api, _ := scriptedClient(t,
		answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "1"}, body: rateLimitedBody},
		answer{status: http.StatusOK, body: accountBody},
	)

	if err := client.Post(context.Background(), "accounts/acct-1/things", map[string]string{"name": "shop"}, nil); err != nil {
		t.Fatalf("Post() error = %v, want the rate limit waited out", err)
	}
	if len(api.bodies) != 2 || api.bodies[0] == "" || api.bodies[1] != api.bodies[0] {
		t.Errorf("Cloudflare received bodies %q, want the same body on the retry", api.bodies)
	}
}
