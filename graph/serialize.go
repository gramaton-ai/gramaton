package graph

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// MarshalNode encodes a node to a deterministic byte representation.
// Format: [id_length:4][id][properties_bytes]
func MarshalNode(n *Node) ([]byte, error) {
	var buf bytes.Buffer

	// Node ID.
	writeStr(&buf, n.ID)

	// Properties.
	propBytes, err := n.Properties.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal node %s properties: %w", n.ID, err)
	}
	if err := binary.Write(&buf, binary.BigEndian, uint32(len(propBytes))); err != nil {
		return nil, fmt.Errorf("marshal node %s prop length: %w", n.ID, err)
	}
	buf.Write(propBytes)

	return buf.Bytes(), nil
}

// UnmarshalNode decodes a node from bytes.
func UnmarshalNode(data []byte) (*Node, error) {
	r := bytes.NewReader(data)

	id, err := readStr(r)
	if err != nil {
		return nil, fmt.Errorf("unmarshal node id: %w", err)
	}

	var propLen uint32
	if err := binary.Read(r, binary.BigEndian, &propLen); err != nil {
		return nil, fmt.Errorf("unmarshal node prop length: %w", err)
	}
	propBytes := make([]byte, propLen)
	if propLen > 0 {
		if _, err := r.Read(propBytes); err != nil {
			return nil, fmt.Errorf("unmarshal node prop data: %w", err)
		}
	}

	props, err := UnmarshalProperties(propBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshal node %s properties: %w", id, err)
	}

	return &Node{ID: id, Properties: props}, nil
}

// MarshalEdge encodes an edge to a deterministic byte representation.
// Format: [id][source_id][target_id][type][weight:8][properties_bytes]
func MarshalEdge(e *Edge) ([]byte, error) {
	var buf bytes.Buffer

	writeStr(&buf, e.ID)
	writeStr(&buf, e.SourceID)
	writeStr(&buf, e.TargetID)
	writeStr(&buf, e.Type)

	if err := binary.Write(&buf, binary.BigEndian, e.Weight); err != nil {
		return nil, fmt.Errorf("marshal edge %s weight: %w", e.ID, err)
	}

	propBytes, err := e.Properties.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal edge %s properties: %w", e.ID, err)
	}
	if err := binary.Write(&buf, binary.BigEndian, uint32(len(propBytes))); err != nil {
		return nil, fmt.Errorf("marshal edge %s prop length: %w", e.ID, err)
	}
	buf.Write(propBytes)

	return buf.Bytes(), nil
}

// UnmarshalEdge decodes an edge from bytes.
func UnmarshalEdge(data []byte) (*Edge, error) {
	r := bytes.NewReader(data)

	id, err := readStr(r)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge id: %w", err)
	}
	sourceID, err := readStr(r)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge source: %w", err)
	}
	targetID, err := readStr(r)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge target: %w", err)
	}
	typ, err := readStr(r)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge type: %w", err)
	}

	var weight float64
	if err := binary.Read(r, binary.BigEndian, &weight); err != nil {
		return nil, fmt.Errorf("unmarshal edge weight: %w", err)
	}

	var propLen uint32
	if err := binary.Read(r, binary.BigEndian, &propLen); err != nil {
		return nil, fmt.Errorf("unmarshal edge prop length: %w", err)
	}
	propBytes := make([]byte, propLen)
	if propLen > 0 {
		if _, err := r.Read(propBytes); err != nil {
			return nil, fmt.Errorf("unmarshal edge prop data: %w", err)
		}
	}

	props, err := UnmarshalProperties(propBytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge %s properties: %w", id, err)
	}

	return &Edge{
		ID:         id,
		SourceID:   sourceID,
		TargetID:   targetID,
		Type:       typ,
		Weight:     weight,
		Properties: props,
	}, nil
}

// writeStr writes a length-prefixed string to a buffer.
func writeStr(buf *bytes.Buffer, s string) {
	_ = binary.Write(buf, binary.BigEndian, uint32(len(s)))
	buf.WriteString(s)
}

// readStr reads a length-prefixed string from a reader.
func readStr(r *bytes.Reader) (string, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	b := make([]byte, length)
	if _, err := r.Read(b); err != nil {
		return "", err
	}
	return string(b), nil
}
