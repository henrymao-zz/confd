package data

import (
	"encoding/xml"
	"io"
	"strings"

	"github.com/example/confd/internal/sysrepoadapter"
)

// DecodeData parses NETCONF XML (the inner content of a <config> element)
// into a DataNode tree. This is the inverse of Encoder.EncodeData.
func DecodeData(xmlBytes []byte) (*sysrepoadapter.DataNode, error) {
	dec := xml.NewDecoder(strings.NewReader(string(xmlBytes)))
	root := &sysrepoadapter.DataNode{Name: "root", XPath: "/"}
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			child, err := decodeElement(dec, se)
			if err != nil {
				return nil, err
			}
			root.Children = append(root.Children, child)
		}
	}
	return root, nil
}

// decodeElement decodes a single XML element and its children into a DataNode.
func decodeElement(dec *xml.Decoder, se xml.StartElement) (*sysrepoadapter.DataNode, error) {
	node := &sysrepoadapter.DataNode{
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
			// If no children, this is a leaf — check for CharData value.
			if len(node.Children) == 0 && node.Value == "" {
				// The value was already set by CharData if present.
			}
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
