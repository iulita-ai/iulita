package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type runtimePool struct {
	mu      sync.Mutex
	root    context.Context
	closed  bool
	entries map[string]*runtimeEntry
	workers sync.WaitGroup
}
type runtimeEntry struct {
	owner, id, key string
	ready          chan struct{}
	use            chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	session        *sdk.ClientSession
	err            error
	once           sync.Once
}
type dispatchKey struct{}

// SetRoot binds runtime lifetimes to the application shutdown context.
func (p *SDKProtocol) SetRoot(ctx context.Context) {
	p.pool.mu.Lock()
	p.pool.root = ctx
	p.pool.mu.Unlock()
}
func (p *SDKProtocol) acquire(ctx context.Context, c Connection) (*sdk.ClientSession, func(bool), error) {
	// No tenant or credential revision can inherit another runtime, even for the
	// same endpoint. Legacy sessions and stdio process state survive normal calls.
	data, marshalErr := json.Marshal(struct {
		Owner, ID, Endpoint, Transport, Profile string
		Generation                              int64
		Token                                   string
		Headers                                 map[string]string
	}{c.OwnerID, c.ID, c.Endpoint, c.Transport, c.Profile, c.Generation, c.Token, c.Headers})
	if marshalErr != nil {
		return nil, nil, ErrInvalid
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	p.pool.mu.Lock()
	if p.pool.closed {
		p.pool.mu.Unlock()
		return nil, nil, ErrUnavailable
	}
	if p.pool.entries == nil {
		p.pool.entries = map[string]*runtimeEntry{}
	}
	entry := p.pool.entries[key]
	creator := entry == nil
	if creator {
		root := p.pool.root
		if root == nil {
			root = context.Background()
		}
		lifetime, cancel := context.WithTimeout(root, 5*time.Minute)
		entry = &runtimeEntry{owner: c.OwnerID, id: c.ID, key: key, ready: make(chan struct{}), use: make(chan struct{}, 1), ctx: lifetime, cancel: cancel}
		p.pool.entries[key] = entry
	}
	p.pool.mu.Unlock()
	if creator {
		work, cancel := context.WithTimeout(ctx, 30*time.Second)
		stop := context.AfterFunc(entry.ctx, cancel)
		session, err := p.open(work, entry.ctx, c)
		stop()
		cancel()
		entry.session = session
		entry.err = err
		close(entry.ready)
		context.AfterFunc(entry.ctx, func() { p.retire(entry) })
		if err != nil {
			p.retire(entry)
		}
	}
	select {
	case <-entry.ready:
	case <-ctx.Done():
		return nil, nil, ErrUnavailable
	}
	if entry.err != nil {
		return nil, nil, ErrUnavailable
	}
	select {
	case entry.use <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ErrUnavailable
	case <-entry.ctx.Done():
		return nil, nil, ErrUnavailable
	}
	if entry.ctx.Err() != nil {
		<-entry.use
		return nil, nil, ErrUnavailable
	}
	return entry.session, func(healthy bool) {
		if !healthy {
			p.retire(entry)
		}
		<-entry.use
	}, nil
}
func (p *SDKProtocol) retire(entry *runtimeEntry) {
	entry.once.Do(func() {
		entry.cancel()
		p.pool.mu.Lock()
		if p.pool.entries[entry.key] == entry {
			delete(p.pool.entries, entry.key)
		}
		p.pool.workers.Add(1)
		p.pool.mu.Unlock()
		go func() {
			defer p.pool.workers.Done()
			<-entry.ready
			if entry.session != nil {
				closeQuietly(entry.session)
			}
		}()
	})
}

// Invalidate closes admission to the runtime synchronously; teardown runs off
// the service mutex so SDK callbacks cannot deadlock a revoke operation.
func (p *SDKProtocol) Invalidate(owner, id string) {
	p.pool.mu.Lock()
	list := make([]*runtimeEntry, 0, len(p.pool.entries))
	for _, e := range p.pool.entries {
		if e.owner == owner && e.id == id {
			list = append(list, e)
		}
	}
	p.pool.mu.Unlock()
	for _, e := range list {
		p.retire(e)
	}
}

// Close closes admission, drains active work and releases owned runtime resources.
func (p *SDKProtocol) Close() error {
	p.pool.mu.Lock()
	p.pool.closed = true
	list := make([]*runtimeEntry, 0, len(p.pool.entries))
	for _, e := range p.pool.entries {
		list = append(list, e)
	}
	p.pool.mu.Unlock()
	for _, e := range list {
		p.retire(e)
	}
	p.pool.workers.Wait()
	if transport, ok := p.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
	return nil
}
