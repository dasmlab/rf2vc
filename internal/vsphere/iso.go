package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// ISOFileName hashes the source URL to a stable local-cache filename.
func ISOFileName(imageURL string) string {
	sum := sha256.Sum256([]byte(imageURL))
	return hex.EncodeToString(sum[:8]) + ".iso"
}

// contentISOName hashes file bytes so identical Ironic per-node cache URLs
// (same assisted ISO, different boot-*.iso URL) share one datastore object.
func contentISOName(localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:8]) + ".iso", nil
}

func (c *Client) isoDSPath(fileName string) string {
	return path.Join(strings.Trim(c.ep.ISOFolder, "/"), fileName)
}

func (c *Client) datastoreFileExists(ctx context.Context, dsPath string) (bool, int64, error) {
	if err := c.ensure(ctx); err != nil {
		return false, 0, err
	}
	if err := c.requireDatastore(); err != nil {
		return false, 0, err
	}
	info, err := c.ds.Stat(ctx, dsPath)
	if err != nil {
		if _, ok := err.(object.DatastoreNoSuchFileError); ok {
			// Stat only matches files; directories often look "missing". Probe the folder itself.
			if okDir, derr := c.datastoreDirExists(ctx, dsPath); derr != nil {
				return false, 0, derr
			} else if okDir {
				return true, 0, nil
			}
			return false, 0, nil
		}
		if _, ok := err.(object.DatastoreNoSuchDirectoryError); ok {
			return false, 0, nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			if okDir, derr := c.datastoreDirExists(ctx, dsPath); derr != nil {
				return false, 0, derr
			} else if okDir {
				return true, 0, nil
			}
			return false, 0, nil
		}
		return false, 0, err
	}
	size := fileInfoSize(info)
	return size > 0, size, nil
}

// datastoreDirExists is true when SearchDatastore on the path succeeds (even if empty).
func (c *Client) datastoreDirExists(ctx context.Context, dsPath string) (bool, error) {
	dsPath = strings.Trim(dsPath, "/")
	if dsPath == "" {
		return true, nil
	}
	browser, err := c.ds.Browser(ctx)
	if err != nil {
		return false, err
	}
	spec := types.HostDatastoreBrowserSearchSpec{
		Details: &types.FileQueryFlags{FileType: true, FileSize: true},
	}
	task, err := browser.SearchDatastore(ctx, c.ds.Path(dsPath), &spec)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return false, nil
		}
		return false, err
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func fileInfoSize(info types.BaseFileInfo) int64 {
	switch fi := info.(type) {
	case *types.FileInfo:
		return fi.FileSize
	case *types.VmDiskFileInfo:
		return fi.FileSize
	case *types.IsoImageFileInfo:
		return fi.FileSize
	default:
		if fi := info.GetFileInfo(); fi != nil {
			return fi.FileSize
		}
		return 0
	}
}

func fileInfoName(info types.BaseFileInfo) string {
	switch fi := info.(type) {
	case *types.FileInfo:
		return path.Base(fi.Path)
	case *types.VmDiskFileInfo:
		return path.Base(fi.Path)
	case *types.IsoImageFileInfo:
		return path.Base(fi.Path)
	default:
		if fi := info.GetFileInfo(); fi != nil {
			return path.Base(fi.Path)
		}
		return ""
	}
}

func (c *Client) ensureISOFolder(ctx context.Context) error {
	if err := c.ensure(ctx); err != nil {
		return err
	}
	if err := c.requireDatastore(); err != nil {
		return err
	}
	folder := strings.Trim(c.ep.ISOFolder, "/")
	if folder == "" {
		return nil
	}
	fm := object.NewFileManager(c.client.Client)
	accum := ""
	for _, part := range strings.Split(folder, "/") {
		if part == "" {
			continue
		}
		if accum == "" {
			accum = part
		} else {
			accum = path.Join(accum, part)
		}
		dsPath := c.ds.Path(accum)
		err := fm.MakeDirectory(ctx, dsPath, c.dc, true)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "file already exists") {
			continue
		}
		return fmt.Errorf("mkdir %s: %w", dsPath, err)
	}
	ok, err := c.datastoreDirExists(ctx, folder)
	if err != nil {
		return fmt.Errorf("verify iso folder %s: %w", c.ds.Path(folder), err)
	}
	if !ok {
		return fmt.Errorf("iso folder %s missing after mkdir", c.ds.Path(folder))
	}
	return nil
}

// ISOCacheEntry is one staged ISO (local cache and/or datastore).
type ISOCacheEntry struct {
	Name          string `json:"name"`
	DatastorePath string `json:"datastorePath"`
	OnDatastore   bool   `json:"onDatastore"`
	DatastoreSize int64  `json:"datastoreSize,omitempty"`
	LocalCached   bool   `json:"localCached"`
	LocalSize     int64  `json:"localSize,omitempty"`
	LocalPath     string `json:"localPath,omitempty"`
}

