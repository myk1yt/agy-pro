//go:build !windows

package codesnap

import (
	"os"
	"syscall"
)

// linkCount reports how many directory entries the file has, and whether the
// platform told us. On the Unix ports the stat struct carries it directly; the
// number is what lets the hardlink-alias arm skip the os.SameFile comparison for
// the overwhelming majority of files (nlink == 1).
func linkCount(info os.FileInfo) (uint64, bool) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		return uint64(st.Nlink), true
	}
	return 0, false
}
