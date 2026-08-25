package deploy

import (
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func masterUserSecret(args pulumi.MockResourceArgs) resource.PropertyMap {
	if args.TypeToken != "aws:rds/cluster:Cluster" {
		return nil
	}
	return resource.PropertyMap{
		"masterUserSecrets": resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewObjectProperty(resource.PropertyMap{
				"secretArn": resource.NewStringProperty("arn:aws:secretsmanager:eu-west-1:111122223333:secret:db-main"),
			}),
		}),
	}
}

func TestPostgresComponentTags(t *testing.T) {
	rec := recordTags(t, func(ctx *pulumi.Context) error {
		err := registerPostgres(ctx, "shop", "prod", "db--main", translatePostgres(&providerkit.PostgresSpec{}), "vpc-1", "10.0.0.0/16", []string{"subnet-1", "subnet-2"})
		return err
	}, masterUserSecret)

	cases := []struct {
		typeToken string
		name      string
	}{
		{"aws:ec2/securityGroup:SecurityGroup", "db-main-security-group"},
		{"aws:rds/subnetGroup:SubnetGroup", "db-main-subnet-group"},
		{"aws:rds/cluster:Cluster", "db-main"},
		{"aws:rds/clusterInstance:ClusterInstance", "db-main-instance"},
	}
	for _, tc := range cases {
		if got, want := rec.component(t, tc.typeToken, tc.name), naming.KindDatabase.Component(); got != want {
			t.Errorf("%s on %s = %q, want %q", tagComponent, tc.name, got, want)
		}
	}
}

func TestPostgresDescriptionsFitAWSLimits(t *testing.T) {
	long := strings.Repeat("storefront-", 40)
	rec := recordTags(t, func(ctx *pulumi.Context) error {
		return registerPostgres(ctx, long, "prod", "db--main", translatePostgres(&providerkit.PostgresSpec{}), "vpc-1", "10.0.0.0/16", []string{"subnet-1", "subnet-2"})
	}, masterUserSecret)

	sg := rec.inputsOf(t, "aws:ec2/securityGroup:SecurityGroup", "db-main-security-group")
	if got := sg["description"].StringValue(); len(got) != maxSecurityGroupDescriptionLen {
		t.Errorf("security group description is %d chars, want it clamped to the %d EC2 accepts", len(got), maxSecurityGroupDescriptionLen)
	}
	for _, rules := range []string{"ingress", "egress"} {
		for _, rule := range sg[resource.PropertyKey(rules)].ArrayValue() {
			if got := rule.ObjectValue()["description"].StringValue(); len(got) != maxSecurityGroupDescriptionLen {
				t.Errorf("%s rule description is %d chars, want it clamped to the %d EC2 accepts", rules, len(got), maxSecurityGroupDescriptionLen)
			}
		}
	}

	subnets := rec.inputsOf(t, "aws:rds/subnetGroup:SubnetGroup", "db-main-subnet-group")
	if got := subnets["description"].StringValue(); len(got) != maxSubnetGroupDescriptionLen {
		t.Errorf("subnet group description is %d chars, want it clamped to the %d RDS accepts", len(got), maxSubnetGroupDescriptionLen)
	}
}

func TestPostgresResourceIDs(t *testing.T) {
	t.Parallel()

	at := resourceCoordinate("shop", "prod", "db--main", naming.KindDatabase)

	cases := map[string]string{
		"db-main":                naming.ResourceID(at.Kind, at.Name),
		"db-main-security-group": naming.ResourceID(at.Kind, at.Name, "security-group"),
		"db-main-subnet-group":   naming.ResourceID(at.Kind, at.Name, "subnet-group"),
		"db-main-instance":       naming.ResourceID(at.Kind, at.Name, "instance"),
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("resource id = %q, want %q", got, want)
		}
		if strings.Contains(got, "_") {
			t.Errorf("resource id %q mixes alphabets; the deploy log is kebab throughout", got)
		}
	}
}