// ISOCacheStatus summarizes staging for a vCenter endpoint.
type ISOCacheStatus struct {
	Datastore       string          `json:"datastore"`
	ISOFolder       string          `json:"isoFolder"`
	LocalCache      string          `json:"localCache"`
	Reachable       bool            `json:"reachable"`
	Error           string          `json:"error,omitempty"`
	Files           []ISOCacheEntry `json:"files"`
	DatastoreCount  int             `json:"datastoreCount"`
	LocalCacheCount int             `json:"localCacheCount"`
}

func (c *Client) ISOCacheStatus(ctx context.Context) ISOCacheStatus {
	out := ISOCacheStatus{
		Datastore:  c.ep.Datastore,
		ISOFolder:  c.ep.ISOFolder,
		LocalCache: c.ep.ISOCache,
		Files:      []ISOCacheEntry{},
	}

	localByName := map[string]ISOCacheEntry{}
	if entries, err := os.ReadDir(c.ep.ISOCache); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".iso") {
				continue
			}
			info, _ := e.Info()
			size := int64(0)
			if info != nil {
				size = info.Size()
			}
			localByName[e.Name()] = ISOCacheEntry{
				Name:        e.Name(),
				LocalCached: size > 0,
				LocalSize:   size,
				LocalPath:   filepath.Join(c.ep.ISOCache, e.Name()),
			}
			out.LocalCacheCount++
		}
	}

	if err := c.ensure(ctx); err != nil {
		out.Error = err.Error()
		for _, e := range localByName {
			e.DatastorePath = path.Join(c.ep.ISOFolder, e.Name)
			out.Files = append(out.Files, e)
		}
		return out
	}
	if err := c.requireDatastore(); err != nil {
		out.Error = err.Error()
		out.Reachable = true // login OK; ISO path unavailable without DS
		for _, e := range localByName {
			e.DatastorePath = path.Join(c.ep.ISOFolder, e.Name)
			out.Files = append(out.Files, e)
		}
		return out
	}
	out.Reachable = true

	browser, err := c.ds.Browser(ctx)
	if err != nil {
		out.Error = err.Error()
	} else {
		spec := types.HostDatastoreBrowserSearchSpec{
			Details: &types.FileQueryFlags{
				FileType:     true,
				FileSize:     true,
				Modification: true,
			},
			MatchPattern: []string{"*.iso"},
		}
		dsPath := c.ds.Path(c.ep.ISOFolder)
		task, err := browser.SearchDatastore(ctx, dsPath, &spec)
		if err != nil {
			msg := err.Error()
			if !strings.Contains(strings.ToLower(msg), "not found") {
				out.Error = msg
			}
		} else if info, err := task.WaitForResult(ctx, nil); err != nil {
			msg := err.Error()
			if !strings.Contains(strings.ToLower(msg), "not found") {
				out.Error = msg
			}
		} else if res, ok := info.Result.(types.HostDatastoreBrowserSearchResults); ok {
			for _, f := range res.File {
				name := fileInfoName(f)
				size := fileInfoSize(f)
				if name == "" || !strings.HasSuffix(name, ".iso") {
					continue
				}
				ent := localByName[name]
				delete(localByName, name)
				ent.Name = name
				ent.OnDatastore = size > 0
				ent.DatastoreSize = size
				ent.DatastorePath = path.Join(c.ep.ISOFolder, name)
				out.Files = append(out.Files, ent)
				out.DatastoreCount++
			}
		}
	}

	for _, e := range localByName {
		e.DatastorePath = path.Join(c.ep.ISOFolder, e.Name)
		out.Files = append(out.Files, e)
	}
	return out
}

type uploadCall struct {
	done chan struct{}
	err  error
}

