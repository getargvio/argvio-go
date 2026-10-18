//go:build linux || freebsd || netbsd || openbsd

package argvio

// maxrssUnit is the size in bytes of one unit of Rusage.Maxrss, which
// these platforms report in kilobytes.
const maxrssUnit = 1024
