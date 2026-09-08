package gcp

import (
	"context"
	"sync"

	"cloud.google.com/go/firestore"
	kms "cloud.google.com/go/kms/apiv1"
)

type clients struct {
	project  string
	region   string
	endpoint string

	firestoreOnce sync.Once
	firestore     *firestore.Client
	firestoreErr  error

	kmsOnce sync.Once
	kms     *kms.KeyManagementClient
	kmsErr  error
}

func (c *clients) Firestore() (*firestore.Client, error) {
	c.firestoreOnce.Do(func() {
		c.firestore, c.firestoreErr = firestore.NewClientWithDatabase(
			context.Background(), c.project, recordDatabase, EmulatorGRPC(c.endpoint)...)
	})
	if c.firestoreErr != nil {
		return nil, unauthenticated()
	}
	return c.firestore, nil
}

func (c *clients) KMS() (*kms.KeyManagementClient, error) {
	c.kmsOnce.Do(func() {
		c.kms, c.kmsErr = kms.NewKeyManagementClient(context.Background(), EmulatorGRPC(c.endpoint)...)
	})
	if c.kmsErr != nil {
		return nil, unauthenticated()
	}
	return c.kms, nil
}
