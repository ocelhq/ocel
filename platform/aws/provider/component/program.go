package component

import (
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type Program struct {
	Run     func(*sdk.Context) error
	Tags    map[string]string
	Secrets []string
}

func RunProgram(pctx *sdk.Context, program Program) error { return program.Run(pctx) }
