package argvio

// readProcessUsage returns the peak resident set size of the process in
// bytes and the CPU time it has consumed (user plus system) in
// milliseconds, as implemented per platform by processUsage in the
// usage_*.go files. A measure the platform can't provide, or that the OS
// call failed to read, is 0, which RecordCommand omits rather than
// sending: the server treats zero as "not reported" anyway.
//
// Both values cover the whole process since it started, not one command,
// so in a long-lived or multi-command process they are cumulative. Each
// read is one syscall with no allocation and no background sampling.
//
// It is a variable so tests can stand in for platforms or failures that
// the host running them can't produce.
var readProcessUsage = processUsage
