//go:build !windows

package mmap

import (
	"fmt"
	"os"
	"syscall"
)

// Region is a read-only view of a file backed by the OS page cache.
type Region struct {
	data []byte
}

// Open maps the first size bytes of f read-only. Size must be positive;
// empty files are not supported (POSIX mmap rejects length=0).
func Open(f *os.File, size int) (*Region, error) {
	if size < 0 {
		return nil, fmt.Errorf("mmap: negative size %d", size)
	}
	if size == 0 {
		return nil, fmt.Errorf("mmap: cannot map empty file")
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap: syscall.Mmap: %w", err)
	}
	return &Region{data: data}, nil
}

// Bytes returns the mapped region. The slice is valid until Close.
func (r *Region) Bytes() []byte {
	if r == nil {
		return nil
	}
	return r.data
}

// Close unmaps the region. Safe to call multiple times; subsequent
// calls are no-ops.
func (r *Region) Close() error {
	if r == nil || r.data == nil {
		return nil
	}
	data := r.data
	r.data = nil
	if err := syscall.Munmap(data); err != nil {
		return fmt.Errorf("mmap: syscall.Munmap: %w", err)
	}
	return nil
}
