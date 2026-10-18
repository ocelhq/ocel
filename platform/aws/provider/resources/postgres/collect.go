package postgres

import (
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
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

func collectBinding(region, name string, fields map[string]any) (*variablestorev1.PublishedBinding, error) {
	host, err := component.RequireStringField(fields, name, component.OutputKeyHost)
	if err != nil {
		return nil, err
	}
	database, err := component.RequireStringField(fields, name, OutputKeyDatabase)
	if err != nil {
		return nil, err
	}
	username, err := component.RequireStringField(fields, name, OutputKeyUsername)
	if err != nil {
		return nil, err
	}
	clusterARN, err := component.RequireStringField(fields, name, OutputKeyClusterARN)
	if err != nil {
		return nil, err
	}
	resourceID, err := component.RequireStringField(fields, name, OutputKeyResourceID)
	if err != nil {
		return nil, err
	}
	account := component.AccountOfARN(clusterARN)
	if account == "" {
		return nil, fmt.Errorf("postgres %s: the cluster ARN %q names no account, so no app could be granted its database user", name, clusterARN)
	}

	port := Port
	if p, ok := fields[component.OutputKeyPort].(float64); ok {
		port = int(p)
	}

	roots, err := rdsRootCAs(region)
	if err != nil {
		return nil, fmt.Errorf("postgres %s: %w", name, err)
	}

	return &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{
			Name: name,
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
				Host:     host,
				Port:     int32(port),
				Database: database,
				Username: username,
				TlsMode:  bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL,
				TlsCa:    roots,
			}},
		},
		Grants: grants(dbUserARN(region, account, resourceID, username)),
	}, nil
}
