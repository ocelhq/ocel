package postgres

import (
	"github.com/ocelhq/ocel/pkg/environment"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
)

func widestDBUserARN(tier environment.Tier) string {
	return "arn:aws:rds-db:ap-southeast-5:123456789012:dbuser:cluster-ABCDEFGHIJKLMNOPQRSTUVWXYZ/" + signin.AppRoleFor(tier)
}

func dbUserARN(region, account, resourceID, username string) string {
	return "arn:aws:rds-db:" + region + ":" + account + ":dbuser:" + resourceID + "/" + username
}

func grants(dbUser string) []*variablestorev1.Grant {
	return []*variablestorev1.Grant{{
		Label:     provider.GrantIAMAuthentication,
		Actions:   []string{"rds-db:connect"},
		Resources: []string{dbUser},
	}}
}
