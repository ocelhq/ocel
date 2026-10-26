package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
	htransport "google.golang.org/api/transport/http"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	ProjectEnvVar = "OCEL_GCP_PROJECT"
	RegionEnvVar  = "OCEL_GCP_REGION"
	TierEnvVar    = "OCEL_INFRA_TIER"

	ConnectorAccountEnvVar = "OCEL_GCP_CONNECTOR_ACCOUNT"

	CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

	credentialHint = "authenticate with Google Cloud: run `gcloud auth application-default login`"
)

const recordsWord = "records"

func RecordsDatabase(namespace provider.Namespace, tier environment.Tier) string {
	return naming.OcelName(string(namespace), tier, recordsWord)
}

func KeyRing(namespace provider.Namespace) string { return string(namespace) }

type memo[T any] struct {
	once  sync.Once
	value T
	err   error
}

func (m *memo[T]) get(open func() (T, error)) (T, error) {
	m.once.Do(func() { m.value, m.err = open() })
	return m.value, m.err
}

type Clients struct {
	Namespace   provider.Namespace
	Project     string
	Region      string
	Endpoint    string
	TagEndpoint string

	ProjectDatabases map[string]string
	Records          records.Store

	tierStores    sync.Map
	projectStores sync.Map
	tagStores     sync.Map
	ownDatabases  sync.Map
	sealingKeys   sealingKeys
	openedKeys    sync.Map
	kms           memo[*kms.KeyManagementClient]
	pubsub        memo[*pubsub.Service]
	tasks         memo[*cloudtasks.Client]
	scheduler     memo[*cloudscheduler.Service]
	quotaProject  memo[string]
}

func (c *Clients) Emulated() bool { return c.Endpoint != "" }

func (c *Clients) RecordsDatabase(tier environment.Tier) string {
	return RecordsDatabase(c.Namespace, tier)
}

func (c *Clients) KeyRing() string { return KeyRing(c.Namespace) }

func (c *Clients) KeyRingPath() string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", c.Project, c.Region, c.KeyRing())
}

func (c *Clients) KeyPath(name string) string {
	return c.KeyRingPath() + "/cryptoKeys/" + name
}

func Opened[T any](c *Clients, cache *memo[T], doing string, open func() (T, error)) (T, error) {
	client, err := cache.get(func() (T, error) {
		if !c.Emulated() {
			if _, err := google.FindDefaultCredentials(context.Background(), CloudPlatformScope); err != nil {
				var nothing T
				return nothing, Unauthenticated()
			}
		}
		return open()
	})
	if err == nil {
		return client, nil
	}
	var nothing T
	var refused refusal.Refusal
	if errors.As(err, &refused) {
		return nothing, err
	}
	return nothing, fmt.Errorf("open the %s client for project %s: %w", doing, c.Project, err)
}

func Unauthenticated() error {
	return refusal.Refuse(refusal.CodeDenied, "%s", credentialHint)
}

func (c *Clients) TierFirestore(tier environment.Tier) (*firestore.Client, error) {
	database := c.RecordsDatabase(tier)
	held, _ := c.tierStores.LoadOrStore(database, &memo[*firestore.Client]{})
	return Opened(c, held.(*memo[*firestore.Client]), "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(context.Background(), c.Project, database, NewGRPCOptions(c.Endpoint, c.QuotaProject())...)
	})
}

func (c *Clients) tagEndpoint() string {
	if c.TagEndpoint != "" {
		return c.TagEndpoint
	}
	return c.Endpoint
}

func (c *Clients) KMS() (*kms.KeyManagementClient, error) {
	return Opened(c, &c.kms, "Cloud KMS", func() (*kms.KeyManagementClient, error) {
		return kms.NewKeyManagementClient(context.Background(), NewGRPCOptions(c.Endpoint, c.QuotaProject())...)
	})
}

func (c *Clients) PubSub() (*pubsub.Service, error) {
	return Opened(c, &c.pubsub, "Pub/Sub", func() (*pubsub.Service, error) {
		return pubsub.NewService(context.Background(), NewRESTOptions(c.Endpoint, c.QuotaProject())...)
	})
}

func (c *Clients) CloudTasks() (*cloudtasks.Client, error) {
	return Opened(c, &c.tasks, "Cloud Tasks", func() (*cloudtasks.Client, error) {
		return cloudtasks.NewClient(context.Background(), NewGRPCOptions(c.Endpoint, c.QuotaProject())...)
	})
}

func (c *Clients) Scheduler() (*cloudscheduler.Service, error) {
	return Opened(c, &c.scheduler, "Cloud Scheduler", func() (*cloudscheduler.Service, error) {
		return cloudscheduler.NewService(context.Background(), NewRESTOptions(c.Endpoint, c.QuotaProject())...)
	})
}

func NewHTTPClient(ctx context.Context, quotaProject string) (*http.Client, error) {
	client, _, err := htransport.NewClient(ctx, append(newQuotaProjectOptions(quotaProject), option.WithScopes(CloudPlatformScope))...)
	return client, err
}

func NewRESTOptions(endpoint, quotaProject string) []option.ClientOption {
	if endpoint == "" {
		return newQuotaProjectOptions(quotaProject)
	}
	return []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
}

func NewGRPCOptions(endpoint, quotaProject string) []option.ClientOption {
	if endpoint == "" {
		return newQuotaProjectOptions(quotaProject)
	}
	return []option.ClientOption{
		option.WithEndpoint(HostPort(endpoint)),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

type emulatorOwner struct{}

func (emulatorOwner) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer owner"}, nil
}

func (emulatorOwner) RequireTransportSecurity() bool { return false }

func EmulatorAdmin(endpoint string) []option.ClientOption {
	if endpoint == "" || !IsLoopback(endpoint) {
		return nil
	}
	return []option.ClientOption{option.WithGRPCDialOption(grpc.WithPerRPCCredentials(emulatorOwner{}))}
}

func IsLoopback(endpoint string) bool {
	host := HostPort(endpoint)
	if named, _, err := net.SplitHostPort(host); err == nil {
		host = named
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func NewStorageOptions(ctx context.Context, endpoint, quotaProject string) ([]option.ClientOption, error) {
	if endpoint != "" {
		return []option.ClientOption{
			option.WithEndpoint(endpoint + "/storage/v1/"),
			option.WithoutAuthentication(),
		}, nil
	}
	if quotaProject == "" {
		return nil, nil
	}
	client, err := NewHTTPClient(ctx, quotaProject)
	if err != nil {
		return nil, err
	}
	return []option.ClientOption{option.WithHTTPClient(client)}, nil
}

func newQuotaProjectOptions(quotaProject string) []option.ClientOption {
	if quotaProject == "" {
		return nil
	}
	return []option.ClientOption{option.WithQuotaProject(quotaProject)}
}

func HostPort(endpoint string) string {
	authority := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	authority, _, _ = strings.Cut(authority, "/")
	return authority
}

func Tierless(what any) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s names no tier, and this project keeps each tier's state apart from the other tier's", what)
}
