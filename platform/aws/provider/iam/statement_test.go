package iam

import (
	"encoding/json"
	"testing"
)

func TestAnAssumeRolePolicyTrustsTheServiceAloneAndNamesNoResource(t *testing.T) {
	t.Parallel()

	want := `{"Statement":[{"Action":"sts:AssumeRole","Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"}}],"Version":"2012-10-17"}`
	if got := AssumeRolePolicy(LambdaPrincipal); got != want {
		t.Errorf("AssumeRolePolicy = %s, want %s", got, want)
	}
}

type literal string

func (l literal) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"Ref": string(l)})
}

func TestAStatementRendersItsSidPrincipalAndTheResourceValuesBesideItsARNs(t *testing.T) {
	t.Parallel()

	rendered, err := json.Marshal(Statements([]Statement{
		{
			Sid:            "Read",
			Principal:      Principal{AWS: literal("Root")},
			Actions:        []string{"s3:GetObject", "s3:PutObject"},
			Resources:      []string{"arn:aws:s3:::one/*"},
			ResourceValues: []any{literal("Bucket")},
		},
		{Actions: []string{"kms:Decrypt"}, ResourceValues: []any{literal("Key")}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"Action":["s3:GetObject","s3:PutObject"],"Effect":"Allow","Principal":{"AWS":{"Ref":"Root"}},"Resource":["arn:aws:s3:::one/*",{"Ref":"Bucket"}],"Sid":"Read"},` +
		`{"Action":"kms:Decrypt","Effect":"Allow","Resource":{"Ref":"Key"}}]`
	if string(rendered) != want {
		t.Errorf("Statements = %s, want %s", rendered, want)
	}
}
