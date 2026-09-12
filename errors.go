package argvio

import "errors"

var (
	errNoProviderResolved = errors.New("argvio: no consent provider resolved a tier")
	errRecoveredPanic     = errors.New("argvio: recovered from internal panic")
)
