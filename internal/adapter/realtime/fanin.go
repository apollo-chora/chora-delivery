// fanin.go — the per-pod bridge from the cross-pod Backplane to the local
// ws.Broker (ADR-168). One FanIn runs per pod: it pattern-subscribes to the
// realtime channels and re-emits each message into the pod-local broker via the
// deliver callback, which boot wiring binds to broker.Publish(sessionID, msg).
//
// Dependency inversion: FanIn does not import the ws package — it takes a
// deliver func so it stays testable with a fake Backplane and so the realtime
// package has no ws coupling.
package realtime

import "context"

// FanIn re-emits backplane messages into a pod-local sink.
type FanIn struct {
	bp      Backplane
	pattern string
	deliver func(sessionID string, payload []byte)
}

// NewFanIn binds a backplane subscription (pattern) to a deliver sink.
func NewFanIn(bp Backplane, pattern string, deliver func(sessionID string, payload []byte)) *FanIn {
	return &FanIn{bp: bp, pattern: pattern, deliver: deliver}
}

// Run subscribes and pumps messages into deliver until ctx is cancelled.
// Returns the subscription error (if any) or nil on clean ctx shutdown.
func (f *FanIn) Run(ctx context.Context) error {
	ch, cancel, err := f.bp.PSubscribe(ctx, f.pattern)
	if err != nil {
		return err
	}
	defer func() { _ = cancel() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			if id := SessionIDFromChannel(msg.Channel); id != "" {
				f.deliver(id, msg.Payload)
			}
		}
	}
}
