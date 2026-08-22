package providerkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

var (
	ErrUnsourced = errors.New("providerkit: unsourced link")

	ErrUnreadableRecord = errors.New("providerkit: unreadable link record")

	ErrUnscopedGrant = errors.New("providerkit: unscoped grant")

	ErrUnattachedGrant = errors.New("providerkit: unattached grant")
)

func EncodeLink(link *linksv1.Link) ([]byte, error) { return protojson.Marshal(link) }

func DecodeLink(raw []byte) (*linksv1.Link, error) {
	link := &linksv1.Link{}
	if err := protojson.Unmarshal(raw, link); err != nil {
		return nil, err
	}
	return link, nil
}

func linkPair(owner string, link *linksv1.Link) (values.Pair, error) {
	value, err := EncodeLink(link)
	if err != nil {
		return values.Pair{}, fmt.Errorf("render link %s: %w", link.GetName(), err)
	}
	if len(value) > values.MaxValueBytes {
		return values.Pair{}, Refuse(CodeInvalid, "link %s is too large: %d bytes, limit %d", link.GetName(), len(value), values.MaxValueBytes)
	}
	record, err := EncodeLink(redacted(link))
	if err != nil {
		return values.Pair{}, fmt.Errorf("render link %s's record: %w", link.GetName(), err)
	}
	shapes, err := json.Marshal(naming.LinkPropertyShapes(link))
	if err != nil {
		return values.Pair{}, fmt.Errorf("render link %s's shape: %w", link.GetName(), err)
	}
	return values.Pair{Record: record, Shapes: shapes, Value: value, Owner: owner}, nil
}

func redacted(link *linksv1.Link) *linksv1.Link {
	out := proto.Clone(link).(*linksv1.Link)
	m := out.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties")); fd != nil {
		m.Set(fd, protoreflect.ValueOfMessage(m.Get(fd).Message().New()))
	}
	return out
}

func decodeShapes(raw []byte) ([]naming.PropertyShape, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var shapes []naming.PropertyShape
	if err := json.Unmarshal(raw, &shapes); err != nil {
		return nil, err
	}
	return shapes, nil
}

func RefuseUnsourced(publisher string, link *linksv1.Link) error {
	if link.GetSource() != "" {
		return nil
	}
	if naming.LinkTypeOf(link) == linksv1.LinkType_LINK_TYPE_CUSTOM {
		return unsourcedCustom(link)
	}
	return fmt.Errorf(
		"publisher %s leaves link %s unsourced: an empty source names what ocel's own provisioning produces, and an app binds a client to it on that promise. "+
			"Name the tool that publishes it: %w",
		publisher, link.GetName(), ErrUnsourced)
}

func unsourcedCustom(link *linksv1.Link) error {
	return fmt.Errorf(
		"link %s is a custom record with no source: only your own infrastructure publishes a custom link; ocel provisions nothing it cannot type. "+
			"Name the tool that publishes it: %w",
		link.GetName(), ErrUnsourced)
}

func ValidatePublisher(publisher string) error {
	if err := values.ValidateOwner(publisher); err != nil {
		return err
	}
	if publisher == values.OwnerOcel {
		return fmt.Errorf("publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune", values.OwnerOcel)
	}
	return nil
}

func VerifyLink(link *linksv1.Link) error {
	if link.GetName() == "" {
		return fmt.Errorf("a link carries no name; the name is what a consuming app binds to: %w", ErrUnreadableRecord)
	}
	if naming.LinkTypeOf(link) == linksv1.LinkType_LINK_TYPE_UNSPECIFIED {
		return fmt.Errorf("link %s carries no properties, so it has no type a consumer can resolve it against: %w", link.GetName(), ErrUnreadableRecord)
	}
	if naming.LinkTypeOf(link) == linksv1.LinkType_LINK_TYPE_CUSTOM {
		if link.GetSource() == "" {
			return unsourcedCustom(link)
		}
		if len(link.GetGrants()) > 0 {
			return fmt.Errorf(
				"link %s is a custom record carrying %d grants: no consumer attaches a custom link's grants yet; a grant nobody attaches is a permission the record claims and no app holds. "+
					"Publish it without them: %w",
				link.GetName(), len(link.GetGrants()), ErrUnattachedGrant)
		}
	}
	for _, g := range link.GetGrants() {
		if len(g.GetActions()) == 0 || slices.ContainsFunc(g.GetActions(), UnscopedAction) {
			return fmt.Errorf("link %s grants %v over %v: an action naming a whole service reaches past the resource it links: %w",
				link.GetName(), g.GetActions(), g.GetResources(), ErrUnscopedGrant)
		}
		if len(g.GetResources()) == 0 || slices.ContainsFunc(g.GetResources(), UnscopedResource) {
			return fmt.Errorf("link %s grants %v over %v: an app receives permissions for the resource it links and nothing else: %w",
				link.GetName(), g.GetActions(), g.GetResources(), ErrUnscopedGrant)
		}
	}
	return nil
}

const grantWildcard = "*"

func UnscopedAction(action string) bool {
	service, verb, scoped := strings.Cut(action, ":")
	if !scoped {
		return action == grantWildcard
	}
	return verb == grantWildcard || service == grantWildcard
}

func UnscopedResource(resource string) bool { return resource == grantWildcard }
