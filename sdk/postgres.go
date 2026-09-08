package sdk

import (
	"fmt"
	"runtime"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

const defaultPostgresVersion = "17"

// A PostgresOption tunes the database [Postgres] declares.
type PostgresOption func(*resourcesv1.PostgresConfig)

// Version declares the major postgres version to provision, "17" by default.
func Version(v string) PostgresOption {
	return func(c *resourcesv1.PostgresConfig) { c.Version = v }
}

// A PostgresDB is a postgres database an app declares and reads its link from.
type PostgresDB struct {
	name string
}

// Postgres declares a postgres database named name and returns the handle an app
// reads it through. Call it from a file under the project's infra folder: during
// discovery the call is the declaration, and at runtime it reads the link the
// deploy delivered for that name.
func Postgres(name string, opts ...PostgresOption) *PostgresDB {
	if discovering() {
		config := &resourcesv1.PostgresConfig{Version: defaultPostgresVersion}
		for _, opt := range opts {
			opt(config)
		}
		_, file, line, _ := runtime.Caller(1)
		err := declare(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{
				Type: linksv1.LinkType_LINK_TYPE_POSTGRES,
				Name: name,
			},
			Config: &resourcesv1.DeclareRequest_Postgres{Postgres: config},
			Source: fmt.Sprintf("%s:%d", file, line),
		})
		if err != nil {
			panic(fmt.Sprintf("ocel: declare postgres %q: %v", name, err))
		}
	}
	return &PostgresDB{name: name}
}

// Name is the name the database was declared under.
func (p *PostgresDB) Name() string { return p.name }
