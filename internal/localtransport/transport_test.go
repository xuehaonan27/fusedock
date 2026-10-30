package localtransport

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestLocalAuthenticatedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	listener, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		_, err = c.Write([]byte("local"))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := Dial(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	_, err = io.ReadFull(c, b)
	c.Close()
	if err != nil || string(b) != "local" {
		t.Fatalf("round trip: %q %v", b, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
