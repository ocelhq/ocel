package deploy

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAnAppBehindAnEdgeHoldingAKeyToTheAssetBucketTagsItsPagesEdgeReadableAndGetsAPurgeSecret(t *testing.T) {
	t.Parallel()
	cfg, spec := appStackSpec(t)
	cfg.Objects = &fakeArtifactStore{}
	cfg.EdgeAccessKeyID, cfg.EdgeSecretKey = "AKIAEDGE", "edge-secret"
	cfg.PurgeSeed = "a-seed"

	cache := releasing(t, cfg).isrCache(spec)

	if !cache.EdgeReadable {
		t.Error("EdgeReadable = false, want the origin to tag its page entries for an edge that reads them")
	}
	if want := edge.DerivePurgeSecret("a-seed", spec.App.ISR.Coordinate.PagesPrefix()); cache.PurgeSecret != want {
		t.Errorf("PurgeSecret = %q, want %q", cache.PurgeSecret, want)
	}
	env := cache.Env()
	if env["OCEL_ISR_EDGE_READABLE"] != "1" {
		t.Errorf("OCEL_ISR_EDGE_READABLE = %q, want 1", env["OCEL_ISR_EDGE_READABLE"])
	}
	for _, name := range []string{"OCEL_ISR_WRITER_URL", "OCEL_PURGE_SECRET", "OCEL_ISR_STORE_BUCKET"} {
		if got, told := env[name]; told {
			t.Errorf("%s = %q, want none: the origin keeps its pages in the asset bucket and holds no writer", name, got)
		}
	}
}

func TestAnAppBehindAnEdgeHoldingNoKeyToTheAssetBucketTagsNothing(t *testing.T) {
	t.Parallel()
	cfg, spec := appStackSpec(t)
	cfg.Objects = &fakeArtifactStore{}
	cfg.PurgeSeed = "a-seed"

	cache := releasing(t, cfg).isrCache(spec)

	if cache.EdgeReadable || cache.PurgeSecret != "" {
		t.Errorf("cache = %+v, want no tagging and no purge secret where no edge reads the bucket", cache)
	}
	if _, told := cache.Env()["OCEL_ISR_EDGE_READABLE"]; told {
		t.Error("OCEL_ISR_EDGE_READABLE is set, want it absent")
	}
}

func TestADeployBehindCloudflareNeedsTheEdgeReaderKeyAndThePurgeSeedBootstrapMints(t *testing.T) {
	t.Parallel()
	complete := Config{AssetBucket: "assets", Objects: &fakeArtifactStore{}, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "s", PurgeSeed: "seed", Tier: environment.TierProduction, Edge: fakeEdgeOf(cloudflare.Kind)}

	for name, tc := range map[string]struct {
		cfg   func(Config) Config
		front string
		want  string
	}{
		"a complete bootstrap": {cfg: func(c Config) Config { return c }, front: string(cloudflare.Kind)},
		"no edge reader key": {
			cfg:   func(c Config) Config { c.EdgeAccessKeyID, c.EdgeSecretKey = "", ""; return c },
			front: string(cloudflare.Kind),
			want:  "credential for the cloudflare edge to read the asset bucket",
		},
		"no asset bucket": {
			cfg:   func(c Config) Config { c.AssetBucket = ""; return c },
			front: string(cloudflare.Kind),
			want:  "credential for the cloudflare edge to read the asset bucket",
		},
		"no purge seed": {
			cfg:   func(c Config) Config { c.PurgeSeed = ""; return c },
			front: string(cloudflare.Kind),
			want:  "no purge seed",
		},
		"another front needs neither": {
			cfg: func(c Config) Config {
				c.EdgeAccessKeyID, c.EdgeSecretKey, c.PurgeSeed, c.Edge = "", "", "", fakeEdgeOf(cloudfront.Kind)
				return c
			},
			front: string(cloudfront.Kind),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := checkEdgeReaderBootstrapped(tc.cfg(complete), edge.Kind(tc.front))

			if tc.want == "" {
				if err != nil {
					t.Fatalf("checkEdgeReaderBootstrapped = %v, want none", err)
				}
				return
			}
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Fatalf("checkEdgeReaderBootstrapped = %v, want a not-ready refusal", err)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "ocel bootstrap production") {
				t.Errorf("refusal %q, want it to say %q and name the bootstrap to re-run", err, tc.want)
			}
		})
	}
}

func TestADeployBehindCloudflareReportsAnEdgeReaderKeyItCouldNotReadRatherThanAskingForABootstrap(t *testing.T) {
	t.Parallel()
	unreadable := errors.New("parse edge credentials: invalid character 'n'")
	cfg := Config{AssetBucket: "assets", Objects: &fakeArtifactStore{}, PurgeSeed: "seed", Tier: environment.TierProduction, EdgeCredentialsErr: unreadable, Edge: fakeEdgeOf(cloudflare.Kind)}

	err := checkEdgeReaderBootstrapped(cfg, cloudflare.Kind)

	if !errors.Is(err, unreadable) {
		t.Fatalf("checkEdgeReaderBootstrapped = %v, want the read failure surfaced", err)
	}
	if strings.Contains(err.Error(), "re-run") {
		t.Errorf("error %q asks for a bootstrap, want only the read failure", err)
	}
}
