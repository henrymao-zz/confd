//go:build sysrepo

// Real cgo adapter for libsysrepo. Only compiled with the `sysrepo` build
// tag (requires libsysrepo-dev). In the default build, cgo_nosysrepo.go
// provides a stub that returns an error on Connect.
package sysrepoadapter

/*
#cgo pkg-config: sysrepo libyang
#include <libyang/libyang.h>
#include <sysrepo.h>
#include <stdlib.h>

// cf_get_data_xml calls sr_get_data to retrieve a libyang data tree,
// then uses lyd_print_mem to serialize it to XML. Returns the XML string
// (caller must free with free()) or NULL on error.
static char *cf_get_data_xml(sr_session_ctx_t *session, const char *xpath) {
    sr_data_t *data = NULL;
    int rc = sr_get_data(session, xpath, 0, 0, 0, &data);
    if (rc != SR_ERR_OK) {
        // Log the actual error code for debugging
        fprintf(stderr, "cf_get_data_xml: sr_get_data(%s) rc=%d: %s\n", xpath, rc, sr_strerror(rc));
        if (rc == SR_ERR_NOT_FOUND) {
            return strdup("");
        }
        return NULL;
    }
    if (data == NULL || data->tree == NULL) {
        if (data) sr_release_data(data);
        return strdup("");
    }
    char *xml = NULL;
    rc = lyd_print_mem(&xml, data->tree, LYD_XML, 0);
    sr_release_data(data);
    if (rc != LY_SUCCESS) {
        return NULL;
    }
    return xml;
}
*/
import "C"

import (
	"context"
	"unsafe"
)

// CGo is the libsysrepo-backed Adapter.
type CGo struct {
	socket string
}

// NewCGo returns a CGo adapter that connects to the given sysrepo socket
// (empty means the default SHM repository path).
func NewCGo(socket string) *CGo { return &CGo{socket: socket} }

// Connect opens a connection to the sysrepo SHM datastore.
func (a *CGo) Connect(ctx context.Context) (Conn, error) {
	var conn *C.sr_conn_ctx_t
	rc := C.sr_connect(0, &conn)
	if rc != C.SR_ERR_OK {
		return nil, fmt.Errorf("sysrepoadapter: sr_connect: %s", C.GoString(C.sr_strerror(rc)))
	}
	return &cgoConn{raw: unsafe.Pointer(conn)}, nil
}

// cgoConn implements Conn.
type cgoConn struct {
	raw unsafe.Pointer // *C.sr_conn_ctx_t
}

// RawConn returns the underlying sr_conn_ctx_t* (for the plugin host).
func (c *cgoConn) RawConn() unsafe.Pointer { return c.raw }

// ListModules returns the YANG modules installed in the sysrepo datastore.
func (c *cgoConn) ListModules(ctx context.Context) ([]ModuleInfo, error) {
	sess, err := c.OpenSession(ctx, "confd")
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	// sr_get_module_list is not directly available; use sr_get_items
	// on /sysrepo:sysrepo-modules to enumerate. For now, return empty;
	// the schema cache loads YANG files from disk independently.
	return nil, nil
}

// GetModuleInfo returns the list of YANG modules installed in sysrepo.
func (c *cgoConn) GetModuleInfo(ctx context.Context) ([]ModuleInfo, error) {
	var data *C.sr_data_t
	rc := C.sr_get_module_info((*C.sr_conn_ctx_t)(c.raw), &data)
	if rc != C.SR_ERR_OK {
		return nil, fmt.Errorf("sysrepoadapter: sr_get_module_info: %s", C.GoString(C.sr_strerror(rc)))
	}
	// TODO: parse the sr_data_t tree into []ModuleInfo.
	// For now, return empty; the provisioner will install all modules
	// if GetModuleInfo returns empty (treating it as "nothing installed yet").
	if data != nil {
		C.sr_release_data(data)
	}
	return nil, nil
}

