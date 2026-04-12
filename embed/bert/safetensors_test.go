package bert

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// buildTestSafeTensors creates a minimal safetensors file in-memory.
// Contains two tensors:
//   - "test_vec": F32 [3] with values [1.0, 2.0, 3.0]
//   - "test_mat": F32 [2, 3] with values [4.0, 5.0, 6.0, 7.0, 8.0, 9.0]
func buildTestSafeTensors(t *testing.T) string {
	t.Helper()

	// Tensor data: 9 float32 values = 36 bytes.
	// test_vec occupies [0, 12), test_mat occupies [12, 36).
	header := `{"test_vec":{"dtype":"F32","shape":[3],"data_offsets":[0,12]},"test_mat":{"dtype":"F32","shape":[2,3],"data_offsets":[12,36]}}`

	headerBytes := []byte(header)
	dataBytes := make([]byte, 36)
	vals := []float32{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0}
	for i, v := range vals {
		binary.LittleEndian.PutUint32(dataBytes[i*4:], math.Float32bits(v))
	}

	// Write file: [8-byte header_len] [header JSON] [data]
	path := filepath.Join(t.TempDir(), "test.safetensors")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var headerLen [8]byte
	binary.LittleEndian.PutUint64(headerLen[:], uint64(len(headerBytes)))
	f.Write(headerLen[:])
	f.Write(headerBytes)
	f.Write(dataBytes)

	return path
}

func TestSafeTensorsOpen(t *testing.T) {
	path := buildTestSafeTensors(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if !st.Has("test_vec") {
		t.Error("missing test_vec")
	}
	if !st.Has("test_mat") {
		t.Error("missing test_mat")
	}
	if st.Has("nonexistent") {
		t.Error("found nonexistent tensor")
	}
}

func TestSafeTensorsGetFloat32Vec(t *testing.T) {
	path := buildTestSafeTensors(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	data, shape, err := st.GetFloat32("test_vec")
	if err != nil {
		t.Fatal(err)
	}
	if len(shape) != 1 || shape[0] != 3 {
		t.Fatalf("test_vec shape: got %v, want [3]", shape)
	}
	want := []float32{1.0, 2.0, 3.0}
	for i, v := range data {
		if v != want[i] {
			t.Errorf("test_vec[%d]: got %v, want %v", i, v, want[i])
		}
	}
}

func TestSafeTensorsGetFloat32Mat(t *testing.T) {
	path := buildTestSafeTensors(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	data, shape, err := st.GetFloat32("test_mat")
	if err != nil {
		t.Fatal(err)
	}
	if len(shape) != 2 || shape[0] != 2 || shape[1] != 3 {
		t.Fatalf("test_mat shape: got %v, want [2, 3]", shape)
	}
	want := []float32{4.0, 5.0, 6.0, 7.0, 8.0, 9.0}
	for i, v := range data {
		if v != want[i] {
			t.Errorf("test_mat[%d]: got %v, want %v", i, v, want[i])
		}
	}
}

func TestSafeTensorsNotFound(t *testing.T) {
	path := buildTestSafeTensors(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, _, err = st.GetFloat32("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent tensor")
	}
}

func TestSafeTensorsWrongDType(t *testing.T) {
	// Create a file with an F16 tensor.
	header := `{"half":{"dtype":"F16","shape":[4],"data_offsets":[0,8]}}`
	headerBytes := []byte(header)
	dataBytes := make([]byte, 8)

	path := filepath.Join(t.TempDir(), "f16.safetensors")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	var headerLen [8]byte
	binary.LittleEndian.PutUint64(headerLen[:], uint64(len(headerBytes)))
	f.Write(headerLen[:])
	f.Write(headerBytes)
	f.Write(dataBytes)
	f.Close()

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, _, err = st.GetFloat32("half")
	if err == nil {
		t.Error("expected error for F16 tensor accessed as F32")
	}
}

func TestSafeTensorsNames(t *testing.T) {
	path := buildTestSafeTensors(t)

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	names := st.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 tensors, got %d", len(names))
	}
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	if !nameSet["test_vec"] || !nameSet["test_mat"] {
		t.Errorf("unexpected names: %v", names)
	}
}

func TestSafeTensorsMetadata(t *testing.T) {
	// File with __metadata__ key (should be skipped, not parsed as tensor).
	header := `{"__metadata__":{"format":"pt"},"w":{"dtype":"F32","shape":[2],"data_offsets":[0,8]}}`
	headerBytes := []byte(header)
	dataBytes := make([]byte, 8)
	binary.LittleEndian.PutUint32(dataBytes[0:], math.Float32bits(1.0))
	binary.LittleEndian.PutUint32(dataBytes[4:], math.Float32bits(2.0))

	path := filepath.Join(t.TempDir(), "meta.safetensors")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	var headerLen [8]byte
	binary.LittleEndian.PutUint64(headerLen[:], uint64(len(headerBytes)))
	f.Write(headerLen[:])
	f.Write(headerBytes)
	f.Write(dataBytes)
	f.Close()

	st, err := OpenSafeTensors(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if st.Has("__metadata__") {
		t.Error("__metadata__ should not be a tensor")
	}
	data, _, err := st.GetFloat32("w")
	if err != nil {
		t.Fatal(err)
	}
	if data[0] != 1.0 || data[1] != 2.0 {
		t.Errorf("w: got %v, want [1.0, 2.0]", data)
	}
}

func TestSafeTensorsFileTooSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.safetensors")
	os.WriteFile(path, []byte{1, 2, 3}, 0600)

	_, err := OpenSafeTensors(path)
	if err == nil {
		t.Error("expected error for tiny file")
	}
}
