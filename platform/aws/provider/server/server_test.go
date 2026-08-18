package server

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func wellFormedManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		SchemaVersion: "provider.v1",
		Slug:          "proj-123",
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "postgres_main",
				Resource: &resourcesv1.ResourceIdentifier{
					Type: linksv1.LinkType_LINK_TYPE_POSTGRES,
					Name: "main",
				},
				Config: &deploymentsv1.ManifestResource_Postgres{
					Postgres: &resourcesv1.PostgresConfig{Version: "17"},
				},
			},
		},
	}
}

func TestValidateManifest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*deploymentsv1.Manifest)
		wantErr bool
	}{
		{name: "a well-formed manifest"},
		{name: "a missing schema_version", mutate: func(m *deploymentsv1.Manifest) { m.SchemaVersion = "" }, wantErr: true},
		{name: "a missing slug", mutate: func(m *deploymentsv1.Manifest) { m.Slug = "" }, wantErr: true},
		{name: "a resource with no logical_name", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].LogicalName = "" }, wantErr: true},
		{
			name: "a resource of an unspecified type",
			mutate: func(m *deploymentsv1.Manifest) {
				m.Resources[0].Resource.Type = linksv1.LinkType_LINK_TYPE_UNSPECIFIED
			},
			wantErr: true,
		},
		{
			name: "a resource whose config contradicts its type",
			mutate: func(m *deploymentsv1.Manifest) {
				m.Resources[0].Resource.Type = linksv1.LinkType_LINK_TYPE_BUCKET
			},
			wantErr: true,
		},
		{name: "a resource with no identifier", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].Resource = nil }, wantErr: true},
		{name: "a resource with no typed config", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].Config = nil }, wantErr: true},
		{name: "a manifest declaring no resources", mutate: func(m *deploymentsv1.Manifest) { m.Resources = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := wellFormedManifest()
			if tc.mutate != nil {
				tc.mutate(m)
			}
			err := validateManifest(m)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateManifest() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}

	t.Run("a mismatch names the declared type", func(t *testing.T) {
		t.Parallel()
		m := wellFormedManifest()
		m.Resources[0].Resource.Type = linksv1.LinkType_LINK_TYPE_BUCKET
		err := validateManifest(m)
		if err == nil || !strings.Contains(err.Error(), "config does not match resource type LINK_TYPE_BUCKET") {
			t.Fatalf("validateManifest() error = %v, want the enum name in the refusal", err)
		}
	})

	t.Run("a nil manifest", func(t *testing.T) {
		t.Parallel()
		if err := validateManifest(nil); err == nil {
			t.Fatal("validateManifest(nil) error = nil, want error")
		}
	})
}

func TestResourceSummary(t *testing.T) {
	t.Parallel()

	m := wellFormedManifest()
	m.Resources[0].Config = &deploymentsv1.ManifestResource_Postgres{
		Postgres: &resourcesv1.PostgresConfig{Version: "15"},
	}

	got := resourceSummary(m.Resources[0])
	want := "postgres_main: postgres version=15"
	if got != want {
		t.Fatalf("resourceSummary() = %q, want %q", got, want)
	}
}

func TestStackIndexFor(t *testing.T) {
	t.Parallel()

	t.Run("an unindexed substrate is refused up front", func(t *testing.T) {
		t.Parallel()

		_, err := stackIndexFor(aws.Config{Region: "us-east-1"}, bootstrap.Deployed{Present: true}, "ocel bootstrap")
		if err == nil {
			t.Fatal("stackIndexFor err = nil, want a teardown refused before it destroys anything")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("err = %v, want it to name the command that fixes it", err)
		}
	})

	t.Run("an indexed substrate yields its table", func(t *testing.T) {
		t.Parallel()

		index, err := stackIndexFor(aws.Config{Region: "us-east-1"}, bootstrap.Deployed{Present: true, StateTable: "ocel-state"}, "ocel bootstrap")
		if err != nil {
			t.Fatalf("stackIndexFor: %v", err)
		}
		if index == nil {
			t.Fatal("stackIndexFor = nil with no error")
		}
	})
}

