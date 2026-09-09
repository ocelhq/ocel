package main

import (
	"net/http"
	"testing"
)

func TestNewLoopbackClient(t *testing.T) {
	transportOf := func(t *testing.T) *http.Transport {
		t.Helper()
		transport, ok := newLoopbackClient().Transport.(*http.Transport)
		if !ok {
			t.Fatalf("transport = %T, want *http.Transport", newLoopbackClient().Transport)
		}
		return transport
	}

	t.Run("pools connections on an ordinary cold start", func(t *testing.T) {
		t.Setenv(initializationTypeEnvVar, "on-demand")

		transport := transportOf(t)
		if transport.DisableKeepAlives {
			t.Error("keep-alives are off, want the loopback pool that saves a handshake per invocation")
		}
		if transport.MaxIdleConnsPerHost != 16 {
			t.Errorf("MaxIdleConnsPerHost = %d, want 16", transport.MaxIdleConnsPerHost)
		}
	})

	t.Run("pools nothing under snap-start", func(t *testing.T) {
		t.Setenv(initializationTypeEnvVar, snapStartInitialization)

		if !transportOf(t).DisableKeepAlives {
			t.Error("keep-alives are on, want them off: a connection pooled before the snapshot outlives a clock the restore moves")
		}
	})
}
