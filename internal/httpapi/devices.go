package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
	"github.com/abdeen-labs/hark/internal/push"
	"github.com/abdeen-labs/hark/internal/secret"
)

// APNs token bounds. A device token is 32 hex characters today and has grown
// before, so the range is wide on purpose: rejecting a longer token would break
// the app on the first iOS release that lengthens it.
const (
	minAPNsTokenLen     = 32
	maxAPNsTokenLen     = 400
	maxActivityTokenLen = 512
)

// deviceDTO is a registered phone as every response renders it.
type deviceDTO struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Platform string  `json:"platform"`
	// Active is false once APNs has reported the token permanently invalid. The
	// row is kept so history keeps resolving; registering again revives it.
	Active bool `json:"active"`
	// The capability versions the client reported. Null means the feature is not
	// supported by this installation, and nothing that needs it is sent to it.
	InteractionSchemaVersion       *int `json:"interaction_schema_version"`
	LiveActivityInteractionVersion *int `json:"live_activity_interaction_version"`
	// LiveActivityCapable is the derived answer to "can a Live Activity be
	// started here": a push-to-start token, a schema version this server speaks,
	// and a known environment.
	LiveActivityCapable    bool       `json:"live_activity_capable"`
	PushToStartEnvironment *string    `json:"push_to_start_environment"`
	PushToStartUpdatedAt   *Timestamp `json:"push_to_start_updated_at"`
	CreatedAt              Timestamp  `json:"created_at"`
	LastSeenAt             Timestamp  `json:"last_seen_at"`
}

func newDeviceDTO(d db.Device) deviceDTO {
	return deviceDTO{
		ID:                             d.ID,
		Name:                           d.Name,
		Platform:                       d.Platform,
		Active:                         d.Active,
		InteractionSchemaVersion:       d.InteractionSchemaVersion,
		LiveActivityInteractionVersion: d.LiveActivityInteractionVersion,
		LiveActivityCapable:            d.LiveActivityCapable(),
		PushToStartEnvironment:         d.PushToStartEnvironment,
		PushToStartUpdatedAt:           TimestampPtr(d.PushToStartUpdatedAt),
		CreatedAt:                      Timestamp(d.CreatedAt),
		LastSeenAt:                     Timestamp(d.LastSeenAt),
	}
}

type deviceListResponse struct {
	Devices []deviceDTO `json:"devices"`
}

type deviceResponse struct {
	Device deviceDTO `json:"device"`
}

// handleListDevices returns every device on the account, most recently seen
// first. Inactive rows stay listed: "this phone stopped accepting pushes" is
// something an owner needs to be able to see.
func (s *server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	devices, err := s.store().Devices.ListForUser(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing devices failed", err)
		return
	}

	out := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		out = append(out, newDeviceDTO(d))
	}
	WriteJSON(w, r, http.StatusOK, deviceListResponse{Devices: out})
}

// handleGetDevice returns one device.
func (s *server) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	device, err := s.store().Devices.ByID(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "device", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, deviceResponse{Device: newDeviceDTO(*device)})
}

type registerDeviceRequest struct {
	APNsToken string  `json:"apns_token"`
	Name      *string `json:"name"`
	// The versions this installation implements. Omitting one is a statement:
	// the device does not understand that feature and must not be sent it.
	InteractionSchemaVersion       *int `json:"interaction_schema_version"`
	LiveActivityInteractionVersion *int `json:"live_activity_interaction_version"`
}

