package gcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/artifactregistry/v1"
	certmanager "google.golang.org/api/certificatemanager/v1"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/compute/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type memo[T any] struct {
	once  sync.Once
	value T
	err   error
}

func (m *memo[T]) held(open func() (T, error)) (T, error) {
	m.once.Do(func() { m.value, m.err = open() })
	return m.value, m.err
}

type clients struct {
	Names
	region   string
	endpoint string

	runtime   memo[*ports.Clients]
	storage   memo[*storage.Client]
	databases memo[*firestoreadmin.Service]
	secrets   memo[*secretmanager.Service]
	services  memo[*serviceusage.Service]
	images    memo[*artifactregistry.Service]
	accounts  memo[*iam.Service]
	runs      memo[*run.Service]
	compute   memo[*compute.Service]
	certs     memo[*certmanager.Service]
	principal memo[string]
	projects  memo[*cloudresourcemanager.Service]
}

func opened[T any](c *clients, held *memo[T], doing string, open func() (T, error)) (T, error) {
	client, err := held.held(func() (T, error) {
		if !c.emulated() {
			if _, err := google.FindDefaultCredentials(context.Background(), ports.CloudPlatformScope); err != nil {
				var nothing T
				return nothing, unauthenticated()
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
	return nothing, fmt.Errorf("open the %s client for project %s: %w", doing, c.project, err)
}

func (c *clients) Storage() (*storage.Client, error) {
	return opened(c, &c.storage, "Cloud Storage", func() (*storage.Client, error) {
		return storage.NewClient(context.Background(), ports.EmulatorStorage(c.endpoint)...)
	})
}

func (c *clients) Databases() (*firestoreadmin.Service, error) {
	return opened(c, &c.databases, "Firestore admin", func() (*firestoreadmin.Service, error) {
		return firestoreadmin.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Secrets() (*secretmanager.Service, error) {
	return opened(c, &c.secrets, "Secret Manager", func() (*secretmanager.Service, error) {
		return secretmanager.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Services() (*serviceusage.Service, error) {
	return opened(c, &c.services, "Service Usage", func() (*serviceusage.Service, error) {
		return serviceusage.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Repositories() (*artifactregistry.Service, error) {
	return opened(c, &c.images, "Artifact Registry", func() (*artifactregistry.Service, error) {
		return artifactregistry.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Run() (*run.Service, error) {
	return opened(c, &c.runs, "Cloud Run", func() (*run.Service, error) {
		return run.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Compute() (*compute.Service, error) {
	return opened(c, &c.compute, "Compute Engine", func() (*compute.Service, error) {
		return compute.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Certificates() (*certmanager.Service, error) {
	return opened(c, &c.certs, "Certificate Manager", func() (*certmanager.Service, error) {
		return certmanager.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Accounts() (*iam.Service, error) {
	return opened(c, &c.accounts, "IAM", func() (*iam.Service, error) {
		return iam.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Principal(ctx context.Context) (string, error) {
	return c.principal.held(func() (string, error) {
		if c.emulated() {
			return emulatorPrincipal, nil
		}
		token, err := ApplicationDefault{}.Token(ctx)
		if err != nil {
			return "", unauthenticated()
		}
		return principalNamed(ctx, tokenInfoURL, token)
	})
}

func (c *clients) Projects() (*cloudresourcemanager.Service, error) {
	return opened(c, &c.projects, "Resource Manager", func() (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(context.Background(), ports.EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) emulated() bool { return c.endpoint != "" }

func (c *clients) location() string {
	return "projects/" + c.project + "/locations/" + c.region
}

func (c *clients) servicePath(service string) string {
	return c.location() + "/services/" + service
}

func (c *clients) certificatesGlobal() string {
	return "projects/" + c.project + "/locations/global"
}

func (c *clients) backendLink(backend string) string {
	if strings.Contains(backend, "/") {
		return backend
	}
	return "projects/" + c.project + "/global/backendServices/" + backend
}

func (c *clients) Workload() *ports.Clients {
	held, _ := c.runtime.held(func() (*ports.Clients, error) {
		return &ports.Clients{
			Namespace: c.namespace,
			Project:   c.project,
			Region:    c.region,
			Endpoint:  c.endpoint,
		}, nil
	})
	return held
}

func (c *clients) Firestore() (*firestore.Client, error) { return c.Workload().Firestore() }

func (c *clients) KMS() (*kms.KeyManagementClient, error) { return c.Workload().KMS() }
