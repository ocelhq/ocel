package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

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

func adoptedValues(cacheBucket string) map[string]string {
	return map[string]string{valueKeyCacheBucket: cacheBucket}
}

type bucketAPI interface {
	Get(ctx context.Context, bucketName string, params r2.BucketGetParams, opts ...option.RequestOption) (*r2.Bucket, error)
	New(ctx context.Context, params r2.BucketNewParams, opts ...option.RequestOption) (*r2.Bucket, error)
	Delete(ctx context.Context, bucketName string, params r2.BucketDeleteParams, opts ...option.RequestOption) (*r2.BucketDeleteResponse, error)
}

type tokenAPI interface {
	List(ctx context.Context, query user.TokenListParams, opts ...option.RequestOption) (*pagination.V4PagePaginationArray[shared.Token], error)
	New(ctx context.Context, body user.TokenNewParams, opts ...option.RequestOption) (*user.TokenNewResponse, error)
	Verify(ctx context.Context, opts ...option.RequestOption) (*user.TokenVerifyResponse, error)
	Delete(ctx context.Context, tokenID string, opts ...option.RequestOption) (*user.TokenDeleteResponse, error)
}

type temporaryCredentialAPI interface {
	New(ctx context.Context, params r2.TemporaryCredentialNewParams, opts ...option.RequestOption) (*r2.TemporaryCredentialNewResponse, error)
}

type objectAPI interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type permissionGroupAPI interface {
	List(ctx context.Context, query user.TokenPermissionGroupListParams, opts ...option.RequestOption) (*pagination.SinglePage[user.TokenPermissionGroupListResponse], error)
}

type cacheStore struct {
	buckets     bucketAPI
	tokens      tokenAPI
	groups      permissionGroupAPI
	credentials temporaryCredentialAPI
	objects     func(endpoint string, creds r2.TemporaryCredentialNewResponse) objectAPI
	wait        func(context.Context, time.Duration) error
}

func newCacheStore(client *cf.Client) cacheStore {
	return cacheStore{
		buckets:     client.R2.Buckets,
		tokens:      client.User.Tokens,
		groups:      client.User.Tokens.PermissionGroups,
		credentials: client.R2.TemporaryCredentials,
		objects:     s3Objects,
		wait:        waitBeforeRetry,
	}
}

func s3Objects(endpoint string, creds r2.TemporaryCredentialNewResponse) objectAPI {
	return s3.New(s3.Options{
		Region:       r2Region,
		BaseEndpoint: awssdk.String(endpoint),
		UsePathStyle: true,
		Credentials: awssdk.CredentialsProviderFunc(func(context.Context) (awssdk.Credentials, error) {
			return awssdk.Credentials{
				AccessKeyID:     creds.AccessKeyID,
				SecretAccessKey: creds.SecretAccessKey,
				SessionToken:    creds.SessionToken,
				Source:          "cloudflare-r2-temporary-credentials",
			}, nil
		}),
	})
}

type cacheStoreState struct {
	name       string
	bucketHeld bool
	token      shared.Token
	tokenHeld  bool
}

func (s cacheStore) read(ctx context.Context, accountID string, class edge.Class) (cacheStoreState, error) {
	name, ok := cacheStoreNameByClass[class]
	if !ok {
		return cacheStoreState{}, fmt.Errorf("cloudflare: unknown class %q", class)
	}
	bucketHeld, err := s.bucketPresent(ctx, accountID, name)
	if err != nil {
		return cacheStoreState{}, err
	}
	token, tokenHeld, err := s.findToken(ctx, name)
	if err != nil {
		return cacheStoreState{}, err
	}
	return cacheStoreState{name: name, bucketHeld: bucketHeld, token: token, tokenHeld: tokenHeld}, nil
}

func (s cacheStore) bucketPresent(ctx context.Context, accountID, name string) (bool, error) {
	_, err := s.buckets.Get(ctx, name, r2.BucketGetParams{AccountID: cf.F(accountID)})
	switch {
	case err == nil:
		return true, nil
	case hasStatus(err, http.StatusNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("look up R2 bucket %q: %w", name, err)
	}
}

func (s cacheStore) bootstrap(ctx context.Context, accountID string, state cacheStoreState) (edge.BootstrapOutput, error) {
	name := state.name
	if !state.bucketHeld {
		if err := s.createBucket(ctx, accountID, name); err != nil {
			return edge.BootstrapOutput{}, err
		}
	}

	token := mintedToken{ID: state.token.ID}
	if !state.tokenHeld {
		minted, err := s.mintToken(ctx, accountID, name)
		if err != nil {
			return edge.BootstrapOutput{}, err
		}
		token = minted
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
		Values: adoptedValues(name),
		Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: values}},
	}, nil
}

