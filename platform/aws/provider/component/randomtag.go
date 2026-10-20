package component

import (
	"context"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
)

const RandomTagToken = "ocel:index:RandomTag"

type randomTag struct{}

type randomTagArgs struct{}

type randomTagOutputs struct {
	Tag string `pulumi:"tag"`
}

func RandomTagResource() infer.InferredResource { return infer.Resource(&randomTag{}) }

func (*randomTag) Annotate(a infer.Annotator) { a.SetToken("index", "RandomTag") }

func (*randomTag) Create(_ context.Context, req infer.CreateRequest[randomTagArgs]) (infer.CreateResponse[randomTagOutputs], error) {
	if req.DryRun {
		return infer.CreateResponse[randomTagOutputs]{}, nil
	}
	tag := naming.NewRandomTag()
	return infer.CreateResponse[randomTagOutputs]{ID: tag, Output: randomTagOutputs{Tag: tag}}, nil
}

type randomTagState struct {
	pulumi.CustomResourceState
	Tag pulumi.StringOutput `pulumi:"tag"`
}

func NewRandomTag(ctx *pulumi.Context, name string) (pulumi.StringOutput, error) {
	var state randomTagState
	if err := ctx.RegisterResource(RandomTagToken, name, pulumi.Map{}, &state); err != nil {
		return pulumi.StringOutput{}, err
	}
	return state.Tag, nil
}
