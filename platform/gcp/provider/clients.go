package gcp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/storage"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

	firestore memo[*firestore.Client]
	kms       memo[*kms.KeyManagementClient]
	storage   memo[*storage.Client]
	databases memo[*firestoreadmin.Service]
	secrets   memo[*secretmanager.Service]
	services  memo[*serviceusage.Service]
	projects  memo[*cloudresourcemanager.Service]
}

func (c *clients) emulated() bool { return c.endpoint != "" }

func opened[T any](c *clients, held *memo[T], doing string, open func() (T, error)) (T, error) {
	client, err := held.held(func() (T, error) {
		if !c.emulated() {
			if _, err := google.FindDefaultCredentials(context.Background(), cloudPlatformScope); err != nil {
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

func (c *clients) Firestore() (*firestore.Client, error) {
	return opened(c, &c.firestore, "Firestore", func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(
			context.Background(), c.project, c.Database(), EmulatorGRPC(c.endpoint)...)
	})
}

func (c *clients) KMS() (*kms.KeyManagementClient, error) {
	return opened(c, &c.kms, "Cloud KMS", func() (*kms.KeyManagementClient, error) {
		return kms.NewKeyManagementClient(context.Background(), EmulatorGRPC(c.endpoint)...)
	})
}

func (c *clients) Storage() (*storage.Client, error) {
	return opened(c, &c.storage, "Cloud Storage", func() (*storage.Client, error) {
		return storage.NewClient(context.Background(), EmulatorStorage(c.endpoint)...)
	})
}

func (c *clients) Databases() (*firestoreadmin.Service, error) {
	return opened(c, &c.databases, "Firestore admin", func() (*firestoreadmin.Service, error) {
		return firestoreadmin.NewService(context.Background(), EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Secrets() (*secretmanager.Service, error) {
	return opened(c, &c.secrets, "Secret Manager", func() (*secretmanager.Service, error) {
		return secretmanager.NewService(context.Background(), EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Services() (*serviceusage.Service, error) {
	return opened(c, &c.services, "Service Usage", func() (*serviceusage.Service, error) {
		return serviceusage.NewService(context.Background(), EmulatorREST(c.endpoint)...)
	})
}

func (c *clients) Projects() (*cloudresourcemanager.Service, error) {
	return opened(c, &c.projects, "Resource Manager", func() (*cloudresourcemanager.Service, error) {
		return cloudresourcemanager.NewService(context.Background(), EmulatorREST(c.endpoint)...)
	})
}
