package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

func runCheck(t *testing.T, r Runner, m store.Monitor, timeout time.Duration) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Run(ctx, m)
}

func cfgJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func httpMon(t *testing.T, cfg store.HTTPConfig) store.Monitor {
	t.Helper()
	if cfg.Method == "" {
		cfg.Method = "GET"
	}
	return store.Monitor{Type: store.TypeHTTP, Config: cfgJSON(t, cfg)}
}

func TestHTTPStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/echo" {
			w.Write([]byte("marker=" + r.Header.Get("X-Test")))
			return
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("boom"))
			return
		}
		w.Write([]byte("fine"))
	}))
	defer srv.Close()

	r := New(Options{})

	cases := []struct {
		name    string
		mon     store.Monitor
		wantOK  bool
		wantErr string
		wantCD  int
	}{
		{"ok", httpMon(t, store.HTTPConfig{URL: srv.URL}), true, "", 200},
		{"default expects 2xx", httpMon(t, store.HTTPConfig{URL: srv.URL + "/fail"}), false, "unexpected status code: 503", 503},
		{"custom expect", httpMon(t, store.HTTPConfig{URL: srv.URL + "/fail", ExpectStatus: "200,503"}), true, "", 503},
		{"body match hit", httpMon(t, store.HTTPConfig{URL: srv.URL + "/echo", Headers: map[string]string{"X-Test": "good"}, BodyMatch: "marker=good"}), true, "", 200},
		{"body match miss", httpMon(t, store.HTTPConfig{URL: srv.URL + "/echo", Headers: map[string]string{"X-Test": "good"}, BodyMatch: "marker=other"}), false, "body does not contain expected text", 200},
		{"regex hit", httpMon(t, store.HTTPConfig{URL: srv.URL + "/echo", Headers: map[string]string{"X-Test": "good"}, BodyRegex: "marker=g[o]od"}), true, "", 200},
		{"regex miss", httpMon(t, store.HTTPConfig{URL: srv.URL + "/echo", Headers: map[string]string{"X-Test": "good"}, BodyRegex: "marker=z[a-z]+"}), false, "body does not match expected pattern", 200},
		{"invalid regex", httpMon(t, store.HTTPConfig{URL: srv.URL, BodyRegex: "(["}), false, "invalid body_regex", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCheck(t, r, tc.mon, 10*time.Second)
			if res.OK != tc.wantOK {
				t.Errorf("ok = %v, err = %q", res.OK, res.Error)
			}
			if tc.wantErr != "" && !strings.Contains(res.Error, tc.wantErr) {
				t.Errorf("error = %q, want contains %q", res.Error, tc.wantErr)
			}
			if res.StatusCode != tc.wantCD {
				t.Errorf("status = %d, want %d", res.StatusCode, tc.wantCD)
			}
		})
	}
}

func TestHTTPRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	r := New(Options{})
	follow := true
	res := runCheck(t, r, httpMon(t, store.HTTPConfig{URL: srv.URL, FollowRedirects: &follow}), 10*time.Second)
	if !res.OK || res.StatusCode != 200 {
		t.Errorf("follow: ok=%v status=%d err=%q", res.OK, res.StatusCode, res.Error)
	}

	follow = false
	res = runCheck(t, r, httpMon(t, store.HTTPConfig{URL: srv.URL, FollowRedirects: &follow}), 10*time.Second)
	if res.OK || res.StatusCode != 302 {
		t.Errorf("no follow: ok=%v status=%d", res.OK, res.StatusCode)
	}
}

func TestHTTPTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	r := New(Options{})
	res := runCheck(t, r, httpMon(t, store.HTTPConfig{URL: srv.URL}), 300*time.Millisecond)
	if res.OK {
		t.Fatal("expected failure")
	}
	if res.Error != "request timed out" {
		t.Errorf("error = %q, want request timed out", res.Error)
	}
}

