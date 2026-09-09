package ocel

import (
	"fmt"
	"os"
	"strings"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// An UnprovisionedError is returned by every accessor of a resource during
// discovery, the pass that reads declarations before anything stands. Match it
// with errors.As to keep a boot path alive when the resource is optional there.
type UnprovisionedError struct {
	// Resource is the declaration the accessor belongs to, as written in code.
	Resource string
	// Access is the accessor that was called.
	Access string
}

// Error reports which accessor reached for a resource that discovery has not
// provisioned.
func (e *UnprovisionedError) Error() string {
	return fmt.Sprintf(
		"'%s' cannot be used during discovery: tried to access '%s' before the resource was provisioned",
		e.Resource, e.Access,
	)
}

// A MissingLinkError is returned by every accessor of a resource whose link was
// never delivered to the process. Match it with errors.As to tell a resource
// this deploy does not carry from one that is misconfigured.
type MissingLinkError struct {
	// Key is the environment variable the link arrives in.
	Key string
}

// Error names the key and the commands that deliver a link into it.
func (e *MissingLinkError) Error() string {
	return fmt.Sprintf(
		"Value for %s is not defined. Run `ocel dev` to resolve it locally, "+
			"or `ocel deploy` to have it delivered from the resource this app links.",
		e.Key,
	)
}

func linkKey(name string, typ linksv1.LinkType) string {
	return fmt.Sprintf("OCEL_RESOURCE_%s_%s", kindOf(typ), name)
}

func kindOf(typ linksv1.LinkType) string {
	return strings.TrimPrefix(typ.String(), "LINK_TYPE_")
}

func link(name string, typ linksv1.LinkType) (*linksv1.Link, error) {
	key := linkKey(name, typ)
	raw := os.Getenv(key)
	if raw == "" {
		return nil, &MissingLinkError{Key: key}
	}

	delivered := &linksv1.Link{}
	if err := protojson.Unmarshal([]byte(raw), delivered); err != nil {
		return nil, fmt.Errorf("%s does not carry a link record, so this app cannot read it as a %s", key, kindOf(typ))
	}
	if got := typeOf(delivered); got != typ {
		return nil, fmt.Errorf("%s carries a %s link, and this app reads it as a %s", key, kindOf(got), kindOf(typ))
	}
	return delivered, nil
}

func typeOf(delivered *linksv1.Link) linksv1.LinkType {
	switch delivered.GetProperties().(type) {
	case *linksv1.Link_Postgres:
		return linksv1.LinkType_LINK_TYPE_POSTGRES
	case *linksv1.Link_Bucket:
		return linksv1.LinkType_LINK_TYPE_BUCKET
	case *linksv1.Link_Custom:
		return linksv1.LinkType_LINK_TYPE_CUSTOM
	}
	return linksv1.LinkType_LINK_TYPE_UNSPECIFIED
}
