package ocel

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

const defaultPostgresVersion = "17"

// A PostgresOption tunes the database [Postgres] declares.
type PostgresOption func(*resourcesv1.PostgresConfig)

// PostgresVersion declares the major postgres version to provision, "17" by default.
func PostgresVersion(v string) PostgresOption {
	return func(c *resourcesv1.PostgresConfig) { c.Version = v }
}

// A PostgresDB is a postgres database an app declares and reads its link from.
type PostgresDB struct {
	name string

	once sync.Once
	pool *pgxpool.Pool
	err  error
}

// Postgres declares a postgres database named name and returns the handle an app
// reads it through. Call it from a file under the project's discovery folder: during
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

// ConnectionString is the postgres URL of the delivered link, with the
// credentials percent-encoded. It fails when no link was delivered for the
// name, and during discovery.
func (p *PostgresDB) ConnectionString() (string, error) {
	properties, err := p.properties("ConnectionString")
	if err != nil {
		return "", err
	}
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(properties.GetUsername(), properties.GetPassword()),
		Host:   net.JoinHostPort(properties.GetHost(), strconv.Itoa(int(properties.GetPort()))),
		Path:   "/" + properties.GetDatabase(),
	}
	return dsn.String(), nil
}

// Pool is the connection pool over the delivered link. It is opened on the first
// call and the same pool is returned on every one after. It fails when no link
// was delivered for the name, and during discovery.
func (p *PostgresDB) Pool(ctx context.Context) (*pgxpool.Pool, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: p.resource(), Access: "Pool"}
	}
	p.once.Do(func() {
		dsn, err := p.ConnectionString()
		if err != nil {
			p.err = err
			return
		}
		p.pool, p.err = pgxpool.New(ctx, dsn)
	})
	return p.pool, p.err
}

func (p *PostgresDB) properties(access string) (*linksv1.PostgresProperties, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: p.resource(), Access: access}
	}
	delivered, err := link(p.name, linksv1.LinkType_LINK_TYPE_POSTGRES)
	if err != nil {
		return nil, err
	}
	return delivered.GetPostgres(), nil
}

func (p *PostgresDB) resource() string {
	return fmt.Sprintf("postgres(%q)", p.name)
}
