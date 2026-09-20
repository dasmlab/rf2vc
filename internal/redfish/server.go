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
	activity.In(r.Method, r.URL.Path, map[string]any{
		"remote": r.RemoteAddr,
		"ua":     r.UserAgent(),
	})

	switch {
	case p == "/" && r.Method == http.MethodGet:
		s.serviceRoot(w)
	case p == "/Systems" && r.Method == http.MethodGet:
		s.systemsCollection(w)
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/Actions/ComputerSystem.Reset") && r.Method == http.MethodPost:
		id := systemIDFrom(p, "/Actions/ComputerSystem.Reset")
		s.systemReset(w, r, id)
	case strings.HasPrefix(p, "/Systems/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "/Systems/")
		if strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		s.systemGet(w, r, id)
	case strings.HasPrefix(p, "/Systems/") && r.Method == http.MethodPatch:
		id := strings.TrimPrefix(p, "/Systems/")
		s.systemPatch(w, r, id)
	case p == "/Managers" && r.Method == http.MethodGet:
		s.managersCollection(w)
	case p == "/Managers/1" && r.Method == http.MethodGet:
		s.managerGet(w)
	case p == "/Managers/1/VirtualMedia" && r.Method == http.MethodGet:
		s.virtualMediaCollection(w)
	case p == "/Managers/1/VirtualMedia/Cd" && r.Method == http.MethodGet:
		s.virtualMediaCd(w, r, "")
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia") && r.Method == http.MethodGet:
		id := systemIDFrom(p, "/VirtualMedia")
		s.systemVirtualMediaCollection(w, id)
	case strings.HasPrefix(p, "/Systems/") && strings.Contains(p, "/VirtualMedia/Cd") && strings.HasSuffix(p, "/Actions/VirtualMedia.InsertMedia") && r.Method == http.MethodPost:
		id := systemIDFrom(p, "/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia")
		s.insertMedia(w, r, id)
	case strings.HasPrefix(p, "/Systems/") && strings.Contains(p, "/VirtualMedia/Cd") && strings.HasSuffix(p, "/Actions/VirtualMedia.EjectMedia") && r.Method == http.MethodPost:
		id := systemIDFrom(p, "/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia")
		s.ejectMedia(w, r, id)
	case strings.HasPrefix(p, "/Systems/") && strings.HasSuffix(p, "/VirtualMedia/Cd") && r.Method == http.MethodGet:
		id := systemIDFrom(p, "/VirtualMedia/Cd")
		s.virtualMediaCd(w, r, id)
	case strings.HasPrefix(p, "/Managers/1/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia") && r.Method == http.MethodPost:
		sys := r.URL.Query().Get("system")
		s.insertMedia(w, r, sys)
	case strings.HasPrefix(p, "/Managers/1/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia") && r.Method == http.MethodPost:
		sys := r.URL.Query().Get("system")
		s.ejectMedia(w, r, sys)
	default:
		activity.InErr("not-found", r.URL.Path, nil)
		http.NotFound(w, r)
	}
}

func systemIDFrom(p, suffix string) string {
	p = strings.TrimPrefix(p, "/Systems/")
	return strings.TrimSuffix(p, suffix)
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
		"Links": map[string]any{
			"ManagedBy": []map[string]string{
				{"@odata.id": "/redfish/v1/Managers/1"},
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
		if target == "cd" || target == "cdrom" || target == "usb" {
			detail := map[string]any{"uuid": id, "bootTarget": target}
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
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) managersCollection(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Managers",
		"@odata.type":         "#ManagerCollection.ManagerCollection",
		"Name":                "Manager Collection",
		"Members@odata.count": 1,
		"Members": []map[string]string{
			{"@odata.id": "/redfish/v1/Managers/1"},
		},
	})
}

func (s *Server) managerGet(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":    "/redfish/v1/Managers/1",
		"@odata.type":  "#Manager.v1_10_0.Manager",
		"Id":           "1",
		"Name":         "Gateway Manager",
		"ManagerType":  "Service",
		"VirtualMedia": map[string]string{"@odata.id": "/redfish/v1/Managers/1/VirtualMedia"},
	})
}

func (s *Server) virtualMediaCollection(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"@odata.id":           "/redfish/v1/Managers/1/VirtualMedia",
		"@odata.type":         "#VirtualMediaCollection.VirtualMediaCollection",
		"Name":                "Virtual Media Collection",
		"Members@odata.count": 1,
		"Members": []map[string]string{
			{"@odata.id": "/redfish/v1/Managers/1/VirtualMedia/Cd"},
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

func (s *Server) virtualMediaCd(w http.ResponseWriter, r *http.Request, systemID string) {
	if systemID == "" {
		systemID = r.URL.Query().Get("system")
	}
	st := vsphere.MediaStatus{}
	odataID := "/redfish/v1/Managers/1/VirtualMedia/Cd"
	insertTarget := "/redfish/v1/Managers/1/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia"
	ejectTarget := "/redfish/v1/Managers/1/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia"
	if systemID != "" {
		c, ok := s.clientFor(w, systemID)
		if !ok {
			return
		}
		st = c.MediaStatus(systemID)
		odataID = "/redfish/v1/Systems/" + systemID + "/VirtualMedia/Cd"
		insertTarget = odataID + "/Actions/VirtualMedia.InsertMedia"
		ejectTarget = odataID + "/Actions/VirtualMedia.EjectMedia"
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
		http.Error(w, "system id required", http.StatusBadRequest)
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
		http.Error(w, "system id required", http.StatusBadRequest)
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
