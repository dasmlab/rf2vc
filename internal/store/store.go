package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// VCenter is a GOVC-shaped vSphere endpoint.
type VCenter struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Insecure   bool   `json:"insecure"`
	Datacenter string `json:"datacenter"`
	Datastore  string `json:"datastore"`
	Folder     string `json:"folder,omitempty"` // GOVC_FOLDER, e.g. /Montreal/vm/VMs/TDM/OpenShift/ACM/LAB
	ISOFolder  string `json:"isoFolder"`
	Notes      string `json:"notes,omitempty"`
}

// Mapping binds a BIOS UUID to a vCenter.
type Mapping struct {
	UUID      string `json:"uuid"`
	VCenterID string `json:"vcenterId"`
	Name      string `json:"name,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

type State struct {
	VCenters []VCenter `json:"vcenters"`
	Mappings []Mapping `json:"mappings"`
}

// Store is an in-memory view of state.json with atomic PVC persistence.
type Store struct {
	mu       sync.RWMutex
	path     string
	vcenters map[string]VCenter // id -> vc
	byUUID   map[string]Mapping // normalized uuid -> mapping
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "state.json")
	s := &Store{
		path:     path,
		vcenters: map[string]VCenter{},
		byUUID:   map[string]Mapping{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.persistLocked()
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vcenters = map[string]VCenter{}
	s.byUUID = map[string]Mapping{}
	for _, vc := range st.VCenters {
		if vc.ID == "" {
			continue
		}
		if vc.ISOFolder == "" {
			vc.ISOFolder = "rf2vc/isos"
		}
		s.vcenters[vc.ID] = vc
	}
	for _, m := range st.Mappings {
		u := NormalizeUUID(m.UUID)
		if u == "" || m.VCenterID == "" {
			continue
		}
		m.UUID = u
		s.byUUID[u] = m
	}
	return nil
}

func (s *Store) snapshot() State {
	st := State{
		VCenters: make([]VCenter, 0, len(s.vcenters)),
		Mappings: make([]Mapping, 0, len(s.byUUID)),
	}
	for _, vc := range s.vcenters {
		st.VCenters = append(st.VCenters, vc)
	}
	for _, m := range s.byUUID {
		st.Mappings = append(st.Mappings, m)
	}
	return st
}

func (s *Store) persistLocked() error {
	st := s.snapshot()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func NormalizeUUID(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SeedFromGOVC creates one vCenter when store is empty and GOVC_* env is set.
func (s *Store) SeedFromGOVC() (bool, error) {
	url := strings.TrimSpace(os.Getenv("GOVC_URL"))
	user := strings.TrimSpace(os.Getenv("GOVC_USERNAME"))
	pass := os.Getenv("GOVC_PASSWORD")
	dc := strings.TrimSpace(os.Getenv("GOVC_DATACENTER"))
	ds := strings.TrimSpace(os.Getenv("GOVC_DATASTORE"))
	if url == "" || user == "" || pass == "" || dc == "" || ds == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.vcenters) > 0 {
		return false, nil
	}
	insecure := strings.EqualFold(os.Getenv("GOVC_INSECURE"), "1") ||
		strings.EqualFold(os.Getenv("GOVC_INSECURE"), "true")
	iso := strings.TrimSpace(os.Getenv("GOVC_ISO_FOLDER"))
	if iso == "" {
		iso = "rf2vc/isos"
	}
	vc := VCenter{
		ID:         newID(),
		Name:       "seeded-from-govc",
		URL:        url,
		Username:   user,
		Password:   pass,
		Insecure:   insecure,
		Datacenter: dc,
		Datastore:  ds,
		Folder:     strings.TrimSpace(os.Getenv("GOVC_FOLDER")),
		ISOFolder:  iso,
		Notes:      "auto-seeded from GOVC_* env on first boot",
	}
	s.vcenters[vc.ID] = vc
	if err := s.persistLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ListVCenters() []VCenter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]VCenter, 0, len(s.vcenters))
	for _, vc := range s.vcenters {
		out = append(out, redact(vc))
	}
	return out
}

func (s *Store) GetVCenter(id string) (VCenter, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vc, ok := s.vcenters[id]
	if !ok {
		return VCenter{}, false
	}
	return redact(vc), true
}

// GetVCenterSecret returns the full record including password (internal use).
func (s *Store) GetVCenterSecret(id string) (VCenter, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vc, ok := s.vcenters[id]
	return vc, ok
}

func redact(vc VCenter) VCenter {
	vc.Password = ""
	return vc
}

func (s *Store) UpsertVCenter(vc VCenter) (VCenter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if vc.URL == "" || vc.Username == "" || vc.Datacenter == "" || vc.Datastore == "" {
		return VCenter{}, fmt.Errorf("url, username, datacenter, datastore are required")
	}
	if vc.Name == "" {
		vc.Name = vc.URL
	}
	if vc.ISOFolder == "" {
		vc.ISOFolder = "rf2vc/isos"
	}
	if vc.ID == "" {
		vc.ID = newID()
		if vc.Password == "" {
			return VCenter{}, fmt.Errorf("password is required for new vCenter")
		}
	} else {
		existing, ok := s.vcenters[vc.ID]
		if !ok {
			return VCenter{}, fmt.Errorf("vcenter %s not found", vc.ID)
		}
		if vc.Password == "" {
			vc.Password = existing.Password
		}
	}
	s.vcenters[vc.ID] = vc
	if err := s.persistLocked(); err != nil {
		return VCenter{}, err
	}
	return redact(vc), nil
}

func (s *Store) DeleteVCenter(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vcenters[id]; !ok {
		return fmt.Errorf("vcenter %s not found", id)
	}
	for u, m := range s.byUUID {
		if m.VCenterID == id {
			delete(s.byUUID, u)
		}
	}
	delete(s.vcenters, id)
	return s.persistLocked()
}

func (s *Store) ListMappings() []Mapping {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Mapping, 0, len(s.byUUID))
	for _, m := range s.byUUID {
		out = append(out, m)
	}
	return out
}

func (s *Store) LookupUUID(uuid string) (Mapping, VCenter, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.byUUID[NormalizeUUID(uuid)]
	if !ok {
		return Mapping{}, VCenter{}, false
	}
	vc, ok := s.vcenters[m.VCenterID]
	if !ok {
		return m, VCenter{}, false
	}
	return m, vc, true
}

func (s *Store) UpsertMapping(m Mapping) (Mapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.UUID = NormalizeUUID(m.UUID)
	if m.UUID == "" || m.VCenterID == "" {
		return Mapping{}, fmt.Errorf("uuid and vcenterId are required")
	}
	if _, ok := s.vcenters[m.VCenterID]; !ok {
		return Mapping{}, fmt.Errorf("vcenter %s not found", m.VCenterID)
	}
	s.byUUID[m.UUID] = m
	if err := s.persistLocked(); err != nil {
		return Mapping{}, err
	}
	return m, nil
}

func (s *Store) DeleteMapping(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := NormalizeUUID(uuid)
	if _, ok := s.byUUID[u]; !ok {
		return fmt.Errorf("mapping %s not found", u)
	}
	delete(s.byUUID, u)
	return s.persistLocked()
}

func (s *Store) Stats() (vcenters, mappings int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.vcenters), len(s.byUUID)
}