func TestCacheStoreUploader(t *testing.T) {
	t.Parallel()

	t.Run("a zero store is an untyped nil", func(t *testing.T) {
		t.Parallel()
		if up := cacheStoreUploader(bootstrap.CacheStore{}); up != nil {
			t.Errorf("cacheStoreUploader(zero) = %v, want nil", up)
		}
	})

	t.Run("an adopted store is addressable", func(t *testing.T) {
		t.Parallel()
		store := bootstrap.CacheStore{
			Bucket:          "isr",
			Endpoint:        "https://acct.r2.cloudflarestorage.com",
			Region:          "auto",
			AccessKeyID:     "AK",
			SecretAccessKey: "s3cret",
		}
		if up := cacheStoreUploader(store); up == nil {
			t.Error("cacheStoreUploader on an adopted store = nil, want a client")
		}
	})
}

func markedGlobalPreview(state edge.StackState, baseDomain string) edge.StackState {
	marked := maps.Clone(state)
	marked[edge.StackKeyGlobalPreview] = baseDomain
	return marked
}

func TestStackStateChanged(t *testing.T) {
	t.Parallel()

	reconciled := edge.StackState{
		edge.StackKeySlug:       "proj-123",
		edge.StackKeyEndpoint:   "https://store.workers.dev",
		edge.StackKeySecret:     "s3cret",
		edge.StackKeyOwnerToken: "owner",
	}

	tests := []struct {
		name       string
		prior      edge.StackState
		reconciled edge.StackState
		want       bool
	}{
		{
			name:       "a first reconcile has nothing stored yet",
			prior:      nil,
			reconciled: reconciled,
			want:       true,
		},
		{
			name:       "a redeploy that changed nothing writes nothing",
			prior:      maps.Clone(reconciled),
			reconciled: reconciled,
			want:       false,
		},
		{
			name:  "an adopted instance answering with a different secret is persisted",
			prior: maps.Clone(reconciled),
			reconciled: edge.StackState{
				edge.StackKeySlug:       "proj-123",
				edge.StackKeyEndpoint:   "https://store.workers.dev",
				edge.StackKeySecret:     "rotated",
				edge.StackKeyOwnerToken: "owner",
			},
			want: true,
		},
		{
			name:  "a renamed project names a different instance",
			prior: maps.Clone(reconciled),
			reconciled: edge.StackState{
				edge.StackKeySlug:       "proj-456",
				edge.StackKeyEndpoint:   "https://store.workers.dev",
				edge.StackKeySecret:     "s3cret",
				edge.StackKeyOwnerToken: "owner",
			},
			want: true,
		},
		{
			name:       "a deploy that failed before reconcile leaves the stored state alone",
			prior:      maps.Clone(reconciled),
			reconciled: nil,
			want:       false,
		},
		{
			name:       "a project moving onto the global preview domain persists the mark",
			prior:      maps.Clone(reconciled),
			reconciled: markedGlobalPreview(reconciled, "preview.acme.com"),
			want:       true,
		},
		{
			name:       "a project declaring its own preview domain persists the cleared mark",
			prior:      markedGlobalPreview(reconciled, "preview.acme.com"),
			reconciled: maps.Clone(reconciled),
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stackStateChanged(tc.prior, tc.reconciled); got != tc.want {
				t.Errorf("stackStateChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

type presenceCFN struct {
	mu      sync.Mutex
	present map[string]bool
	asked   []string
	fails   int
}

func (p *presenceCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := aws.ToString(in.StackName)
	p.mu.Lock()
	p.asked = append(p.asked, name)
	failing := p.fails > 0
	if failing {
		p.fails--
	}
	p.mu.Unlock()
	if failing {
		return nil, errors.New("throttled")
	}

	if !p.present[name] {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	class := bootstrap.ClassProduction
	if name == bootstrap.PreviewStackName {
		class = bootstrap.ClassPreview
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		Outputs: []cfntypes.Output{{OutputKey: aws.String("InfrastructureClass"), OutputValue: aws.String(class)}},
	}}}, nil
}

func (p *presenceCFN) questions() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.asked)
}

type countingSTS struct {
	mu    sync.Mutex
	calls int
	err   error
	fails int
}

func (c *countingSTS) GetCallerIdentity(_ context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	c.mu.Lock()
	c.calls++
	failing := c.fails > 0
	if failing {
		c.fails--
	}
	c.mu.Unlock()
	if failing {
		return nil, errors.New("throttled")
	}
	if c.err != nil {
		return nil, c.err
	}
	return &sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
		Arn:     aws.String("arn:aws:iam::123456789012:user/deployer"),
	}, nil
}

func (c *countingSTS) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestServerDeployedMemo(t *testing.T) {
	t.Parallel()

	t.Run("one describe serves every caller in the process", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		cfn := &presenceCFN{present: map[string]bool{bootstrap.StackName: true}}

		for range 3 {
			got, err := s.deployed(context.Background(), cfn, "eu-west-1", false)
			if err != nil {
				t.Fatalf("deployed: %v", err)
			}
			if !got.Present {
				t.Fatal("deployed reported absent, want the described stack")
			}
		}
		if asked := cfn.questions(); len(asked) != 1 {
			t.Errorf("describes = %v, want one", asked)
		}
	})

	t.Run("class and region each get their own answer", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		cfn := &presenceCFN{present: map[string]bool{bootstrap.StackName: true}}

		for _, c := range []struct {
			region  string
			preview bool
		}{{"eu-west-1", false}, {"eu-west-1", true}, {"us-east-1", false}} {
			if _, err := s.deployed(context.Background(), cfn, c.region, c.preview); err != nil {
				t.Fatalf("deployed(%s, preview=%t): %v", c.region, c.preview, err)
			}
		}
		want := []string{bootstrap.StackName, bootstrap.PreviewStackName, bootstrap.StackName}
		if asked := cfn.questions(); !slices.Equal(asked, want) {
			t.Errorf("describes = %v, want %v", asked, want)
		}
	})

	t.Run("a transient failure is retried, not remembered", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		cfn := &presenceCFN{present: map[string]bool{bootstrap.StackName: true}, fails: 1}

		if _, err := s.deployed(context.Background(), cfn, "eu-west-1", false); err == nil {
			t.Fatal("deployed = nil error, want the transient failure")
		}
		got, err := s.deployed(context.Background(), cfn, "eu-west-1", false)
		if err != nil {
			t.Fatalf("deployed after the failure: %v", err)
		}
		if !got.Present {
			t.Error("deployed reported absent, want the retry's answer")
		}
		if _, err := s.deployed(context.Background(), cfn, "eu-west-1", false); err != nil {
			t.Fatalf("deployed: %v", err)
		}
		if asked := cfn.questions(); len(asked) != 2 {
			t.Errorf("describes = %v, want the failure retried and the success kept", asked)
		}
	})
}

