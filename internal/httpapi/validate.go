package httpapi

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Field limits. They live here rather than in the database because a value that
// is one character too long is a client mistake worth naming, and a column-level
// truncation would turn that into a silent corruption or a 500.
const (
	maxTitleLen  = 80
	maxBodyLen   = 2000
	maxStatusLen = 60
	maxDetailLen = 240
	maxLabelLen  = 24
	maxKeyLen    = 100
	maxReplyLen  = 4000
	maxURLLen    = 2048
	maxNameLen   = 80
	maxIDLen     = 100

	// maxDeviceIDs bounds an explicit device selection. An account with more
	// phones than this is not a case worth supporting, and an unbounded list is
	// an unbounded query.
	maxDeviceIDs = 50
)

// validator collects every problem with a request body so the response names
// all of them at once. A client fixing one field at a time across four round
// trips is a worse experience than one that sees the whole list.
type validator struct{ fields []FieldError }

// add records a problem with one field.
func (v *validator) add(field, message string) {
	v.fields = append(v.fields, FieldError{Field: field, Message: message})
}

// ok reports whether the body passed.
func (v *validator) ok() bool { return len(v.fields) == 0 }

// done writes the 422 when anything failed, and reports whether the handler may
// continue.
func (v *validator) done(w http.ResponseWriter, r *http.Request) bool {
	if v.ok() {
		return true
	}
	WriteFieldErrors(w, r, "The request body is invalid.", v.fields)
	return false
}

// text validates a required string, returning it trimmed.
func (v *validator) text(field, value string, minLen, maxLen int) string {
	trimmed := strings.TrimSpace(value)
	if n := utf8.RuneCountInString(trimmed); n < minLen || n > maxLen {
		v.add(field, lengthMessage(minLen, maxLen))
	}
	return trimmed
}

// optionalText validates a string that may be absent or explicitly null.
// A present-but-blank value is a client bug, not a way to clear a field: nulling
// is what clears.
func (v *validator) optionalText(field string, value *string, maxLen int) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		v.add(field, "must not be blank; send null to clear it")
		return nil
	}
	if utf8.RuneCountInString(trimmed) > maxLen {
		v.add(field, lengthMessage(1, maxLen))
	}
	return &trimmed
}

// label validates a button label: short, and one line. A control character in a
// label would break the rendering of a Lock Screen button rather than being
// shown.
func (v *validator) label(field string, value *string) *string {
	out := v.optionalText(field, value, maxLabelLen)
	if out == nil {
		return nil
	}
	for _, r := range *out {
		if r < 0x20 || r == 0x7f {
			v.add(field, "must be a single line of text")
			return nil
		}
	}
	return out
}

// enum validates a value against a closed set, substituting fallback when the
// field is absent. An unknown member is refused rather than coerced: silently
// accepting "urgent" as "normal" makes a typo look like it worked.
func (v *validator) enum(field string, value *string, allowed []string, fallback string) string {
	if value == nil {
		return fallback
	}
	got := strings.TrimSpace(*value)
	if !slices.Contains(allowed, got) {
		v.add(field, "must be one of "+strings.Join(allowed, ", "))
		return fallback
	}
	return got
}

// intRange validates an optional integer, substituting fallback when absent.
func (v *validator) intRange(field string, value *int, minValue, maxValue, fallback int) int {
	if value == nil {
		return fallback
	}
	if *value < minValue || *value > maxValue {
		v.add(field, rangeMessage(minValue, maxValue))
		return fallback
	}
	return *value
}

// fraction validates an optional 0…1 progress value.
func (v *validator) fraction(field string, value *float64) *float64 {
	if value == nil {
		return nil
	}
	if *value < 0 || *value > 1 {
		v.add(field, "must be between 0 and 1")
		return nil
	}
	return value
}

