package graph

import (
	"testing"
	"time"
)

// --- Constructors and Accessors ---

func TestStringProperty(t *testing.T) {
	p := StringProperty("hello")
	if p.Type != TypeString {
		t.Fatalf("expected TypeString, got %s", p.Type)
	}
	if p.String() != "hello" {
		t.Fatalf("expected %q, got %q", "hello", p.String())
	}
}

func TestStringPropertyEmpty(t *testing.T) {
	p := StringProperty("")
	if p.String() != "" {
		t.Fatalf("expected empty string, got %q", p.String())
	}
}

func TestFloat64Property(t *testing.T) {
	p := Float64Property(3.14)
	if p.Type != TypeFloat64 {
		t.Fatalf("expected TypeFloat64, got %s", p.Type)
	}
	if p.Float64() != 3.14 {
		t.Fatalf("expected 3.14, got %f", p.Float64())
	}
}

func TestFloat64PropertyZero(t *testing.T) {
	p := Float64Property(0.0)
	if p.Float64() != 0.0 {
		t.Fatalf("expected 0.0, got %f", p.Float64())
	}
}

func TestFloat64PropertyNegative(t *testing.T) {
	p := Float64Property(-1.5)
	if p.Float64() != -1.5 {
		t.Fatalf("expected -1.5, got %f", p.Float64())
	}
}

func TestInt64Property(t *testing.T) {
	p := Int64Property(42)
	if p.Type != TypeInt64 {
		t.Fatalf("expected TypeInt64, got %s", p.Type)
	}
	if p.Int64() != 42 {
		t.Fatalf("expected 42, got %d", p.Int64())
	}
}

func TestInt64PropertyNegative(t *testing.T) {
	p := Int64Property(-100)
	if p.Int64() != -100 {
		t.Fatalf("expected -100, got %d", p.Int64())
	}
}

func TestInt64PropertyMinMax(t *testing.T) {
	pMin := Int64Property(-9223372036854775808)
	pMax := Int64Property(9223372036854775807)
	if pMin.Int64() != -9223372036854775808 {
		t.Fatalf("expected min int64, got %d", pMin.Int64())
	}
	if pMax.Int64() != 9223372036854775807 {
		t.Fatalf("expected max int64, got %d", pMax.Int64())
	}
}

func TestBoolProperty(t *testing.T) {
	pTrue := BoolProperty(true)
	pFalse := BoolProperty(false)
	if pTrue.Type != TypeBool {
		t.Fatalf("expected TypeBool, got %s", pTrue.Type)
	}
	if !pTrue.Bool() {
		t.Fatal("expected true")
	}
	if pFalse.Bool() {
		t.Fatal("expected false")
	}
}

func TestTimestampProperty(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 30, 0, 0, time.UTC)
	p := TimestampProperty(ts)
	if p.Type != TypeTimestamp {
		t.Fatalf("expected TypeTimestamp, got %s", p.Type)
	}
	if !p.Timestamp().Equal(ts) {
		t.Fatalf("expected %v, got %v", ts, p.Timestamp())
	}
}

func TestTimestampPropertyNormalizesToUTC(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	ts := time.Date(2026, 4, 3, 10, 30, 0, 0, loc)
	p := TimestampProperty(ts)
	if p.Timestamp().Location() != time.UTC {
		t.Fatalf("expected UTC, got %s", p.Timestamp().Location())
	}
	if !p.Timestamp().Equal(ts) {
		t.Fatal("timestamp value changed during UTC normalization")
	}
}

func TestVectorProperty(t *testing.T) {
	v := []float32{0.1, 0.2, 0.3}
	p := VectorProperty(v)
	if p.Type != TypeVector {
		t.Fatalf("expected TypeVector, got %s", p.Type)
	}
	got := p.Vector()
	if len(got) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(got))
	}
	for i, want := range v {
		if got[i] != want {
			t.Fatalf("element %d: expected %f, got %f", i, want, got[i])
		}
	}
}

func TestVectorPropertyDefensiveCopy(t *testing.T) {
	v := []float32{1.0, 2.0, 3.0}
	p := VectorProperty(v)
	v[0] = 999.0 // mutate original
	if p.Vector()[0] == 999.0 {
		t.Fatal("constructor did not copy: mutation of original affected property")
	}
	got := p.Vector()
	got[0] = 888.0 // mutate returned slice
	if p.Vector()[0] == 888.0 {
		t.Fatal("accessor did not copy: mutation of returned slice affected property")
	}
}

