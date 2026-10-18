package deploy

import (
	"context"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variables "github.com/ocelhq/ocel/platform/aws/provider/variables/live"
)

// TODO(rds-ca): refresh from https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem when AWS opens a region or issues a root.
//
//go:embed rdsrootca.pem
var rdsRootBundle []byte

type rdsRoot struct {
	commonName string
	block      *pem.Block
}

var rdsRoots = sync.OnceValues(func() ([]rdsRoot, error) {
	var roots []rdsRoot
	rest := rdsRootBundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return roots, nil
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the embedded RDS root bundle holds a block that is no certificate: %w", err)
		}
		roots = append(roots, rdsRoot{commonName: certificate.Subject.CommonName, block: block})
	}
})

func rdsRootCAs(region string) (string, error) {
	roots, err := rdsRoots()
	if err != nil {
		return "", err
	}
	prefix := "Amazon RDS " + region + " Root CA "
	var out strings.Builder
	for _, root := range roots {
		if strings.HasPrefix(root.commonName, prefix) {
			out.Write(pem.EncodeToMemory(root.block))
		}
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("ocel knows no RDS root CA for %s, so no app could verify the database it serves; this build of ocel predates the region", region)
	}
	return out.String(), nil
}

func collectPostgresBinding(ctx context.Context, secrets SecretsAPI, region, name string, fields map[string]any) (*bindingsv1.Binding, error) {
	host, err := requireStringField(fields, name, outputKeyHost)
	if err != nil {
		return nil, err
	}
	database, err := requireStringField(fields, name, outputKeyDatabase)
	if err != nil {
		return nil, err
	}
	username, err := requireStringField(fields, name, outputKeyUsername)
	if err != nil {
		return nil, err
	}
	secretARN, err := requireStringField(fields, name, outputKeySecretARN)
	if err != nil {
		return nil, err
	}

	port := PostgresPort
	if p, ok := fields[outputKeyPort].(float64); ok {
		port = int(p)
	}

	roots, err := rdsRootCAs(region)
	if err != nil {
		return nil, fmt.Errorf("postgres %s: %w", name, err)
	}

	password, err := readMasterPassword(ctx, secrets, secretARN)
	if err != nil {
		return nil, fmt.Errorf("read the master password of %s: %w", name, err)
	}

	return &bindingsv1.Binding{
		Name: name,
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host:     host,
			Port:     int32(port),
			Database: database,
			Username: username,
			Password: password,
			TlsMode:  bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL,
			TlsCa:    roots,
		}},
		Grants: postgresGrants(secretARN),
	}, nil
}

func postgresGrants(secretARN string) []*bindingsv1.Grant {
	return []*bindingsv1.Grant{{
		Label:     variables.PostgresPasswordGrant,
		Actions:   []string{"secretsmanager:GetSecretValue"},
		Resources: []string{secretARN},
	}}
}

func readMasterPassword(ctx context.Context, secrets SecretsAPI, secretARN string) (string, error) {
	if secrets == nil {
		return "", fmt.Errorf("no Secrets Manager client configured")
	}
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretARN})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", secretARN)
	}
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &parsed); err != nil {
		return "", fmt.Errorf("parse managed secret: %w", err)
	}
	return parsed.Password, nil
}
