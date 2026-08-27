package httpapi

import (
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
	"github.com/abdeen-labs/hark/internal/db"
)

// eventDTO is one webhook delivery as every response renders it.
type eventDTO struct {
	ID        string `json:"id"`
	ServiceID string `json:"service_id"`
	// ServiceName is denormalised into the row because a delivery log is read
	// far more often than services are renamed, and a list of events with no
	// sender names is unreadable.
	ServiceName string  `json:"service_name"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	ImageURL    *string `json:"image_url"`
	URL         *string `json:"url"`
	Priority    string  `json:"priority"`
	Status      string  `json:"status"`
	// DeliveredCount counts the messages APNs accepted. It says nothing about
	// what the phone did with them.
	DeliveredCount int `json:"delivered_count"`
	// Error is the provider's own failure text. It is owner-only — an APNs error
	// can embed a device token — which is why it appears here and never in the
	// response to the webhook caller that triggered it.
	Error     *string   `json:"error"`
	CreatedAt Timestamp `json:"created_at"`
}

func newEventDTO(e db.Event, serviceName string) eventDTO {
	return eventDTO{
		ID:             e.ID,
		ServiceID:      e.ServiceID,
		ServiceName:    serviceName,
		Title:          e.Title,
		Body:           e.Body,
		ImageURL:       e.ImageURL,
		URL:            e.URL,
		Priority:       e.Priority,
		Status:         e.Status,
		DeliveredCount: e.DeliveredCount,
		Error:          e.Error,
		CreatedAt:      Timestamp(e.CreatedAt),
	}
}

type eventListResponse struct {
	Events     []eventDTO `json:"events"`
	NextCursor *string    `json:"next_cursor"`
}

// handleListEvents pages the account's webhook deliveries, newest first.
func (s *server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	query, ok := s.parseList(w, r)
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	page, err := s.store().Events.ListForUser(r.Context(), principal.UserID(), query.Cursor, query.Limit)
	if err != nil {
		s.writeInternal(w, r, "listing events failed", err)
		return
	}

	out := make([]eventDTO, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, newEventDTO(item.Event, item.ServiceTitle))
	}
	WriteJSON(w, r, http.StatusOK, eventListResponse{Events: out, NextCursor: nextCursor(page)})
}

// handleDeleteEvent removes one delivery from the account's history.
//
// The cascade takes the question that delivery asked with it, which is the
// point: an owner deleting a notification means "this never happened to me",
// and leaving an orphaned prompt behind would contradict that.
func (s *server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	deleted, err := s.store().Events.Delete(r.Context(), r.PathValue("id"), principal.UserID())
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting an event failed", err)
	case !deleted:
		s.writeNotFound(w, r, "event")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// historyItemDTO is one entry in the unified history.
//
// The four sources have little in common, so most fields are nullable and a
// client reads whichever ones its kind populates. Keeping them in one shape is
// what lets a phone render a single scrolling list without knowing that a
// webhook event and an agent push come from different tables.
type historyItemDTO struct {
	// ID is "<source>:<row id>" — the handle a delete takes, which is why it
	// names the source and not just the row.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// SourceName is the service title, token name, or fallback sender name.
	SourceName     string  `json:"source_name"`
	SourceImageURL *string `json:"source_image_url"`
	Title          string  `json:"title"`
	Detail         *string `json:"detail"`
	URL            *string `json:"url"`
	// Result is the outcome for the kinds that have one: how a question was
	// answered, or which change a Live Activity entry describes.
	Result *string `json:"result"`
	// Status, DeliveredCount, Error and Priority are populated for
	// notifications only.
	Status         *string   `json:"status"`
	DeliveredCount *int      `json:"delivered_count"`
	Error          *string   `json:"error"`
	Priority       *string   `json:"priority"`
	CreatedAt      Timestamp `json:"created_at"`
}

func newHistoryItemDTO(i db.FeedItem) historyItemDTO {
	return historyItemDTO{
		ID:             i.ID,
		Kind:           i.Kind,
		SourceName:     i.SourceName,
		SourceImageURL: i.SourceImageURL,
		Title:          i.Title,
		Detail:         i.Detail,
		URL:            i.URL,
		Result:         i.Result,
		Status:         i.Status,
		DeliveredCount: i.DeliveredCount,
		Error:          i.Error,
		Priority:       i.Priority,
		CreatedAt:      Timestamp(i.CreatedAt),
	}
}

type historyListResponse struct {
	Items      []historyItemDTO `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

// parseHistoryFilters reads the `kind`, `source` and `priority` query
// parameters the history list and its bulk delete share, writing the error
// response itself.
func (s *server) parseHistoryFilters(w http.ResponseWriter, r *http.Request) (db.FeedFilters, bool) {
	q := r.URL.Query()
	filters := db.FeedFilters{
		Kind:     q.Get("kind"),
		Source:   q.Get("source"),
		Priority: q.Get("priority"),
	}
	if filters.Kind == "" {
		filters.Kind = db.FeedFilterAll
	}

	var fields []FieldError
	if !db.ValidFeedFilter(filters.Kind) {
		fields = append(fields, FieldError{
			Field:   "kind",
			Message: "must be one of all, notification, response, live_activity",
		})
	}
	if filters.Priority != "" && !db.ValidRecordedPriority(filters.Priority) {
		fields = append(fields, FieldError{
			Field:   "priority",
			Message: "must be one of normal, time_sensitive, critical",
		})
	}
	if len(fields) > 0 {
		WriteFieldErrors(w, r, "The request query is invalid.", fields)
		return db.FeedFilters{}, false
	}
	return filters, true
}

// handleListHistory pages everything that has happened to the account, newest
// first: webhook deliveries, agent pushes, answered questions and Live Activity
// changes, in one ordering.
//
// An answered question is placed by when it was answered rather than when it was
// asked, because that is where a person looking back expects to find it.
func (s *server) handleListHistory(w http.ResponseWriter, r *http.Request) {
	query, ok := s.parseList(w, r)
	if !ok {
		return
	}
	filters, ok := s.parseHistoryFilters(w, r)
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	page, err := s.store().Feed.ListFiltered(r.Context(), principal.UserID(), filters, query.Cursor, query.Limit)
	if err != nil {
		s.writeInternal(w, r, "listing history failed", err)
		return
	}

	out := make([]historyItemDTO, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, newHistoryItemDTO(item))
	}
	WriteJSON(w, r, http.StatusOK, historyListResponse{Items: out, NextCursor: nextCursor(page)})
}

