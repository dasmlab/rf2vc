package vsphere

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// Endpoint is per-vCenter connection settings (GOVC-shaped).
type Endpoint struct {
	ID         string
	URL        string
	Username   string
	Password   string
	Insecure   bool
	Datacenter string
	Datastore  string
	Folder     string // GOVC_FOLDER inventory path
	ISOFolder  string
	ISOCache   string
	Allowlist  []string
}

type Client struct {
	ep     Endpoint
	client *govmomi.Client
	finder *find.Finder
	dc     *object.Datacenter
	ds     *object.Datastore

	mu       sync.Mutex
	mediaISO map[string]string

	uuidMu            sync.Mutex
	uuidCache         map[string]uuidCacheEntry
	searchIndexDenied bool // FindByUuid permanently denied for this SA

	uploadSerial   sync.Mutex // serialize datastore PUTs (avoid VC/Qumulo 503 storms)
	uploadMu       sync.Mutex
	uploadInFlight map[string]*uploadCall
}

type uuidCacheEntry struct {
	ref  types.ManagedObjectReference
	path string
	at   time.Time
}

const uuidCacheTTL = 2 * time.Minute


// NormalizeDatastore treats placeholders (NONE, notset, n/a, -) as unset.
// Folder listing and UUID lookup work without a datastore; ISO staging does not.
func NormalizeDatastore(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "none", "notset", "not set", "n/a", "na", "-", "null", "undefined":
		return ""
	default:
		return s
	}
}

func NewClient(ep Endpoint) (*Client, error) {
	if ep.ISOCache == "" {
		ep.ISOCache = "/var/tmp/rf2vc"
	}
	if ep.ISOFolder == "" {
		ep.ISOFolder = "rf2vc/isos"
	}
	ep.Datastore = NormalizeDatastore(ep.Datastore)
	if err := os.MkdirAll(ep.ISOCache, 0o755); err != nil {
		return nil, err
	}
	return &Client{
		ep:       ep,
		mediaISO: map[string]string{},
	}, nil
}

func (c *Client) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return nil
	}

	u, err := soap.ParseURL(c.ep.URL)
	if err != nil {
		return fmt.Errorf("parse vsphere url: %w", err)
	}
	u.User = url.UserPassword(c.ep.Username, c.ep.Password)

	client, err := govmomi.NewClient(ctx, u, c.ep.Insecure)
	if err != nil {
		return fmt.Errorf("vsphere login: %w", err)
	}

	finder := find.NewFinder(client.Client, true)
	dc, err := finder.Datacenter(ctx, c.ep.Datacenter)
	if err != nil {
		_ = client.Logout(ctx)
		return fmt.Errorf("datacenter %q: %w", c.ep.Datacenter, err)
	}
	finder.SetDatacenter(dc)

	var ds *object.Datastore
	if name := NormalizeDatastore(c.ep.Datastore); name != "" {
		ds, err = finder.Datastore(ctx, name)
		if err != nil {
			_ = client.Logout(ctx)
			return fmt.Errorf("datastore %q: %w", name, err)
		}
		c.ep.Datastore = name
	}

	c.client = client
	c.finder = finder
	c.dc = dc
	c.ds = ds
	return nil
}

func (c *Client) requireDatastore() error {
	if c.ds == nil || NormalizeDatastore(c.ep.Datastore) == "" {
		return fmt.Errorf("datastore not set (needed for ISO cache; folder/UUID ops do not require it)")
	}
	return nil
}

func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	err := c.client.Logout(ctx)
	c.client = nil
	c.finder = nil
	c.dc = nil
	c.ds = nil
	return err
}

type SystemInfo struct {
	UUID       string
	Name       string
	PowerState string
	MemoryMiB  int32
	CPUs       int32
}

func (c *Client) allow(name string) bool {
	if len(c.ep.Allowlist) == 0 {
		return true
	}
	for _, a := range c.ep.Allowlist {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

func (c *Client) GetSystem(ctx context.Context, uuid string) (*SystemInfo, error) {
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		return nil, err
	}
	info, err := c.infoFromVM(ctx, vm)
	if err != nil {
		return nil, err
	}
	if !c.allow(info.Name) {
		return nil, fmt.Errorf("system %s not allowed", uuid)
	}
	return &info, nil
}

func (c *Client) infoFromVM(ctx context.Context, vm *object.VirtualMachine) (SystemInfo, error) {
	var m mo.VirtualMachine
	// Minimal props first — some service accounts cannot read config.hardware.
	if err := vm.Properties(ctx, vm.Reference(), []string{"name", "config.uuid", "runtime.powerState"}, &m); err != nil {
		return SystemInfo{}, err
	}
	uuid := ""
	if m.Config != nil {
		uuid = m.Config.Uuid
	}
	info := SystemInfo{
		UUID:       uuid,
		Name:       m.Name,
		PowerState: mapPower(m.Runtime.PowerState),
	}
	var hw mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware"}, &hw); err == nil && hw.Config != nil {
		info.MemoryMiB = hw.Config.Hardware.MemoryMB
		info.CPUs = hw.Config.Hardware.NumCPU
	}
	return info, nil
}

