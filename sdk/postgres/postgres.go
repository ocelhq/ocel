// Package postgres declares the postgres databases an app uses and connects
// to them over the binding the deploy delivers.
package postgres

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ocel.dev"
	"ocel.dev/internal/binding"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const defaultPostgresVersion = "17"

// An Option tunes the database [New] declares.
type Option interface {
	applyPostgres(*resourcesv1.PostgresConfig)
}

type option func(*resourcesv1.PostgresConfig)

func (o option) applyPostgres(c *resourcesv1.PostgresConfig) { o(c) }

// Version declares the major postgres version to provision, "17" by default.
func Version(v string) Option {
	return option(func(c *resourcesv1.PostgresConfig) { c.Version = v })
}

// A DB is a postgres database an app declares and reads its binding from.
type DB struct {
	name string

	connection binding.Connection[*pgxpool.Pool]
}

// New declares a postgres database named name and returns the handle an app
// reads it through. Call it from a file under the project's discovery folder: during
// discovery the call is the declaration, and at runtime it reads the binding the
// deploy delivered for that name.
func New(name string, opts ...Option) *DB {
	config := &resourcesv1.PostgresConfig{Version: defaultPostgresVersion}
	for _, opt := range opts {
		opt.applyPostgres(config)
	}
	binding.Declare(binding.CallSite(0), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: name},
		Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: config},
	})
	return &DB{name: name}
}

// Name is the name the database was declared under.
func (p *DB) Name() string { return p.name }

// ConnectionString is the postgres URL of the delivered binding: the record's url
// verbatim when it has one, and otherwise one built from its host, port,
// database and credentials, percent-encoded, with its tls mode as sslmode. It
// fails when no binding was delivered for the name, and during discovery.
func (p *DB) ConnectionString() (string, error) {
	properties, err := p.properties("ConnectionString")
	if err != nil {
		return "", err
	}
	return connectionString(properties), nil
}

var sslmodes = map[bindingsv1.PostgresTlsMode]string{
	bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE:     "require",
	bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL: "verify-full",
}

func connectionString(properties *bindingsv1.PostgresProperties) string {
	if properties.GetUrl() != "" {
		return properties.GetUrl()
	}
	query := url.Values{}
	if mode := sslmodes[properties.GetTlsMode()]; mode != "" {
		query.Set("sslmode", mode)
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
// verify-full that names a CA trusts that CA for the server's certificate, and a
// record that names a TLS server name, as one pointing at a port forward does,
// verifies the certificate against that name instead of the host. The pool
// is opened on the first call and the same pool is returned on every one after,
// and each connection it opens presents the password the binding holds then, so
// a rotated credential reaches the pool without a restart. It
// fails when no binding was delivered for the name, and during discovery.
func (p *DB) Pool(ctx context.Context) (*pgxpool.Pool, error) {
	return p.connection.Connect(p.resource(), "Pool", func() (*pgxpool.Pool, error) {
		properties, err := p.properties("Pool")
		if err != nil {
			return nil, err
		}
		config, err := pgxpool.ParseConfig(connectionString(properties))
		if err != nil {
			return nil, err
		}
		if err := trustCA(config, properties.GetTlsCa()); err != nil {
			return nil, err
		}
		setTLSServerName(config, properties.GetTlsServerName())
		config.BeforeConnect = p.presentCurrentPassword
		return pgxpool.NewWithConfig(ctx, config)
	})
}

func (p *DB) presentCurrentPassword(_ context.Context, next *pgx.ConnConfig) error {
	properties, err := p.properties("Pool")
	if err != nil {
		return err
	}
	current, err := pgx.ParseConfig(connectionString(properties))
	if err != nil {
		return err
	}
	next.Password = current.Password
	return nil
}

func trustCA(config *pgxpool.Config, ca string) error {
	if ca == "" || config.ConnConfig.TLSConfig == nil {
		return nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca)) {
		return errors.New("ocel: the postgres binding's CA contains no PEM certificate")
	}
	config.ConnConfig.TLSConfig.RootCAs = roots
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.TLSConfig != nil {
			fallback.TLSConfig.RootCAs = roots
		}
	}
	return nil
}

func setTLSServerName(config *pgxpool.Config, name string) {
	if name == "" {
		return
	}
	if config.ConnConfig.TLSConfig != nil {
		config.ConnConfig.TLSConfig.ServerName = name
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.TLSConfig != nil {
			fallback.TLSConfig.ServerName = name
		}
	}
}

func (p *DB) properties(access string) (*bindingsv1.PostgresProperties, error) {
	if binding.Discovering() {
		return nil, &ocel.UnprovisionedError{Resource: p.resource(), Access: access}
	}
	delivered, err := binding.Read(p.name, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
	if err != nil {
		return nil, err
	}
	return delivered.GetPostgres(), nil
}

func (p *DB) resource() string {
	return fmt.Sprintf("postgres(%q)", p.name)
}