func TestVectorPropertyEmpty(t *testing.T) {
	p := VectorProperty([]float32{})
	if len(p.Vector()) != 0 {
		t.Fatalf("expected empty vector, got %d elements", len(p.Vector()))
	}
}

func TestStringListProperty(t *testing.T) {
	v := []string{"kafka", "rabbitmq", "pulsar"}
	p := StringListProperty(v)
	if p.Type != TypeStringList {
		t.Fatalf("expected TypeStringList, got %s", p.Type)
	}
	got := p.StringList()
	if len(got) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(got))
	}
	for i, want := range v {
		if got[i] != want {
			t.Fatalf("element %d: expected %q, got %q", i, want, got[i])
		}
	}
}

func TestStringListPropertyDefensiveCopy(t *testing.T) {
	v := []string{"a", "b"}
	p := StringListProperty(v)
	v[0] = "mutated"
	if p.StringList()[0] == "mutated" {
		t.Fatal("constructor did not copy")
	}
	got := p.StringList()
	got[0] = "mutated"
	if p.StringList()[0] == "mutated" {
		t.Fatal("accessor did not copy")
	}
}

func TestBytesProperty(t *testing.T) {
	v := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	p := BytesProperty(v)
	if p.Type != TypeBytes {
		t.Fatalf("expected TypeBytes, got %s", p.Type)
	}
	got := p.Bytes()
	if len(got) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(got))
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("byte %d: expected %x, got %x", i, v[i], got[i])
		}
	}
}

func TestBytesPropertyDefensiveCopy(t *testing.T) {
	v := []byte{1, 2, 3}
	p := BytesProperty(v)
	v[0] = 99
	if p.Bytes()[0] == 99 {
		t.Fatal("constructor did not copy")
	}
	got := p.Bytes()
	got[0] = 88
	if p.Bytes()[0] == 88 {
		t.Fatal("accessor did not copy")
	}
}

// --- Wrong-type accessor panics ---

func TestAccessorPanics(t *testing.T) {
	cases := []struct {
		name string
		prop Property
		fn   func(Property)
	}{
		{"String on Int64", Int64Property(1), func(p Property) { p.String() }},
		{"Float64 on String", StringProperty("x"), func(p Property) { p.Float64() }},
		{"Int64 on Bool", BoolProperty(true), func(p Property) { p.Int64() }},
		{"Bool on Float64", Float64Property(1.0), func(p Property) { p.Bool() }},
		{"Timestamp on String", StringProperty("x"), func(p Property) { p.Timestamp() }},
		{"Vector on Int64", Int64Property(1), func(p Property) { p.Vector() }},
		{"StringList on Bool", BoolProperty(true), func(p Property) { p.StringList() }},
		{"Bytes on String", StringProperty("x"), func(p Property) { p.Bytes() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic, got none")
				}
			}()
			tc.fn(tc.prop)
		})
	}
}

// --- PropertyType.String() ---

func TestPropertyTypeString(t *testing.T) {
	cases := []struct {
		t    PropertyType
		want string
	}{
		{TypeString, "String"},
		{TypeFloat64, "Float64"},
		{TypeInt64, "Int64"},
		{TypeBool, "Bool"},
		{TypeTimestamp, "Timestamp"},
		{TypeVector, "Vector"},
		{TypeStringList, "StringList"},
		{TypeBytes, "Bytes"},
		{PropertyType(99), "Unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.t.String(); got != tc.want {
			t.Errorf("PropertyType(%d).String() = %q, want %q", tc.t, got, tc.want)
		}
	}
}

// --- Equal ---

