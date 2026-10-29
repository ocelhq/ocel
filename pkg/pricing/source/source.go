package source

import (
	"bytes"
	"encoding/json"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/pricing/source/pulumi"
	"github.com/ocelhq/ocel/pkg/pricing/source/terraform"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Document struct {
	Set         *costv1.ResourceSet
	Prior       *costv1.ResourceSet
	Assumptions []string
}

const accepted = "pass the output of pulumi preview --json, pulumi stack export, sst state export, sst diff --json, terraform show -json or ocel cost scan --json"

func Read(raw []byte) (Document, error) {
	doc, recognized, err := read(bytes.TrimSpace(raw))
	if !recognized {
		return Document{}, refusal.Refuse(refusal.CodeInvalid, "this is not a plan, state or scan ocel cost reads; %s", accepted)
	}
	if err != nil {
		return Document{}, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	for _, set := range []*costv1.ResourceSet{doc.Set, doc.Prior} {
		if set == nil {
			continue
		}
		if err := protovalidate.Validate(set); err != nil {
			return Document{}, refusal.Refuse(refusal.CodeInvalid, "the document describes an invalid resource set: %v", err)
		}
		if scope, cyclic := findScopeCycle(set.GetScopes()); cyclic {
			return Document{}, refusal.Refuse(refusal.CodeInvalid, "scope %q is its own ancestor; give every scope a parent chain that ends at a root", scope)
		}
	}
	return doc, nil
}

func findScopeCycle(scopes []*costv1.Scope) (string, bool) {
	parents := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		parents[scope.GetId()] = scope.GetParent()
	}
	for _, scope := range scopes {
		walked := map[string]bool{}
		for id := scope.GetId(); id != ""; id = parents[id] {
			if walked[id] {
				return id, true
			}
			walked[id] = true
		}
	}
	return "", false
}

func read(raw []byte) (Document, bool, error) {
	if bytes.HasPrefix(raw, []byte("[")) {
		var steps []map[string]json.RawMessage
		if json.Unmarshal(raw, &steps) != nil || !areSteps(steps) {
			return Document{}, false, nil
		}
		set, prior, assumptions, err := pulumi.ReadSteps(raw)
		return Document{Set: set, Prior: prior, Assumptions: assumptions}, true, err
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return Document{}, false, nil
	}
	switch {
	case has(top, "format_version") && (has(top, "planned_values") || has(top, "resource_changes")):
		set, prior, err := terraform.ReadPlan(raw)
		return Document{Set: set, Prior: prior}, true, err
	case has(top, "format_version") && has(field(top["values"]), "root_module"):
		set, err := terraform.ReadState(raw)
		return Document{Set: set}, true, err
	case has(top, "steps"):
		var steps []map[string]json.RawMessage
		if json.Unmarshal(top["steps"], &steps) != nil || !areSteps(steps) {
			return Document{}, false, nil
		}
		set, prior, assumptions, err := pulumi.ReadPreview(raw)
		return Document{Set: set, Prior: prior, Assumptions: assumptions}, true, err
	case has(field(top["deployment"]), "resources"):
		set, err := pulumi.ReadExport(raw)
		return Document{Set: set}, true, err
	case has(field(field(top["data"])["resources"]), "source"):
		set, err := readScan(field(top["data"])["resources"])
		return Document{Set: set}, true, err
	}
	return Document{}, false, nil
}

func readScan(raw json.RawMessage) (*costv1.ResourceSet, error) {
	set := &costv1.ResourceSet{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, set); err != nil {
		return nil, err
	}
	return set, nil
}

func areSteps(steps []map[string]json.RawMessage) bool {
	for _, s := range steps {
		if !has(s, "urn") || !has(s, "op") {
			return false
		}
	}
	return true
}

func field(raw json.RawMessage) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	return object
}

func has(object map[string]json.RawMessage, key string) bool {
	value, present := object[key]
	return present && !bytes.Equal(value, []byte("null"))
}
