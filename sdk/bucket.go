package ocel

import (
	"fmt"
	"runtime"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

// A BucketOption tunes the bucket [Bucket] declares.
type BucketOption func(*resourcesv1.BucketConfig)

// BucketPublic serves every object in the bucket anonymously over HTTP.
func BucketPublic() BucketOption {
	return func(c *resourcesv1.BucketConfig) { c.Public = true }
}

// BucketAllowedOrigins names the browser origins allowed to upload straight to
// the store.
func BucketAllowedOrigins(origins ...string) BucketOption {
	return func(c *resourcesv1.BucketConfig) { c.AllowedOrigins = origins }
}

// A BucketStore is a bucket an app declares and reads its objects through.
type BucketStore struct {
	name string
}

// Bucket declares a bucket named name and returns the handle an app reads and
// writes its objects through. Call it from a file under the project's discovery
// folder: during discovery the call is the declaration, and at runtime it reads
// the binding the deploy delivered for that name.
func Bucket(name string, opts ...BucketOption) *BucketStore {
	if discovering() {
		config := &resourcesv1.BucketConfig{}
		for _, opt := range opts {
			opt(config)
		}
		_, file, line, _ := runtime.Caller(1)
		err := declare(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{
				Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
				Name: name,
			},
			Config: &resourcesv1.DeclareRequest_Bucket{Bucket: config},
			Source: fmt.Sprintf("%s:%d", file, line),
		})
		if err != nil {
			panic(fmt.Sprintf("ocel: declare bucket %q: %v", name, err))
		}
	}
	return &BucketStore{name: name}
}

// Name is the name the bucket was declared under.
func (b *BucketStore) Name() string { return b.name }