func mapPower(p types.VirtualMachinePowerState) string {
	switch p {
	case types.VirtualMachinePowerStatePoweredOn:
		return "On"
	case types.VirtualMachinePowerStatePoweredOff:
		return "Off"
	case types.VirtualMachinePowerStateSuspended:
		return "Paused"
	default:
		return "Off"
	}
}

func (c *Client) findByUUID(ctx context.Context, uuid string) (*object.VirtualMachine, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	uuid = strings.ToLower(strings.TrimSpace(uuid))

	if vm := c.cachedVM(uuid); vm != nil {
		return vm, nil
	}

	c.uuidMu.Lock()
	skipSearch := c.searchIndexDenied
	c.uuidMu.Unlock()

	var firstErr error
	if !skipSearch {
		vm, err := c.findByUUIDSearchIndex(ctx, uuid)
		if err == nil {
			if err := c.checkFolder(ctx, vm); err != nil {
				return nil, err
			}
			c.rememberUUID(uuid, vm)
			return vm, nil
		}
		firstErr = err
		if isPermissionDenied(err) {
			c.uuidMu.Lock()
			c.searchIndexDenied = true
			c.uuidMu.Unlock()
		}
	} else {
		firstErr = fmt.Errorf("SearchIndex.FindByUuid previously denied")
	}

	// SearchIndex.FindByUuid is often denied for least-priv SAs that can still
	// list/operate VMs under GOVC_FOLDER (same as govc vm.info $GOVC_FOLDER/...).
	if folder := strings.TrimSpace(c.ep.Folder); folder != "" {
		vm, ferr := c.findByUUIDInFolder(ctx, uuid, folder)
		if ferr == nil {
			c.rememberUUID(uuid, vm)
			activity.Run("vm-lookup", "resolved via folder walk (FindByUuid unavailable)", map[string]any{
				"uuid":           uuid,
				"path":           vm.InventoryPath,
				"searchIndexErr": firstErr.Error(),
			})
			return vm, nil
		}
		activity.RunWarn("vm-lookup", "folder walk missed uuid", map[string]any{
			"uuid":   uuid,
			"folder": folder,
			"error":  ferr.Error(),
			"first":  firstErr.Error(),
		})
		// Prefer the folder-walk error — SearchIndex denial is expected for this SA.
		return nil, ferr
	}
	return nil, firstErr
}

func (c *Client) cachedVM(uuid string) *object.VirtualMachine {
	c.uuidMu.Lock()
	defer c.uuidMu.Unlock()
	if c.client == nil || c.uuidCache == nil {
		return nil
	}
	e, ok := c.uuidCache[uuid]
	if !ok || time.Since(e.at) > uuidCacheTTL {
		return nil
	}
	vm := object.NewVirtualMachine(c.client.Client, e.ref)
	if e.path != "" {
		vm.SetInventoryPath(e.path)
	}
	return vm
}

func (c *Client) rememberUUID(uuid string, vm *object.VirtualMachine) {
	if vm == nil {
		return
	}
	c.uuidMu.Lock()
	defer c.uuidMu.Unlock()
	if c.uuidCache == nil {
		c.uuidCache = map[string]uuidCacheEntry{}
	}
	c.uuidCache[uuid] = uuidCacheEntry{
		ref:  vm.Reference(),
		path: vm.InventoryPath,
		at:   time.Now(),
	}
}

func (c *Client) findByUUIDSearchIndex(ctx context.Context, uuid string) (*object.VirtualMachine, error) {
	search := object.NewSearchIndex(c.client.Client)
	instanceUUID := false
	ref, err := search.FindByUuid(ctx, c.dc, uuid, true, &instanceUUID)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, fmt.Errorf("vm uuid %s not found", uuid)
	}
	vm, ok := ref.(*object.VirtualMachine)
	if !ok {
		return nil, fmt.Errorf("uuid %s is not a VirtualMachine", uuid)
	}
	return vm, nil
}

