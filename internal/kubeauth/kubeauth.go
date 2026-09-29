// Package kubeauth authenticates Kubernetes bearer tokens (e.g. ServiceAccount
// tokens) by calling the API server *with the caller's token*:
// SelfSubjectReview for the identity and SelfSubjectAccessReview for the
// permission. Any valid token may create both, so the gateway's own
// ServiceAccount needs no tokenreviews / system:auth-delegator rights.
package kubeauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var (
	ErrUnauthenticated = errors.New("token not accepted by the Kubernetes API")
	ErrForbidden       = errors.New("token lacks the required permission")
)

// ResourceAttributes is the permission a caller must hold (authorization.k8s.io/v1).
type ResourceAttributes struct {
	Namespace string `json:"namespace,omitempty"`
	Verb      string `json:"verb"`
	Group     string `json:"group,omitempty"`
	Resource  string `json:"resource"`
	Name      string `json:"name,omitempty"`
}

func (a ResourceAttributes) String() string {
	return fmt.Sprintf("%s %s/%s in %s", a.Verb, a.Resource, a.Name, a.Namespace)
}

type Reviewer struct {
	baseURL string
	client  *http.Client
	attrs   ResourceAttributes
	ttl     time.Duration

	mu    sync.Mutex
	cache map[[32]byte]cached
}

type cached struct {
	user    string
	expires time.Time
}

func New(baseURL string, client *http.Client, attrs ResourceAttributes) *Reviewer {
	return &Reviewer{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client:  client,
		attrs:   attrs,
		ttl:     30 * time.Second,
		cache:   map[[32]byte]cached{},
	}
}

// InCluster talks to the API server from the pod. An empty attrs.Namespace
// defaults to the pod's namespace.
func InCluster(attrs ResourceAttributes) (*Reviewer, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster (KUBERNETES_SERVICE_HOST unset)")
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("no certificates in " + saDir + "/ca.crt")
	}
	if attrs.Namespace == "" {
		ns, err := os.ReadFile(saDir + "/namespace")
		if err != nil {
			return nil, err
		}
		attrs.Namespace = strings.TrimSpace(string(ns))
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	return New("https://"+net.JoinHostPort(host, port), client, attrs), nil
}

func (r *Reviewer) Attributes() ResourceAttributes { return r.attrs }

// Review returns the token's username if the token is valid and allowed.
func (r *Reviewer) Review(ctx context.Context, token string) (string, error) {
	key := sha256.Sum256([]byte(token))
	now := time.Now()
	r.mu.Lock()
	if c, ok := r.cache[key]; ok && now.Before(c.expires) {
		r.mu.Unlock()
		return c.user, nil
	}
	r.mu.Unlock()

	user, err := r.whoami(ctx, token)
	if err != nil {
		return "", err
	}
	allowed, err := r.allowed(ctx, token)
	if err != nil {
		return user, err
	}
	if !allowed {
		return user, ErrForbidden
	}

	r.mu.Lock()
	if len(r.cache) > 1024 {
		for k, c := range r.cache {
			if now.After(c.expires) {
				delete(r.cache, k)
			}
		}
	}
	r.cache[key] = cached{user: user, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return user, nil
}

func (r *Reviewer) whoami(ctx context.Context, token string) (string, error) {
	// SelfSubjectReview is GA in Kubernetes 1.28 (OpenShift 4.15); beta before.
	for _, v := range []string{"v1", "v1beta1"} {
		var out struct {
			Status struct {
				UserInfo struct {
					Username string `json:"username"`
				} `json:"userInfo"`
			} `json:"status"`
		}
		body := map[string]any{"apiVersion": "authentication.k8s.io/" + v, "kind": "SelfSubjectReview"}
		code, err := r.post(ctx, token, "/apis/authentication.k8s.io/"+v+"/selfsubjectreviews", body, &out)
		if err != nil {
			return "", err
		}
		if code == http.StatusNotFound {
			continue
		}
		if out.Status.UserInfo.Username == "" {
			return "", errors.New("SelfSubjectReview returned no username")
		}
		return out.Status.UserInfo.Username, nil
	}
	return "", errors.New("SelfSubjectReview API not available")
}

func (r *Reviewer) allowed(ctx context.Context, token string) (bool, error) {
	var out struct {
		Status struct {
			Allowed bool `json:"allowed"`
		} `json:"status"`
	}
	body := map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec":       map[string]any{"resourceAttributes": r.attrs},
	}
	code, err := r.post(ctx, token, "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", body, &out)
	if err != nil {
		return false, err
	}
	if code == http.StatusNotFound {
		return false, errors.New("SelfSubjectAccessReview API not available")
	}
	return out.Status.Allowed, nil
}

// post returns the HTTP status for 2xx and 404; 401 maps to ErrUnauthenticated.
func (r *Reviewer) post(ctx context.Context, token, path string, in, out any) (int, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp.StatusCode, ErrUnauthenticated
	case resp.StatusCode == http.StatusNotFound:
		return resp.StatusCode, nil
	case resp.StatusCode/100 != 2:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}
