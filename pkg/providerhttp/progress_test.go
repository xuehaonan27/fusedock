package providerhttp

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func TestProgressiveRevisionResponseDeliversBeforeTail(t *testing.T) {
	body, writer := io.Pipe()
	defer body.Close()
	defer writer.Close()
	allowTail := make(chan struct{})
	defer close(allowTail)
	prefix := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		if _, err := writer.Write(make([]byte, 32<<10)); err != nil {
			return
		}
		<-allowTail
		writer.Write(make([]byte, 32<<10))
		writer.Close()
	}()
	response := &http.Response{StatusCode: 206, ContentLength: 64 << 10, Header: http.Header{"Content-Range": []string{"bytes 0-65535/65536"}, "Etag": []string{"\"selected\""}}, Body: body}
	go func() {
		_, err := ReadResponseProgress(response, make([]byte, 64<<10), 0, 64<<10, "\"selected\"", func(n int) {
			if n == 32<<10 {
				prefix <- n
			}
		})
		done <- err
	}()
	select {
	case <-prefix:
	case <-time.After(time.Second):
		t.Fatal("validated prefix waited for tail")
	}
	select {
	case err := <-done:
		t.Fatalf("response completed before tail: %v", err)
	default:
	}
}
