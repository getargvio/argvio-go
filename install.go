package argvio

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// installIDFile is the name of the file, next to the consent file in the
// CLI's own config directory, holding the install ID.
const installIDFile = "argvio-install-id"

// WithInstallIDPath overrides where the install ID (see AttrInstallID) is
// persisted. Defaults to argvio-install-id in the same per-CLI config
// directory as FileConsentProvider's consent file. An empty path turns
// install IDs off entirely.
func WithInstallIDPath(path string) Option {
	return func(c *config) {
		if path == "" {
			path = os.DevNull
		}
		c.installIDPath = path
	}
}

func resolveInstallIDPath(cfg *config) string {
	if cfg.installIDPath == os.DevNull {
		return ""
	}
	if cfg.installIDPath != "" {
		return cfg.installIDPath
	}
	consentPath := defaultConsentPath(cfg.cliName)
	if consentPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(consentPath), installIDFile)
}

// loadOrCreateInstallID returns the install ID stored at path, generating
// and persisting a new random one on first use. It returns "" whenever
// the ID can't be persisted: an ID that changes on every run would count
// every invocation as a new install, which is worse than no ID at all.
func loadOrCreateInstallID(path string) string {
	if path == "" {
		return ""
	}
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // path is the CLI's own config file, never user input
		if id := strings.TrimSpace(string(b)); validInstallID(id) {
			return id
		}
	}
	id := newUUID()
	if id == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return ""
	}
	return id
}

// validInstallID accepts exactly what newUUID produces, so a corrupted or
// hand-edited file is replaced rather than sent.
func validInstallID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
	}
	return true
}

// newUUID returns a random (version 4) UUID, or "" if the system's
// randomness source fails.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
