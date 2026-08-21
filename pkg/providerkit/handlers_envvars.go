package providerkit

import (
	"context"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

// The eleven EnvVarsService handlers. Every one of them is name validation, a
// coordinate built from pkg/naming, a tier mapped to a class, one ValueStore call
// and an error mapped to a wire code — which is to say every one of them is kit
// logic over one port, and none of them is a vendor's to write.

func (h *handlers) SetValue(context.Context, *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	return nil, notLifted("SetValue")
}

func (h *handlers) ListValues(context.Context, *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	return nil, notLifted("ListValues")
}

func (h *handlers) GetValue(context.Context, *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	return nil, notLifted("GetValue")
}

func (h *handlers) RevealValues(context.Context, *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	return nil, notLifted("RevealValues")
}

func (h *handlers) DeleteValue(context.Context, *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	return nil, notLifted("DeleteValue")
}

func (h *handlers) SetReference(context.Context, *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	return nil, notLifted("SetReference")
}

func (h *handlers) ListReferences(context.Context, *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	return nil, notLifted("ListReferences")
}

func (h *handlers) ListVersions(context.Context, *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	return nil, notLifted("ListVersions")
}

// SetLink, RemoveLink and ListLinks are the link half. Which links a project may
// read is kit gating over ValueStore, checked before the membrane ever sees them.
func (h *handlers) SetLink(context.Context, *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	return nil, notLifted("SetLink")
}

func (h *handlers) RemoveLink(context.Context, *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	return nil, notLifted("RemoveLink")
}

func (h *handlers) ListLinks(context.Context, *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	return nil, notLifted("ListLinks")
}
