package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HomeEnv overrides the install home.
const HomeEnv = "BB_HOME"

// Home is the install home: settings and certificate authorities.
type Home struct {
	Dir string
}

// DefaultHomeDir returns $BB_HOME, or <user config dir>/breadboard.
func DefaultHomeDir() (string, error) {
	if dir := os.Getenv(HomeEnv); dir != "" {
		return filepath.Abs(dir)
	}

	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory (set %s): %w", HomeEnv, err)
	}

	return filepath.Join(base, "breadboard"), nil
}

// ConfigPath is the install settings file.
func (h Home) ConfigPath() string { return filepath.Join(h.Dir, "config.yaml") }

// CADir holds one certificate and key pair per CA name.
func (h Home) CADir() string { return filepath.Join(h.Dir, "ca") }

// CACertPath is the certificate file of the named CA.
func (h Home) CACertPath(name string) string { return filepath.Join(h.CADir(), name+".crt") }

func (h Home) caKeyPath(name string) string { return filepath.Join(h.CADir(), name+".key") }

// Ensure creates the home and CA directories with mode 0700 and reports
// whether the home was created.
func (h Home) Ensure() (created bool, err error) {
	if _, err := os.Stat(h.Dir); errors.Is(err, os.ErrNotExist) {
		created = true
	}

	for _, dir := range []string{h.Dir, h.CADir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, fmt.Errorf("create %s: %w", dir, err)
		}

		if err := os.Chmod(dir, 0o700); err != nil {
			return false, err
		}
	}

	return created, nil
}

// caLockTimeout bounds how long LockCA waits for another bb process.
const caLockTimeout = 30 * time.Second

// LockCA takes the install-wide CA lock, waiting while another bb process
// holds it. Every project shares the CA, so project locks cannot protect it:
// hold this lock from reading the CA until any replacement is saved.
func (h Home) LockCA(operation string) (*Lock, error) {
	if _, err := h.Ensure(); err != nil {
		return nil, err
	}

	return acquireWait(filepath.Join(h.CADir(), "lock"), operation, caLockTimeout)
}

// LoadCA reads the named CA's certificate and key PEM, or ok=false.
func (h Home) LoadCA(name string) (certPEM, keyPEM []byte, ok bool, err error) {
	certPEM, err = os.ReadFile(h.CACertPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}

	if err != nil {
		return nil, nil, false, err
	}

	keyPEM, err = os.ReadFile(h.caKeyPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}

	if err != nil {
		return nil, nil, false, err
	}

	return certPEM, keyPEM, true, nil
}

// SaveCA writes the named CA: the key first (0600), then the certificate (0644).
func (h Home) SaveCA(name string, certPEM, keyPEM []byte) error {
	if _, err := h.Ensure(); err != nil {
		return err
	}

	if err := WriteFile(h.caKeyPath(name), keyPEM, 0o600); err != nil {
		return err
	}

	return WriteFile(h.CACertPath(name), certPEM, 0o644)
}
