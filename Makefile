.PHONY: build tidy test run image diagrams

VERSION ?= dev

build:
	go build -ldflags "-s -w -X main.buildVersion=$(VERSION)" -o bin/rf2vc ./cmd/gateway

tidy:
	go mod tidy

test:
	go test ./...

run: build
	./bin/rf2vc -config configs/gateway.yaml

image:
	buildah bud --build-arg BUILD_VERSION=$(VERSION) \
	  -f deployments/containers/Containerfile \
	  -t ghcr.io/dasmlab/rf2vc:$(VERSION) .

diagrams:
	d2 diagrams/rf2vc-overview.d2 diagrams/rf2vc-overview.svg
	d2 diagrams/rf2vc-control-path.d2 diagrams/rf2vc-control-path.svg
	d2 diagrams/rf2vc-iso-path.d2 diagrams/rf2vc-iso-path.svg
