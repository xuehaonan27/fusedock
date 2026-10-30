// Package daemon owns account sessions, resolved mounts and the shared cache.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xuehaonan27/fusedock/internal/accounts"
	"github.com/xuehaonan27/fusedock/internal/blockcache"
	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/internal/mountapi"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type MountSpec struct {
	ID          string              `json:"id"`
	Account     string              `json:"account"`
	AccountID   namespace.AccountID `json:"account_id,omitempty"`
	Path        string              `json:"path"`
	Scope       string              `json:"scope"`
	Persistent  bool                `json:"persistent"`
	MetadataTTL time.Duration       `json:"metadata_ttl"`
}
type MountStatus struct {
	Spec     MountSpec `json:"spec"`
	State    string    `json:"state"`
	Error    string    `json:"error,omitempty"`
	VolumeID string    `json:"volume_id,omitempty"`
}
type mounted struct {
	status MountStatus
	result error
	cancel context.CancelFunc
	done   chan struct{}
	target *namespace.Target
}
type session struct {
	account accounts.Account
	vfs     *vfs.Session
}
type MountFunc func(context.Context, provider.Session, string, mountapi.Options) error
type Options struct {
	Directory string
	Accounts  *accounts.Store
	Registry  *provider.Registry
	Cache     blockcache.Options
	Mount     MountFunc
}
type Service struct {
	readSlots     chan struct{}
	metadataSlots chan struct{}
	data          map[string]*dataEndpoint
	dataWorkers   sync.WaitGroup
	mu            sync.Mutex
	sessionMu     sync.Mutex
	options       Options
	cache         *blockcache.Cache
	ctx           context.Context
	cancel        context.CancelFunc
	sessions      map[string]*session
	mounts        map[string]*mounted
	desired       map[string]MountSpec
	closeOnce     sync.Once
	closed        bool
}

