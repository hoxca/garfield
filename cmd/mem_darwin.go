package cmd

import (
	"encoding/binary"
	"syscall"
)

// physicalMemory reports the machine's installed RAM in bytes, or 0 when it
// cannot be read. Darwin returns hw.memsize as a native-endian integer; Sysctl
// trims a trailing NUL byte, so the value is copied into a full-width buffer
// before decoding rather than sized from the returned string.
func physicalMemory() int64 {
	value, err := syscall.Sysctl("hw.memsize")
	if err != nil || value == "" {
		return 0
	}
	var buf [8]byte
	copy(buf[:], value)
	return int64(binary.LittleEndian.Uint64(buf[:]))
}
