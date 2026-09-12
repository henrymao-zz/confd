//go:build sysrepo

package sysrepoadapter

import (
	"encoding/xml"
	"strings"
)

// parseXMLToDataNode parses NETCONF XML (returned by libyang's
// lyd_print_mem) into a DataNode tree. This is the same logic as
// internal/data/decoder.go's DecodeData, but duplicated here to avoid
// a build-tag dependency between internal/sysrepoadapter (which is
// behind the sysrepo tag) and internal/data (which is always built).
func parseXMLToDataNode(xmlStr string) *DataNode {
	dec := xml.NewDecoder(strings.NewReader(xmlStr))
	root := &DataNode{XPath: "/", Name: "root"}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			child, err := decodeElement(dec, se)
			if err != nil {
				break
			}
			root.Children = append(root.Children, child)
		}
	}
	return root
}

func decodeElement(dec *xml.Decoder, se xml.StartElement) (*DataNode, error) {
	node := &DataNode{
		Name: se.Name.Local,
		NS:  se.Name.Space,
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := decodeElement(dec, t)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		case xml.EndElement:
			node.IsLeaf = len(node.Children) == 0
			return node, nil
		case xml.CharData:
			val := strings.TrimSpace(string(t))
			if val != "" && len(node.Children) == 0 {
				node.Value = val
			}
		}
	}
}