func New(o Options) (*Service, error) {
	if o.Directory == "" || o.Accounts == nil || o.Registry == nil {
		return nil, errors.New("daemon directory, account store and registry are required")
	}
	if o.Mount == nil {
		return nil, errors.New("daemon mount adapter is required")
	}
	if err := credentials.EnsurePrivateParent(filepath.Join(o.Directory, "mounts.json")); err != nil {
		return nil, err
	}
	if o.Cache.DiskDirectory == "" {
		o.Cache.DiskDirectory = filepath.Join(o.Directory, "cache")
	}
	if o.Cache.DiskBytes == 0 {
		o.Cache.DiskBytes = 4 << 30
	}
	cache, err := blockcache.New(o.Cache)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Service{options: o, cache: cache, ctx: ctx, cancel: cancel, sessions: map[string]*session{}, data: map[string]*dataEndpoint{}, readSlots: make(chan struct{}, 4), metadataSlots: make(chan struct{}, 4), mounts: map[string]*mounted{}, desired: map[string]MountSpec{}}
	data, err := os.ReadFile(filepath.Join(o.Directory, "mounts.json"))
	if err == nil {
		err = json.Unmarshal(data, &d.desired)
	}
	if err != nil && !os.IsNotExist(err) {
		cache.Close()
		cancel()
		return nil, err
	}
	return d, nil
}
func (d *Service) saveLocked() error {
	data, err := json.MarshalIndent(d.desired, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d.options.Directory, ".mounts-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(d.options.Directory, "mounts.json"))
}
func (d *Service) account(ctx context.Context, spec MountSpec) (accounts.Account, error) {
	config, err := d.options.Accounts.Load(ctx)
	if err != nil {
		return accounts.Account{}, err
	}
	if spec.AccountID != "" {
		for _, a := range config.Accounts {
			if a.AccountID == spec.AccountID {
				return a, nil
			}
		}
		return accounts.Account{}, accounts.ErrNotFound
	}
	a, ok := config.Find(spec.Account)
	if !ok {
		return a, accounts.ErrNotFound
	}
	return a, nil
}
func accountKey(a accounts.Account) string {
	if a.AccountID != "" {
		return string(a.AccountID)
	}
	return "legacy:" + a.Name
}
func (d *Service) getSession(ctx context.Context, a accounts.Account, persist bool, ttl time.Duration) (*vfs.Session, error) {
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	key := accountKey(a) + fmt.Sprint(":", persist, ":", ttl)
	d.mu.Lock()
	existing := d.sessions[key]
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, errors.New("daemon closed")
	}
	if existing != nil {
		return existing.vfs, nil
	}
	factory, ok := d.options.Registry.Get(a.Provider)
	if !ok {
		return nil, provider.ErrUnsupported
	}
	account, secrets, state, err := d.options.Accounts.Credentials(ctx, a.Name)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, b := range secrets {
			clear(b)
		}
		clear(state)
	}()
	if account.AccountID != a.AccountID || account.Binding != a.Binding {
		return nil, provider.ErrConflict
	}
	actual := account
	actual.BindingFields = provider.BindingFields(factory.Descriptor(), account.Options, secrets)
	if account.AccountID != "" && actual.BindingFingerprint() != account.Binding {
		return nil, errors.New("account settings differ from the saved binding")
	}
	raw, err := factory.Open(ctx, provider.OpenRequest{Username: account.Username, Options: account.Options, Secrets: secrets, State: state, SaveState: func(ctx context.Context, b []byte) error { return d.options.Accounts.SaveBoundState(ctx, account, b) }})
	if err != nil {
		return nil, err
	}
	id := account.AccountID
	if id == "" {
		id, err = namespace.NewAccountID()
		if err != nil {
			raw.Close()
			return nil, err
		}
		persist = false
	}
	cached, err := vfs.NewWithOptions(raw, vfs.Options{Namespace: namespace.Namespace{AccountID: id, ProviderID: account.Provider}, Cache: d.cache, MetadataTTL: ttl, Persistent: persist, MetadataSlots: d.metadataSlots})
	if err != nil {
		raw.Close()
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		cached.Close()
		return nil, errors.New("daemon closed")
	}
	d.sessions[key] = &session{account: account, vfs: cached}
	return cached, nil
}
func (d *Service) Mount(ctx context.Context, spec MountSpec) (MountStatus, error) {
	if !accounts.ValidName(spec.ID) || spec.Path == "" {
		return MountStatus{}, errors.New("mount id and path are required")
	}
	a, err := d.account(ctx, spec)
	if err != nil {
		return MountStatus{}, err
	}
	if spec.Persistent && a.AccountID == "" {
		return MountStatus{}, errors.New("persistent caching requires a stable AccountID; use --persistent-cache=false for this legacy account or save a new account binding")
	}
	spec.AccountID = a.AccountID
	spec.Account = a.Name
	absolute, err := filepath.Abs(spec.Path)
	if err != nil {
		return MountStatus{}, err
	}
	if filepath.VolumeName(spec.Path) != spec.Path {
		spec.Path = absolute
	}
	cached, err := d.getSession(ctx, a, spec.Persistent, spec.MetadataTTL)
	if err != nil {
		return MountStatus{}, err
	}
	target, err := namespace.Resolve(ctx, cached, cached.Namespace(), spec.Scope)
	if err != nil {
		return MountStatus{}, err
	}
	mountCtx, cancel := context.WithCancel(d.ctx)
	m := &mounted{status: MountStatus{Spec: spec, State: "mounting", VolumeID: target.VolumeID()}, cancel: cancel, done: make(chan struct{}), target: target}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		cancel()
		target.Revoke()
		return MountStatus{}, errors.New("daemon closed")
	}
	if _, ok := d.mounts[spec.ID]; ok {
		d.mu.Unlock()
		cancel()
		target.Revoke()
		return MountStatus{}, errors.New("mount id is already active")
	}
	for _, old := range d.mounts {
		if old.status.Spec.Path == spec.Path {
			d.mu.Unlock()
			cancel()
			target.Revoke()
			return MountStatus{}, errors.New("mountpoint is already active")
		}
	}
	d.mounts[spec.ID] = m
	d.desired[spec.ID] = spec
	if err := d.saveLocked(); err != nil {
		delete(d.mounts, spec.ID)
		delete(d.desired, spec.ID)
		d.mu.Unlock()
		cancel()
		target.Revoke()
		return MountStatus{}, err
	}
	status := m.status
	d.mu.Unlock()
	go func() {
		defer close(m.done)
		err := d.options.Mount(mountCtx, target, spec.Path, mountapi.Options{ReadSlots: d.readSlots, ReadOnly: true, VolumeName: "FuseDock", Ready: func() { d.mu.Lock(); m.status.State = "mounted"; d.mu.Unlock() }})
		target.Revoke()
		d.mu.Lock()
		m.result = err
		m.status.State = "stopped"
		if err != nil {
			m.status.State = "failed"
			m.status.Error = err.Error()
		}
		d.mu.Unlock()
	}()
	return status, nil
}
func (d *Service) Unmount(ctx context.Context, id string) error {
	d.mu.Lock()
	m := d.mounts[id]
	if m == nil {
		delete(d.desired, id)
		err := d.saveLocked()
		d.mu.Unlock()
		return err
	}
	m.status.State = "unmounting"
	for _, endpoint := range d.data {
		if endpoint.mountID == id {
			endpoint.cancel()
		}
	}
	if m.target != nil {
		m.target.Revoke()
	}
	m.cancel()
	d.mu.Unlock()
	select {
	case <-m.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.result != nil {
		m.status.State = "failed"
		return m.result
	}
	delete(d.desired, id)
	if err := d.saveLocked(); err != nil {
		d.desired[id] = m.status.Spec
		return err
	}
	delete(d.mounts, id)
	return nil
}
func (d *Service) Status() []MountStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]MountStatus, 0, len(d.mounts))
	for _, m := range d.mounts {
		result = append(result, m.status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.ID < result[j].Spec.ID })
	return result
}
func (d *Service) Restore(ctx context.Context) {
	d.mu.Lock()
	specs := make([]MountSpec, 0, len(d.desired))
	for _, s := range d.desired {
		specs = append(specs, s)
	}
	d.mu.Unlock()
	for _, s := range specs {
		if _, err := d.Mount(ctx, s); err != nil {
			done := make(chan struct{})
			close(done)
			d.mu.Lock()
			d.mounts[s.ID] = &mounted{status: MountStatus{Spec: s, State: "failed", Error: err.Error()}, cancel: func() {}, done: done}
			d.mu.Unlock()
		}
	}
}
func (d *Service) Reauthenticate(ctx context.Context, name string) error {
	account, err := d.account(ctx, MountSpec{Account: name})
	if err != nil {
		return err
	}
	d.sessionMu.Lock()
	d.mu.Lock()
	var specs []MountSpec
	var mounts []*mounted
	var sessions []*vfs.Session
	for id, m := range d.mounts {
		if boundAccount(m.status.Spec, account) {
			specs = append(specs, m.status.Spec)
			mounts = append(mounts, m)
			if m.target != nil {
				m.target.Revoke()
			}
			for _, endpoint := range d.data {
				if endpoint.mountID == id {
					endpoint.cancel()
				}
			}
			m.cancel()
		}
	}
	for key, s := range d.sessions {
		if accountKey(s.account) == accountKey(account) {
			sessions = append(sessions, s.vfs)
			delete(d.sessions, key)
		}
	}
	d.mu.Unlock()
	for _, m := range mounts {
		select {
		case <-m.done:
		case <-ctx.Done():
			d.sessionMu.Unlock()
			return ctx.Err()
		}
	}
	var stopped error
	d.mu.Lock()
	for _, m := range mounts {
		stopped = errors.Join(stopped, m.result)
		if m.result == nil {
			delete(d.mounts, m.status.Spec.ID)
		}
	}
	d.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	d.sessionMu.Unlock()
	if stopped != nil {
		return stopped
	}
	for _, spec := range specs {
		if _, err := d.Mount(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}
func (d *Service) Close() error {
	var result error
	d.closeOnce.Do(func() {
		d.cancel()
		d.mu.Lock()
		d.closed = true
		for _, endpoint := range d.data {
			endpoint.cancel()
		}
		mounts := make([]*mounted, 0, len(d.mounts))
		for _, m := range d.mounts {
			mounts = append(mounts, m)
			m.cancel()
		}
		sessions := make([]*vfs.Session, 0, len(d.sessions))
		for _, s := range d.sessions {
			sessions = append(sessions, s.vfs)
		}
		d.mu.Unlock()
		for _, s := range sessions {
			result = errors.Join(result, s.Close())
		}
		for _, m := range mounts {
			<-m.done
			result = errors.Join(result, m.result)
		}
		d.dataWorkers.Wait()
		result = errors.Join(result, d.cache.Close())
	})
	return result
}

func boundAccount(spec MountSpec, account accounts.Account) bool {
	if account.AccountID != "" {
		return spec.AccountID == account.AccountID
	}
	return spec.AccountID == "" && spec.Account == account.Name
}
