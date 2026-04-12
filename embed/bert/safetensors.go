package bert

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// SafeTensors provides zero-copy access to tensors stored in the
// HuggingFace safetensors format. The file is mmap'd read-only;
// float32 tensor data is accessed directly without copying.
//
// Format: [8-byte header_len (uint64 LE)] [JSON header] [tensor data]
type SafeTensors struct {
	meta       map[string]tensorMeta
	data       []byte // mmap'd region (entire file)
	file       *os.File
	dataOffset int // byte offset where tensor data begins
}

type tensorMeta struct {
	DType   string `json:"dtype"`
	Shape   []int  `json:"shape"`
	Offsets [2]int `json:"data_offsets"` // [begin, end) relative to data region
}

const maxHeaderSize = 100 * 1024 * 1024 // 100 MB (DoS prevention)

// OpenSafeTensors opens a safetensors file via mmap for zero-copy access.
func OpenSafeTensors(path string) (*SafeTensors, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("safetensors: open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("safetensors: stat: %w", err)
	}
	size := info.Size()
	if size < 8 {
		f.Close()
		return nil, fmt.Errorf("safetensors: file too small (%d bytes)", size)
	}

	// Mmap the entire file read-only.
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("safetensors: mmap: %w", err)
	}

	// Parse header length.
	headerLen := binary.LittleEndian.Uint64(data[:8])
	if headerLen > maxHeaderSize {
		syscall.Munmap(data)
		f.Close()
		return nil, fmt.Errorf("safetensors: header too large (%d bytes, max %d)", headerLen, maxHeaderSize)
	}
	dataOffset := 8 + int(headerLen)
	if dataOffset > int(size) {
		syscall.Munmap(data)
		f.Close()
		return nil, fmt.Errorf("safetensors: header extends past end of file")
	}

	// Parse JSON header. The header maps tensor names to metadata.
	// The special key "__metadata__" holds arbitrary string KV pairs.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data[8:dataOffset], &raw); err != nil {
		syscall.Munmap(data)
		f.Close()
		return nil, fmt.Errorf("safetensors: parse header: %w", err)
	}

	meta := make(map[string]tensorMeta, len(raw))
	for name, msg := range raw {
		if name == "__metadata__" {
			continue
		}
		var m tensorMeta
		if err := json.Unmarshal(msg, &m); err != nil {
			syscall.Munmap(data)
			f.Close()
			return nil, fmt.Errorf("safetensors: parse tensor %q: %w", name, err)
		}
		meta[name] = m
	}

	return &SafeTensors{
		meta:       meta,
		data:       data,
		file:       f,
		dataOffset: dataOffset,
	}, nil
}

// GetFloat32 returns a float32 slice backed by the mmap'd data for the
// named tensor. The returned slice is valid until Close is called.
// The tensor must have dtype "F32".
func (st *SafeTensors) GetFloat32(name string) ([]float32, []int, error) {
	m, ok := st.meta[name]
	if !ok {
		return nil, nil, fmt.Errorf("safetensors: tensor %q not found", name)
	}
	if m.DType != "F32" {
		return nil, nil, fmt.Errorf("safetensors: tensor %q has dtype %s, want F32", name, m.DType)
	}

	begin := st.dataOffset + m.Offsets[0]
	end := st.dataOffset + m.Offsets[1]
	if begin < st.dataOffset || end > len(st.data) || begin > end {
		return nil, nil, fmt.Errorf("safetensors: tensor %q has invalid offsets [%d, %d)", name, m.Offsets[0], m.Offsets[1])
	}

	byteSlice := st.data[begin:end]
	if len(byteSlice)%4 != 0 {
		return nil, nil, fmt.Errorf("safetensors: tensor %q byte length %d not divisible by 4", name, len(byteSlice))
	}

	// Reinterpret bytes as float32 without copying.
	n := len(byteSlice) / 4
	floats := unsafe.Slice((*float32)(unsafe.Pointer(&byteSlice[0])), n)
	return floats, m.Shape, nil
}

// Has reports whether the named tensor exists.
func (st *SafeTensors) Has(name string) bool {
	_, ok := st.meta[name]
	return ok
}

// Names returns all tensor names (excluding __metadata__).
func (st *SafeTensors) Names() []string {
	names := make([]string, 0, len(st.meta))
	for name := range st.meta {
		names = append(names, name)
	}
	return names
}

// Close unmaps the file and releases resources.
func (st *SafeTensors) Close() error {
	var err error
	if st.data != nil {
		err = syscall.Munmap(st.data)
		st.data = nil
	}
	if st.file != nil {
		if e := st.file.Close(); err == nil {
			err = e
		}
		st.file = nil
	}
	return err
}
