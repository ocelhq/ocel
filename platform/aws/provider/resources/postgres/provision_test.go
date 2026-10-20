package postgres

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestPostgresRefusesAClusterWithNoManagedSecret(t *testing.T) {
	noSecret := func(args pulumi.MockResourceArgs) resource.PropertyMap {
		if args.TypeToken != "aws:rds/cluster:Cluster" {
			return nil
		}
		return resource.PropertyMap{"masterUserSecrets": resource.NewArrayProperty(nil)}
	}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop", testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, pulumi.WithMocks("shop", "prod--infra", componenttest.NewRecorder(noSecret)))

	if err == nil {
		t.Fatal("register() accepted a cluster with no managed master user secret; reading the first of none panics the provider process")
	}
	if !strings.Contains(err.Error(), "managed master user secret") {
		t.Errorf("register() error = %v, want it to name the missing secret", err)
	}
}

func TestPostgresComponentTags(t *testing.T) {
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		err := register(ctx, testStack("shop", testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
		return err
	}, masterUserSecret)

	cases := []struct {
		typeToken string
		name      string
	}{
		{"aws:ec2/securityGroup:SecurityGroup", testCluster + "-sg"},
		{"aws:rds/subnetGroup:SubnetGroup", testCluster + "-subnets"},
		{"aws:rds/cluster:Cluster", testCluster},
		{"aws:rds/clusterInstance:ClusterInstance", testCluster + "-1"},
	}
	for _, tc := range cases {
		if got, want := rec.Component(t, tc.typeToken, tc.name), naming.KindDatabase.Component(); got != want {
			t.Errorf("%s on %s = %q, want %q", component.TagComponent, tc.name, got, want)
		}
	}
}

func TestPostgresDescriptionsFitAWSLimits(t *testing.T) {
	long := strings.Repeat("storefront-", 40)
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack(long, testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, masterUserSecret)

	cluster, err := ClusterName("ocel", environment.TierProduction, component.ResourceCoordinate(long, "prod", "db--main", naming.KindDatabase))
	if err != nil {
		t.Fatal(err)
	}
	sg := rec.InputsOf(t, "aws:ec2/securityGroup:SecurityGroup", cluster+"-sg")
	if got := sg["description"].StringValue(); len(got) != component.MaxSecurityGroupDescriptionLen {
		t.Errorf("security group description is %d chars, want it clamped to the %d EC2 accepts", len(got), component.MaxSecurityGroupDescriptionLen)
	}
	for _, rules := range []string{"ingress", "egress"} {
		for _, rule := range sg[resource.PropertyKey(rules)].ArrayValue() {
			if got := rule.ObjectValue()["description"].StringValue(); len(got) != component.MaxSecurityGroupDescriptionLen {
				t.Errorf("%s rule description is %d chars, want it clamped to the %d EC2 accepts", rules, len(got), component.MaxSecurityGroupDescriptionLen)
			}
		}
	}

	subnets := rec.InputsOf(t, "aws:rds/subnetGroup:SubnetGroup", cluster+"-subnets")
	if got := subnets["description"].StringValue(); len(got) != component.MaxSubnetGroupDescriptionLen {
		t.Errorf("subnet group description is %d chars, want it clamped to the %d RDS accepts", len(got), component.MaxSubnetGroupDescriptionLen)
	}
}

func TestAClusterIsNamedUnderTheAppFenceAndItsTierWithItsMembersBesideIt(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop", testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, masterUserSecret)

	for _, tc := range []struct {
		token, name, key string
	}{
		{component.TokenRDSCluster, testCluster, "clusterIdentifier"},
		{component.TokenRDSClusterMember, testCluster + "-1", "identifier"},
		{component.TokenRDSSubnetGroup, testCluster + "-subnets", "name"},
		{component.TokenEC2SecurityGroup, testCluster + "-sg", "name"},
	} {
		if got := rec.InputsOf(t, tc.token, tc.name)[resource.PropertyKey(tc.key)]; !got.IsString() || got.StringValue() != tc.name {
			t.Errorf("%s %s = %v, want %q, the name it is registered under", tc.token, tc.key, got, tc.name)
		}
	}
	if got := rec.Tags(t, component.TokenEC2SecurityGroup, testCluster+"-sg")["Name"]; got != testCluster+"-sg" {
		t.Errorf("security group Name tag = %q, want its name, which the console lists it by", got)
	}
}

