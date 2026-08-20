package origin

type Kind string

type Role string

const (
	RoleOrigin   Role = "origin"
	RoleEdge     Role = "edge"
	RoleDNS      Role = "dns"
	RoleBucket   Role = "bucket"
	RolePostgres Role = "postgres"
)

type Reach string

const (
	ReachLink     Reach = "link"
	ReachMembrane Reach = "membrane"
)

var roleReach = map[Role]Reach{
	RoleBucket:   ReachMembrane,
	RolePostgres: ReachLink,
}

func ResourceRoles() []Role { return []Role{RoleBucket, RolePostgres} }

func ReachOf(role Role) (Reach, bool) {
	r, ok := roleReach[role]
	return r, ok
}

type Protocol string

const (
	ProtocolS3       Protocol = "s3"
	ProtocolGCS      Protocol = "gcs"
	ProtocolR2Bind   Protocol = "r2-binding"
	ProtocolPostgres Protocol = "postgres"
)

type Class string

const (
	ClassProduction Class = "production"
	ClassPreview    Class = "preview"
)
