package aws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

type partRecordTable struct {
	mu     sync.Mutex
	record string
	reads  int
	other  []string
}

func (s *partRecordTable) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		target := r.Header.Get("X-Amz-Target")
		if !strings.HasSuffix(target, ".GetItem") {
			s.other = append(s.other, target)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.reads++
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		item := map[string]any{"Item": map[string]any{
			"value": map[string]string{"S": s.record},
			"rev":   map[string]string{"S": "r1"},
		}}
		if err := json.NewEncoder(w).Encode(item); err != nil {
			t.Errorf("answer GetItem: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestADeployReadsACurrentPartRecordOnceHoweverManyStepsNeedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	production := environment.TierProduction
	table := &partRecordTable{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(table.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)
	p.writtenBy = "1.0.0"
	if _, err := p.deployed.resolve(production, func() (bootstrap.Reading, error) {
		return bootstrap.Reading{Deployed: bootstrap.Deployed{Present: true, StateBucket: "state", ArtifactBucket: "artifacts", StateTable: "state-table", VariablesTable: "variables", AssetBucket: "assets", AppBoundaryARN: "arn:aws:iam::111122223333:policy/boundary"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	spec, err := p.imageOptimizerSpec(ctx, production)
	if err != nil {
		t.Fatalf("imageOptimizerSpec() = %v", err)
	}
	digest := imageoptimizer.Digest(spec)
	record, err := json.Marshal(map[string]any{
		"state":      "ready",
		"written_by": "1.0.0",
		"digest":     digest,
		"added_at":   "2026-10-16T00:00:00Z",
		"resources":  []provider.PartResource{{Kind: "AWS::Lambda::Url", ID: "https://optimizer.lambda-url.eu-west-2.on.aws/"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	table.record = string(record)
	part := tier.Recorded(p.partRecords(), tier.RecordedSpec{
		Name:    imageoptimizer.Part,
		Summary: imageoptimizer.Summary,
		Work: func(context.Context, environment.Tier) (tier.Work, error) {
			return tier.Work{Digest: digest, Make: func(context.Context, []provider.PartResource, bool) ([]provider.PartResource, error) {
				t.Error("the part was made again, though its record is current for this build")
				return nil, nil
			}}, nil
		},
	})

	if _, err := part.Added.Ensure(ctx, production, "web", "", nil); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	for range 2 {
		url, err := imageoptimizer.ReadURL(ctx, p.partRecords(), production)
		if err != nil {
			t.Fatalf("ReadURL() = %v", err)
		}
		if url != "https://optimizer.lambda-url.eu-west-2.on.aws/" {
			t.Errorf("ReadURL() = %q, want the URL the part recorded", url)
		}
	}

	table.mu.Lock()
	defer table.mu.Unlock()
	if table.reads != 1 || len(table.other) != 0 {
		t.Errorf("the state table was read %d times and also asked %v, want one read: the preflight, the stack config and the edge program all need the image optimizer, and its record is current", table.reads, table.other)
	}
}