func TestAClusterNamesItsTierAfterTheAppFence(t *testing.T) {
	t.Parallel()

	got, err := ClusterName("ocel", environment.TierPreview, component.ResourceCoordinate("shop", "pr-42", "db--main", naming.KindDatabase))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-preview-shop-pr-42-main-f154dd"; got != want {
		t.Errorf("ClusterName = %q, want %q", got, want)
	}
}

func TestAClustersNameLeavesRoomForItsInstance(t *testing.T) {
	t.Parallel()

	long := component.ResourceCoordinate(strings.Repeat("p", 30), strings.Repeat("e", 30), "db--"+strings.Repeat("m", 20), naming.KindDatabase)
	got, err := ClusterName("ocel", environment.TierProduction, long)
	if err != nil {
		t.Fatal(err)
	}
	if len(got+"-1") > maxRDSIdentifierLen {
		t.Errorf("ClusterName = %q leaves its instance %d characters, over RDS's %d", got, len(got+"-1"), maxRDSIdentifierLen)
	}
	if first := got[0]; first < 'a' || first > 'z' {
		t.Errorf("ClusterName = %q, want a letter first: RDS rejects any other", got)
	}
}

func TestAProtectedClustersFinalSnapshotIsNamedAfterIt(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		protected := translate(&provider.PostgresSpec{}, environment.TierProduction)
		protected.Protected = true
		return register(ctx, testStack("shop", testNetwork), "db--main", protected)
	}, masterUserSecret)

	got := rec.InputsOf(t, component.TokenRDSCluster, testCluster)["finalSnapshotIdentifier"]
	if !got.IsString() || !strings.HasPrefix(got.StringValue(), testCluster+"-final-") {
		t.Errorf("final snapshot = %v, want the cluster plus -final- and the time it was taken", got)
	}
}

func TestEC2Description(t *testing.T) {
	t.Parallel()

	at := component.ResourceCoordinate("shop", "prod", "db--main", naming.KindDatabase)
	got := at.Description("security group for the " + at.Name + " database")

	if !strings.HasPrefix(got, "shop / prod / infra") {
		t.Errorf("Description() = %q, want it to open with the coordinate", got)
	}
	for _, r := range got {
		if r > 127 {
			t.Fatalf("Description() = %q, contains %q; EC2 rejects a description outside its ASCII character set", got, r)
		}
	}
	if len(got) > 255 {
		t.Errorf("Description() length = %d, want <= 255", len(got))
	}
}

func TestTranslatePostgres(t *testing.T) {
	t.Parallel()

	t.Run("fixed serverless v2 defaults", func(t *testing.T) {
		t.Parallel()

		got := translate(&provider.PostgresSpec{Version: "15"}, environment.TierPreview)

		if got.Engine != "aurora-postgresql" {
			t.Errorf("Engine = %q, want aurora-postgresql", got.Engine)
		}
		if got.EngineMode != "provisioned" {
			t.Errorf("EngineMode = %q, want provisioned (serverless v2 runs provisioned + scaling config)", got.EngineMode)
		}
		if got.InstanceClass != "db.serverless" {
			t.Errorf("InstanceClass = %q, want db.serverless", got.InstanceClass)
		}
		if !got.ManageMasterPassword {
			t.Error("ManageMasterPassword = false, want true (RDS-managed secret)")
		}
		if got.PubliclyAccessible {
			t.Error("PubliclyAccessible = true, want false (private)")
		}
		if got.Port != 5432 {
			t.Errorf("Port = %d, want 5432", got.Port)
		}
	})

	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"uses the configured version", "15", "15"},
		{"an empty version falls back to the pinned default", "", defaultPostgresEngineVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := translate(&provider.PostgresSpec{Version: tc.version}, environment.TierProduction)
			if got.EngineVersion != tc.want {
				t.Errorf("EngineVersion = %q, want %q", got.EngineVersion, tc.want)
			}
		})
	}
}

