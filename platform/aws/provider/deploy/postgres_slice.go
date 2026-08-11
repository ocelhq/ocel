package deploy

import "github.com/ocelhq/ocel/pkg/naming"

const maxPostgresIdentLen = 63

func sliceDatabaseName(at naming.Coordinate) string {
	ident := at
	ident.Project = naming.SanitizeAlpha(at.Project)
	return naming.Underscore(ident.PhysicalName(maxPostgresIdentLen))
}