// findByUUIDInFolder walks GOVC_FOLDER (recursive) and matches BIOS UUID —
// mirrors how govc resolves $GOVC_FOLDER/... when datacenter-wide FindByUuid is blocked.
func (c *Client) findByUUIDInFolder(ctx context.Context, uuid, folder string) (*object.VirtualMachine, error) {
	vms, err := c.findVMsUnderFolder(ctx, folder)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(uuid))
	for _, vm := range vms {
		var m mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"config.uuid", "name"}, &m); err != nil {
			continue
		}
		got := ""
		if m.Config != nil {
			got = strings.ToLower(strings.TrimSpace(m.Config.Uuid))
		}
		if got == "" || got != want {
			continue
		}
		if !c.allow(m.Name) {
			return nil, fmt.Errorf("system %s not allowed", uuid)
		}
		return vm, nil
	}
	return nil, fmt.Errorf("vm uuid %s not found under folder %q", uuid, folder)
}

// normalizeInventoryPath lowercases and trims for folder compares.
func normalizeInventoryPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return strings.ToLower(strings.TrimSuffix(p, "/"))
}

func (c *Client) checkFolder(ctx context.Context, vm *object.VirtualMachine) error {
	folder := strings.TrimSpace(c.ep.Folder)
	if folder == "" {
		return nil
	}
	pathName := vm.InventoryPath
	if pathName == "" {
		p, err := find.InventoryPath(ctx, c.client.Client, vm.Reference())
		if err != nil {
			// Some service accounts can FindByUuid + list folder VMs but cannot
			// resolve InventoryPath. Soft-skip rather than blocking all Redfish ops.
			if isPermissionDenied(err) {
				activity.RunWarn("folder-check", "InventoryPath denied — skipping folder gate", map[string]any{
					"uuid":   vm.Reference().Value,
					"folder": folder,
					"error":  err.Error(),
				})
				return nil
			}
			return fmt.Errorf("vm path: %w", err)
		}
		pathName = p
		vm.SetInventoryPath(p)
	}
	want := normalizeInventoryPath(folder)
	got := normalizeInventoryPath(pathName)
	if got == want || strings.HasPrefix(got, want+"/") {
		return nil
	}
	return fmt.Errorf("vm %s path %q is outside GOVC_FOLDER %q", vm.Name(), pathName, folder)
}

func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission") && strings.Contains(msg, "denied")
}

func (c *Client) Reset(ctx context.Context, uuid, resetType string) error {
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		return err
	}
	info, err := c.infoFromVM(ctx, vm)
	if err != nil {
		return err
	}
	if !c.allow(info.Name) {
		return fmt.Errorf("system %s not allowed", uuid)
	}

	switch strings.ToLower(resetType) {
	case "on":
		return c.powerOn(ctx, vm)
	case "forceoff", "gracefulshutdown", "nmi":
		return c.powerOff(ctx, vm)
	case "forcerestart", "gracefulrestart", "powercycle":
		if err := c.powerOff(ctx, vm); err != nil {
			return err
		}
		return c.powerOn(ctx, vm)
	default:
		return fmt.Errorf("unsupported ResetType %q", resetType)
	}
}

