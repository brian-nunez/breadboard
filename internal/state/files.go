// Package state owns Breadboard's files: the install home (BB_HOME) and each
// project's .bb/ directory. Writes are atomic, state-changing operations hold
// an advisory lock, and every versioned file only upgrades forward.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// beforeRename runs between writing the temp file and renaming it into
// place. Tests replace it to simulate a crash at the worst moment.
var beforeRename = func(string) error { return nil }

// WriteFile writes data atomically: a temp file in the same directory is
// written, synced, given perm, and renamed over path. A crash leaves either
// the old file or the new one, never a partial file.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	name := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := beforeRename(name); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	ok = true

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// versioned is the envelope every JSON state file shares.
type versioned struct {
	FormatVersion int `json:"formatVersion"`
}

// upgradeStep rewrites a decoded file from one format version to the next.
type upgradeStep func(map[string]any) error

// loadJSON reads a versioned JSON file into out. A missing file leaves out
// untouched and reports false. A newer format is refused with
// errs.ErrNewerFormat; an older one is upgraded step by step in memory.
func loadJSON(path string, current int, steps map[int]upgradeStep, out any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	var head versioned
	if err := json.Unmarshal(data, &head); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	switch {
	case head.FormatVersion > current:
		return false, fmt.Errorf("%s has format version %d, this bb knows up to %d: %w", path, head.FormatVersion, current, errs.ErrNewerFormat)
	case head.FormatVersion < 1:
		return false, fmt.Errorf("read %s: missing formatVersion", path)
	case head.FormatVersion < current:
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("read %s: %w", path, err)
		}

		for v := head.FormatVersion; v < current; v++ {
			step, ok := steps[v]
			if !ok {
				return false, fmt.Errorf("read %s: no upgrade from format version %d", path, v)
			}

			if err := step(raw); err != nil {
				return false, fmt.Errorf("upgrade %s from format version %d: %w", path, v, err)
			}

			raw["formatVersion"] = v + 1
		}

		if data, err = json.Marshal(raw); err != nil {
			return false, err
		}
	}

	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	return true, nil
}

// saveJSON writes v as indented JSON, refusing to replace a file written in a
// newer format.
func saveJSON(path string, current int, v any, perm os.FileMode) error {
	if data, err := os.ReadFile(path); err == nil {
		var head versioned
		if json.Unmarshal(data, &head) == nil && head.FormatVersion > current {
			return fmt.Errorf("%s has format version %d: %w", path, head.FormatVersion, errs.ErrNewerFormat)
		}
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	return WriteFile(path, append(data, '\n'), perm)
}
