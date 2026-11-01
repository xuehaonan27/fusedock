//go:build darwin && cgo

// Package main exports the read-only FuseDock session to the native FSKit
// extension through a C ABI. It is built with -buildmode=c-archive.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct FuseDockHandle FuseDockHandle;
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/xuehaonan27/fusedock/internal/credentials"
	"github.com/xuehaonan27/fusedock/internal/namespace"
	"github.com/xuehaonan27/fusedock/internal/providers"
	"github.com/xuehaonan27/fusedock/internal/vfs"
	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type wireObject struct {
	ContentVersion string `json:"content_version"`
	NodeID         uint64 `json:"node_id"`
	ParentNodeID   uint64 `json:"parent_node_id"`
	ID             string `json:"id"`
	ParentID       string `json:"parent_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Size           int64  `json:"size"`
	Version        string `json:"version"`
	Modified       int64  `json:"modified"`
	Created        int64  `json:"created"`
}

type bridgeState struct {
	entryTTL   time.Duration
	nodes      namespace.Nodes
	volumeID   string
	session    provider.Session
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	mu         sync.Mutex
	readers    map[uint64]provider.Reader
	nextReader uint64
	objects    map[string]provider.Object
}

var handles = struct {
	sync.Mutex
	byID map[*C.FuseDockHandle]*bridgeState
}{byID: make(map[*C.FuseDockHandle]*bridgeState)}

func main() {}

//export fusedock_open
func fusedock_open(configPath *C.char, errorOut **C.char) *C.FuseDockHandle {
	path := C.GoString(configPath)
	values, err := credentials.Read(path)
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	defer clearValues(values)
	entryTTL := 2 * time.Second
	if value := string(values["entry_ttl"]); value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil {
			setError(errorOut, fmt.Errorf("invalid entry TTL: %w", err))
			return nil
		}
		if duration > 0 {
			entryTTL = duration
		}
	}
	providerID := strings.TrimSpace(string(values["provider"]))
	if providerID == "" {
		providerID = "pku"
	}
	registry, err := providers.Builtin()
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	factory, ok := registry.Get(providerID)
	if !ok {
		setError(errorOut, fmt.Errorf("unknown provider %q", providerID))
		return nil
	}
	descriptor := factory.Descriptor()
	options := make(map[string]string)
	secrets := make(map[string][]byte)
	for _, spec := range descriptor.Options {
		if value, exists := values[spec.Key]; exists {
			options[spec.Key] = string(value)
		}
	}
	for _, spec := range descriptor.Credentials {
		if value, exists := values[spec.Key]; exists {
			secrets[spec.Key] = append([]byte(nil), value...)
		}
	}
	defer clearValues(secrets)
	if accountName := strings.TrimSpace(string(values["account"])); accountName != "" {
		setError(errorOut, fmt.Errorf("mount saved account %q through 'fusedock daemon mount' or a broker capability file", accountName))
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := factory.Open(ctx, provider.OpenRequest{Options: options, Secrets: secrets})
	if err != nil {
		cancel()
		setError(errorOut, err)
		return nil
	}
	if providerID != "broker" {
		session = vfs.New(session, 2*time.Second)
	}
	state := &bridgeState{
		entryTTL: entryTTL,
		session:  session, ctx: ctx, cancel: cancel, volumeID: string(values["volume_id"]),
		readers: make(map[uint64]provider.Reader), objects: make(map[string]provider.Object),
	}
	handles.Lock()
	token := (*C.FuseDockHandle)(C.malloc(1))
	handles.byID[token] = state
	handles.Unlock()
	return token
}

//export fusedock_close
func fusedock_close(token *C.FuseDockHandle) {
	state := lookup(token)
	if state == nil {
		return
	}
	handles.Lock()
	delete(handles.byID, token)
	handles.Unlock()
	state.mu.Lock()
	state.closed = true
	clear(state.readers)
	state.mu.Unlock()
	state.cancel()
	_ = state.session.Close()
	C.free(unsafe.Pointer(token))
}

//export fusedock_root
func fusedock_root(token *C.FuseDockHandle, errorOut **C.char) *C.char {
	state := lookup(token)
	if state == nil {
		setError(errorOut, errors.New("FuseDock session is closed"))
		return nil
	}
	object, err := state.session.Root(state.ctx)
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	state.remember(object)
	return marshal(state.encodeObject(object), errorOut)
}

//export fusedock_lookup
func fusedock_lookup(token *C.FuseDockHandle, parentID, name *C.char, errorOut **C.char) *C.char {
	state := lookup(token)
	if state == nil {
		setError(errorOut, errors.New("FuseDock session is closed"))
		return nil
	}
	prefix := normalizeID(C.GoString(parentID))
	objects, err := state.session.List(state.ctx, provider.Object{ID: prefix, Kind: provider.KindDirectory})
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	wanted := C.GoString(name)
	for _, object := range objects {
		if object.Name == wanted {
			state.remember(object)
			return marshal(state.encodeObject(object), errorOut)
		}
	}
	setError(errorOut, fmt.Errorf("%w: %s", provider.ErrNotFound, wanted))
	return nil
}

//export fusedock_list
func fusedock_list(token *C.FuseDockHandle, parentID *C.char, errorOut **C.char) *C.char {
	state := lookup(token)
	if state == nil {
		setError(errorOut, errors.New("FuseDock session is closed"))
		return nil
	}
	objects, err := state.session.List(state.ctx, provider.Object{ID: normalizeID(C.GoString(parentID)), Kind: provider.KindDirectory})
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	wire := make([]wireObject, 0, len(objects))
	for _, object := range objects {
		state.remember(object)
		wire = append(wire, state.encodeObject(object))
	}
	return marshal(wire, errorOut)
}

//export fusedock_stat
func fusedock_stat(token *C.FuseDockHandle, objectID *C.char, errorOut **C.char) *C.char {
	state := lookup(token)
	if state == nil {
		setError(errorOut, errors.New("FuseDock session is closed"))
		return nil
	}
	known, ok := state.known(normalizeID(C.GoString(objectID)))
	if !ok {
		setError(errorOut, fmt.Errorf("%w: object metadata is unavailable", provider.ErrNotFound))
		return nil
	}
	object, err := state.session.Stat(state.ctx, known)
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	state.remember(object)
	return marshal(state.encodeObject(object), errorOut)
}

//export fusedock_read
func fusedock_read(token *C.FuseDockHandle, readID C.uint64_t, offset C.int64_t, dest unsafe.Pointer, length C.int64_t, errorOut **C.char) C.int64_t {
	if offset < 0 || length < 0 || length > 16<<20 {
		setError(errorOut, errors.New("invalid read range"))
		return -1
	}
	if length == 0 {
		return 0
	}
	state := lookup(token)
	if state == nil {
		setError(errorOut, errors.New("FuseDock session is closed"))
		return -1
	}
	state.mu.Lock()
	reader := state.readers[uint64(readID)]
	state.mu.Unlock()
	if reader == nil {
		setError(errorOut, errors.New("read handle is closed"))
		return -1
	}
	buffer := unsafe.Slice((*byte)(dest), int(length))
	n, err := reader.ReadAt(state.ctx, buffer, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		setError(errorOut, err)
		return -1
	}
	return C.int64_t(n)
}

//export fusedock_free
func fusedock_free(pointer unsafe.Pointer) {
	if pointer != nil {
		C.free(pointer)
	}
}

//export fusedock_open_read
func fusedock_open_read(token *C.FuseDockHandle, objectID *C.char, version *C.char, size C.int64_t, errorOut **C.char) *C.char {
	s := lookup(token)
	if s == nil {
		setError(errorOut, errors.New("session closed"))
		return nil
	}
	object, ok := s.known(C.GoString(objectID))
	if !ok {
		setError(errorOut, provider.ErrNotFound)
		return nil
	}
	var reader provider.Reader
	var err error
	selected := C.GoString(version)
	caps, e := provider.CapabilitiesFor(s.ctx, s.session)
	if e != nil {
		setError(errorOut, e)
		return nil
	}
	if selected != "" && caps.ExactRange {
		object.ContentVersion = selected
		object.Size = int64(size)
		reader, err = s.session.(provider.RevisionReader).OpenReadRevision(s.ctx, object.Revision())
	} else {
		object, err = s.session.Stat(s.ctx, object)
		if err == nil {
			reader, err = s.session.OpenRead(s.ctx, object)
		}
	}
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	if snapshot, ok := reader.(provider.SnapshotReader); ok && !(selected != "" && caps.ExactRange) {
		object = snapshot.Snapshot()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		reader.Close()
		setError(errorOut, errors.New("session closed"))
		return nil
	}
	s.nextReader++
	id := s.nextReader
	s.readers[id] = reader
	s.mu.Unlock()
	return marshal(struct {
		Handle   uint64     `json:"handle"`
		Snapshot wireObject `json:"snapshot"`
	}{id, s.encodeObject(object)}, errorOut)
}

//export fusedock_close_read
func fusedock_close_read(token *C.FuseDockHandle, readID C.uint64_t) {
	s := lookup(token)
	if s == nil {
		return
	}
	s.mu.Lock()
	reader := s.readers[uint64(readID)]
	delete(s.readers, uint64(readID))
	s.mu.Unlock()
	if reader != nil {
		reader.Close()
	}
}

func (s *bridgeState) remember(object provider.Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[object.ID] = object
}

func (s *bridgeState) known(id string) (provider.Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	object, ok := s.objects[id]
	return object, ok
}

func lookup(token *C.FuseDockHandle) *bridgeState {
	if token == nil {
		return nil
	}
	handles.Lock()
	defer handles.Unlock()
	return handles.byID[token]
}

func normalizeID(id string) string { return strings.TrimSpace(id) }

func (s *bridgeState) encodeObject(object provider.Object) wireObject {
	kind := "file"
	if object.IsDir() {
		kind = "directory"
	}
	var modified, created int64
	if !object.Modified.IsZero() {
		modified = object.Modified.UnixNano()
	}
	if !object.Created.IsZero() {
		created = object.Created.UnixNano()
	}
	return wireObject{
		ID: object.ID, ParentID: object.ParentID, Name: object.Name, Kind: kind, NodeID: s.nodes.ID(namespace.NodeKey{ObjectID: object.ID}), ParentNodeID: s.parentNodeID(object.ParentID),
		Size: object.Size, Version: object.Version, ContentVersion: object.ContentVersion, Modified: modified, Created: created,
	}
}

func (s *bridgeState) parentNodeID(id string) uint64 {
	if id == "" {
		return 0
	}
	return s.nodes.ID(namespace.NodeKey{ObjectID: id})
}

//export fusedock_volume_id
func fusedock_volume_id(token *C.FuseDockHandle) *C.char {
	s := lookup(token)
	if s == nil {
		return nil
	}
	return C.CString(s.volumeID)
}

//export fusedock_entry_ttl
func fusedock_entry_ttl(token *C.FuseDockHandle) C.double {
	return C.double(lookup(token).entryTTL.Seconds())
}

func marshal(value any, errorOut **C.char) *C.char {
	data, err := json.Marshal(value)
	if err != nil {
		setError(errorOut, err)
		return nil
	}
	return C.CString(string(data))
}

func setError(target **C.char, err error) {
	if target == nil || err == nil {
		return
	}
	*target = C.CString(err.Error())
}

func clearValues(values map[string][]byte) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}
