package vsphere

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
}

func NewClient(ep Endpoint) (*Client, error) {
	if ep.ISOCache == "" {
		ep.ISOCache = "/var/tmp/rf2vc"
	}
	if ep.ISOFolder == "" {
		ep.ISOFolder = "rf2vc/isos"
	}
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

	ds, err := finder.Datastore(ctx, c.ep.Datastore)
	if err != nil {
		_ = client.Logout(ctx)
		return fmt.Errorf("datastore %q: %w", c.ep.Datastore, err)
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
	c.finder = nil
	c.dc = nil
	c.ds = nil
	return err
}

// TestConnection logs in and resolves datacenter/datastore, then logs out.
func TestConnection(ctx context.Context, ep Endpoint) error {
	c, err := NewClient(ep)
	if err != nil {
		return err
	}
	if err := c.ensure(ctx); err != nil {
		return err
	}
	return c.Close(ctx)
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

	dsPath := c.isoDSPath(imageURL)

	// Fast path: ISO already staged on datastore — attach without download/upload.
	if exists, size, err := c.datastoreFileExists(ctx, dsPath); err != nil {
		return fmt.Errorf("stat datastore iso: %w", err)
	} else if exists {
		log.Printf("InsertMedia system=%s reuse datastore iso %s (%d bytes)", uuid, dsPath, size)
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

	local, err := c.downloadISO(ctx, imageURL)
	if err != nil {
		return fmt.Errorf("download iso: %w", err)
	}

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
