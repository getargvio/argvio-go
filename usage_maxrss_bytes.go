//go:build darwin

package argvio

// maxrssUnit is the size in bytes of one unit of Rusage.Maxrss, which
// macOS reports in bytes (unlike the other Unixes, which use kilobytes).
const maxrssUnit = 1
