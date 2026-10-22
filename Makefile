.PHONY: build test test-live-pku test-fuse vet fmt check clean

build:
	mkdir -p bin
	go build -trimpath -o bin/fusedock ./cmd/fusedock

test:
	go test -race ./...

test-live-pku:
	FUSEDOCK_LIVE_PKU=1 go test -count=1 -run '^TestLivePKULoginPage$$' -v ./internal/providers/pku

test-fuse:
	FUSEDOCK_FUSE_TEST=1 go test -count=1 -run '^TestLocalProviderMount$$' -v ./internal/fusefs

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal pkg

check:
	test -z "$$(gofmt -l cmd internal pkg)"
	go vet ./...
	go test -race ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/fusedock-check-linux ./cmd/fusedock
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/fusedock-check-darwin ./cmd/fusedock
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/fusedock-check-windows.exe ./cmd/fusedock

clean:
	rm -rf bin
