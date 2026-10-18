//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package argvio

// processUsage reports nothing on platforms without getrusage (Windows,
// for one), so RecordCommand omits the attributes instead of sending
// zeros.
func processUsage() (peakRSSBytes, cpuTimeMs int64) {
	return 0, 0
}
