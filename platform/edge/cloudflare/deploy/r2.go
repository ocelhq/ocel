package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/packages/pagination"
	"github.com/cloudflare/cloudflare-go/v4/r2"
	"github.com/cloudflare/cloudflare-go/v4/shared"
	"github.com/cloudflare/cloudflare-go/v4/user"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	cacheStorePermissionGroup = "Workers R2 Storage Bucket Item Write"

	r2Region = "auto"

	valueKeyCacheBucket = "cacheBucket"

	tokenPropagationAttempts = 12
	tokenPropagationDelay    = 10 * time.Second

	tokenPageSize = 50
)

var cacheStoreNameByClass = map[edge.Class]string{
	edge.ClassProduction: "ocel-edge-cache",
	edge.ClassPreview:    "ocel-edge-cache-preview",
}

func cacheStoreName(class edge.Class) string { return cacheStoreNameByClass[class] }

type bucketAPI interface {
	Get(ctx context.Context, bucketName string, params r2.BucketGetParams, opts ...option.RequestOption) (*r2.Bucket, error)
	New(ctx context.Context, params r2.BucketNewParams, opts ...option.RequestOption) (*r2.Bucket, error)
}

type tokenAPI interface {
	List(ctx context.Context, query user.TokenListParams, opts ...option.RequestOption) (*pagination.V4PagePaginationArray[shared.Token], error)
	New(ctx context.Context, body user.TokenNewParams, opts ...option.RequestOption) (*user.TokenNewResponse, error)
	Verify(ctx context.Context, opts ...option.RequestOption) (*user.TokenVerifyResponse, error)
}

type permissionGroupAPI interface {
	List(ctx context.Context, query user.TokenPermissionGroupListParams, opts ...option.RequestOption) (*pagination.SinglePage[user.TokenPermissionGroupListResponse], error)
}

type cacheStore struct {
	buckets bucketAPI
	tokens  tokenAPI
	groups  permissionGroupAPI
	sleep   func(time.Duration)
}

func newCacheStore(client *cf.Client) cacheStore {
	return cacheStore{
		buckets: client.R2.Buckets,
		tokens:  client.User.Tokens,
		groups:  client.User.Tokens.PermissionGroups,
		sleep:   time.Sleep,
	}
}

func (s cacheStore) bootstrap(ctx context.Context, accountID string, class edge.Class) (edge.BootstrapOutput, error) {
	name, ok := cacheStoreNameByClass[class]
	if !ok {
		return edge.BootstrapOutput{}, fmt.Errorf("cloudflare: unknown substrate class %q", class)
	}
	if err := s.ensureBucket(ctx, accountID, name); err != nil {
		return edge.BootstrapOutput{}, err
	}

	token, err := s.ensureToken(ctx, accountID, name)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}

	values := map[string]string{
		edge.OfferKeyBucket:      name,
		edge.OfferKeyEndpoint:    fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID),
		edge.OfferKeyRegion:      r2Region,
		edge.OfferKeyAccessKeyID: token.ID,
	}
	if token.SecretAccessKey != "" {
		values[edge.OfferKeySecretAccessKey] = token.SecretAccessKey
	}

	return edge.BootstrapOutput{
		Trust:  edge.TrustExternal,
		Values: map[string]string{valueKeyCacheBucket: name},
		Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: values}},
	}, nil
}

func (s cacheStore) ensureBucket(ctx context.Context, accountID, name string) error {
	_, err := s.buckets.Get(ctx, name, r2.BucketGetParams{AccountID: cf.F(accountID)})
	if err == nil {
		return nil
	}
	if !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("look up R2 bucket %q: %w", name, err)
	}
	if _, err := s.buckets.New(ctx, r2.BucketNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(name),
	}); err != nil && !hasStatus(err, http.StatusConflict) {
		return fmt.Errorf("create R2 bucket %q: %w", name, err)
	}
	return nil
}

