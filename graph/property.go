package graph

import (
	"bytes"
	"encoding/binary"
	"fmt"
"time"
)

// PropertyType identifies the type of a property value.
type PropertyType uint8

const (
	TypeString     PropertyType = 1
	TypeFloat64    PropertyType = 2
	TypeInt64      PropertyType = 3
	TypeBool       PropertyType = 4
	TypeTimestamp  PropertyType = 5
	TypeVector     PropertyType = 6
	TypeStringList PropertyType = 7
	TypeBytes      PropertyType = 8
)

func (t PropertyType) String() string {
	switch t {
	case TypeString:
		return "String"
	case TypeFloat64:
		return "Float64"
	case TypeInt64:
		return "Int64"
	case TypeBool:
		return "Bool"
	case TypeTimestamp:
		return "Timestamp"
	case TypeVector:
		return "Vector"
	case TypeStringList:
		return "StringList"
	case TypeBytes:
		return "Bytes"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// Property is a typed value. Exactly one of the typed fields is valid,
// determined by Type. This is a value type -- copy freely.
type Property struct {
	Type PropertyType

	str     string
	float64 float64
	int64   int64
	bool    bool
	time    time.Time
	vector  []float32
	strList []string
	bytes   []byte
}

// Constructors

func StringProperty(v string) Property {
	return Property{Type: TypeString, str: v}
}

func Float64Property(v float64) Property {
	return Property{Type: TypeFloat64, float64: v}
}

func Int64Property(v int64) Property {
	return Property{Type: TypeInt64, int64: v}
}

func BoolProperty(v bool) Property {
	return Property{Type: TypeBool, bool: v}
}

func TimestampProperty(v time.Time) Property {
	return Property{Type: TypeTimestamp, time: v.UTC()}
}

func VectorProperty(v []float32) Property {
	cp := make([]float32, len(v))
	copy(cp, v)
	return Property{Type: TypeVector, vector: cp}
}

func StringListProperty(v []string) Property {
	cp := make([]string, len(v))
	copy(cp, v)
	return Property{Type: TypeStringList, strList: cp}
}

func BytesProperty(v []byte) Property {
	cp := make([]byte, len(v))
	copy(cp, v)
	return Property{Type: TypeBytes, bytes: cp}
}

// Accessors. Each panics if called on the wrong type.

func (p Property) String() string {
	if p.Type != TypeString {
		panic(fmt.Sprintf("String() called on %s property", p.Type))
	}
	return p.str
}

func (p Property) Float64() float64 {
	if p.Type != TypeFloat64 {
		panic(fmt.Sprintf("Float64() called on %s property", p.Type))
	}
	return p.float64
}

func (p Property) Int64() int64 {
	if p.Type != TypeInt64 {
		panic(fmt.Sprintf("Int64() called on %s property", p.Type))
	}
	return p.int64
}

func (p Property) Bool() bool {
	if p.Type != TypeBool {
		panic(fmt.Sprintf("Bool() called on %s property", p.Type))
	}
	return p.bool
}

func (p Property) Timestamp() time.Time {
	if p.Type != TypeTimestamp {
		panic(fmt.Sprintf("Timestamp() called on %s property", p.Type))
	}
	return p.time
}

func (p Property) Vector() []float32 {
	if p.Type != TypeVector {
		panic(fmt.Sprintf("Vector() called on %s property", p.Type))
	}
	cp := make([]float32, len(p.vector))
	copy(cp, p.vector)
	return cp
}

func (p Property) StringList() []string {
	if p.Type != TypeStringList {
		panic(fmt.Sprintf("StringList() called on %s property", p.Type))
	}
	cp := make([]string, len(p.strList))
	copy(cp, p.strList)
	return cp
}

func (p Property) Bytes() []byte {
	if p.Type != TypeBytes {
		panic(fmt.Sprintf("Bytes() called on %s property", p.Type))
	}
	cp := make([]byte, len(p.bytes))
	copy(cp, p.bytes)
	return cp
}

// Equal reports whether two properties have the same type and value.
func (p Property) Equal(other Property) bool {
	if p.Type != other.Type {
		return false
	}
	switch p.Type {
	case TypeString:
		return p.str == other.str
	case TypeFloat64:
		return p.float64 == other.float64
	case TypeInt64:
		return p.int64 == other.int64
	case TypeBool:
		return p.bool == other.bool
	case TypeTimestamp:
		return p.time.Equal(other.time)
	case TypeVector:
		if len(p.vector) != len(other.vector) {
			return false
		}
		for i := range p.vector {
			if p.vector[i] != other.vector[i] {
				return false
			}
		}
		return true
	case TypeStringList:
		if len(p.strList) != len(other.strList) {
			return false
		}
		for i := range p.strList {
			if p.strList[i] != other.strList[i] {
				return false
			}
		}
		return true
	case TypeBytes:
		return bytes.Equal(p.bytes, other.bytes)
	default:
		return false
	}
}

// Compare returns -1, 0, or 1 for ordered types (String, Float64, Int64,
// Timestamp). Panics for unordered types (Bool, Vector, StringList, Bytes)
// or if the types don't match.
func (p Property) Compare(other Property) int {
	if p.Type != other.Type {
		panic(fmt.Sprintf("Compare: mismatched types %s and %s", p.Type, other.Type))
	}
	switch p.Type {
	case TypeString:
		switch {
		case p.str < other.str:
			return -1
		case p.str > other.str:
			return 1
		default:
			return 0
		}
	case TypeFloat64:
		switch {
		case p.float64 < other.float64:
			return -1
		case p.float64 > other.float64:
			return 1
		default:
			return 0
		}
	case TypeInt64:
		switch {
		case p.int64 < other.int64:
			return -1
		case p.int64 > other.int64:
			return 1
		default:
			return 0
		}
	case TypeTimestamp:
		switch {
		case p.time.Before(other.time):
			return -1
		case p.time.After(other.time):
			return 1
		default:
			return 0
		}
	default:
		panic(fmt.Sprintf("Compare: type %s is not ordered", p.Type))
	}
}

// MarshalBinary encodes a Property to a deterministic byte representation.
// Format: [type_byte] [type-specific payload]
func (p Property) MarshalBinary() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(byte(p.Type))

	switch p.Type {
	case TypeString:
		writeString(&buf, p.str)
	case TypeFloat64:
		if err := binary.Write(&buf, binary.BigEndian, p.float64); err != nil {
			return nil, fmt.Errorf("marshal Float64: %w", err)
		}
	case TypeInt64:
		if err := binary.Write(&buf, binary.BigEndian, p.int64); err != nil {
			return nil, fmt.Errorf("marshal Int64: %w", err)
		}
	case TypeBool:
		if p.bool {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	case TypeTimestamp:
		if err := binary.Write(&buf, binary.BigEndian, p.time.UnixNano()); err != nil {
			return nil, fmt.Errorf("marshal Timestamp: %w", err)
		}
	case TypeVector:
		if err := binary.Write(&buf, binary.BigEndian, uint32(len(p.vector))); err != nil {
			return nil, fmt.Errorf("marshal Vector length: %w", err)
		}
		for _, f := range p.vector {
			if err := binary.Write(&buf, binary.BigEndian, f); err != nil {
				return nil, fmt.Errorf("marshal Vector element: %w", err)
			}
		}
	case TypeStringList:
		if err := binary.Write(&buf, binary.BigEndian, uint32(len(p.strList))); err != nil {
			return nil, fmt.Errorf("marshal StringList length: %w", err)
		}
		for _, s := range p.strList {
			writeString(&buf, s)
		}
	case TypeBytes:
		if err := binary.Write(&buf, binary.BigEndian, uint32(len(p.bytes))); err != nil {
			return nil, fmt.Errorf("marshal Bytes length: %w", err)
		}
		buf.Write(p.bytes)
	default:
		return nil, fmt.Errorf("marshal: unknown type %d", p.Type)
	}
	return buf.Bytes(), nil
}

// maxDeserializeAlloc is the maximum number of elements allowed in a
// deserialized slice (vector, string list, bytes). Prevents memory
// exhaustion from crafted data.
const maxDeserializeAlloc = 10_000_000

// UnmarshalProperty decodes a Property from bytes produced by MarshalBinary.
func UnmarshalProperty(data []byte) (Property, error) {
	if len(data) == 0 {
		return Property{}, fmt.Errorf("unmarshal: empty data")
	}
	r := bytes.NewReader(data[1:])
	t := PropertyType(data[0])

	switch t {
	case TypeString:
		s, err := readString(r)
		if err != nil {
			return Property{}, fmt.Errorf("unmarshal String: %w", err)
		}
		return StringProperty(s), nil
	case TypeFloat64:
		var v float64
		if err := binary.Read(r, binary.BigEndian, &v); err != nil {
			return Property{}, fmt.Errorf("unmarshal Float64: %w", err)
		}
		return Float64Property(v), nil
	case TypeInt64:
		var v int64
		if err := binary.Read(r, binary.BigEndian, &v); err != nil {
			return Property{}, fmt.Errorf("unmarshal Int64: %w", err)
		}
		return Int64Property(v), nil
	case TypeBool:
		b, err := r.ReadByte()
		if err != nil {
			return Property{}, fmt.Errorf("unmarshal Bool: %w", err)
		}
		return BoolProperty(b != 0), nil
	case TypeTimestamp:
		var nanos int64
		if err := binary.Read(r, binary.BigEndian, &nanos); err != nil {
			return Property{}, fmt.Errorf("unmarshal Timestamp: %w", err)
		}
		return TimestampProperty(time.Unix(0, nanos).UTC()), nil
	case TypeVector:
		var length uint32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return Property{}, fmt.Errorf("unmarshal Vector length: %w", err)
		}
		if length > maxDeserializeAlloc {
			return Property{}, fmt.Errorf("unmarshal Vector: length %d exceeds maximum", length)
		}
		vec := make([]float32, length)
		for i := range vec {
			if err := binary.Read(r, binary.BigEndian, &vec[i]); err != nil {
				return Property{}, fmt.Errorf("unmarshal Vector element %d: %w", i, err)
			}
		}
		return VectorProperty(vec), nil
	case TypeStringList:
		var length uint32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return Property{}, fmt.Errorf("unmarshal StringList length: %w", err)
		}
		if length > maxDeserializeAlloc {
			return Property{}, fmt.Errorf("unmarshal StringList: length %d exceeds maximum", length)
		}
		list := make([]string, length)
		for i := range list {
			s, err := readString(r)
			if err != nil {
				return Property{}, fmt.Errorf("unmarshal StringList element %d: %w", i, err)
			}
			list[i] = s
		}
		return StringListProperty(list), nil
	case TypeBytes:
		var length uint32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return Property{}, fmt.Errorf("unmarshal Bytes length: %w", err)
		}
		if length > maxDeserializeAlloc {
			return Property{}, fmt.Errorf("unmarshal Bytes: length %d exceeds maximum", length)
		}
		b := make([]byte, length)
		if length > 0 {
			if _, err := r.Read(b); err != nil {
				return Property{}, fmt.Errorf("unmarshal Bytes: %w", err)
			}
		}
		return BytesProperty(b), nil
	default:
		return Property{}, fmt.Errorf("unmarshal: unknown type %d", t)
	}
}

