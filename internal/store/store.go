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
	DryRun     bool   `json:"dryRun,omitempty"` // per-vCenter: fake outbound mutations

	// ConfigMap that defines this vCenter (namespace of the gateway), if any.
	ConfigMap string `json:"configMap,omitempty"`
	// ConfigOrigin is OriginGitOps (rf2vc never writes it) or OriginRuntime
	// (written by rf2vc for a vCenter created in the UI).
	ConfigOrigin      string     `json:"configOrigin,omitempty"`
	CredentialsSecret *SecretRef `json:"credentialsSecret,omitempty"`
	// PasswordSource is PasswordFromSecret or PasswordManual (typed in the UI).
	PasswordSource string `json:"passwordSource,omitempty"`
	// HasPassword is output-only (the password itself is always redacted).
	HasPassword bool `json:"hasPassword"`
}

// SecretRef points at a Secret in the gateway's namespace holding vSphere credentials.
type SecretRef struct {
	Name        string `json:"name" yaml:"name"`
	PasswordKey string `json:"passwordKey,omitempty" yaml:"passwordKey,omitempty"` // default "password"
	UsernameKey string `json:"usernameKey,omitempty" yaml:"usernameKey,omitempty"` // optional: overrides username
}

const (
	OriginGitOps       = "gitops"
	OriginRuntime      = "runtime"
	PasswordFromSecret = "secret"
	PasswordManual     = "manual"
)