func (s cacheStore) ensureToken(ctx context.Context, accountID, name string) (mintedToken, error) {
	existing, found, err := s.findToken(ctx, name)
	if err != nil {
		return mintedToken{}, err
	}
	if found {
		return mintedToken{ID: existing.ID}, nil
	}
	return s.mintToken(ctx, accountID, name)
}

func (s cacheStore) findToken(ctx context.Context, name string) (shared.Token, bool, error) {
	for page := int64(1); ; page++ {
		res, err := s.tokens.List(ctx, user.TokenListParams{
			Page:    cf.F(float64(page)),
			PerPage: cf.F(float64(tokenPageSize)),
		})
		if err != nil {
			return shared.Token{}, false, mintPermissionError("list Cloudflare API tokens", err)
		}
		for _, token := range res.Result {
			if token.Name == name {
				return token, true, nil
			}
		}
		if len(res.Result) < tokenPageSize {
			return shared.Token{}, false, nil
		}
	}
}

type mintedToken struct {
	ID              string
	SecretAccessKey string
}

func (s cacheStore) mintToken(ctx context.Context, accountID, name string) (mintedToken, error) {
	groupID, err := s.permissionGroupID(ctx, cacheStorePermissionGroup)
	if err != nil {
		return mintedToken{}, err
	}

	created, err := s.tokens.New(ctx, user.TokenNewParams{
		Name: cf.F(name),
		Policies: cf.F([]shared.TokenPolicyParam{{
			Effect:           cf.F(shared.TokenPolicyEffectAllow),
			PermissionGroups: cf.F([]shared.TokenPolicyPermissionGroupParam{{ID: cf.F(groupID)}}),
			Resources: cf.F(map[string]shared.TokenPolicyResourcesUnionParam{
				bucketResource(accountID, name): shared.UnionString("*"),
			}),
		}}),
	})
	if err != nil {
		return mintedToken{}, mintPermissionError(fmt.Sprintf("mint the R2 token %q", name), err)
	}

	if err := s.awaitToken(ctx, created.Value); err != nil {
		return mintedToken{}, fmt.Errorf(
			"minted the R2 token %q (id %s) but it never became usable: %w; "+
				"nothing has stored it, so delete that token in Cloudflare and re-run bootstrap",
			name, created.ID, err,
		)
	}

	sum := sha256.Sum256([]byte(created.Value))
	return mintedToken{ID: created.ID, SecretAccessKey: hex.EncodeToString(sum[:])}, nil
}

func (s cacheStore) awaitToken(ctx context.Context, value string) error {
	var err error
	for attempt := 0; attempt < tokenPropagationAttempts; attempt++ {
		if _, err = s.tokens.Verify(ctx, option.WithAPIToken(value)); err == nil {
			return nil
		}
		if !hasStatus(err, http.StatusUnauthorized) && !hasStatus(err, http.StatusForbidden) {
			return err
		}
		s.sleep(tokenPropagationDelay)
	}
	return err
}

func (s cacheStore) permissionGroupID(ctx context.Context, name string) (string, error) {
	res, err := s.groups.List(ctx, user.TokenPermissionGroupListParams{Name: cf.F(name)})
	if err != nil {
		return "", mintPermissionError("list Cloudflare token permission groups", err)
	}
	for _, group := range res.Result {
		if group.Name == name {
			return group.ID, nil
		}
	}
	return "", fmt.Errorf("Cloudflare reports no %q permission group; the R2 token cannot be scoped to a bucket without it", name)
}

func bucketResource(accountID, bucket string) string {
	return fmt.Sprintf("com.cloudflare.edge.r2.bucket.%s_default_%s", accountID, bucket)
}

func mintPermissionError(op string, err error) error {
	if !hasStatus(err, http.StatusForbidden) && !hasStatus(err, http.StatusUnauthorized) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w\n\n"+
		"%s must carry the \"API Tokens Write\" permission (User scope) to provision the edge cache store. "+
		"Cloudflare does not offer that permission in the Custom Token builder, so reissue the token from its "+
		"\"Create Additional Tokens\" template — adding it to the existing token is not possible",
		op, err, envAPIToken)
}

func hasStatus(err error, status int) bool {
	var apiErr *cf.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}
