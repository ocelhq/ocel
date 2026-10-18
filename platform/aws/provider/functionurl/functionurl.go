package functionurl

import "strings"

const (
	InvokeURLAction = "lambda:InvokeFunctionUrl"
	InvokeAction    = "lambda:InvokeFunction"

	AuthNone = "NONE"
	AuthIAM  = "AWS_IAM"
)

type Permission struct {
	Action      string
	URLAuthType string
	ViaURL      bool
}

func (p Permission) Verb() string { return strings.TrimPrefix(p.Action, "lambda:") }

func Public() []Permission {
	return []Permission{
		{Action: InvokeURLAction, URLAuthType: AuthNone},
		{Action: InvokeAction, ViaURL: true},
	}
}

func InvokerActions() []string { return []string{InvokeURLAction, InvokeAction} }
