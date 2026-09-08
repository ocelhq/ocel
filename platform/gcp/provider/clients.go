package gcp

import (
	"context"
	"fmt"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/storage"
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
	project  string
	region   string
	endpoint string

	firestore memo[*firestore.Client]
	kms       memo[*kms.KeyManagementClient]
	storage   memo[*storage.Client]
}

func (c *clients) Firestore() (*firestore.Client, error) {
	client, err := c.firestore.held(func() (*firestore.Client, error) {
		return firestore.NewClientWithDatabase(
			context.Background(), c.project, recordDatabase, EmulatorGRPC(c.endpoint)...)
	})
	if err != nil {
		return nil, fmt.Errorf("open the %q Firestore database in project %s: %w", recordDatabase, c.project, err)
	}
	return client, nil
}

func (c *clients) KMS() (*kms.KeyManagementClient, error) {
	client, err := c.kms.held(func() (*kms.KeyManagementClient, error) {
		return kms.NewKeyManagementClient(context.Background(), EmulatorGRPC(c.endpoint)...)
	})
	if err != nil {
		return nil, fmt.Errorf("open the Cloud KMS client for project %s: %w", c.project, err)
	}
	return client, nil
}

func (c *clients) Storage() (*storage.Client, error) {
	client, err := c.storage.held(func() (*storage.Client, error) {
		return storage.NewClient(context.Background(), EmulatorStorage(c.endpoint)...)
	})
	if err != nil {
		return nil, fmt.Errorf("open the Cloud Storage client for project %s: %w", c.project, err)
	}
	return client, nil
}
