package vsphere

import (
	"context"
	"fmt"
	"sync"

	"github.com/dasmlab/rf2vc/internal/store"
)

// Pool holds lazy per-vCenter clients keyed by vcenter id.
type Pool struct {
	mu       sync.Mutex
	isoCache string
	clients  map[string]*Client
}

func NewPool(isoCache string) *Pool {
	return &Pool{
		isoCache: isoCache,
		clients:  map[string]*Client{},
	}
}

func (p *Pool) endpointFrom(vc store.VCenter) Endpoint {
	return Endpoint{
		ID:         vc.ID,
		URL:        vc.URL,
		Username:   vc.Username,
		Password:   vc.Password,
		Insecure:   vc.Insecure,
		Datacenter: vc.Datacenter,
		Datastore:  vc.Datastore,
		Folder:     vc.Folder,
		ISOFolder:  vc.ISOFolder,
		ISOCache:   p.isoCache,
	}
}

func sameEndpoint(a, b Endpoint) bool {
	return a.URL == b.URL && a.Username == b.Username && a.Password == b.Password &&
		a.Insecure == b.Insecure && a.Datacenter == b.Datacenter && a.Datastore == b.Datastore &&
		a.Folder == b.Folder && a.ISOFolder == b.ISOFolder
}

func (p *Pool) For(vc store.VCenter) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ep := p.endpointFrom(vc)
	if c, ok := p.clients[vc.ID]; ok {
		if sameEndpoint(c.ep, ep) {
			return c, nil
		}
		_ = c.Close(context.Background())
		delete(p.clients, vc.ID)
	}
	c, err := NewClient(ep)
	if err != nil {
		return nil, err
	}
	p.clients[vc.ID] = c
	return c, nil
}

func (p *Pool) Invalidate(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[id]; ok {
		_ = c.Close(context.Background())
		delete(p.clients, id)
	}
}

func (p *Pool) CloseAll(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		_ = c.Close(ctx)
		delete(p.clients, id)
	}
}

// ClientForUUID resolves store mapping then returns a pooled client.
func (p *Pool) ClientForUUID(st *store.Store, uuid string) (*Client, store.Mapping, error) {
	m, _, ok := st.LookupUUID(uuid)
	if !ok {
		return nil, store.Mapping{}, fmt.Errorf("uuid %s is not mapped to any vCenter", uuid)
	}
	full, ok := st.GetVCenterSecret(m.VCenterID)
	if !ok {
		return nil, m, fmt.Errorf("vcenter %s missing", m.VCenterID)
	}
	c, err := p.For(full)
	if err != nil {
		return nil, m, err
	}
	return c, m, nil
}
