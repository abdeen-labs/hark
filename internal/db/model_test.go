package db

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func ptr[T any](v T) *T { return &v }

// The capability rules decide who a push can even be attempted against, so
// every clause matters: a device that looks capable but is not produces a
// silent non-delivery rather than an error.
func TestDeviceCapabilities(t *testing.T) {
	capable := Device{
		Platform:                       PlatformIOS,
		Active:                         true,
		PushToStartTokenCiphertext:     ptr("v1.iv.tag.ct"),
		PushToStartEnvironment:         ptr(EnvironmentProduction),
		LiveActivitySchemaVersion:      ptr(LiveActivitySchemaVersion),
		InteractionSchemaVersion:       ptr(InteractionSchemaVersion),
		LiveActivityInteractionVersion: ptr(LiveActivityInteractionVersion),
	}

	cases := []struct {
		name        string
		mutate      func(*Device)
		pushable    bool
		liveCapable bool
		interaction bool
		interactive bool
	}{
		{name: "fully capable", mutate: func(*Device) {}, pushable: true, liveCapable: true, interaction: true, interactive: true},
		{name: "inactive", mutate: func(d *Device) { d.Active = false }},
		{
			name:     "no push-to-start token",
			mutate:   func(d *Device) { d.PushToStartTokenCiphertext = nil },
			pushable: true, interaction: true,
		},
		{
			name:     "empty push-to-start token",
			mutate:   func(d *Device) { d.PushToStartTokenCiphertext = ptr("") },
			pushable: true, interaction: true,
		},
		{
			name:     "unknown environment",
			mutate:   func(d *Device) { d.PushToStartEnvironment = ptr("staging") },
			pushable: true, interaction: true,
		},
		{
			name:     "missing environment",
			mutate:   func(d *Device) { d.PushToStartEnvironment = nil },
			pushable: true, interaction: true,
		},
		{
			name:     "future Live Activity schema",
			mutate:   func(d *Device) { d.LiveActivitySchemaVersion = ptr(2) },
			pushable: true, interaction: true,
		},
		{
			name:     "no interaction support",
			mutate:   func(d *Device) { d.InteractionSchemaVersion = nil },
			pushable: true, liveCapable: true, interactive: true,
		},
		{
			name:     "no interactive Live Activity support",
			mutate:   func(d *Device) { d.LiveActivityInteractionVersion = nil },
			pushable: true, liveCapable: true, interaction: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := capable
			tc.mutate(&d)
			if got := d.Pushable(); got != tc.pushable {
				t.Errorf("Pushable() = %v, want %v", got, tc.pushable)
			}
			if got := d.LiveActivityCapable(); got != tc.liveCapable {
				t.Errorf("LiveActivityCapable() = %v, want %v", got, tc.liveCapable)
			}
			if got := d.InteractionCapable(); got != tc.interaction {
				t.Errorf("InteractionCapable() = %v, want %v", got, tc.interaction)
			}
			if got := d.InteractiveLiveActivityCapable(); got != tc.interactive {
				t.Errorf("InteractiveLiveActivityCapable() = %v, want %v", got, tc.interactive)
			}
		})
	}
}

func TestChoicesFor(t *testing.T) {
	for kind, want := range map[string][]string{
		InteractionApproval: {"approve", "deny"},
		InteractionYesNo:    {"yes", "no"},
		InteractionReply:    {"reply"},
		"nonsense":          nil,
	} {
		if got := ChoicesFor(kind); !slices.Equal(got, want) {
			t.Errorf("ChoicesFor(%q) = %v, want %v", kind, got, want)
		}
	}
}