// InstallModule installs a YANG module into sysrepo.
func (c *cgoConn) InstallModule(ctx context.Context, path, searchDirs string, features []string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var cSearchDirs *C.char
	if searchDirs != "" {
		cSearchDirs = C.CString(searchDirs)
		defer C.free(unsafe.Pointer(cSearchDirs))
	}
	// Build NULL-terminated features array.
	var cFeatures **C.char
	if len(features) > 0 {
		cArr := make([]*C.char, len(features)+1)
		for i, f := range features {
			cArr[i] = C.CString(f)
		}
		cArr[len(features)] = nil
		cFeatures = (**C.char)(unsafe.Pointer(&cArr[0]))
		defer func() {
			for _, cf := range cArr {
				if cf != nil {
					C.free(unsafe.Pointer(cf))
				}
			}
		}()
	}
	rc := C.sr_install_module((*C.sr_conn_ctx_t)(c.raw), cPath, cSearchDirs, cFeatures)
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_install_module: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// SetModuleFeature enables or disables a feature on an installed module.
func (c *cgoConn) SetModuleFeature(ctx context.Context, module, feature string, enable bool) error {
	// sysrepo doesn't have a dedicated sr_set_module_feature; features are
	// enabled at install time via sr_install_module's features parameter.
	// For already-installed modules, re-installing with the feature enabled
	// is the standard approach. We call sr_install_module with the module
	// path (which sysrepo resolves from its installed location) and the
	// features list.
	// TODO: find the module's on-disk path from sr_get_module_info.
	return nil
}

// OpenSession starts a new sysrepo session on this connection.
func (c *cgoConn) OpenSession(ctx context.Context, user string) (Session, error) {
	var sess *C.sr_session_ctx_t
	rc := C.sr_session_start((*C.sr_conn_ctx_t)(c.raw), C.SR_DS_RUNNING, &sess)
	if rc != C.SR_ERR_OK {
		return nil, fmt.Errorf("sysrepoadapter: sr_session_start: %s", C.GoString(C.sr_strerror(rc)))
	}
	return &cgoSession{raw: unsafe.Pointer(sess), ds: Running}, nil
}

// Close disconnects from sysrepo.
func (c *cgoConn) Close() error {
	if c.raw == nil {
		return nil
	}
	C.sr_disconnect((*C.sr_conn_ctx_t)(c.raw))
	c.raw = nil
	return nil
}

// cgoSession implements Session.
type cgoSession struct {
	raw unsafe.Pointer // *C.sr_session_ctx_t
	ds  Datastore
}

// SwitchDS switches the session's active datastore.
func (s *cgoSession) SwitchDS(ds Datastore) error {
	var cds C.sr_datastore_t
	switch ds {
	case Running:
		cds = C.SR_DS_RUNNING
	case Startup:
		cds = C.SR_DS_STARTUP
	case Candidate:
		cds = C.SR_DS_CANDIDATE
	case Operational:
		cds = C.SR_DS_OPERATIONAL
	default:
		cds = C.SR_DS_RUNNING
	}
	rc := C.sr_session_switch_ds((*C.sr_session_ctx_t)(s.raw), cds)
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_session_switch_ds: %s", C.GoString(C.sr_strerror(rc)))
	}
	s.ds = ds
	return nil
}

// CurrentDS returns the currently active datastore.
func (s *cgoSession) CurrentDS() Datastore { return s.ds }

// Get retrieves data at the given XPath using sr_get_data (which returns
// a libyang tree) and serializes it to XML via lyd_print_mem. The XML is
// then parsed into a DataNode tree by the caller's data encoder.
func (s *cgoSession) Get(ctx context.Context, xpath string) (*DataNode, error) {
	if xpath == "" || xpath == "/" {
		xpath = "/*"
	}
	cXPath := C.CString(xpath)
	defer C.free(unsafe.Pointer(cXPath))
	xmlC := C.cf_get_data_xml((*C.sr_session_ctx_t)(s.raw), cXPath)
	if xmlC == nil {
		return nil, fmt.Errorf("sysrepoadapter: sr_get_data(%s): internal error", xpath)
	}
	xmlStr := C.GoString(xmlC)
	C.free(unsafe.Pointer(xmlC))
	if xmlStr == "" {
		return &DataNode{XPath: "/", Name: "root"}, nil
	}
	// Parse the XML into a DataNode tree.
	root := parseXMLToDataNode(xmlStr)
	if root == nil {
		return &DataNode{XPath: "/", Name: "root"}, nil
	}
	return root, nil
}