// handleRegisterDevice registers or refreshes this phone.
//
// It is keyed on the APNs token rather than on a client-chosen id, because the
// token *is* the address: iOS reissues one whenever it feels like it, and the
// app registering the new token is how the account learns where to reach it.
// A reissued token therefore produces a new row, and the old one lives on until
// a push to it fails — which is why the list can show the same phone twice.
//
// Every optional field is a full replace, not a merge. The client sends its
// complete current state on every registration, so a field it omits is a
// capability it no longer has, not one to remember.
func (s *server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var body registerDeviceRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	token := v.hexToken("apns_token", body.APNsToken, minAPNsTokenLen, maxAPNsTokenLen)
	name := v.optionalText("name", body.Name, maxNameLen)
	interactionVersion := v.capabilityVersion("interaction_schema_version",
		body.InteractionSchemaVersion, db.InteractionSchemaVersion)
	activityVersion := v.capabilityVersion("live_activity_interaction_version",
		body.LiveActivityInteractionVersion, db.LiveActivityInteractionVersion)
	if !v.done(w, r) {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	now := s.now()

	var (
		registered *db.RegisteredDevice
		welcome    bool
	)
	err := s.store().Tx(r.Context(), func(ctx context.Context, tx *db.Store) error {
		var err error
		registered, err = tx.Devices.Register(ctx, db.RegisterDeviceParams{
			ID:                             newID(),
			UserID:                         principal.UserID(),
			APNsToken:                      token,
			Name:                           name,
			InteractionSchemaVersion:       interactionVersion,
			LiveActivityInteractionVersion: activityVersion,
			Now:                            now,
		})
		if err != nil {
			return err
		}
		// A token that belonged to another account cannot keep driving that
		// account's Live Activities on a phone that is now someone else's.
		if registered.OwnerChanged() {
			if _, err := tx.Deliveries.FailForDevice(ctx, registered.Device.ID, "OwnerChanged", now); err != nil {
				return err
			}
		}
		welcome, err = tx.Users.ClaimWelcome(ctx, principal.UserID(), now)
		return err
	})
	if err != nil {
		s.writeInternal(w, r, "registering a device failed", err)
		return
	}

	device := registered.Device
	if welcome {
		// The claim is what authorises the welcome, and it is deliberately not
		// released when the send fails: the welcome is one-shot per account for
		// all time, and a second copy is worse than none.
		device = s.sendWelcome(r, device)
	}
	WriteJSON(w, r, http.StatusCreated, deviceResponse{Device: newDeviceDTO(device)})
}

// sendWelcome delivers the one-shot welcome sequence, returning the device as it
// stands afterwards.
func (s *server) sendWelcome(r *http.Request, device db.Device) db.Device {
	result := s.opts.Push.SendAlerts(r.Context(), push.WelcomeAlerts(s.publicPath(""), push.Target{
		DeviceID: device.ID,
		Token:    device.APNsToken,
	}))
	if len(result.StaleTokens) == 0 {
		return device
	}

	// The very first push to a brand-new registration came back as
	// undeliverable, which means the token the app just handed over is already
	// dead. Say so in the response rather than reporting a healthy device.
	if _, err := s.store().Devices.Deactivate(detach(r.Context()), result.StaleTokens); err != nil {
		LoggerFrom(r.Context()).ErrorContext(r.Context(), "deactivating a stale device failed", "error", err)
	}
	device.Active = false
	return device
}

// handleDeleteDevice unregisters a phone.
//
// The row is deleted rather than deactivated, which takes its Live Activity
// deliveries with it without sending any end push — an activity can therefore
// stay on a screen with no record of it. That is the right trade for "this is
// not my phone any more": leaving a row behind would keep sending to it.
func (s *server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	deleted, err := s.store().Devices.Delete(r.Context(), r.PathValue("id"), principal.UserID())
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting a device failed", err)
	case !deleted:
		s.writeNotFound(w, r, "device")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type pushToStartTokenRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Version     *int   `json:"schema_version"`
}

// handleSetPushToStartToken records the ActivityKit push-to-start token.
//
// It is what makes a device Live-Activity-capable: without it the server can
// send alerts but cannot create anything on the Lock Screen. PUT because it is
// a replace — the phone re-reports the token whenever iOS reissues it, and the
// same value arriving twice must not be an error.
func (s *server) handleSetPushToStartToken(w http.ResponseWriter, r *http.Request) {
	var body pushToStartTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	token := v.hexToken("token", body.Token, minAPNsTokenLen, maxActivityTokenLen)
	environment := v.enum("environment", &body.Environment, environments, "")
	version := v.intRange("schema_version", body.Version,
		db.LiveActivitySchemaVersion, db.LiveActivitySchemaVersion, db.LiveActivitySchemaVersion)
	if !v.done(w, r) {
		return
	}

	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeActivityToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a push-to-start token failed", err)
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	if _, err := s.store().Devices.SetPushToStartToken(r.Context(), db.SetPushToStartTokenParams{
		DeviceID:      r.PathValue("id"),
		UserID:        principal.UserID(),
		Ciphertext:    ciphertext,
		Environment:   environment,
		SchemaVersion: version,
		Now:           s.now(),
	}); err != nil {
		s.writeStoreError(w, r, "active device", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// environments are the two APNs environments a token can belong to. A token
// minted in one is silently ignored by the other, so it travels with the token
// rather than being assumed from the server's own configuration.
var environments = []string{db.EnvironmentSandbox, db.EnvironmentProduction}

type activityUpdateTokenRequest struct {
	UpdateToken string `json:"update_token"`
	// NativeActivityID is ActivityKit's own identifier for the activity. It is
	// the only handle the phone has, which is why the association below has to
	// be inferred rather than looked up.
	NativeActivityID *string `json:"native_activity_id"`
	// ActivityID is Hark's id, when the client knows it. Supplying it turns the
	// inference into a lookup.
	ActivityID  *string `json:"activity_id"`
	Environment string  `json:"environment"`
	Version     *int    `json:"schema_version"`
}

type activityTokenResponse struct {
	ActivityID string `json:"activity_id"`
	DeliveryID string `json:"delivery_id"`
}

// handleRegisterUpdateToken associates an ActivityKit update token with the
// delivery it belongs to, for a signed-in app.
//
// A Live Activity started by push produces a per-activity update token on the
// phone, and nothing can be updated or ended until that token comes back. The
// phone cannot say which Hark activity it belongs to — it only knows
// ActivityKit's identifier — so the delivery is inferred: an exact match on a
// previously reported native id wins, and failing that the search is narrowed
// to deliveries still waiting to be associated.
//
// Two candidates is a refusal, not a guess. Attaching the token to the wrong
// activity would silently break both, and the phone can simply try again once
// the other pending start has resolved.
func (s *server) handleRegisterUpdateToken(w http.ResponseWriter, r *http.Request) {
	var body activityUpdateTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	token := v.hexToken("update_token", body.UpdateToken, minAPNsTokenLen, maxActivityTokenLen)
	nativeID := v.optionalText("native_activity_id", body.NativeActivityID, 200)
	activityID := v.optionalText("activity_id", body.ActivityID, maxIDLen)
	environment := v.enum("environment", &body.Environment, environments, "")
	version := v.intRange("schema_version", body.Version,
		db.LiveActivitySchemaVersion, db.LiveActivitySchemaVersion, db.LiveActivitySchemaVersion)
	if !v.done(w, r) {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	device, err := s.store().Devices.ByID(r.Context(), r.PathValue("id"), principal.UserID())
	if err != nil {
		s.writeStoreError(w, r, "device", err)
		return
	}
	if !device.Active {
		s.writeNotFound(w, r, "active device")
		return
	}

	candidates, err := s.store().Deliveries.AssociationCandidates(r.Context(), db.AssociationParams{
		DeviceID:         device.ID,
		UserID:           principal.UserID(),
		SchemaVersion:    version,
		ActivityID:       activityID,
		NativeActivityID: nativeID,
		Limit:            2,
	})
	switch {
	case err != nil:
		s.writeInternal(w, r, "matching a Live Activity delivery failed", err)
		return
	case len(candidates) == 0:
		s.writeNotFound(w, r, "Live Activity delivery")
		return
	case len(candidates) > 1:
		s.writeConflict(w, r,
			"More than one Live Activity is waiting for a token on this device. Retry once the other starts have resolved.")
		return
	}

	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeActivityToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a Live Activity update token failed", err)
		return
	}

	delivery, err := s.store().Deliveries.SetUpdateToken(r.Context(), db.SetUpdateTokenParams{
		DeliveryID:       candidates[0].ID,
		NativeActivityID: nativeID,
		Ciphertext:       ciphertext,
		Environment:      db.Value(environment),
		SchemaVersion:    db.Value(version),
		Now:              s.now(),
	})
	if err != nil {
		s.writeStoreError(w, r, "Live Activity delivery", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, activityTokenResponse{
		ActivityID: delivery.ActivityID,
		DeliveryID: delivery.ID,
	})
}

type deliveryUpdateTokenRequest struct {
	// RegistrationToken is the capability the start push carried. It is the
	// whole credential for this route.
	RegistrationToken string  `json:"registration_token"`
	NativeActivityID  *string `json:"native_activity_id"`
	UpdateToken       string  `json:"update_token"`
}

// handleDeliveryUpdateToken is the widget's path for reporting an update token.
//
// It takes no session because the process that has the token may not have one:
// a Live Activity outlives the app that started it, and the widget extension
// holds nothing but the attributes of the push that created it. The capability
// in the body is the credential — it is bound to this delivery, this activity
// and this deadline, so it grants exactly one thing and expires on its own.
//
// Every failure is the same 404, including a well-formed capability that does
// not verify, so the route cannot be used to discover which delivery ids exist.
func (s *server) handleDeliveryUpdateToken(w http.ResponseWriter, r *http.Request) {
	var body deliveryUpdateTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	var v validator
	token := v.hexToken("update_token", body.UpdateToken, minAPNsTokenLen, maxActivityTokenLen)
	nativeID := v.optionalText("native_activity_id", body.NativeActivityID, 200)
	capability := v.text("registration_token", body.RegistrationToken, 1, 128)
	if !v.done(w, r) {
		return
	}

	deliveryID := r.PathValue("id")
	registration, err := s.store().Deliveries.ForRegistration(r.Context(), deliveryID, s.now())
	if err != nil {
		s.writeStoreError(w, r, "Live Activity delivery", err)
		return
	}
	if !s.verifyRegistration(capability, registration.ID, registration.ActivityID, registration.ActivityExpiresAt) {
		s.writeNotFound(w, r, "Live Activity delivery")
		return
	}

	ciphertext, err := s.opts.Secrets.Encrypt(secret.PurposeActivityToken, token)
	if err != nil {
		s.writeInternal(w, r, "sealing a Live Activity update token failed", err)
		return
	}

	// The environment and schema version stay as the start recorded them: this
	// caller has no way to know them, and a start that reached the phone proves
	// the ones on file are right.
	if _, err := s.store().Deliveries.SetUpdateToken(r.Context(), db.SetUpdateTokenParams{
		DeliveryID:       registration.ID,
		NativeActivityID: nativeID,
		Ciphertext:       ciphertext,
		Now:              s.now(),
	}); err != nil {
		s.writeStoreError(w, r, "Live Activity delivery", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// registrationToken mints the capability a start push hands to the phone.
//
// It binds the delivery, its activity and the activity's deadline, so it cannot
// be replayed against another delivery and stops working when the activity
// does. Nothing is stored: the signature is the record.
func (s *server) registrationToken(deliveryID, activityID string, expiresAt time.Time) string {
	return s.opts.Secrets.Sign(secret.PurposeActivityRegistration,
		deliveryID, activityID, strconv.FormatInt(db.Millis(expiresAt).UnixMilli(), 10))
}

func (s *server) verifyRegistration(token, deliveryID, activityID string, expiresAt time.Time) bool {
	return s.opts.Secrets.Verify(secret.PurposeActivityRegistration, token,
		deliveryID, activityID, strconv.FormatInt(db.Millis(expiresAt).UnixMilli(), 10))
}

// capabilityVersion validates a reported feature version, which must be exactly
// the one this server speaks. A client announcing a version the server does not
// implement is refused rather than downgraded: the two would then disagree
// about what a payload means.
func (v *validator) capabilityVersion(field string, value *int, supported int) *int {
	if value == nil {
		return nil
	}
	if *value != supported {
		v.add(field, "must be "+strconv.Itoa(supported))
		return nil
	}
	return value
}
