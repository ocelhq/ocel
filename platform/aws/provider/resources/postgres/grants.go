package postgres

import (
	_ "embed"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func grants(secretARN string) []*variablestorev1.Grant {
	return []*variablestorev1.Grant{{
		Label:     provider.GrantPasswordSecret,
		Actions:   []string{"secretsmanager:GetSecretValue"},
		Resources: []string{secretARN},
	}}
}

const (
	widestMasterSecretARN = "arn:aws:secretsmanager:ap-southeast-5:123456789012:secret:rds!cluster-01234567-89ab-cdef-0123-456789abcdef-AbCdEf"
)
