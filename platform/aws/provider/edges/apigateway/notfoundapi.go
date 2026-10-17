package apigateway

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
)

const notFoundAddedWith = "ocel domain use --preview"

func (p *apiGateway) ensureNotFoundAPI(ctx context.Context, say func(string)) (string, error) {
	if p.ensurePart == nil {
		return "", fmt.Errorf("the %q edge was built without a way to add the %s bootstrap part", Kind, notfoundapi.Part)
	}
	resources, err := p.ensurePart(ctx, environment.TierPreview, notfoundapi.Part, "", notFoundAddedWith, say)
	if err != nil {
		return "", err
	}
	id := notfoundapi.IDOf(resources)
	if id == "" {
		return "", fmt.Errorf("the preview tier's %s part records no REST API, so nothing would answer a preview host no preview claims", notfoundapi.Part)
	}
	return id, nil
}
