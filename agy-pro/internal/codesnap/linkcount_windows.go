package codesnap

import "os"

// linkCount reports how many directory entries the file has, and whether the
// platform told us.
//
// Windows says nothing here: os.FileInfo.Sys() is *syscall.Win32FileAttributeData,
// which has no link count field (the count lives behind GetFileInformationByHandle,
// which this package does not call). The hardlink-alias arm therefore treats the
// count as UNKNOWN and always runs the os.SameFile comparison, which is the
// authoritative test anyway (it compares volume serial + file index, and a
// hardlink shares both). The cost is O(claims x denylisted names) identity
// comparisons on Windows, with the denylisted set being a handful of names.
//
// Consequence, stated plainly: the nlink FAST GATE is unavailable on this host,
// and the arm still works - it is the Linux/Unix ports that skip the comparison
// for ordinary files.
func linkCount(info os.FileInfo) (uint64, bool) {
	return 0, false
}
