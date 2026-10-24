package gcloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/artifactregistry/v1"
	certmanager "google.golang.org/api/certificatemanager/v1"
	"google.golang.org/api/cloudresourcemanager/v1"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/compute/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iam/v1"
	loggingrest "google.golang.org/api/logging/v2"
	"google.golang.org/api/networkconnectivity/v1"
	"google.golang.org/api/option"
	runv1 "google.golang.org/api/run/v1"
	run "google.golang.org/api/run/v2"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/api/serviceusage/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Memo[T any] struct {
	once  sync.Once
	value T
	err   error
}

func (m *Memo[T]) Get(open func() (T, error)) (T, error) {
	m.once.Do(func() { m.value, m.err = open() })
	return m.value, m.err
}

type Clients struct {
	names.Names
	Region      string
	Endpoint    string
	TagEndpoint string
	Records     records.Store

	TierTagEndpoint string

	workload      Memo[*ports.Clients]
	storage       Memo[*storage.Client]
	databases     Memo[*firestoreadmin.Service]
	secrets       Memo[*secretmanager.Service]
	services      Memo[*serviceusage.Service]
	images        Memo[*artifactregistry.Service]
	accounts      Memo[*iam.Service]
	runs          Memo[*run.Service]
	runsV1        Memo[*runv1.APIService]
	runLocations  Memo[*runv1.APIService]
	schedules     Memo[*cloudscheduler.Service]
	loggingREST   Memo[*loggingrest.Service]
	compute       Memo[*compute.Service]
	certs         Memo[*certmanager.Service]
	connectivity  Memo[*networkconnectivity.Service]
	sql           Memo[*sqladmin.Service]
	principal     Memo[string]
	projects      Memo[*cloudresourcemanager.Service]
	globalTags    Memo[*crm.Service]
	regionalTags  Memo[*crm.Service]
	logs          Memo[*logging.Client]
	numberLock    sync.Mutex
	projectNumber int64
	tierBuckets   tierBucketsRead
	tierTags      tierTagsRead
}

