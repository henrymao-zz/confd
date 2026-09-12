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
// (caller must free with free()) or empty string for no data, or NULL on error.
static char *cf_get_data_xml(sr_session_ctx_t *session, const char *xpath) {
    sr_data_t *data = NULL;
    int rc = sr_get_data(session, xpath, 0, 0, 0, &data);
    if (rc != SR_ERR_OK) {
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

// cf_get_all_data_xml queries all installed modules and concatenates
// their XML output. This is used for the no-filter get-config case where
// we need to return the entire datastore.
static char *cf_get_all_data_xml(sr_conn_ctx_t *conn, sr_session_ctx_t *session) {
    sr_data_t *info = NULL;
    int rc = sr_get_module_info(conn, &info);
    if (rc != SR_ERR_OK) {
        return strdup("");
    }
    if (info == NULL || info->tree == NULL) {
        if (info) sr_release_data(info);
        return strdup("");
    }

    // Iterate over the module list in the sysrepo data tree.
    // The sysrepo internal data tree has /sysrepo:sysrepo-modules/module
    // entries with a "name" leaf for each installed module.
    char *result = strdup("");
    struct lyd_node *mod_node = NULL;
    struct lyd_node *first = lyd_child(info->tree);

    // Walk all siblings at the top level
    for (struct lyd_node *iter = info->tree; iter; iter = (struct lyd_node *)iter->next) {
        // Look for module entries
        for (struct lyd_node *child = lyd_child(iter); child; child = (struct lyd_node *)child->next) {
            // Get the module name from the "name" leaf
            struct lyd_node *name_node = NULL;
            for (struct lyd_node *n = lyd_child(child); n; n = (struct lyd_node *)n->next) {
                const char *node_name = LYD_NAME(n);
                if (node_name && strcmp(node_name, "name") == 0) {
                    name_node = n;
                    break;
                }
            }
            if (!name_node) continue;

            const char *mod_name = lyd_get_value(name_node);
            if (!mod_name) continue;

            // Skip internal sysrepo modules
            if (strncmp(mod_name, "sysrepo", 7) == 0) continue;
            if (strncmp(mod_name, "ietf-netconf", 12) == 0) continue;
            if (strncmp(mod_name, "ietf-datastores", 15) == 0) continue;
            if (strncmp(mod_name, "ietf-origin", 11) == 0) continue;
            if (strncmp(mod_name, "ietf-factory-default", 20) == 0) continue;

            // Build XPath: /<mod_name>:*
            char xpath[256];
            snprintf(xpath, sizeof(xpath), "/%s:*", mod_name);

            // Query data for this module
            char *mod_xml = cf_get_data_xml(session, xpath);
            if (mod_xml && mod_xml[0] != '\0') {
                // Append to result
                char *new_result = NULL;
                asprintf(&new_result, "%s%s", result, mod_xml);
                free(result);
                result = new_result;
            }
            free(mod_xml);
        }
    }

    sr_release_data(info);
    return result;
}
*/
import "C"

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
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
// It parses the sysrepo internal data tree (/sysrepo:sysrepo-modules/module)
// to extract module names. If the sysrepo internal data is not available
// (e.g. on first run), returns empty list without error.
func (c *cgoConn) GetModuleInfo(ctx context.Context) ([]ModuleInfo, error) {
	var data *C.sr_data_t
	rc := C.sr_get_module_info((*C.sr_conn_ctx_t)(c.raw), &data)
	if rc != C.SR_ERR_OK {
		return nil, fmt.Errorf("sysrepoadapter: sr_get_module_info: %s", C.GoString(C.sr_strerror(rc)))
	}
	if data == nil || data.tree == nil {
		if data != nil {
			C.sr_release_data(data)
		}
		return nil, nil
	}

	// Serialize the data tree to XML for parsing
	var xmlC *C.char
	lyRc := C.lyd_print_mem(&xmlC, data.tree, C.LYD_XML, 0)
	C.sr_release_data(data)
	if lyRc != C.LY_SUCCESS || xmlC == nil {
		return nil, nil
	}
	xmlStr := C.GoString(xmlC)
	C.free(unsafe.Pointer(xmlC))

	// Parse XML to extract module names from /sysrepo:sysrepo-modules/module/name
	var modules []ModuleInfo
	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local == "module" {
				var name string
				inner := false
				for {
					it, ierr := dec.Token()
					if ierr != nil {
						break
					}
					switch t := it.(type) {
					case xml.StartElement:
						if t.Name.Local == "name" {
							inner = true
						}
					case xml.CharData:
						if inner {
							name = strings.TrimSpace(string(t))
						}
					case xml.EndElement:
						if t.Name.Local == "name" {
							inner = false
						}
						if t.Name.Local == "module" {
							if name != "" {
								modules = append(modules, ModuleInfo{Name: name})
							}
							goto nextToken
						}
					}
				}
			nextToken:
			}
		}
	}
	return modules, nil
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
		errMsg := C.GoString(C.sr_strerror(rc))
		// Module already installed — not an error (idempotent).
		if strings.Contains(errMsg, "already exists") || strings.Contains(errMsg, "Exists") || rc == C.SR_ERR_EXISTS {
			return nil
		}
		return fmt.Errorf("sysrepoadapter: sr_install_module: %s", errMsg)
	}
	return nil
}

