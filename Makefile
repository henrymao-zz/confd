.PHONY: build test test-race vet cover clean sysrepo plugins install

GO ?= go
PLUGINS_DIR ?= /usr/lib/confd/plugins
PLUGINS_SRC ?= ./sysrepo-plugins

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

plugins:
	@echo "Building sysrepo-plugins from $(PLUGINS_SRC)..."
	cd $(PLUGINS_SRC) && mkdir -p build && cd build && cmake .. && make -j$$(nproc)
	@echo "Copying plugin .so files to $(PLUGINS_DIR)..."
	mkdir -p $(PLUGINS_DIR)
	cp $(PLUGINS_SRC)/build/plugins/*/libsrplg-*.so $(PLUGINS_DIR)/ 2>/dev/null || true
	@echo "Done. Plugins installed to $(PLUGINS_DIR)"

install: build
	$(GO) build -o $(DESTDIR)/usr/bin/confd ./cmd/confd
	mkdir -p $(DESTDIR)$(PLUGINS_DIR)
	cp plugins/*.so $(DESTDIR)$(PLUGINS_DIR)/ 2>/dev/null || true
