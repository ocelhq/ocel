package ports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	ProjectEnvVar = "OCEL_GCP_PROJECT"
	RegionEnvVar  = "OCEL_GCP_REGION"

	CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

	credentialHint = "authenticate with Google Cloud: run `gcloud auth application-default login`"
)

func Database(namespace providerkit.Namespace) string { return string(namespace) }

func KeyRing(namespace providerkit.Namespace) string { return string(namespace) }

type memo[T any] struct {
	once  sync.Once
	value T
	err   error
}

func (m *memo[T]) held(open func() (T, error)) (T, error) {
	m.once.Do(func() { m.value, m.err = open() })
	return m.value, m.err
}

type Clients struct {
	Namespace providerkit.Namespace
	Project   string
	Region    string
	Endpoint  string

	firestore memo[*firestore.Client]
	kms       memo[*kms.KeyManagementClient]
}

func (c *Clients) Emulated() bool { return c.Endpoint != "" }

func (c *Clients) Database() string { return Database(c.Namespace) }

func (c *Clients) KeyRing() string { return KeyRing(c.Namespace) }

func (c *Clients) KeyRingPath() string {
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s", c.Project, c.Region, c.KeyRing())
}

func (c *Clients) KeyPath(name string) string {
	return c.KeyRingPath() + "/cryptoKeys/" + name
}

func Opened[T any](c *Clients, held *memo[T], doing string, open func() (T, error)) (T, error) {
	client, err := held.held(func() (T, error) {
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
	var refusal providerkit.Refusal
	if errors.As(err, &refusal) {
		return nothing, err
	}
	return nothing, fmt.Errorf("open the %s client for project %s: %w", doing, c.Project, err)
}

func Unauthenticated() error {
	return providerkit.Refuse(providerkit.CodeDenied, "%s", credentialHint)
}

func (c *Clients) Firestore() (*firestore.Client, error) {
	return Opened(c, &c.firestore, "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(
			context.Background(), c.Project, c.Database(), EmulatorGRPC(c.Endpoint)...)
	})
}

func (c *Clients) KMS() (*kms.KeyManagementClient, error) {
	return Opened(c, &c.kms, "Cloud KMS", func() (*kms.KeyManagementClient, error) {
		return kms.NewKeyManagementClient(context.Background(), EmulatorGRPC(c.Endpoint)...)
	})
}

func EmulatorREST(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
}

func EmulatorGRPC(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{
		option.WithEndpoint(HostPort(endpoint)),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

func EmulatorStorage(endpoint string) []option.ClientOption {
	if endpoint == "" {
		return nil
	}
	return []option.ClientOption{
		option.WithEndpoint(endpoint + "/storage/v1/"),
		option.WithoutAuthentication(),
	}
}

func HostPort(endpoint string) string {
	authority := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	authority, _, _ = strings.Cut(authority, "/")
	return authority
}

func Classless(what any) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s names no class, and this project keeps each class's state apart from the other class's", what)
}
