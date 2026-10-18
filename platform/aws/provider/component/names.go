package component

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	MaxRoleNameLen          = 64
	IAMAutonameSuffixLen    = 26
	MaxRolePrefixLen        = MaxRoleNameLen - IAMAutonameSuffixLen
	MaxLambdaNameLen        = 64
	LambdaAutonameSuffixLen = 8
	MaxLambdaBaseNameLen    = MaxLambdaNameLen - LambdaAutonameSuffixLen
)

func AccountOfARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

func RolePrefix(coord naming.Coordinate) string {
	return coord.PhysicalName(MaxRolePrefixLen-len(naming.WordSeparator)) + naming.WordSeparator
}

func ResourceCoordinate(project, env, logicalName string, kind naming.Kind) naming.Coordinate {
	name := logicalName
	if _, local, ok := strings.Cut(logicalName, naming.FieldSeparator); ok {
		name = local
	}
	return naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: kind, Name: name}
}

func AppPhysicalPrefix(at naming.Coordinate, max int) string {
	scope := awsports.AppScope + naming.WordSeparator
	return scope + at.PhysicalPrefix(max-len(scope))
}

func OptionalARN(arn string) pulumi.StringPtrInput {
	if arn == "" {
		return nil
	}
	return pulumi.StringPtr(arn)
}

func JoinARN(arn pulumi.StringOutput, suffix string) pulumi.StringInput {
	return arn.ApplyT(func(a string) string { return a + suffix }).(pulumi.StringOutput)
}
