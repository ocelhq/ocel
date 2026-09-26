package inlinebinding

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const probeTimeout = 15 * time.Second

func ProbePostgres(ctx context.Context, props *bindingsv1.PostgresProperties) (int, error) {
	config, err := ConnConfig(props)
	if err != nil {
		return 0, err
	}
	config.ConnectTimeout = probeTimeout
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
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

func ConnConfig(props *bindingsv1.PostgresProperties) (*pgconn.Config, error) {
	if props.GetUrl() != "" {
		return pgconn.ParseConfig(props.GetUrl())
	}
	query := url.Values{}
	if mode := sslmode(props.GetTlsMode()); mode != "" {
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
