package providerserver

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const (
	postgresAttempts       = 4
	postgresAttemptTimeout = 10 * time.Second
	postgresBackoff        = 500 * time.Millisecond
	postgresBackoffCeiling = 4 * time.Second
)

var postgresTLSModes = map[bindingsv1.PostgresTlsMode]string{
	bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE:     "require",
	bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL: "verify-full",
}

func readPostgresVersion(ctx context.Context, props *bindingsv1.PostgresProperties) (int, error) {
	config, err := postgresConfig(props)
	if err != nil {
		return 0, err
	}
	config.ConnectTimeout = postgresAttemptTimeout
	for attempt := 1; ; attempt++ {
		version, err := queryPostgresVersion(ctx, config)
		if err == nil || attempt == postgresAttempts || !isTransientPostgres(ctx, err) {
			return version, err
		}
		select {
		case <-ctx.Done():
			return 0, err
		case <-time.After(postgresWait(attempt)):
		}
	}
}

func postgresWait(attempt int) time.Duration {
	ceiling := min(postgresBackoff<<(attempt-1), postgresBackoffCeiling)
	return ceiling/2 + rand.N(ceiling/2+1)
}

func queryPostgresVersion(ctx context.Context, config *pgconn.Config) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, postgresAttemptTimeout)
	defer cancel()

	conn, err := pgconn.ConnectConfig(ctx, config)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close(context.Background()) }()

	results, err := conn.Exec(ctx, "SELECT 1, current_setting('server_version_num')").ReadAll()
	if err != nil {
		return 0, err
	}
	if len(results) != 1 || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 2 {
		return 0, errors.New("SELECT 1 answered with no row")
	}
	version, err := strconv.Atoi(string(results[0].Rows[0][1]))
	if err != nil {
		return 0, fmt.Errorf("server_version_num is %q, which is no version number", results[0].Rows[0][1])
	}
	return version, nil
}

func isTransientPostgres(ctx context.Context, err error) bool {
	var server *pgconn.PgError
	if errors.As(err, &server) {
		return strings.HasPrefix(server.Code, "53") || server.Code == "57P03"
	}
	if ctx.Err() != nil {
		return false
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout())
}

func postgresConfig(props *bindingsv1.PostgresProperties) (*pgconn.Config, error) {
	if props.GetUrl() != "" {
		return pgconn.ParseConfig(props.GetUrl())
	}
	query := url.Values{}
	if mode := postgresTLSModes[props.GetTlsMode()]; mode != "" {
		query.Set("sslmode", mode)
	}
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(props.GetUsername(), props.GetPassword()),
		Host:     net.JoinHostPort(props.GetHost(), strconv.Itoa(int(props.GetPort()))),
		Path:     "/" + props.GetDatabase(),
		RawQuery: query.Encode(),
	}
	config, err := pgconn.ParseConfig(dsn.String())
	if err != nil {
		return nil, err
	}
	if props.GetTlsCa() != "" && config.TLSConfig != nil {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(props.GetTlsCa())) {
			return nil, errors.New("the CA contains no PEM certificate")
		}
		config.TLSConfig.RootCAs = roots
		for _, fallback := range config.Fallbacks {
			if fallback.TLSConfig != nil {
				fallback.TLSConfig.RootCAs = roots
			}
		}
	}
	return config, nil
}

func majorOf(serverVersionNum int) string {
	if serverVersionNum >= 100000 {
		return strconv.Itoa(serverVersionNum / 10000)
	}
	return strconv.Itoa(serverVersionNum/10000) + "." + strconv.Itoa(serverVersionNum/100%100)
}

func declaredMajor(declared string) string {
	parts := strings.Split(strings.TrimSpace(declared), ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return ""
	}
	if major >= 10 || len(parts) < 2 {
		return strconv.Itoa(major)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}
