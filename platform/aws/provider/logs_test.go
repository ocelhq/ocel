package aws_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
)

type loggedAWS struct {
	mutex  sync.Mutex
	groups []string
	prefix []string
	limit  []int32
	filter []string
}

func (l *loggedAWS) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if name, ok := strings.CutPrefix(req.URL.Path, "/2015-03-31/functions/"); ok {
		name = strings.TrimSuffix(name, "/configuration")
		json.NewEncoder(writer).Encode(map[string]any{"FunctionName": name, "LoggingConfig": map[string]any{"LogGroup": "/aws/lambda/" + name, "LogFormat": "Text"}})
		return
	}
	var filter struct {
		LogGroupName        string `json:"logGroupName"`
		LogStreamNamePrefix string `json:"logStreamNamePrefix"`
		FilterPattern       string `json:"filterPattern"`
		Limit               int32  `json:"limit"`
	}
	body, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(body, &filter)
	l.groups = append(l.groups, filter.LogGroupName)
	l.prefix = append(l.prefix, filter.LogStreamNamePrefix)
	l.limit = append(l.limit, filter.Limit)
	l.filter = append(l.filter, filter.FilterPattern)
	json.NewEncoder(writer).Encode(map[string]any{"events": []map[string]any{{
		"logStreamName": "2026/01/05/[$LATEST]abcdef",
		"timestamp":     time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC).UnixMilli(),
		"message":       "from " + filter.LogGroupName + "\n",
	}}})
}

func providerOn(t *testing.T, server http.Handler) provider.Provider {
	t.Helper()
	stub := httptest.NewServer(server)
	t.Cleanup(stub.Close)
	cfg := awssdk.Config{
		Region:           "us-east-1",
		BaseEndpoint:     awssdk.String(stub.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
	return aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, cfg, defaultNamespace)
}

func TestLogsReadsEachFunctionFromItsLogGroupAndEachContainerFromTheTiersSharedGroup(t *testing.T) {
	t.Parallel()

	stub := &loggedAWS{}
	p := providerOn(t, stub)
	query := provider.LogQuery{
		Tier: environment.TierProduction,
		Targets: []provider.LogTarget{
			{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web-server", Physical: "web-fn"}},
			{App: "web", Release: "r1", Source: "media", Function: &provider.Function{Name: "worker:media", Physical: "media-fn"}},
			{App: "api", Release: "r2", Source: "http", Container: &provider.AppContainer{Name: "api", Physical: "api-box"}},
		},
		Since:    time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:    50,
		Contains: "boom",
	}

	var entries []provider.LogEntry
	err := p.Logs().Read(context.Background(), query, func(batch []provider.LogEntry) error {
		entries = append(entries, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	slices.Sort(stub.groups)
	if want := []string{"/aws/lambda/media-fn", "/aws/lambda/web-fn", "/ocel/containers/production"}; !slices.Equal(stub.groups, want) {
		t.Errorf("Read() read the log groups %v, want %v", stub.groups, want)
	}
	if !slices.Contains(stub.prefix, "api-box/") {
		t.Errorf("Read() filtered container streams by %v, want the prefix api-box/", stub.prefix)
	}
	for _, pattern := range stub.filter {
		if pattern != `"boom"` {
			t.Errorf("Read() sent the filter %q, want the contains text quoted", pattern)
		}
	}

	byGroup := map[string]provider.LogEntry{}
	for _, entry := range entries {
		byGroup[strings.TrimPrefix(entry.Message, "from ")] = entry
	}
	for group, want := range map[string]provider.LogEntry{
		"/aws/lambda/web-fn":          {App: "web", Release: "r1", Source: "http"},
		"/aws/lambda/media-fn":        {App: "web", Release: "r1", Source: "media"},
		"/ocel/containers/production": {App: "api", Release: "r2", Source: "http"},
	} {
		got := byGroup[group]
		if got.App != want.App || got.Release != want.Release || got.Source != want.Source {
			t.Errorf("Read() labelled the entry of %s with %s/%s/%s, want %s/%s/%s", group, got.App, got.Release, got.Source, want.App, want.Release, want.Source)
		}
	}
	if got := byGroup["/aws/lambda/web-fn"]; got.Instance != "[$LATEST]abcdef" || !got.Time.Equal(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Read() carried instance %q at %s, want the Lambda version and id at the event's time", got.Instance, got.Time)
	}
}

func TestLogsReadsNothingFromAnEmptyTargetList(t *testing.T) {
	t.Parallel()

	stub := &loggedAWS{}
	p := providerOn(t, stub)

	err := p.Logs().Read(context.Background(), provider.LogQuery{Tier: environment.TierProduction}, func([]provider.LogEntry) error {
		t.Error("Read() emitted a batch for no targets")
		return nil
	})
	if err != nil {
		t.Errorf("Read() error = %v, want none", err)
	}
	if len(stub.groups) != 0 {
		t.Errorf("Read() made requests for %v with no targets", stub.groups)
	}
}

func TestLogsReadsNothingForALimitBelowOne(t *testing.T) {
	t.Parallel()

	stub := &loggedAWS{}
	p := providerOn(t, stub)
	err := p.Logs().Read(context.Background(), provider.LogQuery{
		Tier:    environment.TierProduction,
		Targets: []provider.LogTarget{{App: "web", Release: "r1", Source: "http", Function: &provider.Function{Name: "web", Physical: "web-fn"}}},
		Since:   time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
	}, func([]provider.LogEntry) error {
		t.Error("Read() emitted a batch for a limit of 0")
		return nil
	})
	if err != nil {
		t.Errorf("Read() error = %v, want none", err)
	}
	if len(stub.groups) != 0 {
		t.Errorf("Read() made requests for %v with a limit of 0", stub.groups)
	}
}

func TestLogsRefusesATargetThatNamesNeitherAFunctionNorAContainer(t *testing.T) {
	t.Parallel()

	p := providerOn(t, &loggedAWS{})
	err := p.Logs().Read(context.Background(), provider.LogQuery{
		Tier:    environment.TierProduction,
		Targets: []provider.LogTarget{{App: "web", Release: "r1", Source: "http"}},
		Since:   time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
		Limit:   10,
	}, func([]provider.LogEntry) error { return nil })
	if err == nil {
		t.Error("Read() of a target with no function and no container error = nil, want a failure")
	}
}
