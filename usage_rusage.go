//go:build linux || darwin || freebsd || netbsd || openbsd

package argvio

import "syscall"

func processUsage() (peakRSSBytes, cpuTimeMs int64) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	// The Timeval field widths differ between platforms; convert to int64
	// first so the arithmetic is the same everywhere.
	cpuMicros := int64(ru.Utime.Sec)*1_000_000 + int64(ru.Utime.Usec) + //nolint:unconvert // int32 on some platforms
		int64(ru.Stime.Sec)*1_000_000 + int64(ru.Stime.Usec) //nolint:unconvert // int32 on some platforms
	peakRSSBytes = int64(ru.Maxrss) * maxrssUnit //nolint:unconvert // int32 on some platforms
	if peakRSSBytes < 0 {
		peakRSSBytes = 0
	}
	if cpuMicros < 0 {
		cpuMicros = 0
	}
	return peakRSSBytes, cpuMicros / 1000
}
