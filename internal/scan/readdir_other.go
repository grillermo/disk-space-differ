//go:build unix && !darwin

package scan

func readDir(path string, _ *[]byte, fn func(e *entry)) error {
	return readDirPortable(path, fn)
}
