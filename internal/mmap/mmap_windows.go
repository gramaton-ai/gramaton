//go:build windows

package mmap

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Region is a read-only view of a file backed by a Windows
// file-mapping object.
type Region struct {
	data   []byte
	handle windows.Handle
	addr   uintptr
}

// Open maps the first size bytes of f read-only via CreateFileMapping +
// MapViewOfFile. Size must be positive; empty files are not supported
// (CreateFileMapping rejects a zero maximum size unless the underlying
// file has a real size, but our callers always pass a positive size and
// this API treats 0 as an error for cross-platform parity with Unix).
func Open(f *os.File, size int) (*Region, error) {
	if size < 0 {
		return nil, fmt.Errorf("mmap: negative size %d", size)
	}
	if size == 0 {
		return nil, fmt.Errorf("mmap: cannot map empty file")
	}

	fileHandle := windows.Handle(f.Fd())
	sizeHigh := uint32(uint64(size) >> 32)
	sizeLow := uint32(uint64(size) & 0xFFFFFFFF)

	mapHandle, err := windows.CreateFileMapping(fileHandle, nil, windows.PAGE_READONLY, sizeHigh, sizeLow, nil)
	if err != nil {
		return nil, fmt.Errorf("mmap: CreateFileMapping: %w", err)
	}
	if mapHandle == 0 {
		return nil, fmt.Errorf("mmap: CreateFileMapping returned NULL handle")
	}

	addr, err := windows.MapViewOfFile(mapHandle, windows.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		_ = windows.CloseHandle(mapHandle)
		return nil, fmt.Errorf("mmap: MapViewOfFile: %w", err)
	}

	// Create a []byte aliasing the mapped view. This is safe because
	// the memory is backed by pages owned by the mapping object; it
	// stays valid until UnmapViewOfFile + CloseHandle in Close.
	data := unsafe.Slice((*byte)(unsafe.Pointer(addr)), size)

	return &Region{
		data:   data,
		handle: mapHandle,
		addr:   addr,
	}, nil
}

// Bytes returns the mapped region. The slice is valid until Close.
func (r *Region) Bytes() []byte {
	if r == nil {
		return nil
	}
	return r.data
}

// Close unmaps the view and closes the file-mapping handle. Safe to
// call multiple times; subsequent calls are no-ops.
func (r *Region) Close() error {
	if r == nil || r.data == nil {
		return nil
	}
	addr := r.addr
	handle := r.handle
	r.data = nil
	r.addr = 0
	r.handle = 0

	if err := windows.UnmapViewOfFile(addr); err != nil {
		// Still close the handle even if unmap failed — leaking the
		// mapping handle is worse than returning the unmap error.
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("mmap: UnmapViewOfFile: %w", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		return fmt.Errorf("mmap: CloseHandle: %w", err)
	}
	return nil
}