func TestRDSIdentifierPrefix(t *testing.T) {
	t.Parallel()

	at := resourceCoordinate("shop", "prod", "db--main", naming.KindDatabase)

	t.Run("carries project, env, resource and role", func(t *testing.T) {
		t.Parallel()

		cases := map[string]string{
			"":         "shop-prod-main-",
			"instance": "shop-prod-main-instance-",
			"subnets":  "shop-prod-main-subnets-",
		}
		for role, want := range cases {
			if got := rdsIdentifierPrefix(at, role); got != want {
				t.Errorf("rdsIdentifierPrefix(%q) = %q, want %q", role, got, want)
			}
		}
	})

	t.Run("fits the identifier AWS appends to", func(t *testing.T) {
		t.Parallel()

		long := resourceCoordinate(strings.Repeat("p", 30), strings.Repeat("e", 30), "db--"+strings.Repeat("m", 30), naming.KindDatabase)
		got := rdsIdentifierPrefix(long, "instance")
		if len(got) > maxRDSIdentifierPrefixLen {
			t.Errorf("rdsIdentifierPrefix() = %q, length %d, want <= %d", got, len(got), maxRDSIdentifierPrefixLen)
		}
		if len(got)+rdsAutonameSuffixLen > maxRDSIdentifierLen {
			t.Errorf("rdsIdentifierPrefix() length %d leaves no room for the %d-character suffix within %d", len(got), rdsAutonameSuffixLen, maxRDSIdentifierLen)
		}
	})

	t.Run("starts with a letter even when the project does not", func(t *testing.T) {
		t.Parallel()

		got := rdsIdentifierPrefix(resourceCoordinate("7shop", "prod", "db--main", naming.KindDatabase), "")
		if first := got[0]; !(first >= 'a' && first <= 'z') {
			t.Errorf("rdsIdentifierPrefix() = %q, want a letter first — RDS rejects a leading digit", got)
		}
	})

	t.Run("two long names sharing a prefix stay distinct", func(t *testing.T) {
		t.Parallel()

		shared := strings.Repeat("reporting-", 5)
		a := rdsIdentifierPrefix(resourceCoordinate("shop", "prod", "db--"+shared+"alpha", naming.KindDatabase), "")
		b := rdsIdentifierPrefix(resourceCoordinate("shop", "prod", "db--"+shared+"beta", naming.KindDatabase), "")
		if a == b {
			t.Errorf("rdsIdentifierPrefix() collided: both %q", a)
		}
	})
}

func TestEC2Description(t *testing.T) {
	t.Parallel()

	at := resourceCoordinate("shop", "prod", "db--main", naming.KindDatabase)
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

		got := translatePostgres(&providerkit.PostgresSpec{Version: "15"})

		if got.Engine != "aurora-postgresql" {
			t.Errorf("Engine = %q, want aurora-postgresql", got.Engine)
		}
		if got.EngineMode != "provisioned" {
			t.Errorf("EngineMode = %q, want provisioned (serverless v2 runs provisioned + scaling config)", got.EngineMode)
		}
		if got.MinCapacity != 0 {
			t.Errorf("MinCapacity = %v, want 0 (scale to zero)", got.MinCapacity)
		}
		if got.MaxCapacity != 2 {
			t.Errorf("MaxCapacity = %v, want 2", got.MaxCapacity)
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
		if got.DeletionProtection {
			t.Error("DeletionProtection = true, want false (clean teardown)")
		}
		if !got.SkipFinalSnapshot {
			t.Error("SkipFinalSnapshot = false, want true (clean teardown)")
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

			got := translatePostgres(&providerkit.PostgresSpec{Version: tc.version})
			if got.EngineVersion != tc.want {
				t.Errorf("EngineVersion = %q, want %q", got.EngineVersion, tc.want)
			}
		})
	}
}
