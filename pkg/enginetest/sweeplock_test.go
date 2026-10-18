//go:build unix

package enginetest

import "testing"

func TestTwoRunsSweepingTheSameDeadRunAtOnceBothSucceed(t *testing.T) {
	requireDocker(t)

	for range 3 {
		plant(t, aRunThatEnded(t))
		swept := make(chan error, 4)
		for range cap(swept) {
			go func() { swept <- sweep(runProcessGone) }()
		}
		for range cap(swept) {
			if err := <-swept; err != nil {
				t.Fatalf("sweep beside another = %v: every package a test run starts sweeps on its first engine test, so packages run in parallel race to take the same leftovers", err)
			}
		}
	}
}
