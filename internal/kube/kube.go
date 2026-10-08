// Package kube is a minimal Kubernetes REST client for the gateway's own
// namespace: ConfigMaps (list/get/create/update/delete) and Secrets (get),
// authenticated with the pod's ServiceAccount token.
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

var ErrNotFound = errors.New("not found")

type ObjectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
}

type ConfigMap struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

type Client struct {
	base      string
	http      *http.Client
	token     func() (string, error)
	Namespace string
}

// New builds a client against base (e.g. an httptest server in tests).
func New(base string, hc *http.Client, namespace string, token func() (string, error)) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), http: hc, token: token, Namespace: namespace}
}

// InCluster uses the pod's ServiceAccount; the token is re-read per request
// because projected tokens rotate.
func InCluster() (*Client, error) {
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
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return nil, err
	}
	hc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
	token := func() (string, error) {
		b, err := os.ReadFile(saDir + "/token")
		return strings.TrimSpace(string(b)), err
	}
	return New("https://"+net.JoinHostPort(host, port), hc, strings.TrimSpace(string(ns)), token), nil
}

func (c *Client) cmPath(name string) string {
	p := "/api/v1/namespaces/" + url.PathEscape(c.Namespace) + "/configmaps"
	if name != "" {
		p += "/" + url.PathEscape(name)
	}
	return p
}

func (c *Client) ListConfigMaps(ctx context.Context, labelSelector string) ([]ConfigMap, error) {
	var out struct {
		Items []ConfigMap `json:"items"`
	}
	p := c.cmPath("") + "?labelSelector=" + url.QueryEscape(labelSelector)
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetConfigMap(ctx context.Context, name string) (ConfigMap, error) {
	var cm ConfigMap
	err := c.do(ctx, http.MethodGet, c.cmPath(name), nil, &cm)
	return cm, err
}

func (c *Client) CreateConfigMap(ctx context.Context, cm ConfigMap) (ConfigMap, error) {
	cm.APIVersion, cm.Kind = "v1", "ConfigMap"
	cm.Metadata.Namespace = c.Namespace
	var out ConfigMap
	err := c.do(ctx, http.MethodPost, c.cmPath(""), cm, &out)
	return out, err
}

// UpdateConfigMap replaces cm; Metadata.ResourceVersion guards against lost updates.
func (c *Client) UpdateConfigMap(ctx context.Context, cm ConfigMap) (ConfigMap, error) {
	cm.APIVersion, cm.Kind = "v1", "ConfigMap"
	cm.Metadata.Namespace = c.Namespace
	var out ConfigMap
	err := c.do(ctx, http.MethodPut, c.cmPath(cm.Metadata.Name), cm, &out)
	return out, err
}

func (c *Client) DeleteConfigMap(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, c.cmPath(name), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// GetSecretData returns the decoded data of a Secret in the namespace.
func (c *Client) GetSecretData(ctx context.Context, name string) (map[string][]byte, error) {
	var s struct {
		Data map[string][]byte `json:"data"`
	}
	p := "/api/v1/namespaces/" + url.PathEscape(c.Namespace) + "/secrets/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodGet, p, nil, &s); err != nil {
		return nil, err
	}
	return s.Data, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	tok, err := c.token()
	if err != nil {
		return fmt.Errorf("serviceaccount token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var st struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(msg, &st) == nil && st.Message != "" {
			return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, st.Message)
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