// Properties is a map of named properties. A key is either present with a
// value or absent. No nulls.
type Properties map[string]Property

// Clone returns a deep copy.
func (ps Properties) Clone() Properties {
	if ps == nil {
		return nil
	}
	cp := make(Properties, len(ps))
	for k, v := range ps {
		// Property values that contain slices (Vector, StringList, Bytes)
		// need deep copying. The constructors already copy, so we reconstruct.
		switch v.Type {
		case TypeVector:
			cp[k] = VectorProperty(v.vector)
		case TypeStringList:
			cp[k] = StringListProperty(v.strList)
		case TypeBytes:
			cp[k] = BytesProperty(v.bytes)
		default:
			cp[k] = v
		}
	}
	return cp
}

// MarshalBinary encodes a Properties map to a deterministic byte representation.
// Keys are sorted lexicographically for deterministic output (required for
// content-addressed storage).
func (ps Properties) MarshalBinary() ([]byte, error) {
	if len(ps) == 0 {
		return []byte{}, nil
	}

	// Sort keys for deterministic output.
	keys := sortedKeys(ps)

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, uint32(len(ps))); err != nil {
		return nil, fmt.Errorf("marshal Properties count: %w", err)
	}

	for _, k := range keys {
		writeString(&buf, k)
		propBytes, err := ps[k].MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("marshal property %q: %w", k, err)
		}
		if err := binary.Write(&buf, binary.BigEndian, uint32(len(propBytes))); err != nil {
			return nil, fmt.Errorf("marshal property %q length: %w", k, err)
		}
		buf.Write(propBytes)
	}
	return buf.Bytes(), nil
}

