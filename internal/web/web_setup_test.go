package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSetupTokenStableAcrossReloads(t *testing.T) {
	h, s := newWebServer(t)
	cookie, token := setupTokens(t, h)

	// A second request for /setup, such as a redirected favicon fetch or a
	// reload in another tab, must not replace the token the first page shows.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/setup", nil)
	req.AddCookie(&http.Cookie{Name: setupCookie, Value: cookie})
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("second setup load = %d", rec.Code)
	}
	var second string
	for _, c := range rec.Result().Cookies() {
		if c.Name == setupCookie {
			second = c.Value
		}
	}
	if second != cookie {
		t.Fatalf("setup cookie changed on reload: %q -> %q", cookie, second)
	}
	if !strings.Contains(rec.Body.String(), `value="`+token+`"`) {
		t.Error("reloaded page shows a different token")
	}

	resp := submitSetup(t, h, second, token, "admin", "setup-password-1", "setup-password-1")
	if resp.Code != 303 && resp.Code != 302 {
		t.Fatalf("setup after reload = %d, body: %s", resp.Code, resp.Body.String())
	}
	if n, _ := s.UserCount(); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestSetupFailureKeepsToken(t *testing.T) {
	h, _ := newWebServer(t)
	cookie, token := setupTokens(t, h)

	rec := submitSetup(t, h, cookie, token, "admin", "short", "short")
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var after string
	for _, c := range rec.Result().Cookies() {
		if c.Name == setupCookie {
			after = c.Value
		}
	}
	if after != cookie {
		t.Errorf("setup cookie changed after a validation error: %q -> %q", cookie, after)
	}
	if !strings.Contains(rec.Body.String(), `value="`+token+`"`) {
		t.Error("error page does not keep the token")
	}
}

func TestSetupMissingCookieIsReportedSeparately(t *testing.T) {
	h, _ := newWebServer(t)
	_, token := setupTokens(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/setup", strings.NewReader(url.Values{
		"setup_token":      {token},
		"username":         {"admin"},
		"password":         {"setup-password-1"},
		"confirm_password": {"setup-password-1"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "did not send the setup cookie") {
		t.Errorf("missing-cookie message not shown: %s", rec.Body.String())
	}
}
