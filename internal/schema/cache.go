// Package schema implements the goyang-backed schema cache used by confd
// for capability advertisement, <get-schema>, and filter-path validation.
//
// The cache is the *only* package in confd that imports goyang. It exposes a
// small Go-native API (ModuleInfo, EntryInfo) so that the rest of confd does
// not depend on goyang directly.
package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/openconfig/goyang/pkg/yang"
)

// ModuleInfo describes one loaded YANG module for capability advertisement
// and for <get-schema> (RFC 6022).
type ModuleInfo struct {
	Name       string
	Revision   string
	Namespace  string
	Prefix     string
	SourceFile string // absolute path to the .yang file
	SourceText string // raw YANG text (for <get-schema>)
	Entry      *EntryInfo
}

// EntryInfo is the Go-native projection of a yang.Entry subtree.
type EntryInfo struct {
	Name     string
	Kind     string // "container" | "leaf" | "leaf-list" | "list"
	IsConfig bool
	Children []*EntryInfo
}

// Cache holds a resolved set of YANG modules.
type Cache struct {
	mu      sync.RWMutex
	modules map[string]*ModuleInfo // by module name
	byNS    map[string]*ModuleInfo // by namespace
	caps    []string               // capability URIs
	text    map[string]string      // module name -> source text
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{
		modules: map[string]*ModuleInfo{},
		byNS:    map[string]*ModuleInfo{},
		text:    map[string]string{},
	}
}

// LoadDirectory parses every *.yang file directly in dir (no subdirectory
// recursion). Modules that fail to parse are skipped (goyang is stricter
// than libyang and may reject some valid YANG files).
func (c *Cache) LoadDirectory(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yang"))
	if err != nil {
		return fmt.Errorf("schema: glob %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil
	}
	return c.LoadFilesTolerant(paths...)
}

// LoadFilesTolerant is like LoadFiles but skips modules that fail to
// parse instead of returning an error. This is used when loading from
// sysrepo's YANG directory where some modules may have augments that
// goyang can't handle.
func (c *Cache) LoadFilesTolerant(paths ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	type src struct{ path, text string }
	sources := map[string]src{}
	ms := yang.NewModules()
	seen := map[string]bool{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		name := extractModuleName(string(data))
		if name == "" || seen[name] {
			continue
		}
		abs, _ := filepath.Abs(p)
		if err := ms.Parse(string(data), abs); err != nil {
			continue // skip modules that goyang can't parse
		}
		seen[name] = true
		sources[name] = src{path: abs, text: string(data)}
	}

	_ = ms.Process() // may return errors for some modules; ignore

	for _, mod := range ms.Modules {
		if mod == nil || mod.Kind() != "module" {
			continue
		}
		name := mod.Name
		if c.modules[name] != nil {
			continue
		}
		entry := yang.ToEntry(mod)
		srcInfo := sources[name]
		info := &ModuleInfo{
			Name:        name,
			Revision:    mod.Current(),
			Namespace:   strVal(mod.Namespace),
			Prefix:      mod.GetPrefix(),
			SourceFile:  srcInfo.path,
			SourceText:  srcInfo.text,
			Entry:       entryInfo(entry),
		}
		c.modules[name] = info
		if info.Namespace != "" {
			c.byNS[info.Namespace] = info
		}
		c.text[name] = info.SourceText
	}

	c.rebuildCaps()
	return nil
}

// LoadFiles parses the given YANG files into the cache.
func (c *Cache) LoadFiles(paths ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	type src struct{ path, text string }
	sources := map[string]src{} // module-name -> source

	ms := yang.NewModules()
	seen := map[string]bool{} // module-name -> loaded
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("schema: read %s: %w", p, err)
		}
		name := extractModuleName(string(data))
		if name == "" {
			return fmt.Errorf("schema: %s: no module declaration", p)
		}
		if seen[name] {
			continue // skip duplicate module (same name, different path)
		}
		abs, _ := filepath.Abs(p)
		if err := ms.Parse(string(data), abs); err != nil {
			return fmt.Errorf("schema: parse %s: %w", p, err)
		}
		seen[name] = true
		sources[name] = src{path: abs, text: string(data)}
	}

	if errs := ms.Process(); len(errs) > 0 {
		return fmt.Errorf("schema: process: %v", errs[0])
	}

	for _, mod := range ms.Modules {
		if mod == nil || mod.Kind() != "module" {
			continue
		}
		name := mod.Name
		if c.modules[name] != nil {
			continue // goyang may index a module under both "name" and
			         // "name@revision"; keep the first.
		}
		entry := yang.ToEntry(mod)
		srcInfo := sources[name]
		info := &ModuleInfo{
			Name:        name,
			Revision:    mod.Current(),
			Namespace:   strVal(mod.Namespace),
			Prefix:      mod.GetPrefix(),
			SourceFile:  srcInfo.path,
			SourceText:  srcInfo.text,
			Entry:       entryInfo(entry),
		}
		c.modules[name] = info
		if info.Namespace != "" {
			c.byNS[info.Namespace] = info
		}
		c.text[name] = info.SourceText
	}

	c.rebuildCaps()
	return nil
}

var moduleNameRe = regexp.MustCompile(`(?m)^\s*module\s+([A-Za-z_][A-Za-z0-9_.\-]*)\s*\{`)

func extractModuleName(text string) string {
	m := moduleNameRe.FindStringSubmatch(text)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func strVal(v *yang.Value) string {
	if v == nil {
		return ""
	}
	return v.Name
}

func entryInfo(e *yang.Entry) *EntryInfo {
	if e == nil {
		return nil
	}
	info := &EntryInfo{
		Name: e.Name,
		Kind: entryKindString(e),
		IsConfig: e.Config == yang.TSTrue || e.Config == yang.TSUnset,
	}
	if e.Dir != nil {
		keys := make([]string, 0, len(e.Dir))
		for k := range e.Dir {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if child := entryInfo(e.Dir[k]); child != nil {
				info.Children = append(info.Children, child)
			}
		}
	}
	return info
}

func entryKindString(e *yang.Entry) string {
	switch {
	case e.IsContainer():
		return "container"
	case e.IsList():
		return "list"
	case e.IsLeafList():
		return "leaf-list"
	case e.IsLeaf():
		return "leaf"
	case e.IsChoice():
		return "choice"
	case e.IsCase():
		return "case"
	default:
		return "directory"
	}
}

func (c *Cache) rebuildCaps() {
	names := make([]string, 0, len(c.modules))
	for n := range c.modules {
		names = append(names, n)
	}
	sort.Strings(names)
	caps := make([]string, 0, len(names))
	for _, n := range names {
		m := c.modules[n]
		uri := m.Namespace
		if m.Revision != "" {
			uri += "?revision=" + m.Revision
		}
		caps = append(caps, uri)
	}
	c.caps = caps
}

// Modules returns all loaded module infos, sorted by name.
func (c *Cache) Modules() []*ModuleInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*ModuleInfo, 0, len(c.modules))
	for _, m := range c.modules {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Module returns the named module info, or nil.
func (c *Cache) Module(name string) *ModuleInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.modules[name]
}

// ModuleByNamespace returns the module bound to a YANG namespace URI.
func (c *Cache) ModuleByNamespace(ns string) *ModuleInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byNS[ns]
}

// Capabilities returns the capability URIs to advertise in <hello>.
func (c *Cache) Capabilities() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.caps))
	copy(out, c.caps)
	return out
}

// SourceText returns the raw YANG source for a module (for <get-schema>).
func (c *Cache) SourceText(name string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.text[name]
	return t, ok
}
