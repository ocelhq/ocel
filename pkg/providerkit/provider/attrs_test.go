package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestClassifyError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, provider.ErrorKindCanceled},
		{"timeout", context.DeadlineExceeded, provider.ErrorKindTimeout},
		{"anything else", errors.New("boom"), provider.ErrorKindFailed},
	} {
		if got := provider.ClassifyError(tc.err); got != tc.want {
			t.Errorf("ClassifyError(%v) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
