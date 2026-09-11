.PHONY: configure build clean run install

GO ?= go
PLUGINS_DIR ?= /usr/lib/confd/plugins

# All dependencies are git submodules under src/.
# Run 'git submodule update --init --recursive' before building.
#
# configure builds C++ dependencies (libyang, sysrepo, libyang-cpp,
# sysrepo-cpp, umgmt) and the Telekom sysrepo-plugins from source.
# Requires: cmake, g++, libnl-3-dev, libnl-route-3-dev, libsystemd-dev,
# libsdbus-c++-dev, libnftables-dev, libsensors-dev, libproc2-dev,
# nlohmann-json3-dev, doctest-dev, pkg-config.
configure:
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
	@echo "Building sysrepo-plugins..."
	cd src/sysrepo-plugins && mkdir -p build && cd build && cmake -DSYSTEMD_IFINDEX=1 -DBUILD_OS_METRICS_PLUGIN=OFF .. && make -j$$(nproc)
	@echo "Copying plugin .so files to $(PLUGINS_DIR)..."
	sudo mkdir -p $(PLUGINS_DIR)
	sudo cp src/sysrepo-plugins/build/plugins/*/libsrplg-*.so $(PLUGINS_DIR)/ 2>/dev/null || true
	@echo "Running ldconfig..."
	sudo ldconfig
	@echo "Configure done."

# Build confd with the real sysrepo cgo backend (default).
# Depends on configure to ensure C++ deps + plugins are built.
build: configure
	$(GO) build -tags sysrepo -o confd ./cmd/confd

clean:
	rm -f coverage.out confd

run: build
	sudo ./confd serve --bind=127.0.0.1:830 --password=confd

install:
	install -D confd $(DESTDIR)/usr/bin/confd
	mkdir -p $(DESTDIR)/etc/confd
	cp confd.yaml $(DESTDIR)/etc/confd/confd.yaml
	mkdir -p $(DESTDIR)$(PLUGINS_DIR)
	cp $(PLUGINS_DIR)/*.so $(DESTDIR)$(PLUGINS_DIR)/ 2>/dev/null || true