func TestEqualSameType(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		a, b Property
		want bool
	}{
		{"string equal", StringProperty("x"), StringProperty("x"), true},
		{"string not equal", StringProperty("x"), StringProperty("y"), false},
		{"float64 equal", Float64Property(1.5), Float64Property(1.5), true},
		{"float64 not equal", Float64Property(1.5), Float64Property(2.5), false},
		{"int64 equal", Int64Property(10), Int64Property(10), true},
		{"int64 not equal", Int64Property(10), Int64Property(20), false},
		{"bool equal true", BoolProperty(true), BoolProperty(true), true},
		{"bool equal false", BoolProperty(false), BoolProperty(false), true},
		{"bool not equal", BoolProperty(true), BoolProperty(false), false},
		{"timestamp equal", TimestampProperty(ts), TimestampProperty(ts), true},
		{"timestamp not equal", TimestampProperty(ts), TimestampProperty(ts.Add(time.Second)), false},
		{"vector equal", VectorProperty([]float32{1, 2}), VectorProperty([]float32{1, 2}), true},
		{"vector not equal value", VectorProperty([]float32{1, 2}), VectorProperty([]float32{1, 3}), false},
		{"vector not equal length", VectorProperty([]float32{1}), VectorProperty([]float32{1, 2}), false},
		{"stringlist equal", StringListProperty([]string{"a", "b"}), StringListProperty([]string{"a", "b"}), true},
		{"stringlist not equal", StringListProperty([]string{"a"}), StringListProperty([]string{"b"}), false},
		{"stringlist not equal length", StringListProperty([]string{"a"}), StringListProperty([]string{"a", "b"}), false},
		{"bytes equal", BytesProperty([]byte{1, 2}), BytesProperty([]byte{1, 2}), true},
		{"bytes not equal", BytesProperty([]byte{1}), BytesProperty([]byte{2}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Errorf("Equal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEqualDifferentTypes(t *testing.T) {
	if StringProperty("1").Equal(Int64Property(1)) {
		t.Fatal("different types should not be equal")
	}
}

// --- Compare ---

func TestCompareOrdered(t *testing.T) {
	ts1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		a, b Property
		want int
	}{
		{"string less", StringProperty("a"), StringProperty("b"), -1},
		{"string equal", StringProperty("a"), StringProperty("a"), 0},
		{"string greater", StringProperty("b"), StringProperty("a"), 1},
		{"float64 less", Float64Property(1.0), Float64Property(2.0), -1},
		{"float64 equal", Float64Property(1.0), Float64Property(1.0), 0},
		{"float64 greater", Float64Property(2.0), Float64Property(1.0), 1},
		{"int64 less", Int64Property(-1), Int64Property(1), -1},
		{"int64 equal", Int64Property(0), Int64Property(0), 0},
		{"int64 greater", Int64Property(1), Int64Property(-1), 1},
		{"timestamp less", TimestampProperty(ts1), TimestampProperty(ts2), -1},
		{"timestamp equal", TimestampProperty(ts1), TimestampProperty(ts1), 0},
		{"timestamp greater", TimestampProperty(ts2), TimestampProperty(ts1), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Errorf("Compare() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCompareUnorderedPanics(t *testing.T) {
	cases := []struct {
		name string
		prop Property
	}{
		{"bool", BoolProperty(true)},
		{"vector", VectorProperty([]float32{1})},
		{"stringlist", StringListProperty([]string{"a"})},
		{"bytes", BytesProperty([]byte{1})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic for unordered type")
				}
			}()
			tc.prop.Compare(tc.prop)
		})
	}
}

func TestCompareMismatchedTypesPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for mismatched types")
		}
	}()
	StringProperty("a").Compare(Int64Property(1))
}

// --- Serialization round-trips ---

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 3, 14, 30, 0, 123456789, time.UTC)
	cases := []struct {
		name string
		prop Property
	}{
		{"string", StringProperty("hello world")},
		{"string empty", StringProperty("")},
		{"string unicode", StringProperty("\u00e9\u00e0\u00fc \xe4\xb8\xad\xe6\x96\x87")},
		{"float64", Float64Property(3.14159)},
		{"float64 zero", Float64Property(0.0)},
		{"float64 negative", Float64Property(-273.15)},
		{"int64", Int64Property(42)},
		{"int64 zero", Int64Property(0)},
		{"int64 min", Int64Property(-9223372036854775808)},
		{"int64 max", Int64Property(9223372036854775807)},
		{"bool true", BoolProperty(true)},
		{"bool false", BoolProperty(false)},
		{"timestamp", TimestampProperty(ts)},
		{"vector", VectorProperty([]float32{0.1, -0.2, 0.3})},
		{"vector empty", VectorProperty([]float32{})},
		{"stringlist", StringListProperty([]string{"kafka", "rabbitmq"})},
		{"stringlist empty", StringListProperty([]string{})},
		{"stringlist with empty strings", StringListProperty([]string{"", "a", ""})},
		{"bytes", BytesProperty([]byte{0x00, 0xFF, 0x42})},
		{"bytes empty", BytesProperty([]byte{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.prop.MarshalBinary()
			if err != nil {
				t.Fatalf("MarshalBinary: %v", err)
			}
			got, err := UnmarshalProperty(data)
			if err != nil {
				t.Fatalf("UnmarshalProperty: %v", err)
			}
			if !got.Equal(tc.prop) {
				t.Fatalf("round-trip failed: got %v, want %v", got.FormatValue(), tc.prop.FormatValue())
			}
		})
	}
}

