package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func TestAnAWSTargetForwardsPortsAndForwardsNothingWhenNoBindingAsksWithoutReachingAWS(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String("http://127.0.0.1:1")}
	forward := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace).Hooks().BindingAccess
	if forward == nil {
		t.Fatal("the aws provider sets no ForwardPorts hook, so a build never gets the bindings of a private Aurora or Valkey")
	}

	forwards, err := forward.ForwardPorts(context.Background(), provider.PortForwardRequest{Tier: environment.TierProduction}, progress.Discard())

	if err != nil || len(forwards) != 0 {
		t.Errorf("ForwardPorts() of no binding = %v, %v, want nothing forwarded and no call to AWS", forwards, err)
	}
}

func placementOf(built bool, released *int) bastionPlacement {
	return bastionPlacement{
		claim: func(context.Context) (network.Network, error) { return network.Network{VPC: "vpc-ocel"}, nil },
		reconcile: func(context.Context, network.Network) (bastion.Bastion, error) {
			return bastion.Bastion{}, errors.New("the bastion's task role needs the app boundary of its tier, and none is named")
		},
		isBuilt: func(context.Context) (bool, error) { return built, nil },
		release: func(context.Context) error { *released++; return nil },
	}
}

func TestAPortForwardWhoseBastionWasNeverBuiltLetsGoOfTheNetworkItClaimed(t *testing.T) {
	released := 0
	if _, err := placementOf(false, &released).place(context.Background()); err == nil {
		t.Fatal("place() = nil, want the reconcile error")
	}
	if released != 1 {
		t.Errorf("released the network %d times, want once: no bastion part exists whose removal would let go of it", released)
	}

	released = 0
	if _, err := placementOf(true, &released).place(context.Background()); err == nil {
		t.Fatal("place() = nil, want the reconcile error")
	}
	if released != 0 {
		t.Errorf("released the network %d times, want never: the bastion stands in it, and removing the bastion part lets go of it", released)
	}
}

const forwardedSecretARN = "arn:aws:secretsmanager:eu-west-2:111122223333:secret:rds!cluster-0a1b2c3d-AbCdEf"

type clusterSecret struct {
	password string
	reads    []string
}

func (s *clusterSecret) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	s.reads = append(s.reads, *in.SecretId)
	value := fmt.Sprintf(`{"username":"ocel","password":%q}`, s.password)
	return &secretsmanager.GetSecretValueOutput{SecretString: &value}, nil
}

func forwardedPostgres(password string, grants ...provider.Grant) provider.Binding {
	properties := map[string]string{
		provider.PropertyHost: "shop-prod-main.cluster-cxyz.eu-west-2.rds.amazonaws.com", provider.PropertyPort: "5432",
		provider.PropertyDatabase: "ocel", provider.PropertyUsername: "ocel",
	}
	if password != "" {
		properties[provider.PropertyPassword] = password
	}
	return provider.Binding{Type: provider.BindingPostgres, Name: "db--main", Properties: properties, Grants: grants}
}

func TestAForwardOfAPostgresBindingThatNamesItsPasswordBySecretHandsBackThePasswordInTheSecret(t *testing.T) {
	t.Parallel()

	secret := &clusterSecret{password: "rotated-yesterday"}
	binding := forwardedPostgres("", provider.Grant{Label: provider.GrantPasswordSecret, Actions: []string{"secretsmanager:GetSecretValue"}, Resources: []string{forwardedSecretARN}})

	resolved, err := readForwardedPasswords(context.Background(), signin.Sources{Secrets: secret}, []provider.Binding{binding})
	if err != nil {
		t.Fatalf("readForwardedPasswords() = %v", err)
	}
	if got := resolved[binding.Name][provider.PropertyPassword]; got != "rotated-yesterday" {
		t.Errorf("the forward of %s carries a password of %d bytes, want the one the cluster secret holds now", binding.Name, len(got))
	}
	if !slices.Equal(secret.reads, []string{forwardedSecretARN}) {
		t.Errorf("read secrets %v, want the one the binding names", secret.reads)
	}
}

