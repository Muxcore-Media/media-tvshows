package internal

import (
	"context"
	"testing"
	"time"

	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
)

func TestSubscribeWhenMeshReadyWaitsForClient(t *testing.T) {
	m := NewModule(Config{GRPCAddr: ":0", HTTPAddr: ":0"})
	m.stopCh = make(chan struct{})

	subscribed := make(chan struct{}, 1)
	m.subscribeWhenMeshReady(contracts.EventDownloadDispatched, func(c *client.Client) error {
		if c == nil {
			t.Fatal("expected non-nil client")
		}
		subscribed <- struct{}{}
		return nil
	})

	go func() {
		time.Sleep(100 * time.Millisecond)
		m.mu.Lock()
		m.mc = &client.Client{}
		m.mu.Unlock()
	}()

	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe did not run after mesh client became available")
	}
}

func TestSubscribeWhenMeshReadyStopsOnModuleStop(t *testing.T) {
	m := NewModule(Config{GRPCAddr: ":0", HTTPAddr: ":0"})
	m.stopCh = make(chan struct{})

	done := make(chan struct{})
	m.subscribeWhenMeshReady(contracts.EventFileImported, func(c *client.Client) error {
		close(done)
		return nil
	})

	close(m.stopCh)

	select {
	case <-done:
		t.Fatal("subscribe should not succeed after stop")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestWaitForMeshClientReturnsOnStop(t *testing.T) {
	m := NewModule(Config{GRPCAddr: ":0", HTTPAddr: ":0"})
	m.stopCh = make(chan struct{})
	close(m.stopCh)
	if got := m.waitForMeshClient(context.Background()); got != nil {
		t.Fatalf("expected nil client after stop, got %v", got)
	}
}
