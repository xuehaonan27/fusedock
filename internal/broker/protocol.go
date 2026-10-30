// Package broker serves a single read-only provider session to the sandboxed
// desktop filesystem host. It never exposes account-store secrets.
package broker

import "github.com/xuehaonan27/fusedock/pkg/provider"

const (
	ProtocolVersion = 2
	MaxReadBytes    = 16 << 20
)

type ListRequest struct {
	ParentID string `json:"parent_id"`
}

type LookupRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

type StatRequest struct {
	ObjectID string `json:"object_id"`
}

type OpenRequest struct {
	ObjectID string                   `json:"object_id"`
	Revision *provider.ObjectRevision `json:"revision,omitempty"`
}
type OpenResponse struct {
	Handle   string          `json:"handle"`
	Snapshot provider.Object `json:"snapshot"`
}
type ReleaseRequest struct {
	Handle string `json:"handle"`
}
type ReadRequest struct {
	Handle string `json:"handle"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type ClaimResponse struct {
	Capabilities provider.ReadCapabilities `json:"capabilities"`
	Version      int                       `json:"version"`
	Token        string                    `json:"token"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}
