package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// Light is a UI status tone: green | yellow | red | blue.
type Light string

const (
	LightGreen  Light = "green"
	LightYellow Light = "yellow"
	LightRed    Light = "red"
	LightBlue   Light = "blue"
)

// HealthProbe summarizes vCenter connectivity for the admin UI.
type HealthProbe struct {
	Connection Light  `json:"connection"` // green/yellow/red
	ConnDetail string `json:"connectionDetail"`
	ISOCache   Light  `json:"isoCache"`
	ISODetail  string `json:"isoCacheDetail"`
	FolderOK   bool   `json:"folderOk"`
	FolderPath string `json:"folderPath,omitempty"`
	OK         bool   `json:"ok"`
}

// ProbeHealth logs in and classifies connection + ISO datastore/folder health.
func ProbeHealth(ctx context.Context, ep Endpoint) HealthProbe {
	out := HealthProbe{
		Connection: LightRed,
		ISOCache:   LightRed,
		FolderPath: ep.Folder,
	}
	ep.Datastore = NormalizeDatastore(ep.Datastore)

	if _, err := url.Parse(ep.URL); err != nil || ep.URL == "" {
		out.ConnDetail = "invalid URL"
		out.ISODetail = "skipped"
		return out
	}

	c, err := NewClient(ep)
	if err != nil {
		out.ConnDetail = err.Error()
		out.ISODetail = "skipped"
		return out
	}
	defer func() { _ = c.Close(ctx) }()

	if err := c.ensure(ctx); err != nil {
		msg := err.Error()
		out.ConnDetail = msg
		out.ISODetail = msg
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "login") || strings.Contains(lower, "datacenter") ||
			strings.Contains(lower, "datastore") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "unauthorized") {
			out.Connection = LightYellow
			out.ISOCache = LightYellow
		} else if isNetErr(err) {
			out.Connection = LightRed
			out.ISOCache = LightRed
		} else {
			out.Connection = LightYellow
			out.ISOCache = LightYellow
		}
		return out
	}

	// Login + DC succeeded (datastore may be unset).
	out.Connection = LightGreen
	if ep.Datastore != "" {
		out.ConnDetail = "connected · datacenter and datastore OK"
	} else {
		out.ConnDetail = "connected · datacenter OK · datastore not set"
	}

	if folder := strings.TrimSpace(ep.Folder); folder != "" {
		if err := c.verifyFolderExists(ctx, folder); err != nil {
			out.Connection = LightYellow
			out.ConnDetail = "connected, but folder: " + err.Error()
			out.FolderOK = false
		} else {
			out.FolderOK = true
			if ep.Datastore != "" {
				out.ConnDetail = "connected · datacenter, datastore, and folder OK"
			} else {
				out.ConnDetail = "connected · datacenter and folder OK · datastore not set"
			}
		}
	} else {
		out.FolderOK = true
	}

	if ep.Datastore == "" {
		out.ISOCache = LightYellow
		out.ISODetail = "datastore not set (ISO staging unavailable until you set a real datastore name)"
		out.OK = out.Connection == LightGreen || out.Connection == LightYellow
		return out
	}

	// ISO folder on datastore
	exists, _, err := c.datastoreFileExists(ctx, strings.Trim(ep.ISOFolder, "/"))
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			out.ISOCache = LightYellow
			out.ISODetail = fmt.Sprintf("datastore OK; ISO folder %q not found yet", ep.ISOFolder)
		} else {
			out.ISOCache = LightRed
			out.ISODetail = err.Error()
		}
	} else if exists {
		out.ISOCache = LightGreen
		out.ISODetail = fmt.Sprintf("datastore %s · folder %s", ep.Datastore, ep.ISOFolder)
	} else {
		out.ISOCache = LightYellow
		out.ISODetail = fmt.Sprintf("datastore OK; ISO folder %q missing (will be created on upload)", ep.ISOFolder)
	}

	if out.ISOCache != LightRed {
		if err := c.probeISOFolder(ctx); err != nil {
			out.ISOCache = LightYellow
			out.ISODetail = err.Error()
		} else {
			out.ISOCache = LightGreen
			out.ISODetail = fmt.Sprintf("datastore %s · folder %s reachable", ep.Datastore, ep.ISOFolder)
		}
	}

	out.OK = (out.Connection == LightGreen || out.Connection == LightYellow) &&
		(out.ISOCache == LightGreen || out.ISOCache == LightYellow)
	return out
}

func isNetErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "network is unreachable")
}

func (c *Client) verifyFolderExists(ctx context.Context, folder string) error {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return nil
	}
	// Prefer inventory path lookup (govc-style absolute path).
	search := object.NewSearchIndex(c.client.Client)
	ref, err := search.FindByInventoryPath(ctx, folder)
	if err != nil {
		return err
	}
	if ref == nil {
		// Retry with /vm/ normalization
		alt := strings.Replace(folder, "/VMs/", "/vm/", 1)
		alt = strings.Replace(alt, "/vms/", "/vm/", 1)
		if alt != folder {
			ref, err = search.FindByInventoryPath(ctx, alt)
			if err != nil {
				return err
			}
		}
	}
	if ref == nil {
		return fmt.Errorf("%q not found", folder)
	}
	return nil
}

func (c *Client) probeISOFolder(ctx context.Context) error {
	folder := strings.Trim(c.ep.ISOFolder, "/")
	if folder == "" {
		return nil
	}
	exists, _, err := c.datastoreFileExists(ctx, folder)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("ISO folder %q not found on datastore (created on first upload)", folder)
	}
	return nil
}

// MappingStatus is the live VM status for a bound UUID.
type MappingStatus struct {
	UUID       string `json:"uuid"`
	Found      bool   `json:"found"`
	Light      Light  `json:"light"` // green=on, blue=off, yellow=error, red=missing
	PowerState string `json:"powerState,omitempty"`
	Name       string `json:"name,omitempty"`
	Path       string `json:"path,omitempty"`
	CDROMISO   string `json:"cdromIso,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (c *Client) MappingStatus(ctx context.Context, uuid string) MappingStatus {
	out := MappingStatus{UUID: uuid, Light: LightRed}
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		out.Error = err.Error()
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not found") || strings.Contains(lower, "outside govc_folder") {
			out.Light = LightRed
			out.Found = false
		} else if isPermissionDenied(err) {
			// Soft: mapping exists; inventory probe limited for this SA.
			out.Light = LightYellow
			out.Found = true
			out.Error = "vSphere permission limited — power/name may be incomplete (VM still bound)"
		} else {
			out.Light = LightYellow
			out.Found = false
		}
		return out
	}
	out.Found = true
	info, err := c.infoFromVM(ctx, vm)
	if err != nil {
		if isPermissionDenied(err) {
			out.Light = LightYellow
			out.Error = "vSphere permission limited — cannot read VM props (VM still bound)"
			return out
		}
		out.Light = LightYellow
		out.Error = err.Error()
		return out
	}
	out.Name = info.Name
	out.PowerState = info.PowerState
	if pathName := vm.InventoryPath; pathName != "" {
		out.Path = pathName
	} else if p, err := find.InventoryPath(ctx, c.client.Client, vm.Reference()); err == nil {
		out.Path = p
	}
	// Path/CDROM permission issues are warnings, not hard failures.
	switch info.PowerState {
	case "On":
		out.Light = LightGreen
	case "Off", "Paused":
		out.Light = LightBlue
	default:
		out.Light = LightYellow
	}
	if iso, err := c.cdromISOPath(ctx, vm); err == nil {
		out.CDROMISO = iso
	} else if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no cdrom") {
		if isPermissionDenied(err) {
			if out.Error == "" {
				out.Error = "cdrom: permission limited (ISO map may need VirtualMachine.Interact / Config.Read)"
			}
		} else if out.Error == "" {
			out.Error = "cdrom: " + err.Error()
		}
	}
	return out
}

func (c *Client) cdromISOPath(ctx context.Context, vm *object.VirtualMachine) (string, error) {
	var m mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware.device"}, &m); err != nil {
		return "", err
	}
	if m.Config == nil {
		return "", fmt.Errorf("no cdrom")
	}
	for _, d := range m.Config.Hardware.Device {
		cd, ok := d.(*types.VirtualCdrom)
		if !ok {
			continue
		}
		switch b := cd.Backing.(type) {
		case *types.VirtualCdromIsoBackingInfo:
			return b.FileName, nil
		case *types.VirtualCdromRemotePassthroughBackingInfo:
			return "", nil
		case *types.VirtualCdromRemoteAtapiBackingInfo:
			return "", nil
		default:
			return "", nil
		}
	}
	return "", fmt.Errorf("no cdrom")
}

// CheckResult is one row in the Test connection checklist.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Skip   bool   `json:"skip,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ConnectionTest is the structured result of POST …/test.
type ConnectionTest struct {
	OK     bool          `json:"ok"`
	Error  string        `json:"error,omitempty"`
	Checks []CheckResult `json:"checks"`
}

func appendCheck(checks *[]CheckResult, name string, ok, skip bool, detail string) {
	*checks = append(*checks, CheckResult{Name: name, OK: ok, Skip: skip, Detail: detail})
}

// RunConnectionTest probes login → datacenter → datastore → folder → ISO folder.
// Datastore may be unset (skipped); folder listing still works without it.
func RunConnectionTest(ctx context.Context, ep Endpoint) ConnectionTest {
	ep.Datastore = NormalizeDatastore(ep.Datastore)
	out := ConnectionTest{Checks: make([]CheckResult, 0, 5)}

	if ep.URL == "" || ep.Username == "" || ep.Password == "" || ep.Datacenter == "" {
		out.Error = "url, username, password, and datacenter are required to test"
		appendCheck(&out.Checks, "Login", false, false, out.Error)
		return out
	}

	c, err := NewClient(ep)
	if err != nil {
		out.Error = err.Error()
		appendCheck(&out.Checks, "Login", false, false, err.Error())
		return out
	}
	defer func() { _ = c.Close(ctx) }()

	if err := c.ensure(ctx); err != nil {
		msg := err.Error()
		out.Error = msg
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "datacenter") {
			appendCheck(&out.Checks, "Login", true, false, "authenticated")
			appendCheck(&out.Checks, "Datacenter", false, false, msg)
		} else if strings.Contains(lower, "datastore") {
			appendCheck(&out.Checks, "Login", true, false, "authenticated")
			appendCheck(&out.Checks, "Datacenter", true, false, ep.Datacenter)
			appendCheck(&out.Checks, "Datastore", false, false, msg)
		} else {
			appendCheck(&out.Checks, "Login", false, false, msg)
		}
		return out
	}

	appendCheck(&out.Checks, "Login", true, false, "authenticated")
	appendCheck(&out.Checks, "Datacenter", true, false, ep.Datacenter)

	if ep.Datastore == "" {
		appendCheck(&out.Checks, "Datastore", false, true, "not set — ISO staging unavailable; folder/UUID still work")
	} else {
		appendCheck(&out.Checks, "Datastore", true, false, ep.Datastore)
	}

	if folder := strings.TrimSpace(ep.Folder); folder == "" {
		appendCheck(&out.Checks, "Folder", false, true, "not set — set GOVC_FOLDER to discover VMs")
	} else if err := c.verifyFolderExists(ctx, folder); err != nil {
		out.Error = err.Error()
		appendCheck(&out.Checks, "Folder", false, false, err.Error())
	} else {
		appendCheck(&out.Checks, "Folder", true, false, folder)
	}

	if ep.Datastore == "" {
		appendCheck(&out.Checks, "ISO folder", false, true, "skipped (no datastore)")
	} else if err := c.probeISOFolder(ctx); err != nil {
		// missing folder is OK (created on upload)
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "not found") {
			appendCheck(&out.Checks, "ISO folder", true, false, fmt.Sprintf("%s (will be created on upload)", ep.ISOFolder))
		} else {
			appendCheck(&out.Checks, "ISO folder", false, false, msg)
			if out.Error == "" {
				out.Error = msg
			}
		}
	} else {
		appendCheck(&out.Checks, "ISO folder", true, false, fmt.Sprintf("%s on %s", ep.ISOFolder, ep.Datastore))
	}

	// Overall ok: login+DC must pass; folder must pass if set; datastore failure is hard fail only when set.
	out.OK = true
	for _, ch := range out.Checks {
		if ch.Skip {
			continue
		}
		if !ch.OK {
			out.OK = false
			break
		}
	}
	if out.OK {
		out.Error = ""
	}
	return out
}

// TestConnection is kept for callers that only need an error.
func TestConnection(ctx context.Context, ep Endpoint) error {
	res := RunConnectionTest(ctx, ep)
	if res.OK {
		return nil
	}
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error)
	}
	return fmt.Errorf("connection test failed")
}