func TestAForwardOfAPostgresBindingCarryingItsPasswordPassesItThroughAndReadsNoSecret(t *testing.T) {
	t.Parallel()

	secret := &clusterSecret{password: "never-read"}

	resolved, err := readForwardedPasswords(context.Background(), signin.Sources{Secrets: secret}, []provider.Binding{forwardedPostgres("published-password")})
	if err != nil {
		t.Fatalf("readForwardedPasswords() = %v", err)
	}
	if len(resolved) != 0 || len(secret.reads) != 0 {
		t.Errorf("resolved %d bindings reading %v, want the binding passed through untouched and no secret read", len(resolved), secret.reads)
	}
}

func TestAForwardOfAnIAMAuthenticatedDatabaseHandsBackATokenTheOperatorMintsAndReadsNoSecret(t *testing.T) {
	t.Parallel()

	secret := &clusterSecret{password: "never-read"}
	var minted []string
	mint := func(_ context.Context, endpoint, username string) (string, error) {
		minted = append(minted, endpoint+" as "+username)
		return "operator-token", nil
	}
	binding := forwardedPostgres("", provider.Grant{Label: provider.GrantIAMAuthentication, Actions: []string{"rds-db:connect"}, Resources: []string{"arn:aws:rds-db:eu-west-2:111122223333:dbuser:cluster-ABC/ocel"}})

	resolved, err := readForwardedPasswords(context.Background(), signin.Sources{Secrets: secret, MintToken: mint}, []provider.Binding{binding})
	if err != nil {
		t.Fatalf("readForwardedPasswords() = %v", err)
	}
	if got := resolved[binding.Name][provider.PropertyPassword]; got != "operator-token" {
		t.Errorf("the forward carries password %q, want the token minted for it", got)
	}
	if want := "shop-prod-main.cluster-cxyz.eu-west-2.rds.amazonaws.com:5432 as ocel"; len(minted) != 1 || minted[0] != want {
		t.Errorf("minted %v, want one token for %s: RDS checks it against the cluster's endpoint, not the forward's", minted, want)
	}
	if len(secret.reads) != 0 {
		t.Errorf("read secrets %v, want none", secret.reads)
	}
}

type storeSeen struct {
	mutex sync.Mutex
	paths []string
}

func (s *storeSeen) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mutex.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mutex.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

type bearer string

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}

func servedProxy(t *testing.T, bindings []provider.Binding) (provider.BindingProxy, *storeSeen) {
	t.Helper()
	return servedGrants(t, provider.BindingGrant{Grantee: "web", Bindings: bindings})
}

func tokenOf(t *testing.T, proxy provider.BindingProxy, grantee string) string {
	t.Helper()
	for _, session := range proxy.Sessions {
		if session.Grantee == grantee {
			return session.SessionToken
		}
	}
	t.Fatalf("the proxy minted no token for %q: %+v", grantee, proxy.Sessions)
	return ""
}

func servedGrants(t *testing.T, grants ...provider.BindingGrant) (provider.BindingProxy, *storeSeen) {
	t.Helper()
	store := &storeSeen{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(store.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)
	if _, err := p.deployed.resolve(environment.TierProduction, func() (account.Reading, error) {
		return account.Reading{Deployed: account.Deployed{Present: true, StateTable: "state-table", VariablesTable: "variables", Buckets: naming.Buckets{naming.StorageState: "state", naming.StorageArtifacts: "artifacts", naming.StorageEdge: "edge", naming.StorageCache: "cache"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	serve := p.Hooks().BindingAccess
	if serve == nil {
		t.Fatal("the aws provider sets no ServeBindingProxy hook, so a build never reaches a bucket")
	}
	proxy, err := serve.ServeProxy(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: grants}, progress.Discard())
	if err != nil {
		t.Fatalf("ServeBindingProxy() error = %v", err)
	}
	t.Cleanup(proxy.Close)
	return proxy, store
}

func bucketBinding(name, bucket string) provider.Binding {
	return provider.Binding{Type: provider.BindingBucket, Name: name, Properties: map[string]string{provider.PropertyBucket: bucket}}
}

func TestTheAWSBindingProxyServesTheBucketsABuildBindsUnderTheDeployCredentialsAndNoOtherBucket(t *testing.T) {
	proxy, store := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})
	client := bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(proxy.Sessions[0].SessionToken)}, proxy.Address)

	if _, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); err != nil {
		t.Fatalf("Head of the bound bucket = %v, want it answered", err)
	}
	store.mutex.Lock()
	reached := slices.Clone(store.paths)
	store.mutex.Unlock()
	if len(reached) != 1 || reached[0] != "HEAD /shop-uploads/a.png" {
		t.Errorf("the store saw %v, want the one head the build asked for", reached)
	}

	_, err := client.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "someone-elses", Key: "a.png"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Head of a bucket the build does not bind = %v, want it refused: the deploy credentials reach far more than the app's grant", err)
	}
}

