package graph

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// MarshalEdgeAdjacency serializes the three edge adjacency maps
// (outEdges, inEdges, typeEdges) into a binary format. This allows
// EdgesFrom/EdgesTo to work at startup without loading all edges.
func (g *Graph) MarshalEdgeAdjacency() ([]byte, error) {
	// Format: magic(4) + version(2) + 3 maps (out, in, type)
	// Each map: numKeys(uint32) + for each key: keyLen(uint16) + key + numVals(uint32) + for each val: valLen(uint16) + val
	buf := make([]byte, 0, 1024)
	buf = append(buf, 'E', 'A', 'D', 'J')
	buf = binary.LittleEndian.AppendUint16(buf, 1)

	buf = marshalAdjMap(buf, g.outEdges)
	buf = marshalAdjMap(buf, g.inEdges)
	buf = marshalAdjMap(buf, g.typeEdges)

	return buf, nil
}

// UnmarshalEdgeAdjacency restores the edge adjacency maps from binary
// data. Clears existing adjacency state.
func (g *Graph) UnmarshalEdgeAdjacency(data []byte) error {
	if len(data) < 6 || string(data[:4]) != "EADJ" {
		return fmt.Errorf("edge adjacency: invalid magic")
	}
	// version := binary.LittleEndian.Uint16(data[4:6])
	pos := 6

	var err error
	g.outEdges, pos, err = unmarshalAdjMap(data, pos)
	if err != nil {
		return fmt.Errorf("edge adjacency: outEdges: %w", err)
	}
	g.inEdges, pos, err = unmarshalAdjMap(data, pos)
	if err != nil {
		return fmt.Errorf("edge adjacency: inEdges: %w", err)
	}
	g.typeEdges, _, err = unmarshalAdjMap(data, pos)
	if err != nil {
		return fmt.Errorf("edge adjacency: typeEdges: %w", err)
	}
	return nil
}

func marshalAdjMap(buf []byte, m map[string]map[string]struct{}) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(keys)))
	for _, key := range keys {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(key)))
		buf = append(buf, key...)

		vals := make([]string, 0, len(m[key]))
		for v := range m[key] {
			vals = append(vals, v)
		}
		sort.Strings(vals)

		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(vals)))
		for _, val := range vals {
			buf = binary.LittleEndian.AppendUint16(buf, uint16(len(val)))
			buf = append(buf, val...)
		}
	}
	return buf
}

const (
	maxAdjKeys = 1_000_000 // upper bound on keys per adjacency map
	maxAdjVals = 1_000_000 // upper bound on values per key
)

func unmarshalAdjMap(data []byte, pos int) (map[string]map[string]struct{}, int, error) {
	if pos+4 > len(data) {
		return nil, pos, fmt.Errorf("truncated map header")
	}
	numKeys := int(binary.LittleEndian.Uint32(data[pos:]))
	pos += 4
	if numKeys > maxAdjKeys {
		return nil, pos, fmt.Errorf("numKeys %d exceeds maximum %d", numKeys, maxAdjKeys)
	}

	m := make(map[string]map[string]struct{}, numKeys)
	for i := 0; i < numKeys; i++ {
		if pos+2 > len(data) {
			return nil, pos, fmt.Errorf("truncated key at %d", i)
		}
		keyLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+keyLen > len(data) {
			return nil, pos, fmt.Errorf("truncated key data at %d", i)
		}
		key := string(data[pos : pos+keyLen])
		pos += keyLen

		if pos+4 > len(data) {
			return nil, pos, fmt.Errorf("truncated vals header at key %d", i)
		}
		numVals := int(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
		if numVals > maxAdjVals {
			return nil, pos, fmt.Errorf("numVals %d at key %d exceeds maximum %d", numVals, i, maxAdjVals)
		}

		vals := make(map[string]struct{}, numVals)
		for j := 0; j < numVals; j++ {
			if pos+2 > len(data) {
				return nil, pos, fmt.Errorf("truncated val at key %d val %d", i, j)
			}
			valLen := int(binary.LittleEndian.Uint16(data[pos:]))
			pos += 2
			if pos+valLen > len(data) {
				return nil, pos, fmt.Errorf("truncated val data at key %d val %d", i, j)
			}
			vals[string(data[pos:pos+valLen])] = struct{}{}
			pos += valLen
		}
		m[key] = vals
	}
	return m, pos, nil
}