// Mapping binds a BIOS UUID to a vCenter.
type Mapping struct {
	UUID      string `json:"uuid"`
	VCenterID string `json:"vcenterId"`
	Name      string `json:"name,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// Settings are process-wide prefs persisted in state.json.
type Settings struct {
	DryRun bool `json:"dryRun"` // global: fake all outbound mutations
}

type State struct {
	VCenters []VCenter `json:"vcenters"`
	Mappings []Mapping `json:"mappings"`
	Settings Settings  `json:"settings"`
}

// Store is an in-memory view of state.json with atomic PVC persistence.
type Store struct {
	mu       sync.RWMutex
	path     string
	vcenters map[string]VCenter // id -> vc
	byUUID   map[string]Mapping // normalized uuid -> mapping
	settings Settings
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
	s.settings = st.Settings
	for _, vc := range st.VCenters {
		if vc.ID == "" {
			continue
		}
		if vc.ISOFolder == "" {
			vc.ISOFolder = "rf2vc/isos"
		}
		vc.Datastore = normalizeDatastore(vc.Datastore)
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
		Settings: s.settings,
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

// normalizeDatastore clears placeholders like NONE / notset so they are not
// treated as real vSphere datastore names.
func normalizeDatastore(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "none", "notset", "not set", "n/a", "na", "-", "null", "undefined":
		return ""
	default:
		return s
	}
}

// SeedFromGOVC creates one vCenter when store is empty and GOVC_* env is set.
func (s *Store) SeedFromGOVC() (bool, error) {
	url := strings.TrimSpace(os.Getenv("GOVC_URL"))
	user := strings.TrimSpace(os.Getenv("GOVC_USERNAME"))
	pass := os.Getenv("GOVC_PASSWORD")
	dc := strings.TrimSpace(os.Getenv("GOVC_DATACENTER"))
	ds := normalizeDatastore(os.Getenv("GOVC_DATASTORE"))
	if url == "" || user == "" || pass == "" || dc == "" {
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
	if !ok {
		return VCenter{}, false
	}
	vc.Datastore = normalizeDatastore(vc.Datastore)
	return vc, true
}

func redact(vc VCenter) VCenter {
	vc.HasPassword = vc.Password != ""
	vc.Password = ""
	vc.Datastore = normalizeDatastore(vc.Datastore)
	return vc
}

func (s *Store) UpsertVCenter(vc VCenter) (VCenter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vc.Datastore = normalizeDatastore(vc.Datastore)
	if vc.URL == "" || vc.Username == "" || vc.Datacenter == "" {
		return VCenter{}, fmt.Errorf("url, username, and datacenter are required")
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
		vc.PasswordSource = PasswordManual
		vc.ConfigMap, vc.ConfigOrigin, vc.CredentialsSecret = "", "", nil
	} else {
		existing, ok := s.vcenters[vc.ID]
		if !ok {
			return VCenter{}, fmt.Errorf("vcenter %s not found", vc.ID)
		}
		if vc.Password == "" {
			vc.Password = existing.Password
			vc.PasswordSource = existing.PasswordSource
		} else {
			vc.PasswordSource = PasswordManual
		}
		// Preserve dry-run unless caller set it via SetVCenterDryRun.
		// Upsert from the edit form does not toggle DryRun.
		vc.DryRun = existing.DryRun
		vc.ConfigMap, vc.ConfigOrigin, vc.CredentialsSecret = existing.ConfigMap, existing.ConfigOrigin, existing.CredentialsSecret
	}
	vc.HasPassword = false
	s.vcenters[vc.ID] = vc
	if err := s.persistLocked(); err != nil {
		return VCenter{}, err
	}
	return redact(vc), nil
}

// ApplyConfigVCenter merges a vCenter defined in a ConfigMap into the store.
// It matches an existing record by ID, then by ConfigMap name, then by name
// (so UUID mappings survive moving a vCenter into a ConfigMap). ConfigMap
// fields win; secretPassword wins when non-empty, otherwise a password typed
// in the UI is kept. dryRun is applied only when set.
func (s *Store) ApplyConfigVCenter(in VCenter, dryRun *bool, secretPassword string) (VCenter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.URL == "" || in.Datacenter == "" {
		return VCenter{}, fmt.Errorf("configmap %s: url and datacenter are required", in.ConfigMap)
	}
	if in.Name == "" {
		in.Name = in.URL
	}
	if in.ISOFolder == "" {
		in.ISOFolder = "rf2vc/isos"
	}
	in.Datastore = normalizeDatastore(in.Datastore)

	existing, found := s.vcenters[in.ID]
	if !found || in.ID == "" {
		found = false
		for _, vc := range s.vcenters {
			if in.ConfigMap != "" && vc.ConfigMap == in.ConfigMap {
				existing, found = vc, true
				break
			}
		}
	}
	if !found {
		for _, vc := range s.vcenters {
			if vc.Name == in.Name && (vc.ConfigMap == "" || vc.ConfigMap == in.ConfigMap) {
				existing, found = vc, true
				break
			}
		}
	}
	if found {
		in.ID = existing.ID
		in.DryRun = existing.DryRun
	} else if in.ID == "" {
		in.ID = newID()
	}
	if dryRun != nil {
		in.DryRun = *dryRun
	}
	switch {
	case secretPassword != "":
		in.Password, in.PasswordSource = secretPassword, PasswordFromSecret
	case found && existing.Password != "":
		in.Password = existing.Password
		in.PasswordSource = existing.PasswordSource
		if in.PasswordSource == PasswordFromSecret {
			in.PasswordSource = PasswordManual // secret gone; last known value kept
		}
	default:
		in.Password, in.PasswordSource = "", ""
	}
	in.HasPassword = false
	s.vcenters[in.ID] = in
	if err := s.persistLocked(); err != nil {
		return VCenter{}, err
	}
	return redact(in), nil
}

// SetVCenterConfigMap records the ConfigMap backing a vCenter.
func (s *Store) SetVCenterConfigMap(id, configMap, origin string, ref *SecretRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	vc, ok := s.vcenters[id]
	if !ok {
		return fmt.Errorf("vcenter %s not found", id)
	}
	vc.ConfigMap, vc.ConfigOrigin, vc.CredentialsSecret = configMap, origin, ref
	s.vcenters[id] = vc
	return s.persistLocked()
}

// GetSettings returns process-wide settings.
func (s *Store) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetGlobalDryRun toggles fake-outbound mode for all vCenters.
func (s *Store) SetGlobalDryRun(on bool) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.DryRun = on
	if err := s.persistLocked(); err != nil {
		return Settings{}, err
	}
	return s.settings, nil
}

// SetVCenterDryRun toggles dry-run on one endpoint.
func (s *Store) SetVCenterDryRun(id string, on bool) (VCenter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vc, ok := s.vcenters[id]
	if !ok {
		return VCenter{}, fmt.Errorf("vcenter %s not found", id)
	}
	vc.DryRun = on
	s.vcenters[id] = vc
	if err := s.persistLocked(); err != nil {
		return VCenter{}, err
	}
	return redact(vc), nil
}

// DryRunActive reports whether outbound mutations should be faked for this vCenter.
func (s *Store) DryRunActive(vcID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.DryRun {
		return true
	}
	if vc, ok := s.vcenters[vcID]; ok {
		return vc.DryRun
	}
	return false
}

// DryRunForUUID resolves the mapping's vCenter and checks dry-run.
func (s *Store) DryRunForUUID(uuid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.DryRun {
		return true
	}
	m, ok := s.byUUID[NormalizeUUID(uuid)]
	if !ok {
		return false
	}
	if vc, ok := s.vcenters[m.VCenterID]; ok {
		return vc.DryRun
	}
	return false
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
