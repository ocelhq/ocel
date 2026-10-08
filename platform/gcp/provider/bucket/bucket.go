package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
	storage "google.golang.org/api/storage/v1"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	googleStorage = "https://storage.googleapis.com"
	platformScope = "https://www.googleapis.com/auth/cloud-platform"
	tagLength     = 6
)

type Store struct {
	Endpoint string
	Client   *http.Client
	Account  func(ctx context.Context) (string, error)
	SignBlob func(ctx context.Context, account string, payload []byte) ([]byte, error)
}

func Open(ctx context.Context, endpoint string) (Store, error) {
	store := Store{Endpoint: endpoint, Client: http.DefaultClient}
	if endpoint == "" {
		client, err := google.DefaultClient(ctx, platformScope)
		if err != nil {
			return Store{}, fmt.Errorf("find the Google credentials Cloud Storage is reached with: %w", err)
		}
		store.Client = client
	}
	signing, err := iamcredentials.NewService(ctx, option.WithHTTPClient(store.Client))
	if err != nil {
		return Store{}, fmt.Errorf("open the IAM credentials client a bucket's URLs are signed through: %w", err)
	}
	var email struct {
		sync.Mutex
		account string
	}
	store.Account = func(ctx context.Context) (string, error) {
		email.Lock()
		defer email.Unlock()
		if email.account != "" {
			return email.account, nil
		}
		account, err := metadata.EmailWithContext(ctx, "default")
		if err != nil {
			return "", fmt.Errorf("read the service account a bucket URL is signed as from the metadata server, which only an app Google Cloud runs can reach, so a build cannot sign a bucket URL: %w", err)
		}
		email.account = account
		return account, nil
	}
	store.SignBlob = func(ctx context.Context, account string, payload []byte) ([]byte, error) {
		return retried(ctx, func() ([]byte, int, error) {
			signed, err := signing.Projects.ServiceAccounts.SignBlob("projects/-/serviceAccounts/"+account,
				&iamcredentials.SignBlobRequest{Payload: base64.StdEncoding.EncodeToString(payload)}).Context(ctx).Do()
			if err != nil {
				return nil, answeredStatus(err), err
			}
			blob, err := base64.StdEncoding.DecodeString(signed.SignedBlob)
			return blob, http.StatusOK, err
		})
	}
	return store, nil
}

func (s Store) base() string {
	if s.Endpoint == "" {
		return googleStorage
	}
	return s.Endpoint
}

func NewDispatch(ctx context.Context, store Store, records s3store.Records, callbacks s3store.Poster) (bucketv1connect.BucketServiceHandler, error) {
	buckets, err := storedBuckets(records)
	if err != nil {
		return nil, err
	}
	if len(buckets) == 0 {
		return s3store.NewDispatch(nil, records, callbacks), nil
	}
	options := []option.ClientOption{option.WithHTTPClient(store.Client)}
	if store.Endpoint != "" {
		options = append(options, option.WithEndpoint(store.Endpoint+"/storage/v1/"))
	}
	service, err := storage.NewService(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("open the Cloud Storage client this app's buckets are served through: %w", err)
	}
	public, err := url.Parse(store.base())
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Storage address %q: %w", store.base(), err)
	}
	signs := signer{store: store, host: public.Host, insecure: public.Scheme == "http"}
	stored := objects{service: service, multipart: multipart{endpoint: store.base(), client: store.Client}}
	services := make([]*s3store.Service, 0, len(buckets))
	for _, name := range buckets {
		services = append(services, s3store.New(s3store.Config{
			Tag:            tagOf(name),
			Objects:        stored,
			Internal:       signs,
			External:       func(context.Context) (s3store.PresignAPI, string) { return signs, store.base() },
			Callbacks:      callbacks,
			PostPolicies:   true,
			Sessions:       name,
			Granted:        []string{name},
			MetadataPrefix: metadataHeader,
		}))
	}
	return s3store.NewDispatch(s3store.NewGrantedDispatch(services...), records, callbacks), nil
}

func storedBuckets(records s3store.Records) ([]string, error) {
	var buckets []string
	for _, bound := range records.Bindings() {
		if bound.Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET {
			continue
		}
		record := &bindingsv1.Binding{}
		if err := protojson.Unmarshal([]byte(records.Value(bound.Key)), record); err != nil {
			return nil, fmt.Errorf("the bucket record %s delivered under %s is not a binding record", bound.Name, bound.Key)
		}
		properties := record.GetBucket()
		if s3store.Endpointed(properties) || properties.GetBucket() == "" || slices.Contains(buckets, properties.GetBucket()) {
			continue
		}
		buckets = append(buckets, properties.GetBucket())
	}
	return buckets, nil
}

func tagOf(bucket string) string {
	sum := sha256.Sum256([]byte("gcs\x00" + bucket))
	return hex.EncodeToString(sum[:tagLength])
}
