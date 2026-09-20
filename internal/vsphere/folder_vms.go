package vsphere

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
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
		activity.RunWarn("folder-scan", "folder not set", nil)
		return nil, fmt.Errorf("folder not set")
	}
	activity.Run("folder-scan", "starting", map[string]any{
		"folder":     folder,
		"datacenter": c.ep.Datacenter,
		"datastore":  c.ep.Datastore,
		"vcenter":    c.ep.URL,
	})
	if err := c.ensure(ctx); err != nil {
		activity.RunErr("folder-scan", "ensure failed: "+err.Error(), map[string]any{"folder": folder})
		return nil, err
	}

	vms, err := c.findVMsUnderFolder(ctx, folder)
	if err != nil {
		activity.RunErr("folder-scan", "list failed: "+err.Error(), map[string]any{"folder": folder})
		return nil, err
	}

	out := make([]FolderVM, 0, len(vms))
	seen := map[string]struct{}{}
	skippedNoUUID := 0
	for _, vm := range vms {
		var m mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"name", "config.uuid", "runtime.powerState"}, &m); err != nil {
			activity.RunWarn("folder-scan", "skip vm props: "+err.Error(), map[string]any{"path": vm.InventoryPath})
			continue
		}
		uuid := ""
		if m.Config != nil {
			uuid = strings.ToLower(strings.TrimSpace(m.Config.Uuid))
		}
		if uuid == "" {
			skippedNoUUID++
			continue
		}
		if !c.allow(m.Name) {
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
	activity.Run("folder-scan", fmt.Sprintf("found %d VM(s)", len(out)), map[string]any{
		"folder":         folder,
		"raw":            len(vms),
		"returned":       len(out),
		"skippedNoUUID":  skippedNoUUID,
	})
	return out, nil
}

func (c *Client) findVMsUnderFolder(ctx context.Context, folder string) ([]*object.VirtualMachine, error) {
	var lastErr error
	candidates := folderPathCandidates(folder)
	activity.Run("folder-scan", "trying path candidates", map[string]any{"candidates": candidates})

	for _, base := range candidates {
		// 1) Recursive finder pattern (govmomi "..." = any depth).
		for _, pattern := range []string{
			strings.TrimSuffix(base, "/") + "/...",
			strings.TrimSuffix(base, "/") + "/*",
		} {
			vms, err := c.finder.VirtualMachineList(ctx, pattern)
			if err == nil {
				activity.Run("folder-scan", "finder match", map[string]any{
					"pattern": pattern,
					"count":   len(vms),
				})
				return vms, nil
			}
			var nf *find.NotFoundError
			if errors.As(err, &nf) {
				activity.Run("folder-scan", "finder empty", map[string]any{"pattern": pattern})
				lastErr = nil
				continue
			}
			activity.RunWarn("folder-scan", "finder error: "+err.Error(), map[string]any{"pattern": pattern})
			lastErr = err
		}

		// 2) Inventory-path folder walk (handles odd /VMs/ vs /vm/ layouts).
		vms, err := c.walkFolderInventory(ctx, base)
		if err == nil {
			activity.Run("folder-scan", "inventory walk match", map[string]any{
				"path":  base,
				"count": len(vms),
			})
			return vms, nil
		}
		activity.RunWarn("folder-scan", "inventory walk: "+err.Error(), map[string]any{"path": base})
		if lastErr == nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

func (c *Client) walkFolderInventory(ctx context.Context, folderPath string) ([]*object.VirtualMachine, error) {
	search := object.NewSearchIndex(c.client.Client)
	ref, err := search.FindByInventoryPath(ctx, folderPath)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, fmt.Errorf("inventory path %q not found", folderPath)
	}
	fol, ok := ref.(*object.Folder)
	if !ok {
		// Might be a datacenter vm folder alias — try casting via reference.
		fol = object.NewFolder(c.client.Client, ref.Reference())
	}
	return c.collectVMs(ctx, fol, 0)
}

func (c *Client) collectVMs(ctx context.Context, fol *object.Folder, depth int) ([]*object.VirtualMachine, error) {
	if depth > 32 {
		return nil, fmt.Errorf("folder depth exceeded")
	}
	children, err := fol.Children(ctx)
	if err != nil {
		return nil, err
	}
	var out []*object.VirtualMachine
	for _, ch := range children {
		switch o := ch.(type) {
		case *object.VirtualMachine:
			out = append(out, o)
		case *object.Folder:
			sub, err := c.collectVMs(ctx, o, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		default:
			// VirtualApp / ResourcePool may contain VMs — best-effort via type name.
			if ch.Reference().Type == "VirtualApp" {
				continue
			}
			_ = types.ManagedObjectReference{}
		}
	}
	return out, nil
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
