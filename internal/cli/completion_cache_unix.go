//go:build unix

package cli

import (
	"os"
	"strconv"
	"syscall"
	"time"
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

// cacheDirSafe reports whether dir is a real directory (not a symlink) owned
// by the current user with mode 0700. Read-side check; never modifies.
func cacheDirSafe(dir string) bool {
	fi, err := os.Lstat(dir)
	return err == nil && fi.IsDir() && ownedByMe(fi) && fi.Mode().Perm() == 0o700
}

// prepareCacheDir makes dir a safe cache directory before a write, deciding
// from a single Lstat. A missing dir is created 0700; a dir of ours with a
// loose mode is not tightened in place but atomically replaced by a fresh,
// empty 0700 dir (rename aside, then remove). So nothing written while it
// was open — a planted file, or one of ours renamed over another key — can
// ever be read, even by a completion racing this repair: it sees either the
// old (unsafe, skipped) dir or the new empty one. Anything else (not ours,
// a symlink, a file) is refused.
func prepareCacheDir(dir string) bool {
	fi, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		return os.Mkdir(dir, 0o700) == nil || cacheDirSafe(dir)
	case err != nil || !fi.IsDir() || !ownedByMe(fi):
		return false
	case fi.Mode().Perm() == 0o700:
		return true
	}
	stale := dir + ".stale-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if os.Rename(dir, stale) != nil {
		return false
	}
	defer os.RemoveAll(stale) // never follows symlinks
	// A racing completion may have created the new dir first.
	return os.Mkdir(dir, 0o700) == nil || cacheDirSafe(dir)
}