func TestMarshalDeterministic(t *testing.T) {
	p := StringProperty("deterministic")
	d1, _ := p.MarshalBinary()
	d2, _ := p.MarshalBinary()
	if len(d1) != len(d2) {
		t.Fatal("non-deterministic marshal: different lengths")
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("non-deterministic marshal: differ at byte %d", i)
		}
	}
}

func TestUnmarshalEmptyData(t *testing.T) {
	_, err := UnmarshalProperty([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestUnmarshalUnknownType(t *testing.T) {
	_, err := UnmarshalProperty([]byte{99})
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

// --- Properties map ---

func TestPropertiesClone(t *testing.T) {
	ps := Properties{
		"name":     StringProperty("test"),
		"count":    Int64Property(5),
		"vec":      VectorProperty([]float32{1, 2, 3}),
		"tags":     StringListProperty([]string{"a", "b"}),
		"raw":      BytesProperty([]byte{1, 2}),
		"score":    Float64Property(0.9),
		"active":   BoolProperty(true),
		"created":  TimestampProperty(time.Now().UTC()),
	}

	cp := ps.Clone()

	// Same values
	for k, v := range ps {
		if !cp[k].Equal(v) {
			t.Fatalf("clone mismatch for key %q", k)
		}
	}

	// Mutation isolation: mutate clone, original unaffected
	cp["name"] = StringProperty("mutated")
	if ps["name"].String() == "mutated" {
		t.Fatal("clone mutation affected original")
	}

	// Delete from clone
	delete(cp, "count")
	if _, ok := ps["count"]; !ok {
		t.Fatal("delete from clone affected original")
	}
}

func TestPropertiesCloneNil(t *testing.T) {
	var ps Properties
	cp := ps.Clone()
	if cp != nil {
		t.Fatal("clone of nil should be nil")
	}
}

func TestPropertiesMarshalRoundTrip(t *testing.T) {
	ts := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	ps := Properties{
		"content":     StringProperty("We chose Kafka"),
		"confidence":  Float64Property(0.9),
		"access_count": Int64Property(7),
		"active":      BoolProperty(true),
		"created_at":  TimestampProperty(ts),
		"embedding":   VectorProperty([]float32{0.1, 0.2, 0.3}),
		"keywords":    StringListProperty([]string{"kafka", "rabbitmq"}),
		"bloom":       BytesProperty([]byte{0xFF, 0x00}),
	}

	data, err := ps.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	got, err := UnmarshalProperties(data)
	if err != nil {
		t.Fatalf("UnmarshalProperties: %v", err)
	}

	if len(got) != len(ps) {
		t.Fatalf("expected %d properties, got %d", len(ps), len(got))
	}
	for k, v := range ps {
		if !got[k].Equal(v) {
			t.Fatalf("property %q: round-trip mismatch", k)
		}
	}
}

func TestPropertiesMarshalEmpty(t *testing.T) {
	ps := Properties{}
	data, err := ps.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	got, err := UnmarshalProperties(data)
	if err != nil {
		t.Fatalf("UnmarshalProperties: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 properties, got %d", len(got))
	}
}

func TestPropertiesMarshalDeterministic(t *testing.T) {
	ps := Properties{
		"zebra":  StringProperty("z"),
		"alpha":  StringProperty("a"),
		"middle": StringProperty("m"),
	}
	d1, _ := ps.MarshalBinary()
	d2, _ := ps.MarshalBinary()
	if len(d1) != len(d2) {
		t.Fatal("non-deterministic Properties marshal")
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("non-deterministic Properties marshal at byte %d", i)
		}
	}
}

func TestPropertiesPresenceAbsence(t *testing.T) {
	ps := Properties{
		"exists": StringProperty("yes"),
	}
	if _, ok := ps["exists"]; !ok {
		t.Fatal("expected key to be present")
	}
	if _, ok := ps["missing"]; ok {
		t.Fatal("expected key to be absent")
	}
}

// --- FormatValue ---

func TestFormatValue(t *testing.T) {
	cases := []struct {
		name string
		prop Property
		want string
	}{
		{"string", StringProperty("hello"), "hello"},
		{"float64", Float64Property(3.14), "3.14"},
		{"int64", Int64Property(42), "42"},
		{"bool true", BoolProperty(true), "true"},
		{"bool false", BoolProperty(false), "false"},
		{"vector", VectorProperty([]float32{1, 2, 3}), "Vector[3]"},
		{"stringlist", StringListProperty([]string{"a", "b"}), "[a b]"},
		{"bytes", BytesProperty([]byte{1, 2, 3}), "Bytes[3]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.prop.FormatValue(); got != tc.want {
				t.Errorf("FormatValue() = %q, want %q", got, tc.want)
			}
		})
	}
}
