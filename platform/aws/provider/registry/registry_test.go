package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

type fakeECR struct {
	created  []string
	existing []string
	token    string
	endpoint string
}

func (f *fakeECR) CreateRepository(_ context.Context, in *ecr.CreateRepositoryInput, _ ...func(*ecr.Options)) (*ecr.CreateRepositoryOutput, error) {
	name := aws.ToString(in.RepositoryName)
	if slices.Contains(f.existing, name) {
		return nil, &ecrtypes.RepositoryAlreadyExistsException{Message: aws.String(name + " exists")}
	}
	if in.ImageTagMutability != ecrtypes.ImageTagMutabilityImmutable {
		return nil, errors.New("a release pins a digest under its tag, so the tag must never repoint")
	}
	f.created = append(f.created, name)
	return &ecr.CreateRepositoryOutput{}, nil
}

func (f *fakeECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []ecrtypes.AuthorizationData{{
		AuthorizationToken: aws.String(base64.StdEncoding.EncodeToString([]byte(f.token))),
		ProxyEndpoint:      aws.String(f.endpoint),
	}}}, nil
}

func TestResolveCreatesEachRepositoryOnceUnderTheNamespaceAndLogsIn(t *testing.T) {
	t.Parallel()

	api := &fakeECR{existing: []string{"ocel/api"}, token: "AWS:tok3n", endpoint: "https://123456789012.dkr.ecr.us-east-1.amazonaws.com"}
	target, err := Resolve(context.Background(), api, []string{"web", "api"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Equal(api.created, []string{"ocel/web"}) {
		t.Errorf("created %v, want only the repository that did not exist, under the namespace the coordinate is pushed to", api.created)
	}
	if target.Server != "123456789012.dkr.ecr.us-east-1.amazonaws.com" {
		t.Errorf("Server = %q, want the proxy endpoint without its scheme, which is what a docker push addresses", target.Server)
	}
	if target.Namespace != Namespace || target.Username != "AWS" || target.Password != "tok3n" {
		t.Errorf("target = %v, want namespace %q and the login ECR minted", target, Namespace)
	}
	if got := target.Coordinate("web", "sha256-abc"); got != "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-abc" {
		t.Errorf("Coordinate = %q, want the repository the deploy created", got)
	}
}

func TestResolveRefusesALoginItCannotSplit(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "no-separator", endpoint: "https://x.dkr.ecr.us-east-1.amazonaws.com"}
	if _, err := Resolve(context.Background(), api, []string{"web"}); err == nil {
		t.Fatal("Resolve accepted a login that is not user:password, which nothing can push with")
	}
}
