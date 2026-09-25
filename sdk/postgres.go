package ocel

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const defaultPostgresVersion = "17"

// A PostgresOption tunes the database [Postgres] declares.
type PostgresOption func(*resourcesv1.PostgresConfig)

// PostgresVersion declares the major postgres version to provision, "17" by default.
func PostgresVersion(v string) PostgresOption {
	return func(c *resourcesv1.PostgresConfig) { c.Version = v }
}

// A PostgresDB is a postgres database an app declares and reads its binding from.
type PostgresDB struct {
	name string

	once sync.Once
	pool *pgxpool.Pool
	err  error
}

// Postgres declares a postgres database named name and returns the handle an app
// reads it through. Call it from a file under the project's discovery folder: during
// discovery the call is the declaration, and at runtime it reads the binding the
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
				Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
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

// ConnectionString is the postgres URL of the delivered binding: the record's url
// verbatim when it carries one, and otherwise one built from its host, port,
// database and credentials, percent-encoded, with its tls mode as sslmode. It
// fails when no binding was delivered for the name, and during discovery.
func (p *PostgresDB) ConnectionString() (string, error) {
	properties, err := p.properties("ConnectionString")
	if err != nil {
		return "", err
	}
	return connectionString(properties), nil
}

func connectionString(properties *bindingsv1.PostgresProperties) string {
	if properties.GetUrl() != "" {
		return properties.GetUrl()
	}
	query := url.Values{}
	if properties.GetTlsMode() != "" {
		query.Set("sslmode", properties.GetTlsMode())
	}
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(properties.GetUsername(), properties.GetPassword()),
		Host:     net.JoinHostPort(properties.GetHost(), strconv.Itoa(int(properties.GetPort()))),
		Path:     "/" + properties.GetDatabase(),
		RawQuery: query.Encode(),
	}
	return dsn.String()
}

// Pool is the connection pool over the delivered binding. A record under
// verify-full that names a CA trusts that CA for the server's certificate. The pool
// is opened on the first call and the same pool is returned on every one after. It
// fails when no binding was delivered for the name, and during discovery.
func (p *PostgresDB) Pool(ctx context.Context) (*pgxpool.Pool, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: p.resource(), Access: "Pool"}
	}
	p.once.Do(func() {
		properties, err := p.properties("Pool")
		if err != nil {
			p.err = err
			return
		}
		config, err := pgxpool.ParseConfig(connectionString(properties))
		if err != nil {
			p.err = err
			return
		}
		if err := trustCA(config, properties.GetTlsCa()); err != nil {
			p.err = err
			return
		}
		p.pool, p.err = pgxpool.NewWithConfig(ctx, config)
	})
	return p.pool, p.err
}

func trustCA(config *pgxpool.Config, ca string) error {
	if ca == "" || config.ConnConfig.TLSConfig == nil {
		return nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca)) {
		return errors.New("ocel: the postgres binding's CA holds no PEM certificate")
	}
	config.ConnConfig.TLSConfig.RootCAs = roots
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.TLSConfig != nil {
			fallback.TLSConfig.RootCAs = roots
		}
	}
	return nil
}

func (p *PostgresDB) properties(access string) (*bindingsv1.PostgresProperties, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: p.resource(), Access: access}
	}
	delivered, err := binding(p.name, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
	if err != nil {
		return nil, err
	}
	return delivered.GetPostgres(), nil
}

func (p *PostgresDB) resource() string {
	return fmt.Sprintf("postgres(%q)", p.name)
}
