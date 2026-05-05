package forgemcp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// subscriptionRegistry tracks which URIs each SSE connection has subscribed to,
// and holds a per-connection send function for pushing resource-update
// notifications back over the SSE stream.
//
// The registry is safe for concurrent access. All exported methods acquire the
// appropriate lock before modifying state.
type subscriptionRegistry struct {
	mu   sync.RWMutex
	subs map[string]map[string]struct{} // connID → set of subscribed URIs
	send map[string]func(uri string)    // connID → SSE send function
}

// newSubscriptionRegistry returns an initialised registry ready for use.
func newSubscriptionRegistry() *subscriptionRegistry {
	return &subscriptionRegistry{
		subs: make(map[string]map[string]struct{}),
		send: make(map[string]func(uri string)),
	}
}

// Register adds uri to the subscription set for connID.
// Calling Register with a connID that has no prior entries creates the entry.
func (r *subscriptionRegistry) Register(connID, uri string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[connID]; !ok {
		r.subs[connID] = make(map[string]struct{})
	}
	r.subs[connID][uri] = struct{}{}
}

// Unsubscribe removes uri from connID's subscription set.
// It is a no-op when connID or uri is not registered.
func (r *subscriptionRegistry) Unsubscribe(connID, uri string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.subs[connID]; ok {
		delete(m, uri)
	}
}

// RemoveConn removes all subscriptions and the send function for connID.
// Called when an SSE connection closes.
func (r *subscriptionRegistry) RemoveConn(connID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subs, connID)
	delete(r.send, connID)
}

// RegisterSend stores fn as the notification sender for connID.
// fn is called by [Notify] with the URI of the updated resource.
func (r *subscriptionRegistry) RegisterSend(connID string, fn func(uri string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.send[connID] = fn
}

// Notify calls the send function for every connection that has subscribed to uri.
// It recovers from any panic in a send function to avoid crashing the caller.
func (r *subscriptionRegistry) Notify(uri string) {
	r.mu.RLock()
	var fns []func(string)
	for connID, uris := range r.subs {
		if _, ok := uris[uri]; ok {
			if fn, ok := r.send[connID]; ok {
				fns = append(fns, fn)
			}
		}
	}
	r.mu.RUnlock()

	for _, fn := range fns {
		func() {
			defer func() { recover() }() //nolint:errcheck
			fn(uri)
		}()
	}
}

// buildNotifyEvent formats an SSE event for a notifications/resources/updated
// notification. The payload follows the MCP protocol: {"uri": "<resource-uri>"}.
func buildNotifyEvent(uri string) string {
	payload, _ := json.Marshal(map[string]any{"uri": uri})
	return fmt.Sprintf("event: notifications/resources/updated\ndata: %s\n\n", payload)
}

// newSessionID returns a cryptographically random 16-byte hex string suitable
// for use as an SSE connection identifier in resource subscription requests.
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a timestamp-based ID if rand is unavailable.
		return fmt.Sprintf("%016x", uint64(0))
	}
	return hex.EncodeToString(b)
}
