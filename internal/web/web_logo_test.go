package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFaviconStaticAsset(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/favicon.svg", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("favicon asset status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("favicon Content-Type = %q, want svg", ct)
	}
}

func TestLoginHasFaviconLink(t *testing.T) {
	h, s := newWebServer(t)
	createBootstrapUser(t, s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`rel="icon"`, "favicon.svg", `class="brand-logo"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}

func TestFaviconIcoBeforeSetup(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("favicon.ico status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("favicon.ico Content-Type = %q, want image/svg+xml", ct)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("favicon.ico redirected to %q", loc)
	}
}

func TestAuthPagesIncludeLogo(t *testing.T) {
	h, _ := newWebServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="auth-logo"`) {
		t.Error("setup page missing auth logo")
	}

	h2, s2 := newWebServer(t)
	createBootstrapUser(t, s2)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("login status = %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), `class="auth-logo"`) {
		t.Error("login page missing auth logo")
	}
}
