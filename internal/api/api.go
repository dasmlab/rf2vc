package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dasmlab/rf2vc/internal/store"
	"github.com/dasmlab/rf2vc/internal/vsphere"
)

type Server struct {
	st      *store.Store
	pool    *vsphere.Pool
	version string
}

func New(st *store.Store, pool *vsphere.Pool, version string) *Server {
	return &Server{st: st, pool: pool, version: version}
}

func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/status", s.status)
	mux.HandleFunc("/api/v1/vcenters", s.vcenters)
	mux.HandleFunc("/api/v1/vcenters/", s.vcenterItem)
	mux.HandleFunc("/api/v1/mappings", s.mappings)
	mux.HandleFunc("/api/v1/mappings/", s.mappingItem)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	vc, mp := s.st.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"service":  "rf2vc",
		"version":  s.version,
		"vcenters": vc,
		"mappings": mp,
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) vcenters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.st.ListVCenters())
	case http.MethodPost:
		var vc store.VCenter
		if err := json.NewDecoder(r.Body).Decode(&vc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vc.ID = "" // always create
		out, err := s.st.UpsertVCenter(vc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) vcenterItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/vcenters/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")

	// POST /api/v1/vcenters/test — unsaved form body (no id)
	if len(parts) == 1 && parts[0] == "test" && r.Method == http.MethodPost {
		s.testVCenterBody(w, r, store.VCenter{})
		return
	}

	id := parts[0]
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		s.testVCenter(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "iso-status" && r.Method == http.MethodGet {
		s.isoStatus(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "health" && r.Method == http.MethodGet {
		s.vcHealth(w, r, id)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		vc, ok := s.st.GetVCenter(id)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, vc)
	case http.MethodPut:
		var vc store.VCenter
		if err := json.NewDecoder(r.Body).Decode(&vc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		vc.ID = id
		out, err := s.st.UpsertVCenter(vc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.pool.Invalidate(id)
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := s.st.DeleteVCenter(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		s.pool.Invalidate(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func applyVCOverrides(vc *store.VCenter, override store.VCenter) {
	if override.URL != "" {
		vc.URL = override.URL
	}
	if override.Username != "" {
		vc.Username = override.Username
	}
	if override.Password != "" {
		vc.Password = override.Password
	}
	if override.Datacenter != "" {
		vc.Datacenter = override.Datacenter
	}
	if override.Datastore != "" {
		vc.Datastore = override.Datastore
	}
	if override.Folder != "" {
		vc.Folder = override.Folder
	}
	if override.ISOFolder != "" {
		vc.ISOFolder = override.ISOFolder
	}
	vc.Insecure = vc.Insecure || override.Insecure
}

func (s *Server) testVCenter(w http.ResponseWriter, r *http.Request, id string) {
	vc, ok := s.st.GetVCenterSecret(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var override store.VCenter
	_ = json.NewDecoder(r.Body).Decode(&override)
	applyVCOverrides(&vc, override)
	s.testVCenterBody(w, r, vc)
}

func (s *Server) testVCenterBody(w http.ResponseWriter, r *http.Request, vc store.VCenter) {
	// If vc empty (unsaved test), decode full body.
	if vc.URL == "" {
		if err := json.NewDecoder(r.Body).Decode(&vc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if vc.URL == "" || vc.Username == "" || vc.Password == "" || vc.Datacenter == "" || vc.Datastore == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "url, username, password, datacenter, and datastore are required to test",
		})
		return
	}
	if vc.ISOFolder == "" {
		vc.ISOFolder = "rf2vc/isos"
	}
	ep := vsphere.Endpoint{
		URL: vc.URL, Username: vc.Username, Password: vc.Password,
		Insecure: vc.Insecure, Datacenter: vc.Datacenter, Datastore: vc.Datastore,
		Folder: vc.Folder, ISOFolder: vc.ISOFolder,
	}
	if err := vsphere.TestConnection(r.Context(), ep); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) isoStatus(w http.ResponseWriter, r *http.Request, id string) {
	vc, ok := s.st.GetVCenterSecret(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	c, err := s.pool.For(vc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st := c.ISOCacheStatus(r.Context())
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) vcHealth(w http.ResponseWriter, r *http.Request, id string) {
	vc, ok := s.st.GetVCenterSecret(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	ep := vsphere.Endpoint{
		URL: vc.URL, Username: vc.Username, Password: vc.Password,
		Insecure: vc.Insecure, Datacenter: vc.Datacenter, Datastore: vc.Datastore,
		Folder: vc.Folder, ISOFolder: vc.ISOFolder,
	}
	writeJSON(w, http.StatusOK, vsphere.ProbeHealth(r.Context(), ep))
}

func (s *Server) mappings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.st.ListMappings())
	case http.MethodPost:
		var m store.Mapping
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := s.st.UpsertMapping(m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) mappingItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/mappings/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(path, "/")
	uuid := parts[0]

	if len(parts) == 2 && parts[1] == "status" && r.Method == http.MethodGet {
		s.mappingStatus(w, r, uuid)
		return
	}
	if len(parts) == 2 && parts[1] == "power" && r.Method == http.MethodPost {
		s.mappingPower(w, r, uuid)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var m store.Mapping
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.UUID = uuid
		out, err := s.st.UpsertMapping(m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := s.st.DeleteMapping(uuid); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) mappingStatus(w http.ResponseWriter, r *http.Request, uuid string) {
	c, _, err := s.pool.ClientForUUID(s.st, uuid)
	if err != nil {
		writeJSON(w, http.StatusOK, vsphere.MappingStatus{
			UUID:  uuid,
			Found: false,
			Light: vsphere.LightRed,
			Error: err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, c.MappingStatus(r.Context(), uuid))
}

func (s *Server) mappingPower(w http.ResponseWriter, r *http.Request, uuid string) {
	var body struct {
		ResetType string `json:"resetType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rt := strings.TrimSpace(body.ResetType)
	if rt != "On" && rt != "ForceOff" {
		http.Error(w, `resetType must be "On" or "ForceOff"`, http.StatusBadRequest)
		return
	}
	c, _, err := s.pool.ClientForUUID(s.st, uuid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := c.Reset(r.Context(), uuid, rt); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	st := c.MappingStatus(r.Context(), uuid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st})
}
