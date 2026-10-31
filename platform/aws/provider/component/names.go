package component

import (
	"path"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	MaxRoleNameLen   = 64
	MaxLambdaNameLen = 64

	AppRoleWord = "role"

	PartSeparator = "/"
)

type Name struct {
	Pulumi string
	AWS    pulumi.StringInput
}

func NameOf(physical string) Name {
	return Name{Pulumi: physical, AWS: pulumi.String(physical)}
}

func (n Name) Child(suffix string) Name {
	return Name{Pulumi: n.Pulumi + naming.WordSeparator + suffix, AWS: pulumi.Sprintf("%s%s%s", n.AWS, naming.WordSeparator, suffix)}
}

func (n Name) Part(part string) string {
	return n.Pulumi + PartSeparator + part
}

func (n Name) Attachment(policyARN string) string {
	return n.Part(path.Base(policyARN))
}

func AppRoleCoordinate(project string, stack naming.StackName) naming.Coordinate {
	return naming.Coordinate{
		Project: project,
		Env:     stack.Env,
		App:     stack.App,
		Kind:    naming.KindRole,
		Name:    AppRoleWord,
		Release: stack.Release,
	}
}

func RoleName(namespace string, at naming.Coordinate) (Name, error) {
	physical, err := at.TaggedResourceName(namespace).Fit(MaxRoleNameLen)
	return NameOf(physical), err
}

func UntaggedName(namespace string, at naming.Coordinate, limit int, childSuffixes ...string) (string, error) {
	return at.AppResourceName(namespace, "").FitUntagged(limit, childSuffixes...)
}

func RandomTaggedName(ctx *pulumi.Context, untagged string) (Name, error) {
	tag, err := NewRandomTag(ctx, untagged+PartSeparator+"tag")
	if err != nil {
		return Name{}, err
	}
	return Name{Pulumi: untagged, AWS: pulumi.Sprintf("%s%s%s", untagged, naming.WordSeparator, tag)}, nil
}

func AccountOfARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

func ResourceCoordinate(project, env, logicalName string, kind naming.Kind) naming.Coordinate {
	name := logicalName
	if _, local, ok := strings.Cut(logicalName, naming.FieldSeparator); ok {
		name = local
	}
	return naming.Coordinate{Project: project, Env: env, App: naming.InfraApp, Kind: kind, Name: name}
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