// SetModuleFeature enables or disables a feature on an installed module.
func (c *cgoConn) SetModuleFeature(ctx context.Context, module, feature string, enable bool) error {
	cMod := C.CString(module)
	cFeat := C.CString(feature)
	defer C.free(unsafe.Pointer(cMod))
	defer C.free(unsafe.Pointer(cFeat))
	var rc C.int
	if enable {
		rc = C.sr_enable_module_feature((*C.sr_conn_ctx_t)(c.raw), cMod, cFeat)
	} else {
		rc = C.sr_disable_module_feature((*C.sr_conn_ctx_t)(c.raw), cMod, cFeat)
	}
	if rc != C.SR_ERR_OK {
		errMsg := C.GoString(C.sr_strerror(rc))
		// Feature already in desired state — not an error.
		if strings.Contains(errMsg, "already exists") || rc == C.SR_ERR_EXISTS {
			return nil
		}
		return fmt.Errorf("sysrepoadapter: sr_%s_module_feature: %s",
			map[bool]string{true: "enable", false: "disable"}[enable],
			errMsg)
	}
	return nil
}

// OpenSession starts a new sysrepo session on this connection.
func (c *cgoConn) OpenSession(ctx context.Context, user string) (Session, error) {
	var sess *C.sr_session_ctx_t
	rc := C.sr_session_start((*C.sr_conn_ctx_t)(c.raw), C.SR_DS_RUNNING, &sess)
	if rc != C.SR_ERR_OK {
		return nil, fmt.Errorf("sysrepoadapter: sr_session_start: %s", C.GoString(C.sr_strerror(rc)))
	}
	return &cgoSession{raw: unsafe.Pointer(sess), connRaw: c.raw, ds: Running}, nil
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
	raw     unsafe.Pointer // *C.sr_session_ctx_t
	connRaw unsafe.Pointer // *C.sr_conn_ctx_t (for module info queries)
	ds      Datastore
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
	var xmlC *C.char
	if xpath == "" || xpath == "/" {
		// No-filter case: query all installed modules and concatenate.
		xmlC = C.cf_get_all_data_xml((*C.sr_conn_ctx_t)(s.connRaw), (*C.sr_session_ctx_t)(s.raw))
	} else {
		cXPath := C.CString(xpath)
		xmlC = C.cf_get_data_xml((*C.sr_session_ctx_t)(s.raw), cXPath)
		C.free(unsafe.Pointer(cXPath))
	}
	if xmlC == nil {
		return nil, fmt.Errorf("sysrepoadapter: sr_get_data(%s): internal error", xpath)
	}
	xmlStr := C.GoString(xmlC)
	C.free(unsafe.Pointer(xmlC))
	if xmlStr == "" {
		return &DataNode{XPath: "/", Name: "root"}, nil
	}
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
