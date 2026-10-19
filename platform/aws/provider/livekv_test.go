//go:build integration

package aws_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

const (
	liveKVTokenRoot    = "/ocel-live/kv"
	liveSigningKeyRoot = "ocel-live/realtime"
)

func writeRESP(conn net.Conn, args ...string) {
	var command strings.Builder
	fmt.Fprintf(&command, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&command, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, _ = conn.Write([]byte(command.String()))
}

func (a liveAccount) commandStore(t *testing.T, binding provider.Binding, token string, command string) string {
	t.Helper()
	props := binding.Properties
	address := net.JoinHostPort(props[provider.PropertyHost], props[provider.PropertyPort])
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var (
		conn net.Conn
		err  error
	)
	if props[provider.PropertyTLS] == "true" && !a.emulated() {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: props[provider.PropertyHost], InsecureSkipVerify: true})
	} else {
		conn, err = dialer.Dial("tcp", address)
	}
	if err != nil {
		t.Fatalf("dial %s (binding tls %s, emulated %t): %v", address, props[provider.PropertyTLS], a.emulated(), err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	if token != "" {
		writeRESP(conn, "AUTH", token)
		if said, _ := reader.ReadString('\n'); !strings.HasPrefix(said, "+OK") {
			return strings.TrimSpace(said)
		}
	}
	writeRESP(conn, command)
	said, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read the answer to %s: %v", command, err)
	}
	return strings.TrimSpace(said)
}

func (a liveAccount) clientsGroup(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	network := ec2.NewFromConfig(a.aws)
	vpcs, err := network.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: awssdk.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil || len(vpcs.Vpcs) == 0 {
		t.Fatalf("DescribeVpcs() of the default VPC = %v, %v", vpcs, err)
	}
	made, err := network.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   awssdk.String(name),
		Description: awssdk.String("the clients a live test's store admits"),
		VpcId:       vpcs.Vpcs[0].VpcId,
	})
	if err != nil {
		t.Fatalf("CreateSecurityGroup(%s) = %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := network.DeleteSecurityGroup(context.WithoutCancel(ctx), &ec2.DeleteSecurityGroupInput{GroupId: made.GroupId}); err != nil {
			t.Errorf("DeleteSecurityGroup(%s) = %v", name, err)
		}
	})
	return awssdk.ToString(made.GroupId)
}

func TestLiveAKVStoreAnswersItsTokenAloneAndItsTokenGoesWithIt(t *testing.T) {
	a := live(t)
	ctx := context.Background()
	params := ssm.NewFromConfig(a.aws)
	clients := a.clientsGroup(t, "ocel-live-kv-clients")
	backend := "file://" + t.TempDir()
	stacks := deploy.NewStacks(func(context.Context, deploy.Scope) (deploy.Config, error) {
		return deploy.Config{
			Region:         liveRegion,
			Network:        ec2.NewFromConfig(a.aws),
			VPCClients:     clients,
			BackendURL:     backend,
			Passphrase:     "live-suite",
			PulumiProject:  naming.PulumiProject("kvlive"),
			Parameters:     params,
			KVTokenRoot:    liveKVTokenRoot,
			SigningKeys:    secretsmanager.NewFromConfig(a.aws),
			SigningKeyRoot: liveSigningKeyRoot,
		}, nil
	}, &deploy.Realized{})
	ref := provider.StackRef{Project: "kvlive", Tier: environment.TierProduction, Name: naming.InfraStack("production")}
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
				t.Errorf("Destroy() in cleanup = %v", err)
			}
		}
	})

	result, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "kv--cache", Declared: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Bindings) != 1 {
		t.Fatalf("Provision() returned %d bindings, want the store's", len(result.Bindings))
	}
	binding := result.Bindings[0]
	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the store's binding is one providerserver refuses: %v", err)
	}
	token := binding.Properties[provider.PropertyPassword]
	parameter := liveKVTokenRoot + "/production/kvlive/production/kv--cache"
	if !a.paramExists(t, parameter) {
		t.Errorf("no parameter %s holds the store's token", parameter)
	}

	if said := a.commandStore(t, binding, "", "PING"); !strings.HasPrefix(said, "-NOAUTH") {
		t.Errorf("a PING without the token was answered %q, want NOAUTH", said)
	}
	if said := a.commandStore(t, binding, "not-the-token", "PING"); !strings.HasPrefix(said, "-WRONGPASS") && !strings.HasPrefix(said, "-ERR") {
		t.Errorf("a PING with another token was answered %q, want it refused", said)
	}
	if said := a.commandStore(t, binding, token, "PING"); said != "+PONG" {
		t.Errorf("a PING with the store's token was answered %q, want PONG", said)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	destroyed = true
	if a.paramExists(t, parameter) {
		t.Errorf("the store's token outlives the stack that declared it in %s", parameter)
	}
}
