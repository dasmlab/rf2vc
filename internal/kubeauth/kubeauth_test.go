package kubeauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeAPI accepts tokens "<user>|allow" or "<user>|deny"; v1 SelfSubjectReview
// can be switched off to exercise the v1beta1 fallback.
func fakeAPI(t *testing.T, v1 bool, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		user, verdict, ok := strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "|")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/apis/authentication.k8s.io/v1/selfsubjectreviews", "/apis/authentication.k8s.io/v1beta1/selfsubjectreviews":
			if !v1 && strings.Contains(r.URL.Path, "/v1/") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"userInfo": map[string]any{"username": user}}})
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var body struct {
				Spec struct {
					ResourceAttributes ResourceAttributes `json:"resourceAttributes"`
				} `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			a := body.Spec.ResourceAttributes
			if a.Name != "rf2vc" || a.Namespace != "rf2vc-system" || a.Verb != "get" || a.Resource != "services" {
				t.Errorf("unexpected attributes %+v", a)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"allowed": verdict == "allow"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

var attrs = ResourceAttributes{Namespace: "rf2vc-system", Verb: "get", Resource: "services", Name: "rf2vc"}

func TestReview(t *testing.T) {
	for _, v1 := range []bool{true, false} {
		var calls int32
		srv := fakeAPI(t, v1, &calls)
		r := New(srv.URL, srv.Client(), attrs)
		ctx := context.Background()

		user, err := r.Review(ctx, "system:serviceaccount:rf2vc-system:client|allow")
		if err != nil || user != "system:serviceaccount:rf2vc-system:client" {
			t.Fatalf("v1=%v allow: %q %v", v1, user, err)
		}
		before := atomic.LoadInt32(&calls)
		if _, err := r.Review(ctx, "system:serviceaccount:rf2vc-system:client|allow"); err != nil {
			t.Fatal(err)
		}
		if atomic.LoadInt32(&calls) != before {
			t.Fatalf("v1=%v: cached token hit the API again", v1)
		}
		if user, err := r.Review(ctx, "bob|deny"); !errors.Is(err, ErrForbidden) || user != "bob" {
			t.Fatalf("v1=%v deny: %q %v", v1, user, err)
		}
		if _, err := r.Review(ctx, "garbage"); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("v1=%v invalid: %v", v1, err)
		}
		srv.Close()
	}
}
