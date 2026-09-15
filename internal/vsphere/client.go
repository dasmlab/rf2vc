package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dasmlab/rf2vc/internal/config"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

type Client struct {
	cfg    *config.Config
	client *govmomi.Client
	finder *find.Finder
	dc     *object.Datacenter
	ds     *object.Datastore

	mu       sync.Mutex
	mediaISO map[string]string // systemUUID -> datastore ISO path
}

// New creates a client that connects to vSphere lazily on first use so the
// HTTP surface (healthz / ServiceRoot) can come up before vCenter is reachable.
func New(_ context.Context, cfg *config.Config) (*Client, error) {
	if err := os.MkdirAll(cfg.ISOCacheDir, 0o755); err != nil {
		return nil, err
	}
	return &Client{
		cfg:      cfg,
		mediaISO: map[string]string{},
	}, nil
}

func (c *Client) ensure(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return nil
	}

	u, err := soap.ParseURL(c.cfg.VSphere.URL)
	if err != nil {
		return fmt.Errorf("parse vsphere url: %w", err)
	}
	u.User = url.UserPassword(c.cfg.VSphere.Username, c.cfg.VSphere.Password)

	client, err := govmomi.NewClient(ctx, u, c.cfg.VSphere.Insecure)
	if err != nil {
		return fmt.Errorf("vsphere login: %w", err)
	}

	finder := find.NewFinder(client.Client, true)
	dc, err := finder.Datacenter(ctx, c.cfg.VSphere.Datacenter)
	if err != nil {
		_ = client.Logout(ctx)
		return fmt.Errorf("datacenter %q: %w", c.cfg.VSphere.Datacenter, err)
	}
	finder.SetDatacenter(dc)

	ds, err := finder.Datastore(ctx, c.cfg.VSphere.Datastore)
	if err != nil {
		_ = client.Logout(ctx)
		return fmt.Errorf("datastore %q: %w", c.cfg.VSphere.Datastore, err)
	}

	c.client = client
	c.finder = finder
	c.dc = dc
	c.ds = ds
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
	return err
}

type SystemInfo struct {
	UUID       string
	Name       string
	PowerState string // On | Off | Paused | ...
	MemoryMiB  int32
	CPUs       int32
}

func (c *Client) allow(name string) bool {
	if len(c.cfg.VSphere.VMAllowlist) == 0 {
		return true
	}
	for _, a := range c.cfg.VSphere.VMAllowlist {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

func (c *Client) ListSystems(ctx context.Context) ([]SystemInfo, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	vms, err := c.finder.VirtualMachineList(ctx, "*")
	if err != nil {
		// empty inventory is ok
		if _, ok := err.(*find.NotFoundError); ok {
			return nil, nil
		}
		return nil, err
	}
	out := make([]SystemInfo, 0, len(vms))
	for _, vm := range vms {
		info, err := c.infoFromVM(ctx, vm)
		if err != nil {
			continue
		}
		if !c.allow(info.Name) {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) GetSystem(ctx context.Context, uuid string) (*SystemInfo, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
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
	if err := vm.Properties(ctx, vm.Reference(), []string{"name", "config.uuid", "runtime.powerState", "config.hardware"}, &m); err != nil {
		return SystemInfo{}, err
	}
	uuid := ""
	if m.Config != nil {
		uuid = m.Config.Uuid
	}
	mem := int32(0)
	cpus := int32(0)
	if m.Config != nil && m.Config.Hardware.MemoryMB != 0 {
		mem = m.Config.Hardware.MemoryMB
		cpus = m.Config.Hardware.NumCPU
	}
	return SystemInfo{
		UUID:       uuid,
		Name:       m.Name,
		PowerState: mapPower(m.Runtime.PowerState),
		MemoryMiB:  mem,
		CPUs:       cpus,
	}, nil
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
	search := object.NewSearchIndex(c.client.Client)
	// false => BIOS UUID (matches BMH Systems/<uuid>); true => instance UUID
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

// Reset implements ComputerSystem.Reset ResetType values Ironic commonly sends.
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

// InsertMedia downloads imageURL, uploads to datastore, attaches as CD-ROM, boots CD once.
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

	dsPath := path.Join(c.cfg.VSphere.ISOFolder, filepath.Base(local))
	if err := c.uploadISO(ctx, local, dsPath); err != nil {
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
	sum := sha256.Sum256([]byte(imageURL))
	name := hex.EncodeToString(sum[:8]) + ".iso"
	dest := filepath.Join(c.cfg.ISOCacheDir, name)
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		return dest, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", err
	}
	httpClient := &http.Client{Timeout: 30 * time.Minute}
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

func (c *Client) uploadISO(ctx context.Context, localPath, dsPath string) error {
	p := soap.DefaultUpload
	return c.ds.UploadFile(ctx, localPath, dsPath, &p)
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
