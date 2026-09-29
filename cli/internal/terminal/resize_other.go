//go:build !unix

package terminal

import "os"

func resizeSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
