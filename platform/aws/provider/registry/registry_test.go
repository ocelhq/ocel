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

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func TestResolveMintsALoginAndCreatesNothing(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "AWS:tok3n", endpoint: "https://123456789012.dkr.ecr.us-east-1.amazonaws.com"}
	target, err := Resolve(context.Background(), api)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(api.created) != 0 {
		t.Errorf("Resolve created %v, want nothing: a dry run resolves the registry too and must leave no repository behind", api.created)
	}
	if target.Server != "123456789012.dkr.ecr.us-east-1.amazonaws.com" {
		t.Errorf("Server = %q, want the proxy endpoint without its scheme, which is what a docker push addresses", target.Server)
	}
	if target.Namespace != Namespace || target.Username != "AWS" || target.Password != "tok3n" {
		t.Errorf("target = %v, want namespace %q and the login ECR minted", target, Namespace)
	}
	if got := target.Coordinate("web", "sha256-abc"); got != "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-abc" {
		t.Errorf("Coordinate = %q, want the repository a push creates", got)
	}
	if !Owns(target) {
		t.Error("Owns() = false for the target Resolve minted, so its pushes would never create the repository")
	}
	if Owns(providerkit.RegistryTarget{Server: "ghcr.io", Username: "me", Password: "x"}) {
		t.Error("Owns() = true for a registry the project named, whose repositories are not this provider's to create")
	}
}

func TestResolveRefusesALoginItCannotSplit(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "no-separator", endpoint: "https://x.dkr.ecr.us-east-1.amazonaws.com"}
	if _, err := Resolve(context.Background(), api); err == nil {
		t.Fatal("Resolve accepted a login that is not user:password, which nothing can push with")
	}
}

func TestAPushCreatesTheRepositoryTheCoordinateNamesOnce(t *testing.T) {
	t.Parallel()

	target := providerkit.RegistryTarget{Server: "123456789012.dkr.ecr.us-east-1.amazonaws.com", Namespace: Namespace, Username: "AWS", Password: "tok3n"}
	for coordinate, want := range map[string]string{
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-abc":              "ocel/web",
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/api@sha256:" + digest64("0"): "ocel/api",
	} {
		got, err := repositoryOf(target, coordinate)
		if err != nil || got != want {
			t.Errorf("repositoryOf(%s) = %q, %v, want %q", coordinate, got, err, want)
		}
	}
	if _, err := repositoryOf(target, "ghcr.io/acme/web:tag"); err == nil {
		t.Error("repositoryOf accepted a coordinate under another registry, which no repository of this account's holds")
	}

	api := &fakeECR{existing: []string{"ocel/api"}}
	if err := ensure(context.Background(), api, "ocel/api"); err != nil || len(api.created) != 0 {
		t.Errorf("ensure of an existing repository = %v, created %v; want a no-op", err, api.created)
	}
	if err := ensure(context.Background(), api, "ocel/web"); err != nil || !slices.Equal(api.created, []string{"ocel/web"}) {
		t.Errorf("ensure of a new repository = %v, created %v; want it created immutable under the namespace", err, api.created)
	}
}

func digest64(fill string) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = fill[0]
	}
	return string(out)
}
