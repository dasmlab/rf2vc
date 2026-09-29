package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/dasmlab/rf2vc/internal/activity"
)

const (
	headerForwardedUser  = "X-Forwarded-User"
	headerForwardedEmail = "X-Forwarded-Email"
)

func isHealthz(path string) bool {
	return path == "/healthz"
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
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
		if !ok || u != user || p != pass {
			reason := "missing"
			if ok {
				if u != user {
					reason = "bad-user"
				} else {
					reason = "bad-password"
				}
			}
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

// whoami reports the signed-in dashboard user. mode is "oauth" behind the
// proxy (sign-out at /oauth/sign_out) and "basic" on the single-listener setup.
func whoami(mode string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]string{"mode": mode}
		if mode == "oauth" {
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
