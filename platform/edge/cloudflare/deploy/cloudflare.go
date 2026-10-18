package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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
	store     *http.Client
	namespace string
	options   Options

	workerClientCertificate bool
	trustClientCertificate  ClientCertificateTrust
	skipChecks              bool

	zoneMu    sync.Mutex
	zonesSeen map[string]zoneRef
}

func New(namespace string, options Options) edge.Edge { return newCloudflare(namespace, options) }

type ClientCertificateTrust func(ctx context.Context, tier environment.Tier, offer edge.Offer) error

func NewWithWorkerClientCertificate(namespace string, options Options, trust ClientCertificateTrust) edge.Edge {
	front := newCloudflare(namespace, options)
	front.workerClientCertificate = true
	front.trustClientCertificate = trust
	return front
}

func newCloudflare(namespace string, options Options) *cloudflare {
	return &cloudflare{client: newAPIClient(), namespace: namespace, options: options, skipChecks: processenv.SkipChecks()}
}

func (p *cloudflare) Kind() edge.Kind { return Kind }

func readAccountID() string { return os.Getenv(envAccountID) }

func requireAccountID(doing string) (string, error) {
	accountID := readAccountID()
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to %s", envAccountID, doing)
	}
	return accountID, nil
}

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
			Ensure:  p.ensureClientCertificates,
			Present: p.presentClientCertificates,
		},
	}
}

const recordTTL = 5 * time.Second

func hasStatus(err error, status int) bool {
	var apiErr *cf.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}
