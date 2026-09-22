package collectors

import (
	"net/http"
	"testing"
	"time"
)

// TestWithHTTPClient pins the one seam the end-to-end tests rely on to point the
// collectors at a stand-in server: a client passed in replaces the default, and
// a nil client leaves the 30 s default in place so production wiring is unchanged.
func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 3 * time.Second}
	if got := NewRegistry(nil, WithHTTPClient(custom)).http; got != custom {
		t.Fatalf("WithHTTPClient did not replace the client: got %p, want %p", got, custom)
	}
	if got := NewRegistry(nil, WithHTTPClient(nil)).http; got == nil || got.Timeout != 30*time.Second {
		t.Fatalf("a nil client must keep the 30s default, got %+v", got)
	}
	if got := NewRegistry(nil).http; got == nil || got.Timeout != 30*time.Second {
		t.Fatalf("no option must keep the 30s default, got %+v", got)
	}
}
