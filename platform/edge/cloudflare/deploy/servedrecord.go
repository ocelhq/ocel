package cloudflare

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/router"
)

type servedRecord struct {
	router.ReleaseRecord
	Edge servedEdge `json:"edge"`
}

type servedEdge struct {
	Code        *servedCode       `json:"code,omitempty"`
	PurgeSecret string            `json:"purgeSecret,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Envelope    string            `json:"envelope,omitempty"`
}

type servedCode struct {
	BundleKey   string   `json:"bundleKey"`
	LoaderID    string   `json:"loaderId"`
	CompatDate  string   `json:"compatDate"`
	CompatFlags []string `json:"compatFlags"`
}

func newServedRecord(record router.ReleaseRecord, envelope string) servedRecord {
	secrets := record.Secrets
	record.Secrets, record.Sealed = router.ReleaseSecrets{}, nil
	served := servedRecord{ReleaseRecord: record, Edge: servedEdge{PurgeSecret: secrets.PurgeSecret, Env: secrets.Env, Envelope: envelope}}
	if bundle := record.EdgeBundle; bundle != nil {
		served.Edge.Code = &servedCode{
			BundleKey:   bundle.Key,
			LoaderID:    loaderID(bundle.Digest, compatDate, compatFlags),
			CompatDate:  compatDate,
			CompatFlags: slices.Clone(compatFlags),
		}
	}
	return served
}

func loaderID(bundleDigest, compatDate string, compatFlags []string) string {
	sum := sha256.New()
	sum.Write([]byte(bundleDigest))
	sum.Write([]byte{0})
	sum.Write([]byte(compatDate))
	sum.Write([]byte{0})
	sum.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(sum.Sum(nil))
}
