package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/xuehaonan27/fusedock/internal/broker"
	"net"
)

type dataEndpoint struct {
	mountID string
	cancel  context.CancelFunc
}
type Access struct {
	Endpoint  string `json:"endpoint"`
	Bootstrap string `json:"bootstrap"`
	VolumeID  string `json:"volume_id"`
}

func (d *Service) Attach(id string) (Access, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.mounts[id]
	if m == nil || m.target == nil || m.status.State != "mounted" || d.closed {
		return Access{}, errors.New("mount target is unavailable")
	}
	if len(d.data) >= 64 {
		return Access{}, errors.New("data connection limit reached")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Access{}, err
	}
	bootstrap := base64.RawURLEncoding.EncodeToString(random[:])
	server, err := broker.NewServer(m.target, bootstrap)
	if err != nil {
		return Access{}, err
	}
	server.SetReadSlots(d.readSlots)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Access{}, err
	}
	ctx, cancel := context.WithCancel(d.ctx)
	endpoint := listener.Addr().String()
	d.data[endpoint] = &dataEndpoint{mountID: id, cancel: cancel}
	server.SetOnClose(cancel)
	d.dataWorkers.Add(1)
	go func() {
		defer d.dataWorkers.Done()
		defer listener.Close()
		defer cancel()
		server.Serve(ctx, listener)
		d.mu.Lock()
		delete(d.data, endpoint)
		d.mu.Unlock()
	}()
	return Access{Endpoint: "http://" + endpoint, Bootstrap: bootstrap, VolumeID: m.status.VolumeID}, nil
}
