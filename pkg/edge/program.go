package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const OriginReachVar = "OCEL_ORIGIN"

type Program struct {
	Origin            OriginReach
	ReleasesStore     ReleasesStore
	PreviewBaseDomain string
	PreviewKey        PreviewKey
}

type ReleasesStore struct {
	Name                string
	Endpoint            string
	BootstrapCredential string
}

type OriginReach struct {
	Reach    Reach         `json:"reach"`
	Store    *OriginStore  `json:"store,omitempty"`
	Throttle Throttle      `json:"throttle"`
	Images   *OriginImages `json:"images,omitempty"`
}

type ReachKind string

const (
	ReachSigV4        ReachKind = "sigv4"
	ReachOriginSecret ReachKind = "origin-secret"
)

type Reach struct {
	Kind                ReachKind `json:"kind"`
	Service             string    `json:"service,omitempty"`
	Region              string    `json:"region,omitempty"`
	AccessKeyID         string    `json:"accessKeyId,omitempty"`
	SecretAccessKey     string    `json:"secretAccessKey,omitempty"`
	Secret              string    `json:"secret,omitempty"`
	PreviousSecret      string    `json:"previousSecret,omitempty"`
	PreviousFingerprint string    `json:"previousFingerprint,omitempty"`
}

const originSecretFingerprintBytes = 8

func OriginSecretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:originSecretFingerprintBytes])
}

func (r Reach) listNamedKinds() []ReachKind {
	var kinds []ReachKind
	if r.Service != "" || r.Region != "" || r.AccessKeyID != "" || r.SecretAccessKey != "" {
		kinds = append(kinds, ReachSigV4)
	}
	if r.Secret != "" || r.PreviousSecret != "" || r.PreviousFingerprint != "" {
		kinds = append(kinds, ReachOriginSecret)
	}
	return kinds
}

type OriginStore struct {
	URL             string `json:"url"`
	Region          string `json:"region"`
	HoldsEntries    bool   `json:"holdsEntries"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

type Throttle struct {
	Status       int    `json:"status"`
	Header       string `json:"header,omitempty"`
	BodyContains string `json:"bodyContains,omitempty"`
}

type OriginImages struct {
	OptimizerURL    string `json:"optimizerUrl,omitempty"`
	MaxRequestBytes int64  `json:"maxRequestBytes,omitempty"`
}

var errUnnamedReach = errors.New("the origin names no way for the edge to reach it")

func (o OriginReach) RefuseIncomplete() error {
	var errs []error
	switch o.Reach.Kind {
	case ReachSigV4:
		if o.Reach.Service == "" || o.Reach.Region == "" || o.Reach.AccessKeyID == "" || o.Reach.SecretAccessKey == "" {
			errs = append(errs, fmt.Errorf("the origin is reached by signing requests with %s, and it names no service, region and key to sign them with", ReachSigV4))
		}
	case ReachOriginSecret:
		if o.Reach.Secret == "" {
			errs = append(errs, errors.New("the origin is reached by presenting its origin secret, and it names none"))
		}
		if o.Reach.PreviousSecret != "" && o.Reach.PreviousFingerprint != OriginSecretFingerprint(o.Reach.PreviousSecret) ||
			o.Reach.PreviousSecret == "" && o.Reach.PreviousFingerprint != "" {
			errs = append(errs, errors.New("the origin names a previous origin secret without its fingerprint, so the edge could not tell which releases hold it"))
		}
	case "":
		errs = append(errs, errUnnamedReach)
	default:
		errs = append(errs, fmt.Errorf("the origin is reached by %q, which no edge knows", o.Reach.Kind))
	}
	if o.Reach.Kind != "" {
		for _, named := range o.Reach.listNamedKinds() {
			if named != o.Reach.Kind {
				errs = append(errs, fmt.Errorf("the origin is reached by %s and names what %s reaches it with too; an origin is reached one way", o.Reach.Kind, named))
			}
		}
	}
	if store := o.Store; store != nil && (store.URL == "" || store.Region == "" || store.AccessKeyID == "" || store.SecretAccessKey == "") {
		errs = append(errs, errors.New("the origin names a store for the edge to read, without its URL, region and key"))
	}
	if o.Throttle.Status == 0 {
		errs = append(errs, errors.New("the origin names no status its throttled answers carry, so the edge could not tell a throttle from the app's own answer"))
	}
	return errors.Join(errs...)
}