func TestAProductionDatabaseStaysAwakeAndScalesFurtherWhileAPreviewPausesWhenIdle(t *testing.T) {
	t.Parallel()

	production := translate(&provider.PostgresSpec{}, environment.TierProduction)
	if production.MinCapacity != 0.5 || production.MaxCapacity != 8 {
		t.Errorf("production capacity = %v-%v ACU, want 0.5-8: a production database never pauses, so its first query after a quiet spell is not a resume, and it grows past what a preview needs", production.MinCapacity, production.MaxCapacity)
	}
	preview := translate(&provider.PostgresSpec{}, environment.TierPreview)
	if preview.MinCapacity != 0 || preview.MaxCapacity != 2 {
		t.Errorf("preview capacity = %v-%v ACU, want 0-2: a preview pauses when idle and stays small", preview.MinCapacity, preview.MaxCapacity)
	}
}

func TestAClusterLetsItsAppRoleSignInByIAMAndTheDeployCreateItThroughTheDataAPI(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop", testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, masterUserSecret)

	cluster := rec.InputsOf(t, component.TokenRDSCluster, testCluster)
	if !cluster["iamDatabaseAuthenticationEnabled"].BoolValue() {
		t.Error("iamDatabaseAuthenticationEnabled = false, want true: the app connects with a token its role mints, never a password")
	}
	if !cluster["enableHttpEndpoint"].BoolValue() {
		t.Error("enableHttpEndpoint = false, want true: the deploy creates the app role through the Data API, from outside the VPC")
	}
	role := rec.InputsOf(t, RoleToken, testCluster+"/app-role")
	if role["role"].StringValue() != AppRoleFor(environment.TierProduction) || role["database"].StringValue() != postgresDatabaseName {
		t.Errorf("app role inputs = %v, want %s on the %s database", role, AppRoleFor(environment.TierProduction), postgresDatabaseName)
	}
	if got := role["secret"].StringValue(); got != "arn:aws:secretsmanager:eu-west-1:111122223333:secret:db-main" {
		t.Errorf("app role secret = %q, want the cluster's managed master secret: only the deploy signs in as master", got)
	}
}

func TestADatabaseAdmitsItsPortFromTheTiersVPCClientsAlone(t *testing.T) {
	t.Parallel()

	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop", testNetwork), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, masterUserSecret)

	ingress := rec.InputsOf(t, component.TokenEC2SecurityGroup, testCluster+"-sg")["ingress"].ArrayValue()
	if len(ingress) != 1 {
		t.Fatalf("security group has %d ingress rules, want the one admitting the database's port", len(ingress))
	}
	rule := ingress[0].ObjectValue()
	if cidrs, open := rule["cidrBlocks"]; open && len(cidrs.ArrayValue()) > 0 {
		t.Errorf("ingress admits %v, want no address range: anything in the VPC could reach the database", cidrs)
	}
	if groups := rule["securityGroups"].ArrayValue(); len(groups) != 1 || groups[0].StringValue() != "sg-vpc-clients" {
		t.Errorf("ingress admits %v, want the tier's vpc-clients group alone", groups)
	}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return register(ctx, testStack("shop", component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1"}}), "db--main", translate(&provider.PostgresSpec{}, environment.TierProduction))
	}, pulumi.WithMocks("shop", "prod--infra", componenttest.NewRecorder(masterUserSecret)))
	if err == nil || !strings.Contains(err.Error(), "vpc-clients") {
		t.Errorf("register() with no clients group = %v, want it refused rather than admitting nothing or everything", err)
	}
}
