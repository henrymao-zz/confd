.PHONY: build test test-race vet cover clean plugins install build-deps

GO ?= go
PLUGINS_DIR ?= /usr/lib/confd/plugins

# Build confd with the real sysrepo cgo backend (default).
# The `sysrepo` build tag enables cgo bindings to libsysrepo.
build:
	$(GO) build -tags sysrepo -o confd ./cmd/confd

# Run tests with the mock adapter (no cgo, no sysrepo needed).
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
	./confd serve --bind=127.0.0.1:830 --password=confd --yang-path=./yang

run-mock:
	$(GO) build -o confd ./cmd/confd
	./confd serve --bind=127.0.0.1:830 --password=confd --yang-path=./yang --adapter=mock

# All dependencies are git submodules under src/.
# Run 'git submodule update --init --recursive' before building.
#
# Build C++ dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt)
# from source. libyang-cpp is pinned to a pre-v6 commit for libyang 5.x.
# Requires: cmake, g++, libnl-3-dev, libnl-route-3-dev, libsystemd-dev,
# libsdbus-c++-dev, libnftables-dev, libsensors-dev, libproc2-dev,
# nlohmann-json3-dev, doctest-dev, pkg-config.
build-deps:
	@echo "Building libyang (v5.8.6)..."
	cd src/libyang && mkdir -p build && cd build && cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local .. && make -j$$(nproc) && sudo make install
	@echo "Building sysrepo (v5.1.0)..."
	cd src/sysrepo && mkdir -p build && cd build && cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/usr/local -DNOTIFD_SETUP=OFF -DENABLE_SYSREPO_NOTIFD=OFF .. && make -j$$(nproc) && sudo make install
	@echo "Building libyang-cpp (pinned for libyang 5.x)..."
	cd src/libyang-cpp && mkdir -p build && cd build && cmake -DCMAKE_INSTALL_PREFIX=/usr/local -DBUILD_TESTING=OFF .. && make -j$$(nproc) && sudo make install
	@echo "Building sysrepo-cpp..."
	cd src/sysrepo-cpp && mkdir -p build && cd build && cmake -DCMAKE_INSTALL_PREFIX=/usr/local -DBUILD_TESTING=OFF .. && make -j$$(nproc) && sudo make install
	@echo "Building umgmt..."
	cd src/umgmt && mkdir -p build && cd build && cmake -DCMAKE_INSTALL_PREFIX=/usr/local -DCMAKE_POLICY_VERSION_MINIMUM=3.5 .. && make -j$$(nproc) && sudo make install
	@echo "Done. Run 'sudo ldconfig' to refresh the library cache."

plugins: build-deps
	@echo "Building sysrepo-plugins..."
	cd src/sysrepo-plugins && mkdir -p build && cd build && cmake -DSYSTEMD_IFINDEX=1 -DBUILD_OS_METRICS_PLUGIN=OFF .. && make -j$$(nproc)
	@echo "Copying plugin .so files to $(PLUGINS_DIR)..."
	sudo mkdir -p $(PLUGINS_DIR)
	sudo cp src/sysrepo-plugins/build/plugins/*/libsrplg-*.so $(PLUGINS_DIR)/ 2>/dev/null || true
	@echo "Done. Plugins installed to $(PLUGINS_DIR)"

install: build
	$(GO) build -tags sysrepo -o $(DESTDIR)/usr/bin/confd ./cmd/confd
	mkdir -p $(DESTDIR)$(PLUGINS_DIR)
	cp plugins/*.so $(DESTDIR)$(PLUGINS_DIR)/ 2>/dev/null || true
