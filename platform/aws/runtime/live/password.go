package live

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
)

func currentPasswords(ctx context.Context, sources signin.Sources, bindings []live.Binding, records []variablestore.StoredBinding, values map[string]string) (bool, error) {
	minted := false
	for i, record := range records {
		l := bindings[i]
		if l.Type != bindingsv1.BindingType_BINDING_TYPE_POSTGRES {
			continue
		}
		published := &variablestorev1.PublishedBinding{}
		if err := protojson.Unmarshal(record.Value, published); err != nil || published.GetBinding().GetPostgres() == nil {
			continue
		}
		password, signsIn, err := sources.Read(ctx, provider.PublishedBindingOf(published))
		if err != nil {
			return false, fmt.Errorf("read the current password of %s: %w", l.Name, err)
		}
		if !signsIn {
			continue
		}
		minted = minted || password.Minted
		binding := published.GetBinding()
		binding.GetPostgres().Password = password.Value
		encoded, err := protojson.Marshal(binding)
		if err != nil {
			return false, fmt.Errorf("render %s with its current password: %w", l.Name, err)
		}
		values[l.Key] = string(encoded)
	}
	return minted, nil
}
