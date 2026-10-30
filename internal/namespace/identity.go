// Package namespace owns account-scoped object identities and surface lookup.
package namespace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
)

type AccountID string

func NewAccountID() (AccountID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return AccountID("acct_" + hex.EncodeToString(b[:])), nil
}
func (id AccountID) Valid() bool {
	s := string(id)
	if len(s) != 37 || s[:5] != "acct_" {
		return false
	}
	b, e := hex.DecodeString(s[5:])
	return e == nil && hex.EncodeToString(b) == s[5:]
}

type Namespace struct {
	AccountID  AccountID `json:"account_id"`
	ProviderID string    `json:"provider_id"`
}

func (n Namespace) Validate() error {
	if !n.AccountID.Valid() || n.ProviderID == "" {
		return errors.New("invalid account namespace")
	}
	return nil
}

type NodeKey struct {
	Namespace Namespace `json:"namespace"`
	ObjectID  string    `json:"object_id"`
}

func Encode(fields ...string) []byte {
	var b []byte
	for _, f := range fields {
		b = binary.BigEndian.AppendUint64(b, uint64(len(f)))
		b = append(b, f...)
	}
	return b
}
func (n Namespace) Tag() string {
	h := sha256.Sum256(Encode("fusedock-namespace-v1", string(n.AccountID), n.ProviderID))
	return hex.EncodeToString(h[:])
}
func (k NodeKey) Bytes() []byte {
	return Encode("fusedock-node-v1", string(k.Namespace.AccountID), k.Namespace.ProviderID, k.ObjectID)
}

// Nodes allocates collision-free surface numbers. Values never recycle within
// a mount, even if a kernel forgets and later looks up the object again.
type Nodes struct {
	mu   sync.Mutex
	next uint64
	ids  map[NodeKey]uint64
}

func (n *Nodes) ID(key NodeKey) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ids == nil {
		n.ids = make(map[NodeKey]uint64)
		n.next = 1
	}
	if id := n.ids[key]; id != 0 {
		return id
	}
	n.next++
	if n.next == 0 {
		panic("filesystem node ID space exhausted")
	}
	n.ids[key] = n.next
	return n.next
}
