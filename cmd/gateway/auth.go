package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/dasmlab/rf2vc/internal/kubeauth"
	"github.com/dasmlab/rf2vc/internal/redfish"
)

const (
	headerForwardedUser  = "X-Forwarded-User"
	headerForwardedEmail = "X-Forwarded-Email"
)

func isHealthz(path string) bool {
	return path == "/healthz"
}

func isRedfishPath(path string) bool {
	return path == "/redfish" || strings.HasPrefix(path, "/redfish/")
}

func basicAuth(creds *credentials, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthz(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Redfish 7.2.3: ServiceRoot (/redfish/v1/) and /redfish must not require auth.
		// Ironic/sushy probes these with no Authorization (python-requests) first.
		if r.Method == http.MethodGet && isPublicRedfishRoot(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		reason := "missing"
		if ok {
			ok, reason = creds.check(u, p, isRedfishPath(r.URL.Path))
		}
		if !ok {
			// Log before rejecting — BMH 401s previously looked like "no traffic"
			// because inbound activity only ran after auth succeeded.
			activity.InErr("auth", "401 Unauthorized", map[string]any{
				"method": r.Method,
				"path":   r.URL.Path,
				"remote": r.RemoteAddr,
				"ua":     r.UserAgent(),
				"reason": reason,
				"user":   u, // attempted username only (never password)
			})
			w.Header().Set("WWW-Authenticate", `Basic realm="rf2vc"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(redfish.WithClient(r.Context(), u)))
	})
}

type tokenReviewer interface {
	Review(ctx context.Context, token string) (string, error)
}

// tokenAuth guards the system-caller API listener: a Kubernetes bearer token
// (ServiceAccount) that may "get" the rf2vc Service.
func tokenAuth(rev tokenReviewer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthz(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		fail := func(code int, reason, user string) {
			activity.InErr("auth", fmt.Sprintf("%d %s", code, http.StatusText(code)), map[string]any{
				"method": r.Method,
				"path":   r.URL.Path,
				"remote": r.RemoteAddr,
				"reason": reason,
				"user":   user,
			})
			if code == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", `Bearer realm="rf2vc"`)
			}
			http.Error(w, http.StatusText(code), code)
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			fail(http.StatusUnauthorized, "missing-bearer", "")
			return
		}
		user, err := rev.Review(r.Context(), strings.TrimSpace(token))
		switch {
		case errors.Is(err, kubeauth.ErrUnauthenticated):
			fail(http.StatusUnauthorized, "invalid-token", "")
			return
		case errors.Is(err, kubeauth.ErrForbidden):
			fail(http.StatusForbidden, "forbidden", user)
			return
		case err != nil:
			log.Printf("token review: %v", err)
			fail(http.StatusServiceUnavailable, "review-failed", user)
			return
		}
		r.Header.Del(headerForwardedEmail)
		r.Header.Set(headerForwardedUser, user)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			activity.Run("api", r.Method+" "+r.URL.Path, map[string]any{"user": user})
		}
		next.ServeHTTP(w, r)
	})
}

// proxyAuth guards the UI listener. It trusts the identity headers set by the
// oauth-proxy sidecar, so that listener must only be reachable from the pod
// (loopback); anything else could forge X-Forwarded-User.
func proxyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthz(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		user := forwardedUser(r)
		if user == "" {
			http.Error(w, "Unauthorized: sign in through the rf2vc Route", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			activity.Run("ui", r.Method+" "+r.URL.Path, map[string]any{"user": user})
		}
		next.ServeHTTP(w, r)
	})
}

func forwardedUser(r *http.Request) string {
	if u := strings.TrimSpace(r.Header.Get(headerForwardedUser)); u != "" {
		return u
	}
	return strings.TrimSpace(r.Header.Get(headerForwardedEmail))
}

// whoami reports the caller. mode is "oauth" behind the proxy (sign-out at
// /oauth/sign_out), "token" for bearer callers and "basic" on the
// single-listener setup.
func whoami(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]string{"mode": mode}
		if mode != "basic" {
			out["user"] = forwardedUser(r)
			out["email"] = strings.TrimSpace(r.Header.Get(headerForwardedEmail))
		} else if u, _, ok := r.BasicAuth(); ok {
			out["user"] = u
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// writeHtpasswd writes a bcrypt htpasswd entry for the break-glass account so
// oauth-proxy can offer it on its sign-in page next to the cluster IdP.
func writeHtpasswd(path, user, pass string) error {
	if user == "" || pass == "" {
		return fmt.Errorf("RF2VC_AUTH_USERNAME and RF2VC_AUTH_PASSWORD are required")
	}
	if strings.Contains(user, ":") {
		return fmt.Errorf("username must not contain ':'")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(user+":"+string(hash)+"\n"), 0o600)
}

func warnIfNotLoopback(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("WARNING: uiListen %q is not host:port", addr)
		return
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return
	}
	log.Printf("WARNING: uiListen %q is not loopback; X-Forwarded-User can be forged by anything that reaches it", addr)
}
