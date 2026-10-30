package signin

import "github.com/ocelhq/ocel/pkg/environment"

const appRolePrefix = "ocel_app_"

func AppRoleFor(tier environment.Tier) string { return appRolePrefix + string(tier) }
