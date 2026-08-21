package providerkit

type Resource struct {
	Name string
	Type LinkType

	Postgres  *PostgresSpec
	Bucket    *BucketSpec
	Container *ContainerSpec
	Custom    *CustomSpec
}

type PostgresSpec struct {
	Version string
}

type BucketSpec struct{}

type ContainerSpec struct {
	Image string
	Port  int
	Env   map[string]string
}

type CustomSpec struct {
	Type   string
	Config map[string]any
}

const (
	LinkPostgres LinkType = "postgres"
	LinkBucket   LinkType = "bucket"
	LinkCustom   LinkType = "custom"
)

func RequiredProperties(t LinkType) []string {
	switch t {
	case LinkPostgres:
		return []string{"host", "port", "database", "username", "password"}
	case LinkBucket:
		return []string{"name"}
	}
	return nil
}
