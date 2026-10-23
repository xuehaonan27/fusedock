// Package broker serves a single read-only provider session to the sandboxed
// desktop filesystem host. It never exposes account-store secrets.
package broker

import "github.com/xuehaonan/fusedock/pkg/provider"

const (
	ProtocolVersion = 1
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
	Object provider.Object `json:"object"`
}

type ReadRequest struct {
	Object provider.Object `json:"object"`
	Offset int64           `json:"offset"`
	Length int64           `json:"length"`
}

type ClaimResponse struct {
	Version int    `json:"version"`
	Token   string `json:"token"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}
