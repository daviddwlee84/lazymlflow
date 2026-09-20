.PHONY: build test check pty
build:
	go build -o bin/lazymlflow ./cmd/lazymlflow
test:
	go test -race ./...
check:
	go vet ./...
pty: build
	python3 scripts/pty_smoke.py --binary ./bin/lazymlflow
	python3 scripts/pty_inspection.py --binary ./bin/lazymlflow