// An action that does not belong to the kind must never map to a status: that
// is what stops a yes/no question being answered "approve".
func TestStatusForAction(t *testing.T) {
	valid := map[string]map[string]string{
		InteractionApproval: {"approve": InteractionApproved, "deny": InteractionDenied},
		InteractionYesNo:    {"yes": InteractionYes, "no": InteractionNo},
		InteractionReply:    {"reply": InteractionReplied},
	}
	for kind, actions := range valid {
		for action, want := range actions {
			got, ok := StatusForAction(kind, action)
			if !ok || got != want {
				t.Errorf("StatusForAction(%q, %q) = (%q, %v), want (%q, true)", kind, action, got, ok, want)
			}
		}
	}

	mismatched := [][2]string{
		{InteractionApproval, "yes"},
		{InteractionApproval, "reply"},
		{InteractionYesNo, "approve"},
		{InteractionReply, "approve"},
		{InteractionReply, "yes"},
		{"nonsense", "approve"},
		{InteractionApproval, ""},
	}
	for _, tc := range mismatched {
		if got, ok := StatusForAction(tc[0], tc[1]); ok {
			t.Errorf("StatusForAction(%q, %q) = (%q, true), want rejection", tc[0], tc[1], got)
		}
	}
}

func TestInteractionStatePredicates(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	pending := Interaction{Status: InteractionPending, ExpiresAt: now.Add(time.Minute)}
	if pending.Terminal() || pending.Answered() || !pending.Live(now) {
		t.Error("a pending, unexpired interaction should be live and neither terminal nor answered")
	}

	lapsed := Interaction{Status: InteractionPending, ExpiresAt: now}
	if lapsed.Live(now) {
		t.Error("an interaction whose deadline has arrived is no longer live")
	}

	for _, status := range AnsweredStatuses() {
		i := Interaction{Status: status, ExpiresAt: now.Add(time.Minute)}
		if !i.Terminal() || !i.Answered() || i.Live(now) {
			t.Errorf("status %q should be terminal and answered", status)
		}
	}
	for _, status := range []string{InteractionCanceled, InteractionExpired} {
		i := Interaction{Status: status, ExpiresAt: now.Add(time.Minute)}
		if !i.Terminal() || i.Answered() {
			t.Errorf("status %q should be terminal but not answered", status)
		}
	}
}

// AnsweredStatuses hands out a copy: a caller that sorts or appends to the
// result must not be able to change what the store considers answered.
func TestAnsweredStatusesIsACopy(t *testing.T) {
	got := AnsweredStatuses()
	got[0] = "tampered"
	if AnsweredStatuses()[0] == "tampered" {
		t.Error("AnsweredStatuses returned the package-level slice")
	}
}

func TestNormalizeScopes(t *testing.T) {
	got, ok := NormalizeScopes([]string{
		ScopeNotificationsNew, ScopeActivitiesRead, ScopeNotificationsNew, ScopeActivitiesRead,
	})
	if !ok {
		t.Fatal("NormalizeScopes rejected a list of known scopes")
	}
	want := []string{ScopeActivitiesRead, ScopeNotificationsNew}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, ok := NormalizeScopes([]string{ScopeActivitiesRead, "activities:destroy"}); ok {
		t.Error("NormalizeScopes accepted an unknown scope")
	}
	if _, ok := NormalizeScopes(nil); !ok {
		t.Error("an empty scope list is not itself invalid; the caller enforces the minimum")
	}
}

func TestEveryScopeHasADescription(t *testing.T) {
	for _, scope := range Scopes {
		if ScopeDescription(scope) == "" {
			t.Errorf("scope %q has no human-readable description", scope)
		}
	}
	if got := ScopeDescription("activities:destroy"); got != "" {
		t.Errorf("unknown scope description = %q, want empty", got)
	}
}

// NormalizeScopes must not disturb its argument: callers pass a slice they
// still hold, and re-sorting it under them would be a surprise.
func TestNormalizeScopesDoesNotMutateInput(t *testing.T) {
	in := []string{ScopeServicesWrite, ScopeActivitiesRead}
	NormalizeScopes(in)
	if in[0] != ScopeServicesWrite {
		t.Errorf("input was reordered: %v", in)
	}
}

