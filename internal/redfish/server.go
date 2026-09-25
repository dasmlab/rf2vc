package redfish

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/dasmlab/rf2vc/internal/config"
	"github.com/dasmlab/rf2vc/internal/store"
	"github.com/dasmlab/rf2vc/internal/vsphere"
)

// Minimal Redfish surface used by Ironic/BMO redfish + redfish-virtualmedia.
//
// Typical BMH sequence (python-requests / sushy):
//  1. GET /redfish/v1/                         (no auth — ServiceRoot)
//  2. GET /redfish/v1/Systems/{uuid}            (power, Boot, Links.ManagedBy)
//  3. GET /redfish/v1/Managers/{uuid}           (via ManagedBy)
//  4. GET …/Managers/{uuid}/VirtualMedia[/Cd]   (or Systems/…/VirtualMedia)
//  5. GET …/EthernetInterfaces                 (often empty OK)
//  6. POST InsertMedia / PATCH Boot / Reset
type Server struct {
	cfg  *config.Config
	st   *store.Store
	pool *vsphere.Pool
}

func NewServer(cfg *config.Config, st *store.Store, pool *vsphere.Pool) *Server {
	return &Server{cfg: cfg, st: st, pool: pool}
}

func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/redfish/v1/", s.route)
	mux.HandleFunc("/redfish/v1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redfish/v1/", http.StatusPermanentRedirect)
	})
}