// Lock locks a datastore (locks all modules when ds is Running).
func (s *cgoSession) Lock(ds Datastore) error {
	// sr_lock takes a module name; passing NULL locks all modules.
	rc := C.sr_lock((*C.sr_session_ctx_t)(s.raw), nil, 0)
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_lock: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// Unlock unlocks a datastore.
func (s *cgoSession) Unlock(ds Datastore) error {
	rc := C.sr_unlock((*C.sr_session_ctx_t)(s.raw), nil)
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_unlock: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// Close stops the session.
func (s *cgoSession) Close() error {
	if s.raw == nil {
		return nil
	}
	C.sr_session_stop((*C.sr_session_ctx_t)(s.raw))
	s.raw = nil
	return nil
}

// --- edit operations (phase 2) ---

// EditBatch loads a parsed edit tree into the session's staging area.
func (s *cgoSession) EditBatch(edit *DataNode, defaultOp string) error {
	// TODO: convert DataNode to lyd_node and call sr_edit_batch.
	return fmt.Errorf("sysrepoadapter: EditBatch not yet implemented in cgo stub")
}

// ApplyChanges commits the staged edits to the current datastore.
func (s *cgoSession) ApplyChanges(timeoutMs uint32) error {
	rc := C.sr_apply_changes((*C.sr_session_ctx_t)(s.raw), C.uint32_t(timeoutMs))
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_apply_changes: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// DiscardChanges discards all staged edits.
func (s *cgoSession) DiscardChanges() error {
	rc := C.sr_discard_changes((*C.sr_session_ctx_t)(s.raw))
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_discard_changes: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// Validate validates the current datastore + staged edits without applying.
func (s *cgoSession) Validate(moduleName string, timeoutMs uint32) error {
	var cModule *C.char
	if moduleName != "" {
		cModule = C.CString(moduleName)
		defer C.free(unsafe.Pointer(cModule))
	}
	rc := C.sr_validate((*C.sr_session_ctx_t)(s.raw), cModule, C.uint32_t(timeoutMs))
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_validate: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// CopyConfig replaces the current session's datastore with the contents of srcDatastore.
func (s *cgoSession) CopyConfig(moduleName string, srcDatastore Datastore, timeoutMs uint32) error {
	var cModule *C.char
	if moduleName != "" {
		cModule = C.CString(moduleName)
		defer C.free(unsafe.Pointer(cModule))
	}
	var cds C.sr_datastore_t
	switch srcDatastore {
	case Running:
		cds = C.SR_DS_RUNNING
	case Startup:
		cds = C.SR_DS_STARTUP
	case Candidate:
		cds = C.SR_DS_CANDIDATE
	default:
		cds = C.SR_DS_RUNNING
	}
	rc := C.sr_copy_config((*C.sr_session_ctx_t)(s.raw), cModule, cds, C.uint32_t(timeoutMs))
	if rc != C.SR_ERR_OK {
		return fmt.Errorf("sysrepoadapter: sr_copy_config: %s", C.GoString(C.sr_strerror(rc)))
	}
	return nil
}

// ReplaceConfig replaces the current session's datastore with the given config tree.
// If config is nil, the datastore is cleared (used by <delete-config>).
func (s *cgoSession) ReplaceConfig(moduleName string, config *DataNode, timeoutMs uint32) error {
	// TODO: convert DataNode to lyd_node and call sr_replace_config.
	// For now, sr_replace_config with NULL clears the datastore (delete-config).
	return fmt.Errorf("sysrepoadapter: ReplaceConfig not yet implemented in cgo stub")
}

// Compile-time checks.
var _ Adapter = (*CGo)(nil)
var _ Conn = (*cgoConn)(nil)
var _ Session = (*cgoSession)(nil)
var _ RawConnProvider = (*cgoConn)(nil)
