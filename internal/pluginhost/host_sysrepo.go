//go:build sysrepo

// Real dlopen-based plugin host. Only compiled with the `sysrepo` build
// tag (requires libsysrepo-dev + dlfcn.h). Replaces sysrepo-plugind:
// dlopen → sr_session_start → sr_plugin_init_cb on Start;
// sr_plugin_cleanup_cb → sr_session_stop → dlclose on Stop.
package pluginhost

/*
#cgo pkg-config: sysrepo
#include <sysrepo.h>
#include <dlfcn.h>
#include <stdlib.h>

// Load one plugin: dlopen, create a session, call init.
// Returns 0 on success, negative on error.
// On success, *handle_out, *sess_out, *priv_out are set.
// We use void** for all out-params to avoid cgo type friction.
static int cf_plugin_load(const char *path, void *conn,
                          void **handle_out,
                          void **sess_out, void **priv_out) {
    void *h = dlopen(path, RTLD_NOW | RTLD_GLOBAL);
    if (!h) return -1;

    sr_session_ctx_t *sess = NULL;
    int rc = sr_session_start((sr_conn_ctx_t *)conn, SR_DS_RUNNING, &sess);
    if (rc != SR_ERR_OK) { dlclose(h); return rc; }

    int (*init_cb)(sr_session_ctx_t *, void **) =
        (int (*)(sr_session_ctx_t *, void **))dlsym(h, "sr_plugin_init_cb");
    if (!init_cb) { sr_session_stop(sess); dlclose(h); return -1; }

    void *priv = NULL;
    rc = init_cb(sess, &priv);
    if (rc != SR_ERR_OK) { sr_session_stop(sess); dlclose(h); return rc; }

    *handle_out = h;
    *sess_out = sess;
    *priv_out = priv;
    return 0;
}

// Unload one plugin: call cleanup, stop session, dlclose.
static void cf_plugin_unload(void *handle, void *sess, void *priv) {
    void (*cleanup_cb)(sr_session_ctx_t *, void *) =
        (void (*)(sr_session_ctx_t *, void *))dlsym(handle, "sr_plugin_cleanup_cb");
    if (cleanup_cb) cleanup_cb((sr_session_ctx_t *)sess, priv);
    sr_session_stop((sr_session_ctx_t *)sess);
    dlclose(handle);
}
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"github.com/example/confd/internal/sysrepoadapter"
)

// CGoHost is the real dlopen-based plugin host.
type CGoHost struct {
	mu      sync.Mutex
	conn    sysrepoadapter.Conn
	loaded  []cgoPlugin
}

type cgoPlugin struct {
	spec    Spec
	handle  unsafe.Pointer // dlopen handle
	session unsafe.Pointer // sr_session_ctx_t*
	priv    unsafe.Pointer // plugin's private data
}

// NewCGoHost returns a CGoHost.
func NewCGoHost() *CGoHost { return &CGoHost{} }

// Start loads and initializes all specs against the connection.
func (h *CGoHost) Start(conn sysrepoadapter.Conn, specs []Spec) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	rcp, ok := conn.(sysrepoadapter.RawConnProvider)
	if !ok {
		return fmt.Errorf("pluginhost: connection does not expose a raw sr_conn_ctx_t (need the cgo adapter)")
	}
	rawConn := rcp.RawConn()
	if rawConn == nil {
		return fmt.Errorf("pluginhost: nil raw connection")
	}
	h.conn = conn

	for _, s := range specs {
		cPath := C.CString(s.Path)
		var handle, sess, priv unsafe.Pointer
		rc := int(C.cf_plugin_load(cPath, rawConn,
			&handle, &sess, &priv))
		C.free(unsafe.Pointer(cPath))
		if rc != 0 {
			// init failure → disable, log, skip (improves on sysrepo-plugind)
			fmt.Fprintf(os.Stderr, "pluginhost: plugin %q init failed (rc=%d), disabled\n", s.Name, rc)
			continue
		}
		h.loaded = append(h.loaded, cgoPlugin{
			spec:    s,
			handle:  handle,
			session: sess,
			priv:     priv,
		})
	}
	return nil
}

// Stop cleans up all loaded plugins in reverse load order.
func (h *CGoHost) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.loaded) - 1; i >= 0; i-- {
		p := h.loaded[i]
		C.cf_plugin_unload(p.handle, p.session, p.priv)
	}
	h.loaded = nil
	return nil
}

// Names returns the names of successfully loaded plugins.
func (h *CGoHost) Names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.loaded))
	for i, p := range h.loaded {
		out[i] = p.spec.Name
	}
	return out
}

var _ Host = (*CGoHost)(nil)
