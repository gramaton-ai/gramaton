// Package mmap provides a cross-platform read-only memory-mapped file.
//
// On Linux/macOS the implementation wraps syscall.Mmap/Munmap. On Windows
// it wraps CreateFileMapping + MapViewOfFile from golang.org/x/sys/windows.
// Both platforms expose the mapping as a Region whose Bytes() slice points
// at pages backed by the file.
//
// All mappings are read-only. Writes must be performed via file.WriteAt
// (or equivalent); the caller must remap after any write that extends or
// truncates the file to observe the new content.
//
// Region.Close is idempotent: a second call is a no-op. A Region whose
// Close has been called must not have Bytes() accessed afterward — on
// Unix Munmap invalidates the pages immediately (access SIGSEGVs); on
// Windows UnmapViewOfFile invalidates lazily but accessing is still
// undefined. Callers should drop slice references before Close.
package mmap