// UnmarshalProperties decodes a Properties map from bytes.
func UnmarshalProperties(data []byte) (Properties, error) {
	if len(data) == 0 {
		return Properties{}, nil
	}

	r := bytes.NewReader(data)
	var count uint32
	if err := binary.Read(r, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("unmarshal Properties count: %w", err)
	}

	ps := make(Properties, count)
	for i := uint32(0); i < count; i++ {
		key, err := readString(r)
		if err != nil {
			return nil, fmt.Errorf("unmarshal property key %d: %w", i, err)
		}

		var propLen uint32
		if err := binary.Read(r, binary.BigEndian, &propLen); err != nil {
			return nil, fmt.Errorf("unmarshal property %q length: %w", key, err)
		}
		propBytes := make([]byte, propLen)
		if _, err := r.Read(propBytes); err != nil {
			return nil, fmt.Errorf("unmarshal property %q data: %w", key, err)
		}

		prop, err := UnmarshalProperty(propBytes)
		if err != nil {
			return nil, fmt.Errorf("unmarshal property %q: %w", key, err)
		}
		ps[key] = prop
	}
	return ps, nil
}

// Helper: write a length-prefixed string.
func writeString(buf *bytes.Buffer, s string) {
	_ = binary.Write(buf, binary.BigEndian, uint32(len(s)))
	buf.WriteString(s)
}