func TestServerCallerIdentity(t *testing.T) {
	t.Parallel()

	t.Run("one call serves every caller in the process", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		api := &countingSTS{}

		id, err := s.callerIdentity(context.Background(), api, "eu-west-1")
		if err != nil {
			t.Fatalf("callerIdentity: %v", err)
		}
		if id.account != "123456789012" || id.arn != "arn:aws:iam::123456789012:user/deployer" {
			t.Errorf("identity = %+v, want the caller reported whole", id)
		}
		account, err := s.accountID(context.Background(), api, "eu-west-1")
		if err != nil {
			t.Fatalf("accountID: %v", err)
		}
		if account != "123456789012" {
			t.Errorf("accountID = %q, want 123456789012", account)
		}
		if api.count() != 1 {
			t.Errorf("GetCallerIdentity calls = %d, want 1", api.count())
		}
	})

	t.Run("each region asks once", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		api := &countingSTS{}

		for _, region := range []string{"eu-west-1", "us-east-1", "eu-west-1"} {
			if _, err := s.callerIdentity(context.Background(), api, region); err != nil {
				t.Fatalf("callerIdentity(%s): %v", region, err)
			}
		}
		if api.count() != 2 {
			t.Errorf("GetCallerIdentity calls = %d, want 2", api.count())
		}
	})

	t.Run("a failure reaches accountID named", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		api := &countingSTS{err: errors.New("expired token")}

		_, err := s.accountID(context.Background(), api, "eu-west-1")
		if err == nil {
			t.Fatal("accountID = nil error, want the failure")
		}
		if !strings.Contains(err.Error(), "resolve AWS account id") || !strings.Contains(err.Error(), "expired token") {
			t.Errorf("error = %v, want it to name the resolution and the cause", err)
		}
		if _, err := s.callerIdentity(context.Background(), api, "eu-west-1"); err == nil || strings.Contains(err.Error(), "resolve AWS account id") {
			t.Errorf("callerIdentity error = %v, want the bare cause", err)
		}
		if api.count() != 2 {
			t.Errorf("GetCallerIdentity calls = %d, want the failure retried", api.count())
		}
	})

	t.Run("a transient failure is retried, not remembered", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		api := &countingSTS{fails: 1}

		if _, err := s.callerIdentity(context.Background(), api, "eu-west-1"); err == nil {
			t.Fatal("callerIdentity = nil error, want the transient failure")
		}
		id, err := s.callerIdentity(context.Background(), api, "eu-west-1")
		if err != nil {
			t.Fatalf("callerIdentity after the failure: %v", err)
		}
		if id.account != "123456789012" {
			t.Errorf("identity = %+v, want the retry's answer", id)
		}
		if _, err := s.callerIdentity(context.Background(), api, "eu-west-1"); err != nil {
			t.Fatalf("callerIdentity: %v", err)
		}
		if api.count() != 2 {
			t.Errorf("GetCallerIdentity calls = %d, want the failure retried and the success kept", api.count())
		}
	})
}

