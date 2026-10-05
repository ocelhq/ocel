//go:build !unix

package enginetest

func sweepingAlone() (release func()) { return func() {} }
