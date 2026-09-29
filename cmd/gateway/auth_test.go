package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/dasmlab/rf2vc/internal/kubeauth"
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
	h := basicAuth(newCredentials("redfish", "pw", "", true), okHandler)
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

func basic(h http.Handler, method, path, user, pass string) int {
	r := httptest.NewRequest(method, path, nil)
	r.SetBasicAuth(user, pass)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestRedfishClients(t *testing.T) {
	dir := t.TempDir()
	for name, pass := range map[string]string{"bmh-mo-lab": "c1\n", "redfish": "shadowed", "..data": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(pass), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := basicAuth(newCredentials("redfish", "pw", dir, true), okHandler)
	cases := []struct {
		path, user, pass string
		want             int
	}{
		{"/redfish/v1/Systems", "bmh-mo-lab", "c1", http.StatusOK},
		{"/redfish/v1/Systems", "bmh-mo-lab", "wrong", http.StatusUnauthorized},
		{"/redfish/v1/Systems", "redfish", "pw", http.StatusOK}, // shared still allowed
		{"/redfish/v1/Systems", "redfish", "shadowed", http.StatusUnauthorized},
		{"/redfish/v1/Systems", "..data", "x", http.StatusUnauthorized},
		{"/api/v1/vcenters", "bmh-mo-lab", "c1", http.StatusUnauthorized}, // clients are Redfish-only
		{"/api/v1/vcenters", "redfish", "pw", http.StatusOK},
	}
	for _, c := range cases {
		if got := basic(h, http.MethodGet, c.path, c.user, c.pass); got != c.want {
			t.Errorf("%s %s/%s: got %d want %d", c.path, c.user, c.pass, got, c.want)
		}
	}

	strict := basicAuth(newCredentials("redfish", "pw", dir, false), okHandler)
	if got := basic(strict, http.MethodGet, "/redfish/v1/Systems", "redfish", "pw"); got != http.StatusUnauthorized {
		t.Errorf("shared disabled on redfish: got %d", got)
	}
	if got := basic(strict, http.MethodGet, "/redfish/v1/Systems", "bmh-mo-lab", "c1"); got != http.StatusOK {
		t.Errorf("client with shared disabled: got %d", got)
	}
	if got := basic(strict, http.MethodGet, "/", "redfish", "pw"); got != http.StatusOK {
		t.Errorf("shared on non-redfish path: got %d", got)
	}
}

func TestRedfishClientsRotation(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bmh-a")
	if err := os.WriteFile(f, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCredentials("redfish", "pw", dir, true)
	if ok, _ := c.check("bmh-a", "old", true); !ok {
		t.Fatal("old password rejected")
	}
	if err := os.WriteFile(f, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.loadedAt = c.loadedAt.Add(-redfishClientsReload)
	c.mu.Unlock()
	if ok, _ := c.check("bmh-a", "new", true); !ok {
		t.Fatal("rotated password rejected")
	}
	if ok, _ := c.check("bmh-a", "old", true); ok {
		t.Fatal("old password still accepted")
	}
}

type fakeReviewer map[string]struct {
	user string
	err  error
}

func (f fakeReviewer) Review(_ context.Context, token string) (string, error) {
	r, ok := f[token]
	if !ok {
		return "", kubeauth.ErrUnauthenticated
	}
	return r.user, r.err
}

func TestTokenAuth(t *testing.T) {
	var seen string
	h := tokenAuth(fakeReviewer{
		"good":   {user: "system:serviceaccount:rf2vc-system:rf2vc-api-client"},
		"nope":   {user: "system:serviceaccount:default:other", err: kubeauth.ErrForbidden},
		"broken": {err: errors.New("api down")},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = forwardedUser(r)
	}))
	cases := []struct {
		hdr  map[string]string
		want int
	}{
		{nil, http.StatusUnauthorized},
		{map[string]string{"Authorization": "Basic cmVkZmlzaDpwdw=="}, http.StatusUnauthorized},
		{map[string]string{"Authorization": "Bearer unknown"}, http.StatusUnauthorized},
		{map[string]string{"Authorization": "Bearer nope"}, http.StatusForbidden},
		{map[string]string{"Authorization": "Bearer broken"}, http.StatusServiceUnavailable},
		{map[string]string{headerForwardedUser: "mallory"}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if got := serve(h, http.MethodGet, "/api/v1/status", c.hdr); got != c.want {
			t.Errorf("%v: got %d want %d", c.hdr, got, c.want)
		}
	}
	got := serve(h, http.MethodPost, "/api/v1/mappings", map[string]string{
		"Authorization":     "Bearer good",
		headerForwardedUser: "mallory",
	})
	if got != http.StatusOK || seen != "system:serviceaccount:rf2vc-system:rf2vc-api-client" {
		t.Fatalf("good token: code %d user %q", got, seen)
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
