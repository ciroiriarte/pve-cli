//go:build !unix

package cli

import "os"

// On non-unix platforms (Windows) ownership and POSIX modes aren't meaningful;
// the cache lives in the per-user profile directory. Still refuse symlinks and
// non-regular files.
func cacheFileTrusted(fi os.FileInfo) bool { return fi.Mode().IsRegular() }

func cacheDirUsable(dir string) bool {
	fi, err := os.Lstat(dir)
	return err == nil && fi.IsDir()
}