func (s cacheStore) teardown(ctx context.Context, accountID string, class edge.Class) error {
	name, ok := cacheStoreNameByClass[class]
	if !ok {
		return fmt.Errorf("cloudflare: unknown class %q", class)
	}
	token, held, err := s.findToken(ctx, name)
	if err != nil {
		return err
	}
	if held {
		if err := s.emptyBucket(ctx, accountID, name, token.ID); err != nil {
			return err
		}
	}
	if err := s.deleteBucket(ctx, accountID, name); err != nil {
		return err
	}
	if !held {
		return nil
	}
	if _, err := s.tokens.Delete(ctx, token.ID); err != nil && !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("delete the R2 token %q (id %s): %w", name, token.ID, err)
	}
	return nil
}

func (s cacheStore) deleteBucket(ctx context.Context, accountID, name string) error {
	_, err := s.buckets.Delete(ctx, name, r2.BucketDeleteParams{AccountID: cf.F(accountID)})
	switch {
	case err == nil, hasStatus(err, http.StatusNotFound):
		return nil
	case hasStatus(err, http.StatusConflict):
		return fmt.Errorf("delete R2 bucket %q: it still holds objects and no %q API token is left to empty it with; empty it from the Cloudflare dashboard, then re-run: %w", name, name, err)
	default:
		return fmt.Errorf("delete R2 bucket %q: %w", name, err)
	}
}

const emptyCredentialTTL = 30 * time.Minute

func (s cacheStore) emptyBucket(ctx context.Context, accountID, name, parentAccessKeyID string) error {
	if _, err := s.buckets.Get(ctx, name, r2.BucketGetParams{AccountID: cf.F(accountID)}); err != nil {
		if hasStatus(err, http.StatusNotFound) {
			return nil
		}
		return fmt.Errorf("look up R2 bucket %q: %w", name, err)
	}
	creds, err := s.credentials.New(ctx, r2.TemporaryCredentialNewParams{
		AccountID: cf.F(accountID),
		TemporaryCredential: r2.TemporaryCredentialParam{
			Bucket:            cf.F(name),
			ParentAccessKeyID: cf.F(parentAccessKeyID),
			Permission:        cf.F(r2.TemporaryCredentialPermissionObjectReadWrite),
			TTLSeconds:        cf.F(emptyCredentialTTL.Seconds()),
		},
	})
	if err != nil {
		return fmt.Errorf("mint credentials to empty R2 bucket %q: %w", name, err)
	}

	objects := s.objects(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID), *creds)
	var token *string
	for {
		listed, err := objects.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awssdk.String(name),
			ContinuationToken: token,
			MaxKeys:           awssdk.Int32(deleteBatchSize),
		})
		if err != nil {
			if bucketGone(err) {
				return nil
			}
			return fmt.Errorf("list R2 bucket %q: %w", name, err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(listed.Contents))
		for _, object := range listed.Contents {
			ids = append(ids, s3types.ObjectIdentifier{Key: object.Key})
		}
		if len(ids) > 0 {
			deleted, err := objects.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: awssdk.String(name),
				Delete: &s3types.Delete{Objects: ids, Quiet: awssdk.Bool(true)},
			})
			if err != nil {
				if bucketGone(err) {
					return nil
				}
				return fmt.Errorf("delete objects in R2 bucket %q: %w", name, err)
			}
			if err := refusedObjects(name, deleted.Errors); err != nil {
				return err
			}
		}
		if listed.IsTruncated == nil || !*listed.IsTruncated {
			return nil
		}
		token = listed.NextContinuationToken
	}
}

const deleteBatchSize = 1000

const refusedObjectsShown = 10

func refusedObjects(bucket string, refused []s3types.Error) error {
	if len(refused) == 0 {
		return nil
	}
	shown := refused
	if len(shown) > refusedObjectsShown {
		shown = shown[:refusedObjectsShown]
	}
	reasons := make([]string, 0, len(shown))
	for _, e := range shown {
		reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", awssdk.ToString(e.Key), awssdk.ToString(e.Message), awssdk.ToString(e.Code)))
	}
	more := ""
	if len(refused) > len(shown) {
		more = fmt.Sprintf(" (and %d more)", len(refused)-len(shown))
	}
	return fmt.Errorf("empty R2 bucket %q: %d object(s) refused: %s%s", bucket, len(refused), strings.Join(reasons, "; "), more)
}

func bucketGone(err error) bool {
	var missing *s3types.NoSuchBucket
	if errors.As(err, &missing) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NotFound")
}

func (s cacheStore) createBucket(ctx context.Context, accountID, name string) error {
	if _, err := s.buckets.New(ctx, r2.BucketNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(name),
	}); err != nil && !hasStatus(err, http.StatusConflict) {
		return fmt.Errorf("create R2 bucket %q: %w", name, err)
	}
	return nil
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
	for attempt := range tokenPropagationAttempts {
		if attempt > 0 {
			if waitErr := s.wait(ctx, tokenPropagationDelay); waitErr != nil {
				return errors.Join(err, waitErr)
			}
		}
		if _, err = s.tokens.Verify(ctx, option.WithAPIToken(value)); err == nil {
			return nil
		}
		if !hasStatus(err, http.StatusUnauthorized) && !hasStatus(err, http.StatusForbidden) {
			return err
		}
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
	return "", fmt.Errorf("no %q permission group is offered by Cloudflare; the R2 token cannot be scoped to a bucket without it", name)
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
