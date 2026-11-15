//go:build unix

package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// Lock is a held advisory lock on a project's .bb/lock file.
type Lock struct {
	f *os.File
}

type lockHolder struct {
	Operation string    `json:"operation"`
	PID       int       `json:"pid"`
	Since     time.Time `json:"since"`
}

// BusyError reports the operation holding the lock. It matches errs.ErrBusy.
type BusyError struct {
	Operation string
	PID       int
	Since     time.Time
}

func (e *BusyError) Error() string {
	if e.Operation == "" {
		return errs.ErrBusy.Error() + ": another bb command is running"
	}
	return fmt.Sprintf("%s: bb %s is running (pid %d, since %s)", errs.ErrBusy, e.Operation, e.PID, e.Since.Format(time.TimeOnly))
}

// Is makes errors.Is(err, errs.ErrBusy) true.
func (e *BusyError) Is(target error) bool { return target == errs.ErrBusy }

// acquire takes an exclusive, non-blocking flock on path. When another process
// (or another open file description in this process) holds it, it returns a
// *BusyError naming that holder's operation.
func acquire(path, operation string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			var h lockHolder
			data, _ := io.ReadAll(f)
			_ = json.Unmarshal(data, &h)
			return nil, &BusyError{Operation: h.Operation, PID: h.PID, Since: h.Since}
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	data, _ := json.Marshal(lockHolder{Operation: operation, PID: os.Getpid(), Since: time.Now()})
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(data, 0)
	}
	return &Lock{f: f}, nil
}

// acquireWait retries acquire until it succeeds or timeout passes, for short
// critical sections where waiting is better than failing.
func acquireWait(path, operation string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		l, err := acquire(path, operation)
		if err == nil || !errors.Is(err, errs.ErrBusy) || time.Now().After(deadline) {
			return l, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0)
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