func TestServerMemoUnderConcurrency(t *testing.T) {
	t.Parallel()
	s := &Server{}
	cfn := &presenceCFN{present: map[string]bool{bootstrap.StackName: true}}
	api := &countingSTS{}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.deployed(context.Background(), cfn, "eu-west-1", false); err != nil {
				t.Errorf("deployed: %v", err)
			}
			if _, err := s.callerIdentity(context.Background(), api, "eu-west-1"); err != nil {
				t.Errorf("callerIdentity: %v", err)
			}
		}()
	}
	wg.Wait()

	if asked := cfn.questions(); len(asked) != 1 {
		t.Errorf("describes = %v, want one flight", asked)
	}
	if api.count() != 1 {
		t.Errorf("GetCallerIdentity calls = %d, want one flight", api.count())
	}
}

func TestServerBootstrapForgetsDeployed(t *testing.T) {
	t.Parallel()

	t.Run("a successful bootstrap invalidates the memo", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		cfn := &presenceCFN{present: map[string]bool{}}

		before, err := s.deployed(context.Background(), cfn, "eu-west-1", false)
		if err != nil {
			t.Fatalf("deployed: %v", err)
		}
		if before.Present {
			t.Fatal("deployed reported present, want the pre-bootstrap answer")
		}

		cfn.present[bootstrap.StackName] = true
		run := func(context.Context, bootstrap.CFNAPI, bootstrap.SSMAPI, bootstrap.IAMAPI, edge.Edge, bootstrap.Artifacts, func(string), func(string)) error {
			return nil
		}
		if err := s.runBootstrap(context.Background(), run, nil, nil, nil, bootstrap.Artifacts{}, func(string) {}, func(string) {}); err != nil {
			t.Fatalf("runBootstrap: %v", err)
		}

		after, err := s.deployed(context.Background(), cfn, "eu-west-1", false)
		if err != nil {
			t.Fatalf("deployed after bootstrap: %v", err)
		}
		if !after.Present {
			t.Error("deployed reported absent after bootstrap, want the fresh answer")
		}
	})

	t.Run("a failed bootstrap keeps the memo", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		cfn := &presenceCFN{present: map[string]bool{}}

		if _, err := s.deployed(context.Background(), cfn, "eu-west-1", false); err != nil {
			t.Fatalf("deployed: %v", err)
		}
		run := func(context.Context, bootstrap.CFNAPI, bootstrap.SSMAPI, bootstrap.IAMAPI, edge.Edge, bootstrap.Artifacts, func(string), func(string)) error {
			return errors.New("stack rolled back")
		}
		if err := s.runBootstrap(context.Background(), run, nil, nil, nil, bootstrap.Artifacts{}, func(string) {}, func(string) {}); err == nil {
			t.Fatal("runBootstrap = nil error, want the failure")
		}
		if _, err := s.deployed(context.Background(), cfn, "eu-west-1", false); err != nil {
			t.Fatalf("deployed after the failed bootstrap: %v", err)
		}
		if asked := cfn.questions(); len(asked) != 1 {
			t.Errorf("describes = %v, want the memo kept", asked)
		}
	})
}

func TestServerEdge(t *testing.T) {
	t.Parallel()

	t.Run("it comes from the registry, once", func(t *testing.T) {
		t.Parallel()

		s := &Server{}
		first, err := s.edge()
		if err != nil {
			t.Fatalf("edge() error = %v", err)
		}
		second, err := s.edge()
		if err != nil {
			t.Fatalf("second edge() error = %v", err)
		}
		if first != second {
			t.Error("edge() handed out two edges, want one so its zone lookups are remembered")
		}
		if first.Kind() != edge.KindCloudflare {
			t.Errorf("Kind() = %q, want the kind this origin constructs today", first.Kind())
		}
	})
}
