//go:build unix

package cli

import (
	"os"
	"syscall"
)

func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// cacheFileTrusted accepts only a regular file (not a symlink — fi comes from
// Lstat) owned by the current user with mode 0600.
func cacheFileTrusted(fi os.FileInfo) bool {
	return fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 && ownedByMe(fi)
}

// cacheDirUsable reports whether dir is a real directory (not a symlink) owned
// by the current user, tightening it to 0700 if needed.
func cacheDirUsable(dir string) bool {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || !ownedByMe(fi) {
		return false
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700) == nil
	}
	return true
}
