package deploy

const maxPostgresIdentLen = 63

func sliceDatabaseName(identity, logicalName string) string {
	name := identity + "_" + logicalName
	if len(name) > maxPostgresIdentLen {
		name = name[:maxPostgresIdentLen]
	}
	return name
}
