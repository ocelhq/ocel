package cfn

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	pulumiAWSModulePrefix = "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/"
	ocelTokenPrefix       = "ocel:index:"
)

func listDeclaredPulumiTypes(t *testing.T) []string {
	t.Helper()
	declared := map[string]bool{}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "testdata" {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, found := range listPulumiTypesIn(file) {
			declared[found] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the AWS provider's programs: %v", err)
	}
	listed := make([]string, 0, len(declared))
	for found := range declared {
		listed = append(listed, found)
	}
	slices.Sort(listed)
	return listed
}

func listPulumiTypesIn(file *ast.File) []string {
	modules := map[string]string{}
	for _, imported := range file.Imports {
		path, _ := strconv.Unquote(imported.Path.Value)
		module, found := strings.CutPrefix(path, pulumiAWSModulePrefix)
		if !found {
			continue
		}
		name := filepath.Base(path)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		modules[name] = module
	}
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.BasicLit:
			if value, err := strconv.Unquote(node.Value); err == nil && node.Kind == token.STRING && strings.HasPrefix(value, ocelTokenPrefix) {
				found = append(found, value)
			}
		case *ast.CallExpr:
			selector, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			module, imported := modules[pkg.Name]
			name, constructs := strings.CutPrefix(selector.Sel.Name, "New")
			if !imported || !constructs || name == "" || len(node.Args) < 3 {
				return true
			}
			found = append(found, "aws:"+module+"/"+strings.ToLower(name[:1])+name[1:]+":"+name)
		}
		return true
	})
	return found
}

func TestEveryPulumiTypeAnAWSProgramDeclaresIsListedWithItsCloudFormationTypeOrItsOwnToken(t *testing.T) {
	t.Parallel()

	declared := listDeclaredPulumiTypes(t)
	if !slices.Contains(declared, "aws:lambda/function:Function") || !slices.Contains(declared, "ocel:index:AssetSet") {
		t.Fatalf("found %v declared, want at least the app functions and the asset set: the scan reads no program", declared)
	}
	for _, pulumiType := range declared {
		if _, listed := resourceTypes[pulumiType]; !listed {
			t.Errorf("%s is declared by an AWS program and resourceTypes lists no CloudFormation type for it", pulumiType)
		}
	}
	for pulumiType := range resourceTypes {
		if !slices.Contains(declared, pulumiType) {
			t.Errorf("resourceTypes lists %s, which no AWS program declares", pulumiType)
		}
	}
}

func TestAPulumiTypeWithACloudFormationEquivalentIsNamedByIt(t *testing.T) {
	t.Parallel()

	for pulumiType, want := range map[string]string{
		"aws:lambda/function:Function":            "AWS::Lambda::Function",
		"aws:lb/loadBalancer:LoadBalancer":        "AWS::ElasticLoadBalancingV2::LoadBalancer",
		"aws:cloudwatch/logGroup:LogGroup":        "AWS::Logs::LogGroup",
		"aws:s3/bucketV2:BucketV2":                "AWS::S3::Bucket",
		"aws:rds/clusterInstance:ClusterInstance": "AWS::RDS::DBInstance",
	} {
		if got := ResourceTypeOf(pulumiType); got != want {
			t.Errorf("ResourceTypeOf(%s) = %s, want %s", pulumiType, got, want)
		}
	}
}

func TestAPulumiTypeWithoutACloudFormationEquivalentKeepsItsOwnToken(t *testing.T) {
	t.Parallel()

	for _, pulumiType := range []string{"aws:iam/rolePolicyAttachment:RolePolicyAttachment", "ocel:index:PostgresRole", "aws:unknown/thing:Thing"} {
		if got := ResourceTypeOf(pulumiType); got != pulumiType {
			t.Errorf("ResourceTypeOf(%s) = %s, want the token itself", pulumiType, got)
		}
	}
}

func TestAPhysicalResourceOfAGlobalServiceIsLocatedGlobally(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"AWS::IAM::Role", "AWS::CloudFront::Distribution", "AWS::Route53::RecordSet", "aws:iam/rolePolicyAttachment:RolePolicyAttachment"} {
		if got := NewPhysicalResource(kind, "id", "eu-west-1"); got.Location != "global" {
			t.Errorf("NewPhysicalResource(%s).Location = %q, want global", kind, got.Location)
		}
	}
}

func TestAPhysicalResourceOfARegionalServiceIsLocatedInItsRegion(t *testing.T) {
	t.Parallel()

	got := NewPhysicalResource("AWS::Lambda::Function", "shop-prod-web", "eu-west-1")
	if got.Kind != "AWS::Lambda::Function" || got.ID != "shop-prod-web" || got.Location != "eu-west-1" {
		t.Errorf("NewPhysicalResource() = %+v, want the function located in eu-west-1", got)
	}
}