func (c *Client) powerOn(ctx context.Context, vm *object.VirtualMachine) error {
	task, err := vm.PowerOn(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (c *Client) powerOff(ctx context.Context, vm *object.VirtualMachine) error {
	state, err := vm.PowerState(ctx)
	if err != nil {
		return err
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		return nil
	}
	task, err := vm.PowerOff(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (c *Client) SetBootCDOnce(ctx context.Context, uuid string) error {
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		return err
	}
	spec := types.VirtualMachineConfigSpec{
		BootOptions: &types.VirtualMachineBootOptions{
			BootOrder: []types.BaseVirtualMachineBootOptionsBootableDevice{
				&types.VirtualMachineBootOptionsBootableCdromDevice{},
				&types.VirtualMachineBootOptionsBootableDiskDevice{},
			},
		},
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

type MediaStatus struct {
	Inserted bool
	Image    string
}

func (c *Client) MediaStatus(uuid string) MediaStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.mediaISO[strings.ToLower(uuid)]
	return MediaStatus{Inserted: ok && img != "", Image: img}
}

func (c *Client) InsertMedia(ctx context.Context, uuid, imageURL string) error {
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		return err
	}
	info, err := c.infoFromVM(ctx, vm)
	if err != nil {
		return err
	}
	if !c.allow(info.Name) {
		return fmt.Errorf("system %s not allowed", uuid)
	}

	local, err := c.downloadISO(ctx, imageURL)
	if err != nil {
		return fmt.Errorf("download iso: %w", err)
	}

	// Content-hash so all BMHs sharing the same assisted ISO reuse one datastore file
	// (Ironic hands out distinct https://…:6183/redfish/boot-<node>.iso URLs).
	contentName, err := contentISOName(local)
	if err != nil {
		return fmt.Errorf("hash iso: %w", err)
	}
	dsPath := c.isoDSPath(contentName)

	if _, err := c.uploadISOIfNeeded(ctx, local, dsPath); err != nil {
		return fmt.Errorf("upload iso: %w", err)
	}

	if err := c.attachCDROM(ctx, vm, dsPath); err != nil {
		return fmt.Errorf("attach cdrom: %w", err)
	}
	if err := c.SetBootCDOnce(ctx, uuid); err != nil {
		return fmt.Errorf("set boot cd: %w", err)
	}

	c.mu.Lock()
	c.mediaISO[strings.ToLower(uuid)] = imageURL
	c.mu.Unlock()
	return nil
}

func (c *Client) EjectMedia(ctx context.Context, uuid string) error {
	vm, err := c.findByUUID(ctx, uuid)
	if err != nil {
		return err
	}
	if err := c.detachCDROM(ctx, vm); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.mediaISO, strings.ToLower(uuid))
	c.mu.Unlock()
	return nil
}

func (c *Client) downloadISO(ctx context.Context, imageURL string) (string, error) {
	name := ISOFileName(imageURL)
	dest := filepath.Join(c.ep.ISOCache, name)
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		return dest, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	// Ironic hands us its conductor image-cache URL (often https://<provisioning-IP>:6183/…).
	// That cert is self-signed and almost never has an IP SAN — same as BMH
	// disableCertificateVerification. Skip verify for ISO fetch only.
	httpClient := &http.Client{
		Timeout: 30 * time.Minute,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // Ironic/assisted image URLs
			},
		},
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s: %s", imageURL, resp.Status)
	}

	tmp := dest + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (c *Client) attachCDROM(ctx context.Context, vm *object.VirtualMachine, dsISOPath string) error {
	devices, err := vm.Device(ctx)
	if err != nil {
		return err
	}

	iso := &types.VirtualCdromIsoBackingInfo{
		VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
			FileName: c.ds.Path(dsISOPath),
		},
	}

	cdroms := devices.SelectByType((*types.VirtualCdrom)(nil))
	var spec types.VirtualMachineConfigSpec
	if len(cdroms) == 0 {
		controller, err := devices.FindIDEController("")
		if err != nil {
			return fmt.Errorf("find IDE controller for cdrom: %w", err)
		}
		cd, err := devices.CreateCdrom(controller)
		if err != nil {
			return err
		}
		cd.Backing = iso
		cd.Connectable = &types.VirtualDeviceConnectInfo{
			StartConnected:    true,
			Connected:         true,
			AllowGuestControl: true,
		}
		spec.DeviceChange = append(spec.DeviceChange, &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    cd,
		})
	} else {
		cd := cdroms[0].(*types.VirtualCdrom)
		cd.Backing = iso
		cd.Connectable = &types.VirtualDeviceConnectInfo{
			StartConnected:    true,
			Connected:         true,
			AllowGuestControl: true,
		}
		spec.DeviceChange = append(spec.DeviceChange, &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationEdit,
			Device:    cd,
		})
	}

	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (c *Client) detachCDROM(ctx context.Context, vm *object.VirtualMachine) error {
	devices, err := vm.Device(ctx)
	if err != nil {
		return err
	}
	cdroms := devices.SelectByType((*types.VirtualCdrom)(nil))
	if len(cdroms) == 0 {
		return nil
	}
	cd := cdroms[0].(*types.VirtualCdrom)
	cd.Backing = &types.VirtualCdromRemotePassthroughBackingInfo{
		VirtualDeviceRemoteDeviceBackingInfo: types.VirtualDeviceRemoteDeviceBackingInfo{
			DeviceName: "",
		},
	}
	if cd.Connectable != nil {
		cd.Connectable.Connected = false
		cd.Connectable.StartConnected = false
	}
	spec := types.VirtualMachineConfigSpec{
		DeviceChange: []types.BaseVirtualDeviceConfigSpec{
			&types.VirtualDeviceConfigSpec{
				Operation: types.VirtualDeviceConfigSpecOperationEdit,
				Device:    cd,
			},
		},
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}
