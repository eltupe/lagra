package sysmount

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// kernelTable reads the mount table with getfsstat, no parsing of `mount` output.
func kernelTable() ([]Entry, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, fmt.Errorf("getfsstat: %w", err)
	}
	buf := make([]unix.Statfs_t, n+8)
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil, fmt.Errorf("getfsstat: %w", err)
	}
	out := make([]Entry, 0, n)
	for _, st := range buf[:n] {
		out = append(out, Entry{
			Device:     unix.ByteSliceToString(st.Mntfromname[:]),
			MountPoint: unix.ByteSliceToString(st.Mntonname[:]),
			FSType:     unix.ByteSliceToString(st.Fstypename[:]),
		})
	}
	return out, nil
}