// ids validates a list of resource identifiers, returning it deduplicated and
// sorted so that the same selection always hashes the same way for idempotency.
func (v *validator) ids(field string, values []string) []string {
	if len(values) == 0 {
		return nil
	}
	if len(values) > maxDeviceIDs {
		v.add(field, rangeMessage(1, maxDeviceIDs)+" entries")
		return nil
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > maxIDLen {
			v.add(field, "must contain only non-empty identifiers")
			return nil
		}
		out = append(out, trimmed)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// hexToken validates an APNs or ActivityKit push token: hex, and long enough to
// be one.
func (v *validator) hexToken(field, value string, minLen, maxLen int) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if len(trimmed) < minLen || len(trimmed) > maxLen {
		v.add(field, rangeMessage(minLen, maxLen)+" hexadecimal characters")
		return trimmed
	}
	for i := range len(trimmed) {
		c := trimmed[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			v.add(field, "must be hexadecimal")
			return trimmed
		}
	}
	return trimmed
}

// accentColor validates a `#RRGGBB` colour.
func (v *validator) accentColor(field string, value *string) *string {
	if value == nil {
		return nil
	}
	got := strings.TrimSpace(*value)
	valid := len(got) == 7 && got[0] == '#'
	for i := 1; valid && i < len(got); i++ {
		c := got[i] | 0x20 // lowercase
		valid = c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
	}
	if !valid {
		v.add(field, "must be a hex colour such as #E13B3B")
		return nil
	}
	return &got
}

// httpsURL validates a URL the server or the phone will fetch.
//
// It must be public HTTPS. These URLs are dereferenced by something other than
// the caller — the phone loading an avatar, the server posting a callback — so
// accepting a private or loopback address would turn a request into one made on
// someone else's behalf, and accepting plain HTTP would put the traffic on the
// wire in the clear.
func (v *validator) httpsURL(field string, value *string) *string {
	raw := v.optionalText(field, value, maxURLLen)
	if raw == nil {
		return nil
	}
	if !publicHTTPSURL(*raw) {
		v.add(field, "must be a public HTTPS URL")
		return nil
	}
	return raw
}

func publicHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && publicHost(u.Hostname())
}

// ValidAvatarURL reports whether raw — trimmed, non-empty — is acceptable as
// an image_url: public HTTPS, within length. It is exported because the
// embedded dashboard holds its service forms to the same rule the API applies,
// rather than growing a second opinion on what an avatar may point at.
func ValidAvatarURL(raw string) bool {
	return raw != "" && utf8.RuneCountInString(raw) <= maxURLLen && publicHTTPSURL(raw)
}

// linkURL validates a tap destination.
//
// Any scheme is allowed except the ones that execute or embed content, because
// a deep link into another app is a legitimate destination and the server has
// no business deciding which apps exist.
func (v *validator) linkURL(field string, value *string) *string {
	raw := v.optionalText(field, value, maxURLLen)
	if raw == nil {
		return nil
	}
	if !tapURL(*raw) {
		v.add(field, "must be a web URL or an app deep link")
		return nil
	}
	return raw
}

func tapURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme != "" && linkSchemeAllowed(u.Scheme)
}

// ValidTapURL reports whether raw — trimmed, non-empty — is acceptable as a
// tap destination. Exported for the dashboard, like [ValidAvatarURL].
func ValidTapURL(raw string) bool {
	return raw != "" && utf8.RuneCountInString(raw) <= maxURLLen && tapURL(raw)
}

// blockedLinkSchemes execute script or carry inline content. A notification
// that can run something on tap is a notification that can be weaponised.
var blockedLinkSchemes = []string{"about", "blob", "data", "file", "javascript"}

func linkSchemeAllowed(scheme string) bool {
	return !slices.Contains(blockedLinkSchemes, strings.ToLower(scheme))
}

// publicHost reports whether a host is one the open internet can reach.
//
// The check is deliberately conservative and name-based as well as
// address-based: it cannot resolve DNS (that would be a request in itself, and
// the answer could change), so it rejects the names and literals that are
// unroutable by definition and lets everything else through.
func publicHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch {
	case host == "", host == "localhost",
		strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".local"):
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true // a name; only the unroutable suffixes above are refused
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() &&
		!isSharedAddressSpace(ip)
}

// isSharedAddressSpace covers RFC 6598 carrier-grade NAT, which net.IP does not
// classify but which is as unreachable from the internet as RFC 1918 is.
func isSharedAddressSpace(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func lengthMessage(minLen, maxLen int) string {
	if minLen == maxLen {
		return "must be exactly " + strconv.Itoa(minLen) + " characters"
	}
	return "must be " + strconv.Itoa(minLen) + "-" + strconv.Itoa(maxLen) + " characters"
}

func rangeMessage(minValue, maxValue int) string {
	return "must be between " + strconv.Itoa(minValue) + " and " + strconv.Itoa(maxValue)
}
