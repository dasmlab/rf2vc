package main

import (
	"crypto/subtle"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dasmlab/rf2vc/internal/activity"
)

const redfishClientsReload = 15 * time.Second

// credentials validates Basic Auth: the shared (break-glass) account plus
// per-client Redfish credentials read from a directory (mounted Secret, one
// key per client). The directory is re-read so rotated Secrets apply without
// a restart.
type credentials struct {
	sharedUser, sharedPass string
	sharedRedfish          bool
	dir                    string

	mu         sync.Mutex
	loadedAt   time.Time
	clients    map[string]string
	lastErr    string
	lastWarned time.Time
}

func newCredentials(user, pass, dir string, sharedRedfish bool) *credentials {
	return &credentials{sharedUser: user, sharedPass: pass, dir: dir, sharedRedfish: sharedRedfish}
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// check returns ok when user/pass is accepted for the path, and reason
// ("bad-user" | "bad-password" | "shared-disabled") when not.
func (c *credentials) check(user, pass string, redfishPath bool) (ok bool, reason string) {
	if redfishPath {
		clients := c.clientsNow()
		if want, found := clients[user]; found {
			if equal(pass, want) {
				return true, ""
			}
			return false, "bad-password"
		}
		if user == c.sharedUser && !c.sharedRedfish {
			return false, "shared-disabled"
		}
		if user == c.sharedUser && equal(pass, c.sharedPass) && len(clients) > 0 {
			c.warnShared()
		}
	}
	if user != c.sharedUser {
		return false, "bad-user"
	}
	if !equal(pass, c.sharedPass) {
		return false, "bad-password"
	}
	return true, ""
}

// warnShared flags BMHs still on the shared account after per-client
// credentials exist (at most every 10 minutes).
func (c *credentials) warnShared() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastWarned) < 10*time.Minute {
		return
	}
	c.lastWarned = time.Now()
	activity.RunWarn("auth", "Redfish call used the shared account; move this BMH to its own client credential", map[string]any{
		"user": c.sharedUser,
	})
}

func (c *credentials) clientsNow() map[string]string {
	if c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients != nil && time.Since(c.loadedAt) < redfishClientsReload {
		return c.clients
	}
	clients, err := readClientsDir(c.dir, c.sharedUser)
	c.loadedAt = time.Now()
	if err != nil {
		if msg := err.Error(); msg != c.lastErr {
			log.Printf("redfish clients: %v", err)
			c.lastErr = msg
		}
		if c.clients == nil {
			c.clients = map[string]string{}
		}
		return c.clients
	}
	if len(clients) != len(c.clients) {
		log.Printf("redfish clients: %d loaded from %s", len(clients), c.dir)
	}
	c.lastErr = ""
	c.clients = clients
	return clients
}

// readClientsDir skips the kubelet's ..data bookkeeping entries and any entry
// named like the shared account (that name always means break-glass).
func readClientsDir(dir, sharedUser string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == sharedUser {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // directories and dangling links
		}
		if pass := strings.TrimRight(string(b), "\r\n"); pass != "" {
			out[name] = pass
		}
	}
	return out, nil
}
