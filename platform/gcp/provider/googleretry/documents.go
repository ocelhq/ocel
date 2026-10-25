package googleretry

import (
	"context"

	"cloud.google.com/go/firestore"
)

type Documents struct {
	iterator *firestore.DocumentIterator
	first    *firestore.DocumentSnapshot
	err      error
	answered bool
}

func QueryDocuments(ctx context.Context, query firestore.Query) *Documents {
	documents := &Documents{}
	documents.first, documents.err = Called(ctx, func() (*firestore.DocumentSnapshot, error) {
		if documents.iterator != nil {
			documents.iterator.Stop()
		}
		documents.iterator = query.Documents(ctx)
		return documents.iterator.Next()
	})
	return documents
}

func (d *Documents) Next() (*firestore.DocumentSnapshot, error) {
	switch {
	case d.err != nil:
		return nil, d.err
	case d.answered:
		return d.iterator.Next()
	}
	d.answered = true
	return d.first, nil
}

func (d *Documents) Stop() { d.iterator.Stop() }
