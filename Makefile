.PHONY: build test test-race vet cover clean sysrepo plugins install build-deps

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

# All dependencies are git submodules under deps/ and sysrepo-plugins/.
# Run 'git submodule update --init --recursive' before building.
#
# Build C++ dependencies (libyang, sysrepo, libyang-cpp, sysrepo-cpp, umgmt)
# from source. libyang-cpp is patched to accept libyang 5.x.
# Requires: cmake, g++, libnl-3-dev, libnl-route-3-dev, libsystemd-dev,
# libsdbus-c++-dev, libnftables-dev, libsensors-dev, libproc2-dev,
# nlohmann-json3-dev, pkg-config.
build-deps:
	@echo "Building libyang (v5.8.6) from submodule..."
	cd deps/libyang && mkdir -p build && cd build && cmake -DCMAKE_BUILD_TYPE=Release .. && make -j$$(nproc) && sudo make install
	@echo "Building sysrepo (v5.1.0) from submodule..."
	cd deps/sysrepo && mkdir -p build && cd build && cmake -DCMAKE_BUILD_TYPE=Release .. && make -j$$(nproc) && sudo make install
	@echo "Building libyang-cpp from submodule (patched for libyang 5.x)..."
	cd deps/libyang-cpp && sed -i 's/libyang>=6.1.1/libyang>=5.0.0/' CMakeLists.txt && mkdir -p build && cd build && cmake .. && make -j$$(nproc) && sudo make install
	@echo "Building sysrepo-cpp from submodule..."
	cd deps/sysrepo-cpp && mkdir -p build && cd build && cmake .. && make -j$$(nproc) && sudo make install
	@echo "Building umgmt from submodule..."
	cd deps/umgmt && mkdir -p build && cd build && cmake .. && make -j$$(nproc) && sudo make install
	@echo "Done. Run 'sudo ldconfig' to refresh the library cache."

plugins: build-deps
	@echo "Building sysrepo-plugins from $(PLUGINS_SRC)..."
	cd $(PLUGINS_SRC) && rm -rf build && mkdir -p build && cd build && cmake -DSYSTEMD_IFINDEX=1 .. && make -j$$(nproc)
	@echo "Copying plugin .so files to $(PLUGINS_DIR)..."
	sudo mkdir -p $(PLUGINS_DIR)
	sudo cp $(PLUGINS_SRC)/build/plugins/*/libsrplg-*.so $(PLUGINS_DIR)/ 2>/dev/null || true
	@echo "Done. Plugins installed to $(PLUGINS_DIR)"

install: build
	$(GO) build -o $(DESTDIR)/usr/bin/confd ./cmd/confd
	mkdir -p $(DESTDIR)$(PLUGINS_DIR)
	cp plugins/*.so $(DESTDIR)$(PLUGINS_DIR)/ 2>/dev/null || true
