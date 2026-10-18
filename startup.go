package argvio

import (
	"os"
	"time"
)

// initTime is when this package finished loading, the closest thing Go has
// to a process start time. Go exposes no portable one, so startup overhead
// is measured from here: it excludes the runtime's own initialisation and
// the init of every package this one imports, all of which run first.
// Vendors who want those counted can import this package as early as
// possible (a blank import sorts it ahead of the rest of their imports).
var initTime = time.Now()

// readStartupMs returns the milliseconds since the package loaded. It is a
// variable so tests can stand in for a slow startup. The reading uses the
// monotonic clock, so it is never negative; zero is a real value (a fast
// binary can start in under a millisecond), not "unknown".
var readStartupMs = func() int64 {
	return time.Since(initTime).Milliseconds()
}

// readBinarySize returns the size in bytes of the running executable, and
// false when it can't be determined: the path doesn't resolve, the stat
// fails, or the file reports no size. It is one os.Executable and one stat,
// and a variable so tests can stand in for those failures.
var readBinarySize = func() (int64, bool) {
	path, err := os.Executable()
	if err != nil {
		return 0, false
	}
	// Stat follows symlinks, so a symlinked executable reports its target.
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= 0 {
		return 0, false
	}
	return fi.Size(), true
}
