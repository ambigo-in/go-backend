package interfaces

// EventBus defines the contract for our internal messaging system.
// This allows us to swap an InMemory implementation for Redis later
// when we scale to multiple servers (Growth Stage 6).
type EventBus interface {
	Publish(channel string, payload []byte) error
	Subscribe(channel string, handler func(payload []byte)) error
	Unsubscribe(channel string) error
	Close() error
}