func TestAPITokenActive(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		token APIToken
		want  bool
	}{
		"never expires":   {APIToken{}, true},
		"expires later":   {APIToken{ExpiresAt: ptr(now.Add(time.Hour))}, true},
		"expired":         {APIToken{ExpiresAt: ptr(now.Add(-time.Second))}, false},
		"expires now":     {APIToken{ExpiresAt: ptr(now)}, false},
		"revoked":         {APIToken{RevokedAt: ptr(now.Add(-time.Hour))}, false},
		"revoked and old": {APIToken{RevokedAt: ptr(now), ExpiresAt: ptr(now.Add(time.Hour))}, false},
	}
	for name, tc := range cases {
		if got := tc.token.Active(now); got != tc.want {
			t.Errorf("%s: Active() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestAPITokenScopes(t *testing.T) {
	tok := APIToken{Scopes: []string{ScopeActivitiesRead, ScopeNotificationsNew}}
	if !tok.HasScope(ScopeActivitiesRead) || tok.HasScope(ScopeActivitiesWrite) {
		t.Error("HasScope is wrong")
	}
	if !tok.HasScopes(ScopeActivitiesRead, ScopeNotificationsNew) {
		t.Error("HasScopes rejected scopes the token holds")
	}
	if tok.HasScopes(ScopeActivitiesRead, ScopeServicesWrite) {
		t.Error("HasScopes accepted a set the token only partly holds")
	}
	if !tok.HasScopes() {
		t.Error("HasScopes with no requirement should hold")
	}
}

func TestValidPriority(t *testing.T) {
	for _, p := range Priorities {
		if !ValidPriority(p) {
			t.Errorf("ValidPriority(%q) = false", p)
		}
	}
	for _, p := range []string{"", "urgent", "time-sensitive", "NORMAL"} {
		if ValidPriority(p) {
			t.Errorf("ValidPriority(%q) = true", p)
		}
	}
}

func TestActivityLifecyclePredicates(t *testing.T) {
	for _, status := range []string{ActivityStarting, ActivityActive, ActivityPartial} {
		a := LiveActivity{Status: status}
		if !a.Live() || a.Terminal() {
			t.Errorf("status %q should be live", status)
		}
	}
	for _, status := range []string{ActivityFailed, ActivityEnded, ActivityExpired} {
		a := LiveActivity{Status: status}
		if a.Live() || !a.Terminal() {
			t.Errorf("status %q should be terminal", status)
		}
	}
	if (LiveActivity{}).Interactive() {
		t.Error("an activity with no interaction id is not interactive")
	}
	if !(LiveActivity{InteractionID: ptr("int")}).Interactive() {
		t.Error("an activity with an interaction id is interactive")
	}
}

func TestDeliveryPredicates(t *testing.T) {
	for _, status := range LiveStatuses() {
		if !(LiveActivityDelivery{Status: status}).Live() {
			t.Errorf("status %q should hold a device slot", status)
		}
	}
	for _, status := range []string{DeliveryFailed, DeliveryEnded} {
		if (LiveActivityDelivery{Status: status}).Live() {
			t.Errorf("status %q should not hold a device slot", status)
		}
	}
	if (LiveActivityDelivery{UpdateTokenCiphertext: ptr("")}).Updatable() {
		t.Error("an empty update token is not usable")
	}
	if !(LiveActivityDelivery{UpdateTokenCiphertext: ptr("v1.a.b.c")}).Updatable() {
		t.Error("a stored update token makes a delivery updatable")
	}
}

func TestParseFeedID(t *testing.T) {
	id := "0198f3a1-2b4c-7d8e-9f01-23456789abcd"
	for _, source := range []string{FeedSourceEvent, FeedSourceNotification, FeedSourceResponse, FeedSourceLiveActivity} {
		gotSource, gotID, ok := ParseFeedID(source + ":" + id)
		if !ok || gotSource != source || gotID != id {
			t.Errorf("ParseFeedID(%q:%q) = (%q, %q, %v)", source, id, gotSource, gotID, ok)
		}
	}

	for _, bad := range []string{"", "event", ":" + id, "event:", "activity:" + id, "EVENT:" + id} {
		if _, _, ok := ParseFeedID(bad); ok {
			t.Errorf("ParseFeedID(%q) accepted a malformed id", bad)
		}
	}

	// A Live Activity id splits on the first colon only, so the row id keeps
	// any colon it happens to contain.
	source, rowID, ok := ParseFeedID("live_activity:a:b")
	if !ok || source != FeedSourceLiveActivity || rowID != "a:b" {
		t.Errorf("ParseFeedID split on the wrong colon: (%q, %q, %v)", source, rowID, ok)
	}
}

func TestValidFeedFilter(t *testing.T) {
	for _, f := range []string{FeedFilterAll, FeedFilterNotification, FeedFilterResponse, FeedFilterLiveActivity} {
		if !ValidFeedFilter(f) {
			t.Errorf("ValidFeedFilter(%q) = false", f)
		}
	}
	for _, f := range []string{"", "events", "ALL", "event"} {
		if ValidFeedFilter(f) {
			t.Errorf("ValidFeedFilter(%q) = true", f)
		}
	}
}

func TestMillisTruncates(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	in := time.Date(2026, 8, 9, 7, 34, 56, 789_999_999, zone)
	got := Millis(in)
	if got.Location() != time.UTC {
		t.Errorf("Location() = %v, want UTC", got.Location())
	}
	if got.Nanosecond() != 789_000_000 {
		t.Errorf("Nanosecond() = %d, want 789000000", got.Nanosecond())
	}
	if !got.Equal(in.Add(-999_999 * time.Nanosecond)) {
		t.Errorf("Millis(%s) = %s", in, got)
	}
	if got := Millis(time.Time{}); !got.IsZero() {
		t.Errorf("Millis(zero) = %s, want the zero time", got)
	}
}

func TestSetOptional(t *testing.T) {
	var unset Set[string]
	if unset.IsSet() {
		t.Error("the zero Set must mean 'leave alone'")
	}
	if present, v := unset.args(); present || v != "" {
		t.Errorf("args() = (%v, %q)", present, v)
	}

	set := Value("new title")
	v, ok := set.Get()
	if !ok || v != "new title" {
		t.Errorf("Get() = (%q, %v)", v, ok)
	}

	// The three-way distinction a PATCH needs: absent, set, and set to null.
	cleared := Value[*string](nil)
	if !cleared.IsSet() {
		t.Error("an explicit null must still be 'set'")
	}
	if got, _ := cleared.Get(); got != nil {
		t.Errorf("Get() = %v, want nil", got)
	}
}

func TestUniqueViolationHelpers(t *testing.T) {
	pgErr := &pgconn.PgError{Code: uniqueViolationCode, ConstraintName: "events_service_idempotency_key"}
	wrapped := fmt.Errorf("db: create event: %w", pgErr)

	if !IsUniqueViolation(wrapped) {
		t.Error("IsUniqueViolation did not see through the wrapping")
	}
	if !IsUniqueViolation(wrapped, "events_service_idempotency_key") {
		t.Error("IsUniqueViolation rejected the constraint it names")
	}
	// Naming constraints is how an idempotency race is told apart from any
	// other collision, so a different constraint must not match.
	if IsUniqueViolation(wrapped, "interactions_event_key") {
		t.Error("IsUniqueViolation matched an unrelated constraint")
	}
	if got := ConstraintName(wrapped); got != "events_service_idempotency_key" {
		t.Errorf("ConstraintName() = %q", got)
	}

	if IsUniqueViolation(errors.New("boom")) || ConstraintName(errors.New("boom")) != "" {
		t.Error("a plain error is not a constraint violation")
	}
	if IsUniqueViolation(fmt.Errorf("%w", &pgconn.PgError{Code: checkViolationCode})) {
		t.Error("a CHECK violation is not a unique violation")
	}
	if !IsCheckViolation(fmt.Errorf("%w", &pgconn.PgError{Code: checkViolationCode})) {
		t.Error("IsCheckViolation missed a CHECK violation")
	}
	if IsCheckViolation(wrapped) {
		t.Error("IsCheckViolation matched a unique violation")
	}
}
