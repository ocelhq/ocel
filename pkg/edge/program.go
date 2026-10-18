package edge

import (
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
	ReachSigV4             ReachKind = "sigv4"
	ReachClientCertificate ReachKind = "client-certificate"
)

type Reach struct {
	Kind            ReachKind `json:"kind"`
	Service         string    `json:"service,omitempty"`
	Region          string    `json:"region,omitempty"`
	AccessKeyID     string    `json:"accessKeyId,omitempty"`
	SecretAccessKey string    `json:"secretAccessKey,omitempty"`
	Certificate     string    `json:"certificate,omitempty"`
}

type OriginStore struct {
	URL             string `json:"url"`
	Prefix          string `json:"prefix,omitempty"`
	Region          string `json:"region"`
	HoldsEntries    bool   `json:"holdsEntries"`
	EntriesPrefix   string `json:"entriesPrefix,omitempty"`
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
		if o.Reach.Certificate != "" {
			errs = append(errs, fmt.Errorf("the origin is reached by signing requests with %s and names a client certificate too; an origin is reached one way", ReachSigV4))
		}
	case ReachClientCertificate:
		if o.Reach.Certificate == "" {
			errs = append(errs, errors.New("the origin is reached by a client certificate, and it names none"))
		}
		if o.Reach.AccessKeyID != "" || o.Reach.SecretAccessKey != "" {
			errs = append(errs, errors.New("the origin is reached by a client certificate and names signing keys too; an origin is reached one way"))
		}
	case "":
		errs = append(errs, errUnnamedReach)
	default:
		errs = append(errs, fmt.Errorf("the origin is reached by %q, which no edge knows", o.Reach.Kind))
	}
	if store := o.Store; store != nil && (store.URL == "" || store.Region == "" || store.AccessKeyID == "" || store.SecretAccessKey == "") {
		errs = append(errs, errors.New("the origin names a store for the edge to read, without its URL, region and key"))
	}
	if store := o.Store; store != nil && store.EntriesPrefix != "" && !store.HoldsEntries {
		errs = append(errs, errors.New("the origin names where its store keeps cached entries, and says the store holds none"))
	}
	if o.Throttle.Status == 0 {
		errs = append(errs, errors.New("the origin names no status its throttled answers carry, so the edge could not tell a throttle from the app's own answer"))
	}
	return errors.Join(errs...)
}
