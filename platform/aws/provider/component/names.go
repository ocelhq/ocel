package component

import (
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	MaxRoleNameLen   = 64
	MaxLambdaNameLen = 64

	AppRoleWord = "role"

	PartSeparator = "/"

	randomTagPlaceholder = "000000"
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

func PhysicalName(namespace string, at naming.Coordinate, limit int, childSuffixes ...string) (string, error) {
	tag := at.Release.String()
	if at.Release.IsZero() {
		tag = at.HashTag(namespace)
	}
	return Fit(at.AppResourceName(namespace, tag), at.Kind.Component()+" "+at.Name, limit, childSuffixes...)
}

func Fit(name naming.PhysicalName, what string, limit int, childSuffixes ...string) (string, error) {
	fitted, err := name.FitWithChildren(limit, childSuffixes...)
	if err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid, "the %s takes no AWS name within %d characters: %v", what, limit, err)
	}
	return fitted, nil
}

func RandomTaggedHead(namespace string, at naming.Coordinate, limit int, childSuffixes ...string) (string, error) {
	fitted, err := Fit(at.AppResourceName(namespace, randomTagPlaceholder), at.Kind.Component()+" "+at.Name, limit, childSuffixes...)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(fitted, naming.WordSeparator+randomTagPlaceholder), nil
}

func RandomTaggedName(ctx *pulumi.Context, namespace string, at naming.Coordinate, limit int, childSuffixes ...string) (Name, error) {
	head, err := RandomTaggedHead(namespace, at, limit, childSuffixes...)
	if err != nil {
		return Name{}, err
	}
	tag, err := NewRandomTag(ctx, head+PartSeparator+"tag")
	if err != nil {
		return Name{}, err
	}
	return Name{Pulumi: head, AWS: pulumi.Sprintf("%s%s%s", head, naming.WordSeparator, tag)}, nil
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
