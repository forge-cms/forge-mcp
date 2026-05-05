package forgemcp

import (
	"sync"
	"testing"
)

// TestSubscriptionRegistry_RegisterAndNotify checks that Notify fans out only
// to connections that have subscribed to the given URI.
func TestSubscriptionRegistry_RegisterAndNotify(t *testing.T) {
	reg := newSubscriptionRegistry()

	uri := "forge:/posts/hello"
	otherURI := "forge:/posts/other"

	var mu sync.Mutex
	var notified []string

	// conn1 subscribes to uri.
	reg.RegisterSend("conn1", func(u string) {
		mu.Lock()
		notified = append(notified, "conn1:"+u)
		mu.Unlock()
	})
	reg.Register("conn1", uri)

	// conn2 subscribes to a different URI.
	reg.RegisterSend("conn2", func(u string) {
		mu.Lock()
		notified = append(notified, "conn2:"+u)
		mu.Unlock()
	})
	reg.Register("conn2", otherURI)

	reg.Notify(uri)

	mu.Lock()
	defer mu.Unlock()

	if len(notified) != 1 {
		t.Fatalf("Notify(%q) called %d send functions, want 1", uri, len(notified))
	}
	if notified[0] != "conn1:"+uri {
		t.Errorf("notified[0] = %q, want %q", notified[0], "conn1:"+uri)
	}
}

// TestSubscriptionRegistry_Unsubscribe verifies that Unsubscribe prevents
// further notifications for the removed URI.
func TestSubscriptionRegistry_Unsubscribe(t *testing.T) {
	reg := newSubscriptionRegistry()
	uri := "forge:/posts/hello"

	calls := 0
	reg.RegisterSend("conn1", func(string) { calls++ })
	reg.Register("conn1", uri)

	reg.Notify(uri) // expect: called
	reg.Unsubscribe("conn1", uri)
	reg.Notify(uri) // expect: not called

	if calls != 1 {
		t.Errorf("send called %d times, want 1", calls)
	}
}

// TestSubscriptionRegistry_RemoveConn verifies that RemoveConn removes all
// subscriptions and the send function for the connection.
func TestSubscriptionRegistry_RemoveConn(t *testing.T) {
	reg := newSubscriptionRegistry()
	uri := "forge:/posts/hello"

	calls := 0
	reg.RegisterSend("conn1", func(string) { calls++ })
	reg.Register("conn1", uri)
	reg.Register("conn1", "forge:/posts/other")

	reg.RemoveConn("conn1")
	reg.Notify(uri)

	if calls != 0 {
		t.Errorf("send called %d times after RemoveConn, want 0", calls)
	}
}

// TestSubscriptionRegistry_PanicRecovery verifies that a panicking send
// function does not crash the Notify call and does not prevent other
// connections from receiving the notification.
func TestSubscriptionRegistry_PanicRecovery(t *testing.T) {
	reg := newSubscriptionRegistry()
	uri := "forge:/posts/hello"

	reg.RegisterSend("bad", func(string) { panic("send panic") })
	reg.Register("bad", uri)

	good := 0
	reg.RegisterSend("good", func(string) { good++ })
	reg.Register("good", uri)

	// Must not panic.
	reg.Notify(uri)

	if good != 1 {
		t.Errorf("good send called %d times, want 1", good)
	}
}

// TestSubscriptionRegistry_MultipleURIs verifies that a connection can hold
// subscriptions for multiple URIs independently.
func TestSubscriptionRegistry_MultipleURIs(t *testing.T) {
	reg := newSubscriptionRegistry()
	uri1 := "forge:/posts/alpha"
	uri2 := "forge:/posts/beta"

	var calls []string
	reg.RegisterSend("conn1", func(u string) { calls = append(calls, u) })
	reg.Register("conn1", uri1)
	reg.Register("conn1", uri2)

	reg.Notify(uri1)
	reg.Notify(uri2)

	if len(calls) != 2 {
		t.Fatalf("want 2 notifications, got %d", len(calls))
	}
}

// TestNewSessionID verifies that newSessionID returns a non-empty hex string
// of the expected length (32 hex chars = 16 bytes).
func TestNewSessionID(t *testing.T) {
	id := newSessionID()
	if len(id) != 32 {
		t.Errorf("session ID length = %d, want 32", len(id))
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("session ID %q contains non-hex character %c", id, c)
		}
	}
	// Two calls should return different IDs (collision probability ≈ 2^-128).
	id2 := newSessionID()
	if id == id2 {
		t.Errorf("two newSessionID calls returned identical value %q", id)
	}
}