// Helper: read a length-prefixed string.
func readString(r *bytes.Reader) (string, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	if length > maxDeserializeAlloc {
		return "", fmt.Errorf("string length %d exceeds maximum", length)
	}
	b := make([]byte, length)
	if _, err := r.Read(b); err != nil {
		return "", err
	}
	return string(b), nil
}

// Helper: sorted keys for deterministic serialization.
func sortedKeys(ps Properties) []string {
	keys := make([]string, 0, len(ps))
	for k := range ps {
		keys = append(keys, k)
	}
	// Simple insertion sort -- property maps are small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// FormatValue returns a human-readable string representation of the property value.
func (p Property) FormatValue() string {
	switch p.Type {
	case TypeString:
		return p.str
	case TypeFloat64:
		return fmt.Sprintf("%g", p.float64)
	case TypeInt64:
		return fmt.Sprintf("%d", p.int64)
	case TypeBool:
		return fmt.Sprintf("%t", p.bool)
	case TypeTimestamp:
		return p.time.Format(time.RFC3339Nano)
	case TypeVector:
		return fmt.Sprintf("Vector[%d]", len(p.vector))
	case TypeStringList:
		return fmt.Sprintf("%v", p.strList)
	case TypeBytes:
		return fmt.Sprintf("Bytes[%d]", len(p.bytes))
	default:
		return fmt.Sprintf("Unknown(%d)", p.Type)
	}
}

