package internal

import (
	"context"
	"log/slog"
	"time"

	"github.com/Muxcore-Media/core/sdk/go/client"
)

const meshSubscribeRetryInterval = 500 * time.Millisecond

func (m *Module) subscribeWhenMeshReady(eventType string, subscribe func(*client.Client) error) {
	go func() {
		for {
			select {
			case <-m.stopCh:
				return
			default:
			}
			m.mu.RLock()
			mc := m.mc
			m.mu.RUnlock()
			if mc != nil {
				if err := subscribe(mc); err != nil {
					slog.Warn("mesh event subscribe failed", "event", eventType, "error", err)
				} else {
					slog.Info("subscribed to mesh events", "event", eventType)
				}
				return
			}
			select {
			case <-m.stopCh:
				return
			case <-time.After(meshSubscribeRetryInterval):
			}
		}
	}()
}

func (m *Module) waitForMeshClient(ctx context.Context) *client.Client {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.stopCh:
			return nil
		default:
		}
		m.mu.RLock()
		mc := m.mc
		m.mu.RUnlock()
		if mc != nil {
			return mc
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.stopCh:
			return nil
		case <-time.After(meshSubscribeRetryInterval):
		}
	}
}