func opened[T any](c *Clients, cache *Memo[T], doing string, open func() (T, error)) (T, error) {
	client, err := cache.Get(func() (T, error) {
		if !c.Emulated() {
			if _, err := google.FindDefaultCredentials(context.Background(), ports.CloudPlatformScope); err != nil {
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
	return nothing, fmt.Errorf("open the %s client for project %s: %w", doing, c.Project(), err)
}

func (c *Clients) Storage() (*storage.Client, error) {
	return opened(c, &c.storage, "Cloud Storage", func() (*storage.Client, error) {
		return storage.NewClient(context.Background(), ports.EmulatorStorage(c.Endpoint)...)
	})
}

func (c *Clients) Databases() (*firestoreadmin.Service, error) {
	return opened(c, &c.databases, "Firestore admin", func() (*firestoreadmin.Service, error) {
		return firestoreadmin.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Secrets() (*secretmanager.Service, error) {
	return opened(c, &c.secrets, "Secret Manager", func() (*secretmanager.Service, error) {
		return secretmanager.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Services() (*serviceusage.Service, error) {
	return opened(c, &c.services, "Service Usage", func() (*serviceusage.Service, error) {
		return serviceusage.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Repositories() (*artifactregistry.Service, error) {
	return opened(c, &c.images, "Artifact Registry", func() (*artifactregistry.Service, error) {
		return artifactregistry.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Run() (*run.Service, error) {
	return opened(c, &c.runs, "Cloud Run", func() (*run.Service, error) {
		return run.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) RunV1() (*runv1.APIService, error) {
	return opened(c, &c.runsV1, "Cloud Run v1", func() (*runv1.APIService, error) {
		options := ports.EmulatorREST(c.Endpoint)
		if len(options) == 0 {
			options = []option.ClientOption{option.WithEndpoint("https://" + c.Region + "-run.googleapis.com/")}
		}
		return runv1.NewService(context.Background(), options...)
	})
}

func (c *Clients) RunLocations() (*runv1.APIService, error) {
	return opened(c, &c.runLocations, "Cloud Run locations", func() (*runv1.APIService, error) {
		return runv1.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Scheduler() (*cloudscheduler.Service, error) {
	return opened(c, &c.schedules, "Cloud Scheduler", func() (*cloudscheduler.Service, error) {
		return cloudscheduler.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) LoggingREST() (*loggingrest.Service, error) {
	return opened(c, &c.loggingREST, "Cloud Logging", func() (*loggingrest.Service, error) {
		return loggingrest.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Compute() (*compute.Service, error) {
	return opened(c, &c.compute, "Compute Engine", func() (*compute.Service, error) {
		return compute.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Certificates() (*certmanager.Service, error) {
	return opened(c, &c.certs, "Certificate Manager", func() (*certmanager.Service, error) {
		return certmanager.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Connectivity() (*networkconnectivity.Service, error) {
	return opened(c, &c.connectivity, "Network Connectivity", func() (*networkconnectivity.Service, error) {
		return networkconnectivity.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) SQL() (*sqladmin.Service, error) {
	return opened(c, &c.sql, "Cloud SQL Admin", func() (*sqladmin.Service, error) {
		return sqladmin.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Accounts() (*iam.Service, error) {
	return opened(c, &c.accounts, "IAM", func() (*iam.Service, error) {
		return iam.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Principal(ctx context.Context) (string, error) {
	return c.principal.Get(func() (string, error) {
		if c.Emulated() {
			return EmulatorPrincipal, nil
		}
		token, err := ApplicationDefault{}.Token(ctx)
		if err != nil {
			return "", Unauthenticated()
		}
		return PrincipalNamed(ctx, TokenInfoURL, token)
	})
}

func (c *Clients) Projects() (*cloudresourcemanager.Service, error) {
	return opened(c, &c.projects, "Resource Manager", func() (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(context.Background(), ports.EmulatorREST(c.Endpoint)...)
	})
}

func (c *Clients) Logging() (*logging.Client, error) {
	return opened(c, &c.logs, "Cloud Logging", func() (*logging.Client, error) {
		return logging.NewClient(context.Background(), ports.EmulatorGRPC(c.Endpoint)...)
	})
}

func (c *Clients) ReadServiceAgent(ctx context.Context, domain string) (string, error) {
	number, err := c.ReadProjectNumber(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("serviceAccount:service-%d%s", number, domain), nil
}

func (c *Clients) ReadProjectNumber(ctx context.Context) (int64, error) {
	c.numberLock.Lock()
	defer c.numberLock.Unlock()
	if c.projectNumber == 0 {
		service, err := c.Projects()
		if err != nil {
			return 0, err
		}
		project, err := googleretry.Attempted(ctx, service.Projects.Get(c.Project()).Context(ctx).Do)
		if err != nil {
			return 0, fmt.Errorf("read project %s's number, which names its service agents and its resources in IAM conditions: %w", c.Project(), err)
		}
		c.projectNumber = project.ProjectNumber
	}
	return c.projectNumber, nil
}

func (c *Clients) Emulated() bool { return c.Endpoint != "" }

func (c *Clients) Location() string {
	return "projects/" + c.Project() + "/locations/" + c.Region
}

func (c *Clients) ServicePath(service string) string {
	return c.Location() + "/services/" + service
}

func (c *Clients) CertificatesGlobal() string {
	return "projects/" + c.Project() + "/locations/global"
}

func (c *Clients) BackendLink(backend string) string {
	if strings.Contains(backend, "/") {
		return backend
	}
	return "projects/" + c.Project() + "/global/backendServices/" + backend
}

func (c *Clients) BucketLink(backend string) string {
	return "projects/" + c.Project() + "/global/backendBuckets/" + backend
}

func (c *Clients) Workload() *ports.Clients {
	workload, _ := c.workload.Get(func() (*ports.Clients, error) {
		return &ports.Clients{
			Namespace:   c.Namespace(),
			Project:     c.Project(),
			Region:      c.Region,
			Endpoint:    c.Endpoint,
			TagEndpoint: c.TagEndpoint,
			Records:     c.Records,
		}, nil
	})
	return workload
}

func (c *Clients) TierFirestore(tier environment.Tier) (*firestore.Client, error) {
	return c.Workload().TierFirestore(tier)
}

func (c *Clients) KMS() (*kms.KeyManagementClient, error) { return c.Workload().KMS() }
