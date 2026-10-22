package cloudflare

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const Kind edge.Kind = "cloudflare"

const (
	envAccountID = "CLOUDFLARE_ACCOUNT_ID"
	envAPIToken  = "CLOUDFLARE_API_TOKEN"
)

const compatDate = "2026-07-13"

var compatFlags = []string{"nodejs_compat"}

const envObservability = "OCEL_EDGE_OBSERVABILITY"

func observability() map[string]any {
	if strings.EqualFold(os.Getenv(envObservability), "off") {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":            true,
		"head_sampling_rate": 1,
		"logs":               map[string]any{"enabled": true, "invocation_logs": true},
		"traces":             map[string]any{"enabled": true},
	}
}

type cloudflare struct {
	client    *cf.Client
	store     releasesStore
	namespace string
	options   Options

	resolvedOrigin

	skipChecks bool

	zoneMu    sync.Mutex
	zonesSeen map[string]zoneRef
}

func New(namespace string, origin Origin, options Options) edge.Edge {
	return newCloudflare(namespace, origin, options)
}

func newCloudflare(namespace string, origin Origin, options Options) *cloudflare {
	return &cloudflare{client: newAPIClient(), namespace: namespace, resolvedOrigin: resolvedOrigin{Origin: origin}, options: options, skipChecks: processenv.SkipChecks()}
}

func (p *cloudflare) Kind() edge.Kind { return Kind }

func readAccountID() string { return os.Getenv(envAccountID) }

func (p *cloudflare) Facts() edge.Facts {
	return edge.Facts{
		Supported:             edge.AllNeeds(),
		RunsCode:              true,
		CachesResponses:       true,
		CacheControlHeader:    "cloudflare-cdn-cache-control",
		CacheTagLimits:        edge.CacheTagLimits{TagBytes: 1024, Tags: 1000, HeaderBytes: 16 * 1024},
		PurgeBudget:           workersCachePurgeBudget,
		InvalidatesByCacheTag: true,
		Gates:                 true,
		ServesUnbound:         true,
		ProxiesRecords:        true,
		ProxiedRecordNote:     "Turn the Cloudflare proxy (orange cloud) on for these records: each value is a placeholder Cloudflare answers behind.",
		ShieldsOrigin:         true,
		OriginFacingRanges:    listOriginFacingRanges(),
		TunnelsToOrigin:       p.options.Tunnel,
		CredentialScope:       readAccountID(),
	}
}

func (p *cloudflare) Hooks() edge.Hooks {
	return edge.Hooks{
		PlanBootstrap:                 p.planBootstrap,
		PlanRemoveBootstrap:           p.planRemoveBootstrap,
		PlanAdoption:                  p.adoption,
		DescribeBootstrap:             p.describeBootstrap,
		VerifyCredentials:             p.verifyCredentials,
		CheckCodeEntitlement:          p.codeEntitlement,
		DescribeCredentialPermissions: p.describeCredentialPermissions,
		PublishGate:                   p.publishGate,
		ClientCertificates: &edge.ClientCertificateHooks{
			RefuseUnready: p.refuseUnreadyClientCertificates,
			Ensure:        p.ensureClientCertificates,
			Present:       p.presentClientCertificates,
		},
	}
}

const recordTTL = 5 * time.Second

func hasStatus(err error, status int) bool {
	var apiErr *cf.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}
