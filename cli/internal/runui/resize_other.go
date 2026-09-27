//go:build !unix

package runui

import "os"

func resizeSignals() (<-chan os.Signal, func()) {
	return nil, func() {}
}
