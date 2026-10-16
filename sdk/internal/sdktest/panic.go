package sdktest

import (
	"fmt"
	"testing"
)

func PanicError(t *testing.T, run func()) (err error) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		var ok bool
		if err, ok = recovered.(error); !ok {
			t.Fatalf("panicked with %T %v, want an error", recovered, recovered)
		}
	}()
	run()
	return nil
}

func PanicMessage(fn func()) (message string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			message = fmt.Sprint(recovered)
		}
	}()
	fn()
	return ""
}

func ErrorOf[T any](_ T, err error) error { return err }