func TestTheAWSBindingProxyRefusesACallThatPresentsNoSessionToken(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")})

	_, err := bucketv1connect.NewBucketServiceClient(http.DefaultClient, proxy.Address).
		Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"})

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Head without the token = %v, want it refused", err)
	}
}

func TestTheAWSBindingProxyForABuildThatBindsNoBucketServesNothingAndAsksAWSNothing(t *testing.T) {
	store := &storeSeen{}
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String(store.serve(t))}
	p := NewProvider(Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace)

	proxy, err := p.ServeBindingProxy(context.Background(), provider.BindingProxyRequest{Slug: "shop", Tier: environment.TierProduction, Env: "production", Grants: []provider.BindingGrant{{Grantee: "web", Bindings: []provider.Binding{
		{Type: provider.BindingTopic, Name: "topic--events"},
	}}}}, progress.Discard())
	if err != nil {
		t.Fatalf("ServeBindingProxy() error = %v", err)
	}

	if proxy.Address != "" || proxy.Close != nil {
		t.Errorf("ServeBindingProxy() = %+v, want no proxy for a build that binds no bucket", proxy)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if len(store.paths) != 0 {
		t.Errorf("AWS was asked %v, want nothing: the bootstrap is read only to serve a bucket", store.paths)
	}
}

func TestTheAWSBindingProxyNamesTheBindingsItHasNoServiceFor(t *testing.T) {
	proxy, _ := servedProxy(t, []provider.Binding{
		bucketBinding("bucket--uploads", "shop-uploads"),
		{Type: provider.BindingTopic, Name: "topic--events"},
		{Type: provider.BindingTask, Name: "task--resize"},
	})

	if want := []string{"task--resize", "topic--events"}; !slices.Equal(slices.Sorted(slices.Values(proxy.Unserved)), want) {
		t.Errorf("Unserved = %v, want %v: the build is not handed a record nothing answers for", proxy.Unserved, want)
	}
}

func TestTheAWSBindingProxyGrantsEachGranteeOnlyItsOwnBucketsAndAProjectWideGrantEveryBucket(t *testing.T) {
	proxy, _ := servedGrants(t,
		provider.BindingGrant{Grantee: "web", Bindings: []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads")}},
		provider.BindingGrant{Grantee: "docs", Bindings: []provider.Binding{bucketBinding("bucket--manuals", "shop-manuals")}},
		provider.BindingGrant{Grantee: "", Bindings: []provider.Binding{bucketBinding("bucket--uploads", "shop-uploads"), bucketBinding("bucket--manuals", "shop-manuals")}},
	)
	web := tokenOf(t, proxy, "web")
	docs := tokenOf(t, proxy, "docs")
	project := tokenOf(t, proxy, "")
	if web == docs || web == project || docs == project {
		t.Fatalf("the grantees share a token: %+v", proxy.Sessions)
	}
	as := func(token string) bucketv1connect.BucketServiceClient {
		return bucketv1connect.NewBucketServiceClient(&http.Client{Transport: bearer(token)}, proxy.Address)
	}

	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); err != nil {
		t.Errorf("web reaching its own bucket = %v, want it answered", err)
	}
	if _, err := as(web).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-manuals", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("web reaching docs' bucket = %v, want it refused: a build gets only the buckets its own app binds", err)
	}
	if _, err := as(docs).Head(context.Background(), &bucketv1.HeadRequest{Bucket: "shop-uploads", Key: "a.png"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("docs reaching web's bucket = %v, want it refused", err)
	}
	for _, bucket := range []string{"shop-uploads", "shop-manuals"} {
		if _, err := as(project).Head(context.Background(), &bucketv1.HeadRequest{Bucket: bucket, Key: "a.png"}); err != nil {
			t.Errorf("the project-wide grant reaching %s = %v, want it answered: the preBuild is meant to reach every bucket", bucket, err)
		}
	}
}
