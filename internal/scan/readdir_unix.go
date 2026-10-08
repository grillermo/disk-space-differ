//go:build unix

package scan

import (
	"os"
	"path/filepath"
	"syscall"
)

// readDirPortable reads a directory with readdir plus one lstat per
// non-directory. It is the only reader off macOS, and macOS falls back to it
// for filesystems without bulk attribute support.
func readDirPortable(path string, fn func(e *entry)) error {
	dirents, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	var e entry
	for _, d := range dirents {
		e = entry{name: []byte(d.Name())}
		if d.IsDir() {
			e.dir = true
			fn(&e)
			continue
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(filepath.Join(path, d.Name()), &st); err != nil {
			e.failed = true
			fn(&e)
			continue
		}
		e.symlink = d.Type()&os.ModeSymlink != 0
		e.usage = int64(st.Blocks) * 512
		e.nlink = uint64(st.Nlink)
		e.ino = uint64(st.Ino)
		e.dev = uint64(st.Dev)
		fn(&e)
	}
	return nil
}
