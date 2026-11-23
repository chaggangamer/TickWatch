package web

import (
	"net/http"

	"github.com/chaggangamer/tickwatch/internal/store"
)

// setupTokenFor returns the token already stored in the visitor's setup cookie
// when it looks valid, so that extra requests for /setup (a redirected favicon
// fetch, a second tab, a reload) do not invalidate the form already on screen.
func setupTokenFor(r *http.Request) string {
	if c, err := r.Cookie(setupCookie); err == nil && validSetupToken(c.Value) {
		return c.Value
	}
	return store.NewToken(16)
}

func validSetupToken(v string) bool {
	if len(v) < 16 || len(v) > 128 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func setSetupCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     setupCookie,
		Value:    token,
		Path:     "/setup",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   600,
	})
}
