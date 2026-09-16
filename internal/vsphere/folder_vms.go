package vsphere

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
)

// FolderVM is a VM discovered under GOVC_FOLDER (recursive, all subfolders).
type FolderVM struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	PowerState string `json:"powerState,omitempty"`
	Light      Light  `json:"light"`
}

// ListFolderVMs recursively lists VMs under Endpoint.Folder (LAB/, PROD/, …).
// Returns an empty slice when no VMs are found. Errors when Folder is unset
// or the inventory path cannot be resolved.
func (c *Client) ListFolderVMs(ctx context.Context) ([]FolderVM, error) {
	folder := strings.TrimSpace(c.ep.Folder)
	if folder == "" {
		return nil, fmt.Errorf("folder not set")
	}
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}

	vms, err := c.findVMsUnderFolder(ctx, folder)
	if err != nil {
		return nil, err
	}

	out := make([]FolderVM, 0, len(vms))
	seen := map[string]struct{}{}
	for _, vm := range vms {
		var m mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"name", "config.uuid", "runtime.powerState"}, &m); err != nil {
			continue
		}
		uuid := ""
		if m.Config != nil {
			uuid = strings.ToLower(strings.TrimSpace(m.Config.Uuid))
		}
		if uuid == "" || !c.allow(m.Name) {
			continue
		}
		if _, dup := seen[uuid]; dup {
			continue
		}
		seen[uuid] = struct{}{}

		power := mapPower(m.Runtime.PowerState)
		light := LightYellow
		switch power {
		case "On":
			light = LightGreen
		case "Off", "Paused":
			light = LightBlue
		}
		out = append(out, FolderVM{
			UUID:       uuid,
			Name:       m.Name,
			Path:       vm.InventoryPath,
			PowerState: power,
			Light:      light,
		})
	}
	return out, nil
}

func (c *Client) findVMsUnderFolder(ctx context.Context, folder string) ([]*object.VirtualMachine, error) {
	var lastErr error
	for _, base := range folderPathCandidates(folder) {
		pattern := strings.TrimSuffix(base, "/") + "/*"
		vms, err := c.finder.VirtualMachineList(ctx, pattern)
		if err == nil {
			return vms, nil
		}
		var nf *find.NotFoundError
		if errors.As(err, &nf) {
			// Path exists but no VMs — try next candidate; if all empty, return [].
			lastErr = nil
			continue
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// folderPathCandidates returns inventory path variants (/VMs/ vs /vm/).
func folderPathCandidates(folder string) []string {
	folder = strings.TrimSpace(folder)
	folder = strings.TrimSuffix(folder, "/")
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSuffix(strings.TrimSpace(p), "/")
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	add(folder)
	add(strings.Replace(folder, "/VMs/", "/vm/", 1))
	add(strings.Replace(folder, "/vms/", "/vm/", 1))
	add(strings.Replace(folder, "/vm/", "/VMs/", 1))
	return out
}
