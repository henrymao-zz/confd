.PHONY: build test test-race vet cover clean sysrepo

GO ?= go

build:
	$(GO) build ./...

sysrepo:
	$(GO) build -tags sysrepo ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

cover:
	$(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out

clean:
	rm -f coverage.out /tmp/confd

run: build
	./bin/confd serve --bind=127.0.0.1:830 --password=confd --yang-path=./yang
