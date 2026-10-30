package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func TestAnAWSTargetForwardsPortsAndForwardsNothingWhenNoBindingAsksWithoutReachingAWS(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-2", Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""), BaseEndpoint: aws.String("http://127.0.0.1:1")}
	forward := NewProvider(control.Options{Region: "eu-west-2"}, nil, cfg, defaultNamespace).Hooks().BindingAccess
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
		remove:  func(context.Context) error { return nil },
		release: func(context.Context) error { *released++; return nil },
	}
}

func TestAPortForwardWhoseBastionWasNeverBuiltRemovesWhatItMadeInTheNetworkBeforeLettingGoOfIt(t *testing.T) {
	var steps []string
	placement := placementOf(false, new(int))
	placement.remove = func(context.Context) error { steps = append(steps, "remove"); return nil }
	placement.release = func(context.Context) error { steps = append(steps, "release"); return nil }
	if _, err := placement.place(context.Background()); err == nil {
		t.Fatal("place() = nil, want the reconcile error")
	}
	if !slices.Equal(steps, []string{"remove", "release"}) {
		t.Errorf("steps = %v, want the security group a failed reconcile left in the VPC removed before the network is let go: an orphaned group keeps the VPC from being deleted", steps)
	}

	steps = nil
	placement.remove = func(context.Context) error { steps = append(steps, "remove"); return errors.New("DependencyViolation") }
	if _, err := placement.place(context.Background()); err == nil {
		t.Fatal("place() = nil, want the reconcile and removal errors")
	}
	if !slices.Equal(steps, []string{"remove"}) {
		t.Errorf("steps = %v, want the network kept while what the bastion made in it is still there", steps)
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
