package httpapi

import (
	"net/http"
	"slices"
	"testing"
)

func TestTokenPictureIsOwnerSettable(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	var created createTokenResponse
	f.expect(http.MethodPost, "/tokens", f.session,
		`{"name":"deploy-bot","scopes":["notifications:send"]}`, http.StatusCreated, &created)
	if created.Token.ImageURL != nil {
		t.Fatalf("a hand-made token has image_url %q", *created.Token.ImageURL)
	}
	path := "/tokens/" + created.Token.ID

	var updated tokenResponse
	f.expect(http.MethodPatch, path, f.session, `{"image_url":" https://example.com/bot.png "}`, http.StatusOK, &updated)
	if updated.Token.ID != created.Token.ID || updated.Token.ImageURL == nil || *updated.Token.ImageURL != "https://example.com/bot.png" {
		t.Fatalf("updated token = %+v, want the trimmed picture", updated.Token)
	}
	var listed tokenListResponse
	f.expect(http.MethodGet, "/tokens", f.session, "", http.StatusOK, &listed)
	index := slices.IndexFunc(listed.Tokens, func(tok tokenDTO) bool { return tok.ID == created.Token.ID })
	if index < 0 || listed.Tokens[index].ImageURL == nil || *listed.Tokens[index].ImageURL != "https://example.com/bot.png" {
		t.Errorf("the listing does not carry the picture: %+v", listed.Tokens)
	}

	f.expect(http.MethodPatch, path, f.session, `{"image_url":null}`, http.StatusOK, &updated)
	if updated.Token.ImageURL != nil {
		t.Errorf("null did not remove the picture: %q", *updated.Token.ImageURL)
	}

	for name, body := range map[string]string{
		"absent":       `{}`,
		"plain http":   `{"image_url":"http://example.com/bot.png"}`,
		"private host": `{"image_url":"https://localhost/bot.png"}`,
		"not a URL":    `{"image_url":"bot.png"}`,
	} {
		rec := f.request(http.MethodPatch, path, f.session, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422: %s", name, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec); got.Error.Code != CodeValidation {
			t.Errorf("%s: code = %q, want %q", name, got.Error.Code, CodeValidation)
		}
	}

	if rec := f.request(http.MethodPatch, "/tokens/0198f3a1-2b4c-7d8e-9f01-23456789abcd", f.session, `{"image_url":null}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: status = %d, want 404: %s", rec.Code, rec.Body)
	}
	if rec := f.request(http.MethodPatch, path, created.Secret, `{"image_url":null}`); rec.Code != http.StatusForbidden {
		t.Errorf("an API token changing a picture: status = %d, want 403: %s", rec.Code, rec.Body)
	}
}