func TestSSRFBlocking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	r := New(Options{BlockPrivate: true})
	res := runCheck(t, r, httpMon(t, store.HTTPConfig{URL: srv.URL}), 10*time.Second)
	if res.OK {
		t.Fatal("loopback should be blocked")
	}
	if !strings.Contains(res.Error, "not allowed") && !strings.Contains(res.Error, "disallowed") {
		t.Errorf("error = %q, want blocked address message", res.Error)
	}

	tcpMon := store.Monitor{Type: store.TypeTCP, Config: cfgJSON(t, store.TCPConfig{Host: "127.0.0.1", Port: 1})}
	res = runCheck(t, r, tcpMon, 10*time.Second)
	if res.OK {
		t.Fatal("tcp loopback should be blocked")
	}

	r2 := New(Options{})
	res = runCheck(t, r2, httpMon(t, store.HTTPConfig{URL: srv.URL}), 10*time.Second)
	if !res.OK {
		t.Errorf("unprotected check failed: %q", res.Error)
	}
}

func TestTCPChecker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind listener: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	r := New(Options{})
	good := store.Monitor{Type: store.TypeTCP, Config: cfgJSON(t, store.TCPConfig{Host: "127.0.0.1", Port: port})}
	res := runCheck(t, r, good, 10*time.Second)
	if !res.OK {
		t.Errorf("open port failed: %q", res.Error)
	}

	ln.Close()
	res = runCheck(t, r, good, 10*time.Second)
	if res.OK {
		t.Error("closed port should fail")
	}
	if res.Error == "" {
		t.Error("closed port should report an error")
	}
}

func TestPingFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind listener: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	r := &localRunner{fallbackPorts: []int{port}}
	res := r.tcpFallback(context.Background(), "127.0.0.1")
	if !res.OK {
		t.Errorf("fallback to open port failed: %q", res.Error)
	}
	ln.Close()
	res = r.tcpFallback(context.Background(), "127.0.0.1")
	if res.OK {
		t.Error("fallback to closed ports should fail")
	}
}

func TestDescribeError(t *testing.T) {
	if got := describeError(context.DeadlineExceeded); got != "request timed out" {
		t.Errorf("timeout = %q", got)
	}
	dns := &net.DNSError{Err: "no such host", Name: "missing.example.com", IsNotFound: true}
	if got := describeError(dns); !strings.Contains(got, "dns lookup failed") {
		t.Errorf("dns = %q", got)
	}
	if got := describeError(nil); got != "unknown error" {
		t.Errorf("nil = %q", got)
	}
}

func TestSSLExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	cert := srv.Certificate()

	resp := &http.Response{TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	if got := sslExpiry(resp); got != cert.NotAfter.Unix() {
		t.Errorf("ssl expiry = %d, want %d", got, cert.NotAfter.Unix())
	}
	if got := sslExpiry(&http.Response{}); got != 0 {
		t.Errorf("plain response expiry = %d, want 0", got)
	}
}

func TestUnknownType(t *testing.T) {
	r := New(Options{})
	res := runCheck(t, r, store.Monitor{Type: "gopher"}, time.Second)
	if res.OK || !strings.Contains(res.Error, "unknown monitor type") {
		t.Errorf("res = %+v", res)
	}
}

func TestInvalidConfigPaths(t *testing.T) {
	r := New(Options{})
	cases := []struct {
		name string
		mon  store.Monitor
		want string
	}{
		{"http bad json", store.Monitor{Type: store.TypeHTTP, Config: []byte(`{`)}, "invalid config"},
		{"tcp bad json", store.Monitor{Type: store.TypeTCP, Config: []byte(`nope`)}, "invalid config"},
		{"ping bad json", store.Monitor{Type: store.TypePing, Config: []byte(`[1]`)}, "invalid config"},
		{"http bad url", store.Monitor{Type: store.TypeHTTP, Config: cfgJSON(t, store.HTTPConfig{URL: "ht tp://x"})}, "invalid request"},
		{"tcp bad port", store.Monitor{Type: store.TypeTCP, Config: []byte(`{"host":"a","port":0}`)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runCheck(t, r, tc.mon, 5*time.Second)
			if res.OK {
				t.Errorf("expected failure, got %+v", res)
			}
			if tc.want != "" && !strings.Contains(res.Error, tc.want) {
				t.Errorf("error = %q, want contains %q", res.Error, tc.want)
			}
		})
	}
}

func TestRunWithoutDeadline(t *testing.T) {
	r := New(Options{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Run adds its own fallback deadline when none exists; here one exists so the
	// context governs. Cover the dispatch path directly.
	res := r.Run(ctx, store.Monitor{Type: store.TypeHTTP, Config: cfgJSON(t, store.HTTPConfig{URL: srv.URL})})
	if !res.OK {
		t.Errorf("ok check failed: %q", res.Error)
	}
}
