package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func serve(h http.Handler, method, path string, hdr map[string]string) int {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestProxyAuth(t *testing.T) {
	h := proxyAuth(okHandler)
	if c := serve(h, http.MethodGet, "/api/v1/vcenters", nil); c != http.StatusUnauthorized {
		t.Fatalf("no identity: got %d", c)
	}
	if c := serve(h, http.MethodGet, "/healthz", nil); c != http.StatusOK {
		t.Fatalf("healthz: got %d", c)
	}
	if c := serve(h, http.MethodPut, "/api/v1/settings", map[string]string{headerForwardedUser: "alice"}); c != http.StatusOK {
		t.Fatalf("with user: got %d", c)
	}
	if c := serve(h, http.MethodGet, "/", map[string]string{headerForwardedEmail: "bob@example.com"}); c != http.StatusOK {
		t.Fatalf("with email only: got %d", c)
	}
}

func TestBasicAuthRedfish(t *testing.T) {
	h := basicAuth("redfish", "pw", okHandler)
	if c := serve(h, http.MethodGet, "/redfish/v1/", nil); c != http.StatusOK {
		t.Fatalf("public service root: got %d", c)
	}
	if c := serve(h, http.MethodGet, "/redfish/v1/Systems", nil); c != http.StatusUnauthorized {
		t.Fatalf("no creds: got %d", c)
	}
	r := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems", nil)
	r.SetBasicAuth("redfish", "pw")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("good creds: got %d", w.Code)
	}
	if c := serve(h, http.MethodGet, "/api/v1/vcenters", map[string]string{headerForwardedUser: "mallory"}); c != http.StatusUnauthorized {
		t.Fatalf("forged proxy header must not bypass basic auth: got %d", c)
	}
}

func TestWhoami(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	r.Header.Set(headerForwardedUser, "alice")
	r.Header.Set(headerForwardedEmail, "alice@example.com")
	w := httptest.NewRecorder()
	whoami("oauth")(w, r)
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["mode"] != "oauth" || got["user"] != "alice" || got["email"] != "alice@example.com" {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestWriteHtpasswd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users")
	if err := writeHtpasswd(p, "redfish", "s3cret"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	user, hash, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok || user != "redfish" {
		t.Fatalf("bad line: %q", b)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret")); err != nil {
		t.Fatalf("hash does not verify: %v", err)
	}
	if err := writeHtpasswd(p, "", "x"); err == nil {
		t.Fatal("want error for empty user")
	}
}