func (c *Client) uploadISOIfNeeded(ctx context.Context, localPath, dsPath string) (uploaded bool, err error) {
	exists, size, err := c.datastoreFileExists(ctx, dsPath)
	if err != nil {
		return false, err
	}
	if exists {
		log.Printf("iso already on datastore %s (%d bytes) — skip upload", c.ds.Path(dsPath), size)
		return false, nil
	}

	// Single-flight identical datastore targets (Ironic retries × N BMHs).
	c.uploadMu.Lock()
	if c.uploadInFlight == nil {
		c.uploadInFlight = map[string]*uploadCall{}
	}
	if call, ok := c.uploadInFlight[dsPath]; ok {
		c.uploadMu.Unlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-call.done:
			if call.err != nil {
				return false, call.err
			}
			return false, nil
		}
	}
	call := &uploadCall{done: make(chan struct{})}
	c.uploadInFlight[dsPath] = call
	c.uploadMu.Unlock()

	defer func() {
		call.err = err
		close(call.done)
		c.uploadMu.Lock()
		delete(c.uploadInFlight, dsPath)
		c.uploadMu.Unlock()
	}()

	// Serialize PUTs against vCenter — parallel uploads to Qumulo were returning 503.
	c.uploadSerial.Lock()
	defer c.uploadSerial.Unlock()

	// Re-check under the serial lock; another host may have finished.
	exists, size, err = c.datastoreFileExists(ctx, dsPath)
	if err != nil {
		return false, err
	}
	if exists {
		log.Printf("iso already on datastore %s (%d bytes) — skip upload", c.ds.Path(dsPath), size)
		return false, nil
	}

	if err := c.ensureISOFolder(ctx); err != nil {
		return false, fmt.Errorf("ensure iso folder: %w", err)
	}

	st, err := os.Stat(localPath)
	if err != nil {
		return false, err
	}
	if st.Size() < 1024*1024 {
		return false, fmt.Errorf("local iso %s is only %d bytes (not a real ISO; re-download)", localPath, st.Size())
	}

	// Ensure dcPath/dsName are populated for /folder/ URLs (404 when empty/wrong).
	if c.ds.DatacenterPath == "" || c.ds.InventoryPath == "" {
		if ferr := c.ds.FindInventoryPath(ctx); ferr != nil {
			log.Printf("FindInventoryPath: %v", ferr)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= 4; attempt++ {
		lastErr = c.putISO(ctx, localPath, dsPath, st.Size())
		if lastErr == nil {
			// Confirm the file landed (avoids "success" that never appears in the UI).
			ok, got, serr := c.datastoreFileExists(ctx, dsPath)
			if serr != nil {
				return true, fmt.Errorf("upload ok but stat failed: %w", serr)
			}
			if !ok || got == 0 {
				return true, fmt.Errorf("upload reported ok but %s still missing on datastore", c.ds.Path(dsPath))
			}
			log.Printf("uploaded iso %s (%d bytes)", c.ds.Path(dsPath), got)
			return true, nil
		}
		msg := strings.ToLower(lastErr.Error())
		retryable := strings.Contains(msg, "503") || strings.Contains(msg, "502") ||
			strings.Contains(msg, "unavailable") || strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "404")
		if !retryable || attempt == 4 {
			break
		}
		backoff := time.Duration(attempt) * 2 * time.Second
		log.Printf("iso upload attempt %d failed (%v); retry in %s", attempt, lastErr, backoff)
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return false, lastErr
}

// putISO uploads via an ESXi host ticket first (reliable for NFS/Qumulo), then
// falls back to a vCenter /folder/ PUT using the exact ticket URL.
func (c *Client) putISO(ctx context.Context, localPath, dsPath string, size int64) error {
	if err := c.putISOViaHost(ctx, localPath, dsPath, size); err == nil {
		return nil
	} else {
		log.Printf("host-ticket iso upload failed (%v); trying vCenter /folder/", err)
	}
	return c.putISOViaVC(ctx, localPath, dsPath, size)
}

func (c *Client) putISOViaHost(ctx context.Context, localPath, dsPath string, size int64) error {
	hosts, err := c.ds.AttachedHosts(ctx)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no attached hosts for datastore")
	}
	// Try a few hosts — management IPs may be unreachable from the pod network.
	var last error
	limit := 3
	if len(hosts) < limit {
		limit = len(hosts)
	}
	for i := 0; i < limit; i++ {
		h := hosts[i]
		name, _ := h.ObjectName(ctx)
		log.Printf("uploading iso via host %s → %s (%d bytes)", name, c.ds.Path(dsPath), size)
		hctx := c.ds.HostContext(ctx, h)
		p := soap.DefaultUpload
		if err := c.ds.UploadFile(hctx, localPath, dsPath, &p); err != nil {
			last = err
			log.Printf("host %s upload: %v", name, err)
			continue
		}
		return nil
	}
	return last
}

func (c *Client) putISOViaVC(ctx context.Context, localPath, dsPath string, size int64) error {
	u := c.ds.NewURL(dsPath)
	// Strip userinfo — tickets/cookies auth the transfer; userinfo breaks some VC proxies.
	u.User = nil

	log.Printf("uploading iso via vCenter %s (%d bytes) dcPath=%q dsName=%q url=%s",
		c.ds.Path(dsPath), size, c.ds.DatacenterPath, c.ds.Name(), u.Redacted())

	sm := session.NewManager(c.client.Client)
	ticket, err := sm.AcquireGenericServiceTicket(ctx, &types.SessionManagerHttpServiceRequestSpec{
		Url:    u.String(),
		Method: string(types.SessionManagerHttpServiceRequestSpecMethodHttpPut),
	})
	if err != nil {
		return fmt.Errorf("acquire upload ticket: %w", err)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	p := soap.DefaultUpload
	p.Ticket = &http.Cookie{Name: "vmware_cgi_ticket", Value: ticket.Id}
	p.Close = true
	p.ContentLength = size
	// Upload to the exact URL we ticketed (do not re-derive via Datastore.UploadFile).
	if err := c.client.Client.Upload(ctx, f, u, &p); err != nil {
		return err
	}
	return nil
}
