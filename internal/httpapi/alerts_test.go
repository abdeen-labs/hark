package httpapi

import (
	"strings"
	"testing"

	"github.com/abdeen-labs/hark/internal/db"
)

func TestDeliveryStatus(t *testing.T) {
	tests := map[string]struct {
		attempted int
		accepted  int
		want      string
	}{
		"no device to send to":     {0, 0, db.EventNoDevices},
		"nothing got through":      {2, 0, db.EventFailed},
		"some got through":         {3, 1, db.EventPartial},
		"everything got through":   {2, 2, db.EventAccepted},
		"one device, one accepted": {1, 1, db.EventAccepted},
	}
	for name, tc := range tests {
		if got := deliveryStatus(tc.attempted, tc.accepted); got != tc.want {
			t.Errorf("%s: deliveryStatus(%d, %d) = %q, want %q",
				name, tc.attempted, tc.accepted, got, tc.want)
		}
	}
}

func TestThreadKeyGroupsBySenderAndTitle(t *testing.T) {
	same := threadKey("svc", "  Build FINISHED ")
	if same != threadKey("svc", "build finished") {
		t.Error("a title differing only in case and surrounding space should share a thread")
	}
	if same == threadKey("svc", "build failed") {
		t.Error("two different titles from one sender should not share a thread")
	}
	if same == threadKey("other", "build finished") {
		t.Error("two senders should not share a thread")
	}
	if !strings.HasPrefix(same, "svc-") {
		t.Errorf("thread key %q should be namespaced by the sender", same)
	}
}
