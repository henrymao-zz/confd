package data

import (
	"strings"
	"testing"

	"github.com/example/confd/internal/schema"
	"github.com/example/confd/internal/sysrepoadapter"
)

func testCache(t *testing.T) *schema.Cache {
	t.Helper()
	c := schema.New()
	if err := c.LoadFiles("../../yang/confd-test.yang"); err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func TestEncodeData_Leaves(t *testing.T) {
	c := testCache(t)
	tree := &sysrepoadapter.DataNode{
		XPath: "/",
		Name:  "root",
		Children: []*sysrepoadapter.DataNode{
			{
				XPath: "/confd-test:system", Name: "system",
				NS: "urn:ietf:params:xml:ns:yang:confd-test",
				Children: []*sysrepoadapter.DataNode{
					{XPath: "/confd-test:system/hostname", Name: "hostname", NS: "urn:ietf:params:xml:ns:yang:confd-test", IsLeaf: true, Value: "r1"},
					{XPath: "/confd-test:system/interfaces", Name: "interfaces", NS: "urn:ietf:params:xml:ns:yang:confd-test",
						Children: []*sysrepoadapter.DataNode{
							{XPath: "/confd-test:system/interfaces/interface[name='eth0']", Name: "interface", IsList: true, Key: "eth0", NS: "urn:ietf:params:xml:ns:yang:confd-test",
								Children: []*sysrepoadapter.DataNode{
									{Name: "name", IsLeaf: true, Value: "eth0", NS: "urn:ietf:params:xml:ns:yang:confd-test"},
									{Name: "mtu", IsLeaf: true, Value: "1500", NS: "urn:ietf:params:xml:ns:yang:confd-test"},
								}},
						}},
				},
			},
		},
	}
	enc := New(c)
	out := enc.EncodeData(tree)
	s := string(out)
	if !strings.Contains(s, `<hostname xmlns="urn:ietf:params:xml:ns:yang:confd-test">r1</hostname>`) {
		t.Errorf("missing hostname leaf: %s", s)
	}
	if !strings.Contains(s, "eth0") || !strings.Contains(s, "1500") {
		t.Errorf("missing interface data: %s", s)
	}
}

func TestEncodeData_Nil(t *testing.T) {
	enc := New(testCache(t))
	if len(enc.EncodeData(nil)) != 0 {
		t.Error("nil should encode to empty")
	}
}
