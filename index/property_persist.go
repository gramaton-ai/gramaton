package index

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/gramaton-ai/gramaton/graph"
)

// PropertyIndex binary format:
//
//   header:
//     magic      [4]byte  "PIDX"
//     version    uint16   1
//     numEntries uint32
//
//   per entry (sorted by key, then serialized value, then nodeID):
//     key_len     uint16
//     key         []byte
//     val_len     uint16
//     val         []byte   (Property.MarshalBinary output)
//     nodeID_len  uint16
//     nodeID      []byte

var propMagic = [4]byte{'P', 'I', 'D', 'X'}

// MarshalBinary serializes the property index. The format stores each
// (key, property, nodeID) tuple. On unmarshal, Add is called for each
// tuple, rebuilding all derived indexes (sorted, strings, keywords,
// nodeKeys).
func (idx *PropertyIndex) MarshalBinary() ([]byte, error) {
	// Collect all tuples for deterministic output.
	type tuple struct {
		key, serializedVal, nodeID string
	}
	var tuples []tuple
	for key, byVal := range idx.exact {
		for serialized, nodes := range byVal {
			for nodeID := range nodes {
				tuples = append(tuples, tuple{key, serialized, nodeID})
			}
		}
	}

	sort.Slice(tuples, func(i, j int) bool {
		if tuples[i].key != tuples[j].key {
			return tuples[i].key < tuples[j].key
		}
		if tuples[i].serializedVal != tuples[j].serializedVal {
			return tuples[i].serializedVal < tuples[j].serializedVal
		}
		return tuples[i].nodeID < tuples[j].nodeID
	})

	// Estimate size: header(10) + per tuple ~128 bytes.
	buf := make([]byte, 0, 10+len(tuples)*128)
	buf = append(buf, propMagic[:]...)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(tuples)))

	for _, t := range tuples {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(t.key)))
		buf = append(buf, t.key...)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(t.serializedVal)))
		buf = append(buf, t.serializedVal...)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(t.nodeID)))
		buf = append(buf, t.nodeID...)
	}

	return buf, nil
}

// UnmarshalBinary restores the property index from binary data.
// Clears any existing state and replays Add for each stored tuple.
func (idx *PropertyIndex) UnmarshalBinary(data []byte) error {
	if len(data) < 10 {
		return fmt.Errorf("property index: data too short (%d bytes)", len(data))
	}
	if string(data[:4]) != string(propMagic[:]) {
		return fmt.Errorf("property index: invalid magic")
	}
	version := binary.LittleEndian.Uint16(data[4:6])
	if version != 1 {
		return fmt.Errorf("property index: unsupported version %d", version)
	}
	numEntries := binary.LittleEndian.Uint32(data[6:10])
	const maxPropEntries = 10_000_000
	if numEntries > maxPropEntries {
		return fmt.Errorf("property index: numEntries %d exceeds maximum %d", numEntries, maxPropEntries)
	}

	// Reset state.
	idx.exact = make(map[string]map[string]map[string]struct{})
	idx.sorted = make(map[string][]rangeEntry)
	idx.strings = make(map[string]map[string]string)
	idx.keywords = make(map[string]map[string]map[string]struct{})
	idx.nodeKeys = make(map[string]map[string]struct{})

	pos := 10
	for i := uint32(0); i < numEntries; i++ {
		if pos+2 > len(data) {
			return fmt.Errorf("property index: truncated at entry %d", i)
		}
		keyLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+keyLen > len(data) {
			return fmt.Errorf("property index: truncated key at entry %d", i)
		}
		key := string(data[pos : pos+keyLen])
		pos += keyLen

		if pos+2 > len(data) {
			return fmt.Errorf("property index: truncated at entry %d val len", i)
		}
		valLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+valLen > len(data) {
			return fmt.Errorf("property index: truncated val at entry %d", i)
		}
		valBytes := data[pos : pos+valLen]
		pos += valLen

		if pos+2 > len(data) {
			return fmt.Errorf("property index: truncated at entry %d nodeID len", i)
		}
		nodeIDLen := int(binary.LittleEndian.Uint16(data[pos:]))
		pos += 2
		if pos+nodeIDLen > len(data) {
			return fmt.Errorf("property index: truncated nodeID at entry %d", i)
		}
		nodeID := string(data[pos : pos+nodeIDLen])
		pos += nodeIDLen

		// Deserialize the property and call Add to rebuild all indexes.
		prop, err := graph.UnmarshalProperty(valBytes)
		if err != nil {
			return fmt.Errorf("property index: unmarshal property at entry %d: %w", i, err)
		}
		idx.Add(nodeID, key, prop)
	}

	return nil
}
