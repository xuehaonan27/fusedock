package provider

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
)

// Digest describes provider-declared bytes, never a locally invented version.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

func (d Digest) Hash() (hash.Hash, error) {
	switch d.Algorithm {
	case "md5":
		return md5.New(), nil
	case "sha256":
		return sha256.New(), nil
	default:
		return nil, ErrUnsupported
	}
}
func (d Digest) Valid() bool {
	h, err := d.Hash()
	if err != nil {
		return false
	}
	b, err := hex.DecodeString(d.Value)
	return err == nil && len(b) == h.Size() && hex.EncodeToString(b) == d.Value
}

// ObjectRevision is the immutable input to an exact read. It carries the
// digest selected at open time, not one obtained after downloading.
type ObjectRevision struct {
	ObjectID       string `json:"object_id"`
	ContentVersion string `json:"content_version"`
	Size           int64  `json:"size"`
	Digest         Digest `json:"digest,omitempty"`
}

func (o Object) Revision() ObjectRevision {
	return ObjectRevision{ObjectID: o.ID, ContentVersion: o.ContentVersion, Size: o.Size, Digest: o.Digest}
}
func (r ObjectRevision) Validate() error {
	if r.ObjectID == "" || r.ContentVersion == "" || r.Size < 0 {
		return errors.New("incomplete content revision")
	}
	return nil
}

// ReadCapabilities describes independently supported proof paths. Digest
// eligibility includes provider conformance evidence, not just digest syntax.
type ReadCapabilities struct {
	StableObjectID bool `json:"stable_object_id"`
	ExactRange     bool `json:"exact_range"`
	DeclaredDigest bool `json:"declared_digest"`
}
type ReadCapabilitySource interface {
	ReadCapabilities(context.Context) (ReadCapabilities, error)
}
type RevisionReader interface {
	OpenReadRevision(context.Context, ObjectRevision) (Reader, error)
}

// SnapshotReader reports the authoritative snapshot owned by an open handle.
type SnapshotReader interface {
	Reader
	Snapshot() Object
}

func CapabilitiesFor(ctx context.Context, session Session) (ReadCapabilities, error) {
	if s, ok := session.(ReadCapabilitySource); ok {
		return s.ReadCapabilities(ctx)
	}
	return ReadCapabilities{}, nil
}