func (s *Server) clientFor(w http.ResponseWriter, uuid string) (*vsphere.Client, bool) {
	c, _, err := s.pool.ClientForUUID(s.st, uuid)
	if err != nil {
		activity.InErr("resolve", err.Error(), map[string]any{"uuid": uuid})
		http.Error(w, err.Error(), http.StatusNotFound)
		return nil, false
	}
	return c, true
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/redfish/v1")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		p = "/"
	}
	// BMH addresses sometimes omit "Systems/":
	//   redfish-virtualmedia://host/redfish/v1/<uuid>
	// instead of …/redfish/v1/Systems/<uuid>. Rewrite bare-UUID paths.
	if rewritten, ok := rewriteBareUUIDPath(p); ok {
		activity.In(r.Method, r.URL.Path, map[string]any{
			"remote":   r.RemoteAddr,
			"ua":       r.UserAgent(),
			"rewrote":  "/redfish/v1" + rewritten,
		})
		p = rewritten
	} else {
		activity.In(r.Method, r.URL.Path, map[string]any{
			"remote": r.RemoteAddr,
			"ua":     r.UserAgent(),
		})
	}

	switch {
	case p == "/" && r.Method == http.MethodGet:
		s.serviceRoot(w)
	case p == "/Systems" && r.Method == http.MethodGet:
		s.systemsCollection(w)

	// --- System actions / sub-resources BEFORE generic /Systems/{id} ---
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/Actions/ComputerSystem.Reset") && r.Method == http.MethodPost:
		s.systemReset(w, r, systemIDFrom(p, "/Actions/ComputerSystem.Reset"))
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia") && r.Method == http.MethodPost:
		s.insertMedia(w, r, systemIDFrom(p, "/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia"))
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia") && r.Method == http.MethodPost:
		s.ejectMedia(w, r, systemIDFrom(p, "/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia"))
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia/Cd") && r.Method == http.MethodGet:
		s.virtualMediaCd(w, r, systemIDFrom(p, "/VirtualMedia/Cd"))
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia") && r.Method == http.MethodGet:
		s.systemVirtualMediaCollection(w, systemIDFrom(p, "/VirtualMedia"))
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/EthernetInterfaces") && r.Method == http.MethodGet:
		s.ethernetInterfaces(w, "/redfish/v1/Systems/"+systemIDFrom(p, "/EthernetInterfaces")+"/EthernetInterfaces")
	case strings.HasPrefix(p, "/Systems/") && r.Method == http.MethodPatch:
		id := strings.TrimPrefix(p, "/Systems/")
		if strings.Contains(id, "/") {
			activity.InErr("not-found", r.URL.Path, nil)
			http.NotFound(w, r)
			return
		}
		s.systemPatch(w, r, id)
	case strings.HasPrefix(p, "/Systems/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "/Systems/")
		if strings.Contains(id, "/") {
			activity.InErr("not-found", r.URL.Path, nil)
			http.NotFound(w, r)
			return
		}
		s.systemGet(w, r, id)

	// --- Managers (per-system id preferred; "1" kept for discovery) ---
	case p == "/Managers" && r.Method == http.MethodGet:
		s.managersCollection(w)
	case strings.HasPrefix(p, "/Managers/") && strings.HasSuffix(p, "/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia") && r.Method == http.MethodPost:
		s.insertMedia(w, r, managerSystemID(p, r, "/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia"))
	case strings.HasPrefix(p, "/Managers/") && strings.HasSuffix(p, "/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia") && r.Method == http.MethodPost:
		s.ejectMedia(w, r, managerSystemID(p, r, "/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia"))
	case strings.HasPrefix(p, "/Managers/") && strings.HasSuffix(p, "/VirtualMedia/Cd") && r.Method == http.MethodGet:
		s.virtualMediaCd(w, r, managerSystemID(p, r, "/VirtualMedia/Cd"))
	case strings.HasPrefix(p, "/Managers/") && strings.HasSuffix(p, "/VirtualMedia") && r.Method == http.MethodGet:
		s.managerVirtualMediaCollection(w, managerIDFrom(p, "/VirtualMedia"))
	case strings.HasPrefix(p, "/Managers/") && strings.HasSuffix(p, "/EthernetInterfaces") && r.Method == http.MethodGet:
		s.ethernetInterfaces(w, "/redfish/v1/Managers/"+managerIDFrom(p, "/EthernetInterfaces")+"/EthernetInterfaces")
	case strings.HasPrefix(p, "/Managers/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "/Managers/")
		if strings.Contains(id, "/") {
			activity.InErr("not-found", r.URL.Path, nil)
			http.NotFound(w, r)
			return
		}
		s.managerGet(w, id)

	default:
		activity.InErr("not-found", r.URL.Path, nil)
		http.NotFound(w, r)
	}
}

// rewriteBareUUIDPath maps /{uuid}[/…] → /Systems/{uuid}[/…] when the first
// segment looks like a BIOS UUID (BMH address omitted "Systems/").
func rewriteBareUUIDPath(p string) (string, bool) {
	if p == "/" || p == "" {
		return "", false
	}
	rest := strings.TrimPrefix(p, "/")
	seg, more, hasMore := strings.Cut(rest, "/")
	if !looksLikeUUID(seg) {
		return "", false
	}
	// Don't steal real top-level collections if someone names a UUID oddly.
	switch strings.ToLower(seg) {
	case "systems", "managers", "chassis", "sessionservice", "accountservice", "registries", "taskservice", "eventservice", "updateservice", "jsonschemas", "$metadata":
		return "", false
	}
	if hasMore {
		return "/Systems/" + seg + "/" + more, true
	}
	return "/Systems/" + seg, true
}

func looksLikeUUID(s string) bool {
	// Accept standard 8-4-4-4-12 hex form (BIOS UUID).
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func systemIDFrom(p, suffix string) string {
	p = strings.TrimPrefix(p, "/Systems/")
	return strings.TrimSuffix(p, suffix)
}

func managerIDFrom(p, suffix string) string {
	p = strings.TrimPrefix(p, "/Managers/")
	return strings.TrimSuffix(p, suffix)
}

// managerSystemID resolves which VM a Manager-scoped VirtualMedia action targets.
// Prefer Managers/{uuid}/… (uuid == BIOS UUID). Managers/1/… needs ?system=.
func managerSystemID(p string, r *http.Request, suffix string) string {
	id := managerIDFrom(p, suffix)
	if id != "" && id != "1" {
		return id
	}
	if sys := strings.TrimSpace(r.URL.Query().Get("system")); sys != "" {
		return sys
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("OData-Version", "4.0")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serviceRoot(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.context": "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		"@odata.id":      "/redfish/v1/",
		"@odata.type":    "#ServiceRoot.v1_5_0.ServiceRoot",
		"Id":             "RootService",
		"Name":           "rf2vc",
		"RedfishVersion": "1.6.0",
		"UUID":           "00000000-0000-0000-0000-000000000001",
		"Systems":        map[string]string{"@odata.id": "/redfish/v1/Systems"},
		"Managers":       map[string]string{"@odata.id": "/redfish/v1/Managers"},
	})
}

func (s *Server) systemsCollection(w http.ResponseWriter) {
	mappings := s.st.ListMappings()
	members := make([]map[string]string, 0, len(mappings))
	for _, m := range mappings {
		members = append(members, map[string]string{
			"@odata.id": "/redfish/v1/Systems/" + m.UUID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Systems",
		"@odata.type":         "#ComputerSystemCollection.ComputerSystemCollection",
		"Name":                "Computer System Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	})
}

func (s *Server) systemGet(w http.ResponseWriter, r *http.Request, id string) {
	c, ok := s.clientFor(w, id)
	if !ok {
		return
	}
	sys, err := c.GetSystem(r.Context(), id)
	if err != nil {
		activity.InErr("system-get", err.Error(), map[string]any{"uuid": id})
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":   "/redfish/v1/Systems/" + sys.UUID,
		"@odata.type": "#ComputerSystem.v1_14_0.ComputerSystem",
		"Id":          sys.UUID,
		"Name":        sys.Name,
		"UUID":        sys.UUID,
		"SystemType":  "Virtual",
		"PowerState":  sys.PowerState,
		"Status": map[string]string{
			"State":  "Enabled",
			"Health": "OK",
		},
		"MemorySummary": map[string]any{
			"TotalSystemMemoryGiB": float64(sys.MemoryMiB) / 1024.0,
		},
		"ProcessorSummary": map[string]any{
			"Count": sys.CPUs,
		},
		"Boot": map[string]any{
			"BootSourceOverrideEnabled": "Disabled",
			"BootSourceOverrideTarget":  "None",
			"BootSourceOverrideMode":    "UEFI",
			"BootSourceOverrideTarget@Redfish.AllowableValues": []string{
				"None", "Pxe", "Cd", "Usb", "Hdd", "BiosSetup", "UefiTarget", "UefiHttp",
			},
		},
		"Actions": map[string]any{
			"#ComputerSystem.Reset": map[string]any{
				"target": "/redfish/v1/Systems/" + sys.UUID + "/Actions/ComputerSystem.Reset",
				"ResetType@Redfish.AllowableValues": []string{
					"On", "ForceOff", "GracefulShutdown", "ForceRestart", "GracefulRestart", "PowerCycle", "Nmi",
				},
			},
		},
		"VirtualMedia": map[string]string{
			"@odata.id": "/redfish/v1/Systems/" + sys.UUID + "/VirtualMedia",
		},
		"EthernetInterfaces": map[string]string{
			"@odata.id": "/redfish/v1/Systems/" + sys.UUID + "/EthernetInterfaces",
		},
		"Links": map[string]any{
			// Per-system manager so Manager VirtualMedia InsertMedia knows the UUID.
			"ManagedBy": []map[string]string{
				{"@odata.id": "/redfish/v1/Managers/" + sys.UUID},
			},
		},
	})
}

func (s *Server) systemReset(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		ResetType string `json:"ResetType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	detail := map[string]any{"uuid": id, "resetType": body.ResetType}
	if s.st.DryRunForUUID(id) {
		activity.OutDry("ComputerSystem.Reset", "would power/reset VM (dry-run)", detail)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c, ok := s.clientFor(w, id)
	if !ok {
		return
	}
	activity.Out("ComputerSystem.Reset", "applying power action", detail)
	if err := c.Reset(r.Context(), id, body.ResetType); err != nil {
		activity.OutErr("ComputerSystem.Reset", err.Error(), detail)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) systemPatch(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Boot *struct {
			BootSourceOverrideTarget  string `json:"BootSourceOverrideTarget"`
			BootSourceOverrideEnabled string `json:"BootSourceOverrideEnabled"`
		} `json:"Boot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Boot != nil {
		target := strings.ToLower(body.Boot.BootSourceOverrideTarget)
		enabled := strings.ToLower(body.Boot.BootSourceOverrideEnabled)
		detail := map[string]any{"uuid": id, "bootTarget": target, "bootEnabled": enabled}
		switch {
		case target == "cd" || target == "cdrom" || target == "usb" || target == "usbcd":
			if s.st.DryRunForUUID(id) {
				activity.OutDry("BootOverride", "would set boot CD once (dry-run)", detail)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			c, ok := s.clientFor(w, id)
			if !ok {
				return
			}
			activity.Out("BootOverride", "set boot CD once", detail)
			if err := c.SetBootCDOnce(r.Context(), id); err != nil {
				activity.OutErr("BootOverride", err.Error(), detail)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case enabled == "disabled" || target == "" || target == "none" ||
			target == "hdd" || target == "disk" || target == "pxe" || target == "diags":
			// Ironic clears CD override before/after eject so the install reboot hits disk.
			if s.st.DryRunForUUID(id) {
				activity.OutDry("BootOverride", "would set boot disk first (dry-run)", detail)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			c, ok := s.clientFor(w, id)
			if !ok {
				return
			}
			activity.Out("BootOverride", "set boot disk first", detail)
			if err := c.SetBootDiskFirst(r.Context(), id); err != nil {
				activity.OutErr("BootOverride", err.Error(), detail)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) managersCollection(w http.ResponseWriter) {
	mappings := s.st.ListMappings()
	members := make([]map[string]string, 0, len(mappings)+1)
	members = append(members, map[string]string{"@odata.id": "/redfish/v1/Managers/1"})
	for _, m := range mappings {
		members = append(members, map[string]string{
			"@odata.id": "/redfish/v1/Managers/" + m.UUID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Managers",
		"@odata.type":         "#ManagerCollection.ManagerCollection",
		"Name":                "Manager Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	})
}

func (s *Server) managerGet(w http.ResponseWriter, id string) {
	sysLink := ""
	managerFor := []map[string]string{}
	if id == "1" {
		for _, m := range s.st.ListMappings() {
			managerFor = append(managerFor, map[string]string{
				"@odata.id": "/redfish/v1/Systems/" + m.UUID,
			})
		}
	} else {
		if _, _, ok := s.st.LookupUUID(id); !ok {
			http.Error(w, "manager/system not mapped", http.StatusNotFound)
			return
		}
		sysLink = id
		managerFor = []map[string]string{
			{"@odata.id": "/redfish/v1/Systems/" + id},
		}
	}
	vmedia := "/redfish/v1/Managers/" + id + "/VirtualMedia"
	eth := "/redfish/v1/Managers/" + id + "/EthernetInterfaces"
	body := map[string]any{
		"@odata.id":    "/redfish/v1/Managers/" + id,
		"@odata.type":  "#Manager.v1_10_0.Manager",
		"Id":           id,
		"Name":         "Gateway Manager",
		"ManagerType":  "Service",
		"Status":       map[string]string{"State": "Enabled", "Health": "OK"},
		"VirtualMedia": map[string]string{"@odata.id": vmedia},
		"EthernetInterfaces": map[string]string{
			"@odata.id": eth,
		},
		"Links": map[string]any{
			"ManagerForServers": managerFor,
		},
	}
	_ = sysLink
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) managerVirtualMediaCollection(w http.ResponseWriter, managerID string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Managers/" + managerID + "/VirtualMedia",
		"@odata.type":         "#VirtualMediaCollection.VirtualMediaCollection",
		"Name":                "Virtual Media Collection",
		"Members@odata.count": 1,
		"Members": []map[string]string{
			{"@odata.id": "/redfish/v1/Managers/" + managerID + "/VirtualMedia/Cd"},
		},
	})
}

func (s *Server) systemVirtualMediaCollection(w http.ResponseWriter, id string) {
	if _, _, ok := s.st.LookupUUID(id); !ok {
		http.Error(w, "uuid not mapped", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Systems/" + id + "/VirtualMedia",
		"@odata.type":         "#VirtualMediaCollection.VirtualMediaCollection",
		"Name":                "Virtual Media Collection",
		"Members@odata.count": 1,
		"Members": []map[string]string{
			{"@odata.id": "/redfish/v1/Systems/" + id + "/VirtualMedia/Cd"},
		},
	})
}

func (s *Server) ethernetInterfaces(w http.ResponseWriter, odataID string) {
	// Empty collection is enough for redfish-virtualmedia registration/power.
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           odataID,
		"@odata.type":         "#EthernetInterfaceCollection.EthernetInterfaceCollection",
		"Name":                "Ethernet Interface Collection",
		"Members@odata.count": 0,
		"Members":             []map[string]string{},
	})
}

func (s *Server) virtualMediaCd(w http.ResponseWriter, r *http.Request, systemID string) {
	if systemID == "" {
		systemID = strings.TrimSpace(r.URL.Query().Get("system"))
	}
	st := vsphere.MediaStatus{}
	// Derive odata path from request so System vs Manager links stay consistent.
	odataID := r.URL.Path
	if strings.HasSuffix(odataID, "/") {
		odataID = strings.TrimSuffix(odataID, "/")
	}
	insertTarget := odataID + "/Actions/VirtualMedia.InsertMedia"
	ejectTarget := odataID + "/Actions/VirtualMedia.EjectMedia"
	if systemID != "" {
		c, ok := s.clientFor(w, systemID)
		if !ok {
			return
		}
		st = c.MediaStatus(systemID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":            odataID,
		"@odata.type":          "#VirtualMedia.v1_3_0.VirtualMedia",
		"Id":                   "Cd",
		"Name":                 "Virtual CD",
		"MediaTypes":           []string{"CD", "DVD"},
		"Inserted":             st.Inserted,
		"Image":                st.Image,
		"WriteProtected":       true,
		"ConnectedVia":         "URI",
		"TransferProtocolType": "HTTP",
		"Actions": map[string]any{
			"#VirtualMedia.InsertMedia": map[string]any{"target": insertTarget},
			"#VirtualMedia.EjectMedia":  map[string]any{"target": ejectTarget},
		},
	})
}

func (s *Server) insertMedia(w http.ResponseWriter, r *http.Request, systemID string) {
	if systemID == "" {
		http.Error(w, "system id required (use Managers/{uuid}/VirtualMedia or ?system=)", http.StatusBadRequest)
		return
	}
	var body struct {
		Image          string `json:"Image"`
		Inserted       *bool  `json:"Inserted"`
		WriteProtected *bool  `json:"WriteProtected"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Image == "" {
		http.Error(w, "Image is required", http.StatusBadRequest)
		return
	}
	detail := map[string]any{"uuid": systemID, "image": body.Image}
	if s.st.DryRunForUUID(systemID) {
		activity.OutDry("InsertMedia", "would stage/attach ISO (dry-run) — no upload or CDROM change", detail)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c, ok := s.clientFor(w, systemID)
	if !ok {
		return
	}
	activity.Out("InsertMedia", "staging/attaching ISO", detail)
	if err := c.InsertMedia(r.Context(), systemID, body.Image); err != nil {
		activity.OutErr("InsertMedia", err.Error(), detail)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ejectMedia(w http.ResponseWriter, r *http.Request, systemID string) {
	if systemID == "" {
		http.Error(w, "system id required (use Managers/{uuid}/VirtualMedia or ?system=)", http.StatusBadRequest)
		return
	}
	detail := map[string]any{"uuid": systemID}
	if s.st.DryRunForUUID(systemID) {
		activity.OutDry("EjectMedia", "would eject CDROM (dry-run)", detail)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	c, ok := s.clientFor(w, systemID)
	if !ok {
		return
	}
	activity.Out("EjectMedia", "ejecting CDROM", detail)
	if err := c.EjectMedia(r.Context(), systemID); err != nil {
		activity.OutErr("EjectMedia", err.Error(), detail)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
