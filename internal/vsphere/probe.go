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

	// Login + DC + DS succeeded.
	out.Connection = LightGreen
	out.ConnDetail = "connected · datacenter and datastore OK"

	if folder := strings.TrimSpace(ep.Folder); folder != "" {
		if err := c.verifyFolderExists(ctx, folder); err != nil {
			out.Connection = LightYellow
			out.ConnDetail = "connected, but folder: " + err.Error()
			out.FolderOK = false
		} else {
			out.FolderOK = true
			out.ConnDetail = "connected · datacenter, datastore, and folder OK"
		}
	} else {
		out.FolderOK = true
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
		// Stat of a folder path may return directory with size 0 — treat as yellow/createable
		out.ISOCache = LightYellow
		out.ISODetail = fmt.Sprintf("datastore OK; ISO folder %q missing (will be created on upload)", ep.ISOFolder)
	}

	// Better folder check: list via browser / MakeDirectory already exists check
	if out.ISOCache != LightRed {
		if err := c.probeISOFolder(ctx); err != nil {
			out.ISOCache = LightYellow
			out.ISODetail = err.Error()
		} else {
			out.ISOCache = LightGreen
			out.ISODetail = fmt.Sprintf("datastore %s · folder %s reachable", ep.Datastore, ep.ISOFolder)
		}
	}

	out.OK = out.Connection == LightGreen && (out.ISOCache == LightGreen || out.ISOCache == LightYellow)
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
		} else {
			out.Light = LightYellow
			out.Found = false
		}
		return out
	}
	out.Found = true
	info, err := c.infoFromVM(ctx, vm)
	if err != nil {
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
		// keep power light; attach iso error as soft detail
		if out.Error == "" {
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

// TestConnection logs in and resolves datacenter/datastore/folder, then logs out.
func TestConnection(ctx context.Context, ep Endpoint) error {
	c, err := NewClient(ep)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close(ctx) }()
	if err := c.ensure(ctx); err != nil {
		return err
	}
	if folder := strings.TrimSpace(ep.Folder); folder != "" {
		if err := c.verifyFolderExists(ctx, folder); err != nil {
			return fmt.Errorf("folder %q: %w", folder, err)
		}
	}
	return nil
}
