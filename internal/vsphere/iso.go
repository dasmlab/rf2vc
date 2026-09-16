package vsphere

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// ISOFileName hashes the source URL to a stable datastore/local filename.
func ISOFileName(imageURL string) string {
	sum := sha256.Sum256([]byte(imageURL))
	return hex.EncodeToString(sum[:8]) + ".iso"
}

func (c *Client) isoDSPath(imageURL string) string {
	return path.Join(c.ep.ISOFolder, ISOFileName(imageURL))
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
			return false, 0, nil
		}
		if _, ok := err.(object.DatastoreNoSuchDirectoryError); ok {
			return false, 0, nil
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "no such") {
			return false, 0, nil
		}
		return false, 0, err
	}
	size := fileInfoSize(info)
	return size > 0, size, nil
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
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			log.Printf("ensureISOFolder %s: %v", dsPath, err)
		}
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

func (c *Client) uploadISOIfNeeded(ctx context.Context, localPath, dsPath string) (uploaded bool, err error) {
	exists, size, err := c.datastoreFileExists(ctx, dsPath)
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
	log.Printf("uploading iso to datastore %s", c.ds.Path(dsPath))
	p := soap.DefaultUpload
	if err := c.ds.UploadFile(ctx, localPath, dsPath, &p); err != nil {
		return false, err
	}
	return true, nil
}
