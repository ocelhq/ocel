package providerkit

import (
	"context"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func (h *handlers) SetValue(context.Context, *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	return nil, h.unlifted("SetValue")
}

func (h *handlers) ListValues(context.Context, *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	return nil, h.unlifted("ListValues")
}

func (h *handlers) GetValue(context.Context, *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	return nil, h.unlifted("GetValue")
}

func (h *handlers) RevealValues(context.Context, *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	return nil, h.unlifted("RevealValues")
}

func (h *handlers) DeleteValue(context.Context, *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	return nil, h.unlifted("DeleteValue")
}

func (h *handlers) SetReference(context.Context, *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	return nil, h.unlifted("SetReference")
}

func (h *handlers) ListReferences(context.Context, *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	return nil, h.unlifted("ListReferences")
}

func (h *handlers) ListVersions(context.Context, *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	return nil, h.unlifted("ListVersions")
}

func (h *handlers) SetLink(context.Context, *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	return nil, h.unlifted("SetLink")
}

func (h *handlers) RemoveLink(context.Context, *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	return nil, h.unlifted("RemoveLink")
}

func (h *handlers) ListLinks(context.Context, *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	return nil, h.unlifted("ListLinks")
}
