// Package errs holds the sentinel errors shared by the engine's packages.
// The core re-exports each one, so wrappers only ever import the core.
package errs

import "errors"

var (
	// ErrNotInitialized means the directory holds no experiment.yaml.
	ErrNotInitialized = errors.New("not a breadboard experiment (no experiment.yaml); run bb init")
	// ErrExists means the thing being created already exists.
	ErrExists = errors.New("already exists")
	// ErrNotFound means a named service, app, starter, catalog entry, or key does not exist.
	ErrNotFound = errors.New("not found")
	// ErrBusy means another bb process holds the experiment's operation lock.
	ErrBusy = errors.New("experiment is busy")
	// ErrValidation means a spec, setting, or request broke one or more rules.
	ErrValidation = errors.New("validation failed")
	// ErrNewerFormat means a file was written by a newer bb; this binary refuses to change it.
	ErrNewerFormat = errors.New("written by a newer bb; upgrade bb to continue")
	// ErrDockerUnavailable means the docker CLI or daemon cannot be reached.
	ErrDockerUnavailable = errors.New("docker is not available")
	// ErrUnhealthy means a service failed its health check or exited while bb waited for it.
	ErrUnhealthy = errors.New("service is unhealthy")
)
