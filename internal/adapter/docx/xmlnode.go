package docx

import (
	"encoding/xml"
	"fmt"
	"io"
)

// xmlNode is a generic element: enough of the tree to walk children in
// document order, which python-docx's XPath queries depend on.
type xmlNode struct {
	XMLName  xml.Name
	Text     string     `xml:",chardata"`
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []xmlNode  `xml:",any"`
}

func decodeXML(r io.Reader) (*xmlNode, error) {
	var root xmlNode
	if err := xml.NewDecoder(r).Decode(&root); err != nil {
		return nil, fmt.Errorf("decode xml: %w", err)
	}
	return &root, nil
}

func (n *xmlNode) is(space, local string) bool {
	return n != nil && n.XMLName.Space == space && n.XMLName.Local == local
}

// child is the first child element named space:local, or nil. Every
// accessor accepts a nil node, so a chain of lookups needs one check.
func (n *xmlNode) child(space, local string) *xmlNode {
	if n == nil {
		return nil
	}
	for i := range n.Children {
		if n.Children[i].is(space, local) {
			return &n.Children[i]
		}
	}
	return nil
}

func (n *xmlNode) children(space, local string) []*xmlNode {
	if n == nil {
		return nil
	}
	var out []*xmlNode
	for i := range n.Children {
		if n.Children[i].is(space, local) {
			out = append(out, &n.Children[i])
		}
	}
	return out
}

// attr is the value of attribute space:local, or "" when it is absent.
func (n *xmlNode) attr(space, local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