type historySourcesResponse struct {
	Sources []string `json:"sources"`
}

// handleListHistorySources lists the distinct sender names currently in the
// history, for a client building a source filter. The list is bounded by the
// account's services, tokens and safety sources, so it is not paged.
func (s *server) handleListHistorySources(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())
	sources, err := s.store().Feed.Sources(r.Context(), principal.UserID())
	if err != nil {
		s.writeInternal(w, r, "listing history sources failed", err)
		return
	}
	WriteJSON(w, r, http.StatusOK, historySourcesResponse{Sources: sources})
}

// handleDeleteHistory clears the history, or the slice of it the filters name.
// It takes the same parameters as the list, so a client deletes exactly what it
// is looking at.
func (s *server) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	filters, ok := s.parseHistoryFilters(w, r)
	if !ok {
		return
	}

	principal := auth.PrincipalFrom(r.Context())
	if err := s.store().Feed.DeleteAll(r.Context(), principal.UserID(), filters); err != nil {
		s.writeInternal(w, r, "deleting history failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteHistoryItem removes one entry from the history.
//
// The composite id says which table to touch, and each has its own ownership
// guard. A question still awaiting an answer cannot be deleted: making a live
// prompt vanish from a phone by deleting a history row would be a surprising
// amount of power for a list view.
func (s *server) handleDeleteHistoryItem(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFrom(r.Context())

	deleted, err := s.store().Feed.Delete(r.Context(), principal.UserID(), r.PathValue("id"))
	switch {
	case err != nil:
		s.writeInternal(w, r, "deleting a history item failed", err)
	case !deleted:
		s.writeNotFound(w, r, "history item")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
