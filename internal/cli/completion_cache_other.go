//go:build !unix

package cli

import "os"

// On non-unix platforms (Windows) ownership and POSIX modes aren't meaningful;
// the cache lives in the per-user profile directory. Still refuse symlinks and
// non-regular files.
func cacheFileTrusted(fi os.FileInfo) bool { return fi.Mode().IsRegular() }

func cacheDirSafe(dir string) bool {
	fi, err := os.Lstat(dir)
	return err == nil && fi.IsDir()
}

func prepareCacheDir(dir string) bool {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		_ = os.Mkdir(dir, 0o700)
	}
	return cacheDirSafe(dir)
}
