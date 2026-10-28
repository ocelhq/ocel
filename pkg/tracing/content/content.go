package content

import "context"

type omittedKey struct{}

func Omit(ctx context.Context) context.Context {
	return context.WithValue(ctx, omittedKey{}, true)
}

func IsOmitted(ctx context.Context) bool {
	omitted, _ := ctx.Value(omittedKey{}).(bool)
	return omitted
}
