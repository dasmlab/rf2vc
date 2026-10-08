package vcsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dasmlab/rf2vc/internal/kube"
	"github.com/dasmlab/rf2vc/internal/store"
)

// fakeAPI serves ConfigMaps and Secrets for one namespace.
type fakeAPI struct {
	mu      sync.Mutex
	cms     map[string]kube.ConfigMap
	secrets map[string]map[string][]byte
	rv      int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	const cmBase = "/api/v1/namespaces/rf2vc-system/configmaps"
	const secBase = "/api/v1/namespaces/rf2vc-system/secrets/"
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, secBase) && r.Method == http.MethodGet:
		d, ok := f.secrets[strings.TrimPrefix(p, secBase)]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": d})
	case p == cmBase && r.Method == http.MethodGet:
		k, v, _ := strings.Cut(r.URL.Query().Get("labelSelector"), "=")
		items := []kube.ConfigMap{}
		for _, cm := range f.cms {
			if cm.Metadata.Labels[k] == v {
				items = append(items, cm)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case p == cmBase && r.Method == http.MethodPost:
		var cm kube.ConfigMap
		_ = json.NewDecoder(r.Body).Decode(&cm)
		if _, ok := f.cms[cm.Metadata.Name]; ok {
			http.Error(w, `{"message":"already exists"}`, http.StatusConflict)
			return
		}
		f.put(cm)
		_ = json.NewEncoder(w).Encode(f.cms[cm.Metadata.Name])
	case strings.HasPrefix(p, cmBase+"/"):
		name := strings.TrimPrefix(p, cmBase+"/")
		cur, ok := f.cms[name]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(cur)
		case http.MethodPut:
			var cm kube.ConfigMap
			_ = json.NewDecoder(r.Body).Decode(&cm)
			if cm.Metadata.ResourceVersion != cur.Metadata.ResourceVersion {
				http.Error(w, `{"message":"conflict"}`, http.StatusConflict)
				return
			}
			f.put(cm)
			_ = json.NewEncoder(w).Encode(f.cms[name])
		case http.MethodDelete:
			delete(f.cms, name)
			_, _ = w.Write([]byte("{}"))
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) put(cm kube.ConfigMap) {
	f.rv++
	cm.Metadata.ResourceVersion = string(rune('0' + f.rv%10))
	f.cms[cm.Metadata.Name] = cm
}

func setup(t *testing.T) (*fakeAPI, *store.Store, *Sync) {
	t.Helper()
	f := &fakeAPI{cms: map[string]kube.ConfigMap{}, secrets: map[string]map[string][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kc := kube.New(srv.URL, srv.Client(), "rf2vc-system", func() (string, error) { return "tok", nil })
	return f, st, New(kc, st)
}

func gitopsCM(name, body string) kube.ConfigMap {
	return kube.ConfigMap{
		Metadata: kube.ObjectMeta{Name: name, Labels: map[string]string{LabelVCenter: "true"}},
		Data:     map[string]string{DataKey: body},
	}
}

const labSpec = `name: lab
url: https://vc.lab/sdk
username: admin@vsphere.local
datacenter: DC0
datastore: ds0
folder: /DC0/vm
credentialsSecret:
  name: vc-lab-creds
`

func TestGitOpsConfigMapPasswordFromSecret(t *testing.T) {
	f, st, s := setup(t)
	f.cms["vc-lab"] = gitopsCM("vc-lab", labSpec)
	f.secrets["vc-lab-creds"] = map[string][]byte{"password": []byte("s3cret")}
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	vcs := st.ListVCenters()
	if len(vcs) != 1 {
		t.Fatalf("want 1 vCenter, got %d", len(vcs))
	}
	vc, _ := st.GetVCenterSecret(vcs[0].ID)
	if vc.Password != "s3cret" || vc.PasswordSource != store.PasswordFromSecret {
		t.Fatalf("password from secret: %+v", vc)
	}
	if vc.ConfigMap != "vc-lab" || vc.ConfigOrigin != store.OriginGitOps || vc.Folder != "/DC0/vm" {
		t.Fatalf("configmap fields: %+v", vc)
	}
	if err := s.CheckDelete(vc.ID); err == nil {
		t.Fatal("deleting a GitOps vCenter must be refused")
	}
	// UI edit of a GitOps vCenter never writes its ConfigMap.
	before := f.cms["vc-lab"].Data[DataKey]
	if _, err := st.UpsertVCenter(store.VCenter{ID: vc.ID, Name: "lab", URL: vc.URL, Username: vc.Username, Datacenter: "DC0", Notes: "ui"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Saved(context.Background(), vc.ID); err != nil {
		t.Fatal(err)
	}
	if f.cms["vc-lab"].Data[DataKey] != before || len(f.cms) != 1 {
		t.Fatal("GitOps ConfigMap was modified")
	}
}

func TestManualPasswordKeptUntilSecretExists(t *testing.T) {
	f, st, s := setup(t)
	f.cms["vc-lab"] = gitopsCM("vc-lab", labSpec)
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	vc := st.ListVCenters()[0]
	if vc.HasPassword {
		t.Fatal("no secret and no UI password: should have no password")
	}
	// Operator types the password in the UI.
	if _, err := st.UpsertVCenter(store.VCenter{ID: vc.ID, Name: vc.Name, URL: vc.URL, Username: vc.Username, Datacenter: vc.Datacenter, Password: "typed"}); err != nil {
		t.Fatal(err)
	}
	// Restart: ConfigMap re-applied, still no Secret -> typed password survives.
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetVCenterSecret(vc.ID)
	if got.Password != "typed" || got.PasswordSource != store.PasswordManual {
		t.Fatalf("manual password lost: %+v", got)
	}
	// VSO creates the Secret: it wins on the next start.
	f.secrets["vc-lab-creds"] = map[string][]byte{"password": []byte("from-vso")}
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetVCenterSecret(vc.ID)
	if got.Password != "from-vso" || got.PasswordSource != store.PasswordFromSecret {
		t.Fatalf("secret should win: %+v", got)
	}
}

func TestUIVCenterWritesRuntimeConfigMap(t *testing.T) {
	f, st, s := setup(t)
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	vc, err := st.UpsertVCenter(store.VCenter{Name: "New VC #2", URL: "https://vc2/sdk", Username: "u", Password: "p", Datacenter: "DC1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Saved(context.Background(), vc.ID); err != nil {
		t.Fatal(err)
	}
	cm, ok := f.cms["rf2vc-vc-new-vc-2"]
	if !ok {
		t.Fatalf("runtime ConfigMap not created: %v", keys(f.cms))
	}
	if cm.Metadata.Labels[LabelOrigin] != store.OriginRuntime || cm.Metadata.Labels[LabelVCenter] != "true" {
		t.Fatalf("labels: %v", cm.Metadata.Labels)
	}
	if strings.Contains(cm.Data[DataKey], "\"p\"") || strings.Contains(cm.Data[DataKey], ": p\n") {
		t.Fatal("password leaked into ConfigMap")
	}
	var spec Spec
	if err := yaml.Unmarshal([]byte(cm.Data[DataKey]), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ID != vc.ID || spec.CredentialsSecret == nil || spec.CredentialsSecret.Name != "rf2vc-vc-new-vc-2" {
		t.Fatalf("spec: %+v", spec)
	}
	// Edit updates the same ConfigMap.
	if _, err := st.UpsertVCenter(store.VCenter{ID: vc.ID, Name: "New VC #2", URL: "https://vc2/sdk", Username: "u", Datacenter: "DC1", Folder: "/DC1/vm"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Saved(context.Background(), vc.ID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.cms["rf2vc-vc-new-vc-2"].Data[DataKey], "/DC1/vm") || len(f.cms) != 1 {
		t.Fatalf("edit not written: %v", f.cms["rf2vc-vc-new-vc-2"].Data)
	}
	// Restart keeps the same vCenter (id) and its UI password.
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(st.ListVCenters()); n != 1 {
		t.Fatalf("restart duplicated vCenters: %d", n)
	}
	got, _ := st.GetVCenterSecret(vc.ID)
	if got.Password != "p" || got.ConfigOrigin != store.OriginRuntime {
		t.Fatalf("after restart: %+v", got)
	}
	// Delete removes the runtime ConfigMap.
	if err := s.CheckDelete(vc.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Deleted(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if len(f.cms) != 0 {
		t.Fatalf("runtime ConfigMap not deleted: %v", keys(f.cms))
	}
}

func TestStartupExportsStoreOnlyVCenterAndKeepsMappings(t *testing.T) {
	f, st, s := setup(t)
	vc, err := st.UpsertVCenter(store.VCenter{Name: "hub-vc", URL: "https://hub/sdk", Username: "u", Password: "p", Datacenter: "DC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertMapping(store.Mapping{UUID: "aa-bb", VCenterID: vc.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.cms["rf2vc-vc-hub-vc"]; !ok {
		t.Fatalf("store-only vCenter not exported: %v", keys(f.cms))
	}
	// Moving it into Git (same name, no origin label) adopts the same id: mappings stay.
	cm := f.cms["rf2vc-vc-hub-vc"]
	delete(cm.Metadata.Labels, LabelOrigin)
	f.cms["rf2vc-vc-hub-vc"] = cm
	if err := s.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetVCenter(vc.ID)
	if got.ConfigOrigin != store.OriginGitOps {
		t.Fatalf("origin after moving to Git: %+v", got)
	}
	if _, v, ok := st.LookupUUID("aa-bb"); !ok || v.ID != vc.ID {
		t.Fatal("mapping lost")
	}
}

func keys(m map[string]kube.ConfigMap) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
