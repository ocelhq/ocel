package gcp

import (
	"context"
	"sync"

	"cloud.google.com/go/firestore"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type clients struct {
	project  string
	region   string
	endpoint string

	once      sync.Once
	firestore *firestore.Client
	err       error
}

func (c *clients) Firestore() (*firestore.Client, error) {
	c.once.Do(func() {
		c.firestore, c.err = firestore.NewClientWithDatabase(
			context.Background(), c.project, recordDatabase, grpcOptions(c.endpoint)...)
	})
	if c.err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied, "%s", credentialHint)
	}
	return c.firestore, nil
}
