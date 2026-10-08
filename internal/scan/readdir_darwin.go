package scan

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"
)

// On macOS a directory is read with getattrlistbulk(2), which returns the
// name, type and allocated size of a whole batch of entries in one call. The
// portable path needs a readdir plus one lstat per file, and on a home
// directory of a few million files those lstat calls are most of the scan.
//
// x/sys/unix has no wrapper for it, and syscall.Syscall6 on darwin issues a raw
// SVC that Apple does not support, so it is called through libSystem the same
// way x/sys/unix calls everything else: a dynamic import, an assembly
// trampoline (readdir_darwin.s) and the runtime's syscall6.

var libc_getattrlistbulk_trampoline_addr uintptr

//go:cgo_import_dynamic libc_getattrlistbulk getattrlistbulk "/usr/lib/libSystem.B.dylib"

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

const (
	attrBitMapCount = 5

	attrCmnName          = 0x00000001
	attrCmnDevID         = 0x00000002
	attrCmnObjType       = 0x00000008
	attrCmnFileID        = 0x02000000
	attrCmnError         = 0x20000000
	attrCmnReturnedAttrs = 0x80000000

	attrFileLinkCount = 0x00000001
	attrFileAllocSize = 0x00000004

	// fsoptPackInvalAttrs keeps an invalid common attribute in the record
	// instead of leaving it out, so the common block sits at fixed offsets.
	// It does not extend to file attributes, which a directory's record omits.
	fsoptPackInvalAttrs = 0x00000008

	vdir = 2
	vlnk = 5
)

// Offsets into one record, as the kernel actually lays it out: the returned
// set first, then ATTR_CMN_ERROR, then the remaining common attributes in bit
// order, then the file attributes, each 4-byte aligned. The error coming
// second is not what bit order would suggest, but matches Apple's own example.
const (
	offReturned  = 4  // attribute_set_t: common, vol, dir, file, fork
	offError     = 24 // uint32
	offName      = 28 // attrreference_t: int32 offset from here, uint32 length
	offDevID     = 36 // dev_t
	offObjType   = 40 // fsobj_type_t
	offFileID    = 44 // uint64
	offLinkCount = 52 // uint32
	offAllocSize = 56 // off_t
	commonEnd    = 52
	fileEnd      = 64
)

type attrList struct {
	bitmapCount uint16
	reserved    uint16
	commonAttr  uint32
	volAttr     uint32
	dirAttr     uint32
	fileAttr    uint32
	forkAttr    uint32
}

var bulkAttrs = attrList{
	bitmapCount: attrBitMapCount,
	commonAttr: attrCmnReturnedAttrs | attrCmnName | attrCmnDevID |
		attrCmnObjType | attrCmnFileID | attrCmnError,
	fileAttr: attrFileLinkCount | attrFileAllocSize,
}

// bulkBufSize holds a few thousand records per call; every walker goroutine
// owns one, so it is kept well short of anything that would matter in memory.
const bulkBufSize = 256 << 10

func readDir(path string, buf *[]byte, fn func(e *entry)) error {
	fd, err := openDir(path)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)

	if *buf == nil {
		*buf = make([]byte, bulkBufSize)
	}
	b := *buf
	al := bulkAttrs

	var e entry
	for {
		n, _, errno := syscall_syscall6(libc_getattrlistbulk_trampoline_addr,
			uintptr(fd), uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&b[0])),
			uintptr(len(b)), fsoptPackInvalAttrs, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			if errno == syscall.ENOTSUP || errno == syscall.EINVAL {
				// A filesystem without bulk attribute support; read it the
				// slow way rather than reporting it unreadable.
				return readDirPortable(path, fn)
			}
			return &os.PathError{Op: "getattrlistbulk", Path: path, Err: errno}
		}
		if n == 0 {
			return nil
		}

		rec := b
		for range int(n) {
			length := binary.LittleEndian.Uint32(rec)
			if length < commonEnd || int(length) > len(rec) {
				return &os.PathError{Op: "getattrlistbulk", Path: path, Err: syscall.EIO}
			}
			r := rec[:length]
			rec = rec[length:]

			e = entry{}
			returned := binary.LittleEndian.Uint32(r[offReturned:])
			if returned&attrCmnError != 0 && binary.LittleEndian.Uint32(r[offError:]) != 0 {
				e.failed = true
				fn(&e)
				continue
			}
			if returned&attrCmnName == 0 {
				continue
			}
			nameOff := offName + int(int32(binary.LittleEndian.Uint32(r[offName:])))
			nameLen := int(binary.LittleEndian.Uint32(r[offName+4:]))
			if nameLen == 0 || nameOff < 0 || nameOff+nameLen > len(r) {
				continue
			}
			e.name = r[nameOff : nameOff+nameLen-1] // drop the NUL

			switch binary.LittleEndian.Uint32(r[offObjType:]) {
			case vdir:
				e.dir = true
			case vlnk:
				e.symlink = true
			}
			fileAttrs := binary.LittleEndian.Uint32(r[offReturned+12:])
			if !e.dir && fileAttrs&attrFileAllocSize != 0 && len(r) >= fileEnd {
				e.usage = int64(binary.LittleEndian.Uint64(r[offAllocSize:]))
				e.nlink = uint64(binary.LittleEndian.Uint32(r[offLinkCount:]))
				e.ino = binary.LittleEndian.Uint64(r[offFileID:])
				e.dev = uint64(binary.LittleEndian.Uint32(r[offDevID:]))
			}
			fn(&e)
		}
	}
}

// openDir retries EINTR, as os.Open does. Opening a directory inside another
// app's container can block on the system's data protection check and come
// back interrupted; giving up there would drop a readable directory from the
// snapshot, and a different one on every run.
func openDir(path string) (int, error) {
	for {
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}
