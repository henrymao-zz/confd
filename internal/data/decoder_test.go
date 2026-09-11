package data

import (
	"testing"
)

func TestDecodeData_Leaf(t *testing.T) {
	xml := `<system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>router-1</hostname></system>`
	root, err := DecodeData([]byte(xml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(root.Children))
	}
	sys := root.Children[0]
	if sys.Name != "system" || sys.NS != "urn:ietf:params:xml:ns:yang:confd-test" {
		t.Errorf("system: name=%s ns=%s", sys.Name, sys.NS)
	}
	if len(sys.Children) != 1 {
		t.Fatalf("expected 1 child of system, got %d", len(sys.Children))
	}
	hn := sys.Children[0]
	if hn.Name != "hostname" || hn.Value != "router-1" || !hn.IsLeaf {
		t.Errorf("hostname: name=%s value=%s isLeaf=%v", hn.Name, hn.Value, hn.IsLeaf)
	}
}

func TestDecodeData_MultipleLeaves(t *testing.T) {
	xml := `<system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>r1</hostname><domain>example.com</domain></system>`
	root, err := DecodeData([]byte(xml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sys := root.Children[0]
	if len(sys.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(sys.Children))
	}
	if sys.Children[0].Name != "hostname" || sys.Children[0].Value != "r1" {
		t.Errorf("child 0: %s=%s", sys.Children[0].Name, sys.Children[0].Value)
	}
	if sys.Children[1].Name != "domain" || sys.Children[1].Value != "example.com" {
		t.Errorf("child 1: %s=%s", sys.Children[1].Name, sys.Children[1].Value)
	}
}

func TestDecodeData_Empty(t *testing.T) {
	root, err := DecodeData([]byte(""))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(root.Children) != 0 {
		t.Fatalf("expected 0 children, got %d", len(root.Children))
	}
}

func TestDecodeData_NestedContainers(t *testing.T) {
	xml := `<system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><interfaces><interface><name>eth0</name><mtu>1500</mtu></interface></interfaces></system>`
	root, err := DecodeData([]byte(xml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	sys := root.Children[0]
	ifaces := sys.Children[0]
	if ifaces.Name != "interfaces" {
		t.Fatalf("expected interfaces, got %s", ifaces.Name)
	}
	iface := ifaces.Children[0]
	if iface.Name != "interface" || iface.IsLeaf {
		t.Fatalf("expected interface container, got %s (isLeaf=%v)", iface.Name, iface.IsLeaf)
	}
	if len(iface.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(iface.Children))
	}
	if iface.Children[0].Name != "name" || iface.Children[0].Value != "eth0" {
		t.Errorf("name: %s=%s", iface.Children[0].Name, iface.Children[0].Value)
	}
	if iface.Children[1].Name != "mtu" || iface.Children[1].Value != "1500" {
		t.Errorf("mtu: %s=%s", iface.Children[1].Name, iface.Children[1].Value)
	}
}

func TestDecodeData_EncodesBackToSameXML(t *testing.T) {
	xml := `<system xmlns="urn:ietf:params:xml:ns:yang:confd-test"><hostname>r1</hostname></system>`
	root, err := DecodeData([]byte(xml))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	enc := New(nil)
	out := enc.EncodeData(root)
	if string(out) == "" {
		t.Fatal("encoded output is empty")
	}
}
