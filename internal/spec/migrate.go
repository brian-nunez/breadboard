package spec

import (
	"fmt"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// upgrades maps a spec version to the step that rewrites it to the next
// version. Version 1 is the first, so there are no steps yet; each new version
// adds one, with a test that loads a fixture of the previous version.
var upgrades = map[int]func(*Document) error{}

// upgrade rewrites the document to CurrentVersion, one step at a time, and
// reports the version it started from.
func (d *Document) upgrade() (from int, err error) {
	from = d.Version()
	if from > CurrentVersion {
		return from, fmt.Errorf("experiment.yaml version %d: %w", from, errs.ErrNewerFormat)
	}

	if from < 1 {
		return from, Invalid("version", "must be at least 1")
	}

	for v := from; v < CurrentVersion; v++ {
		step, ok := upgrades[v]
		if !ok {
			return from, fmt.Errorf("no upgrade from experiment.yaml version %d", v)
		}

		if err := step(d); err != nil {
			return from, fmt.Errorf("upgrade experiment.yaml from version %d: %w", v, err)
		}

		d.SetVersion(v + 1)
	}

	return from, nil
}

// Migrate upgrades the document in place to CurrentVersion and reports the
// starting version and whether anything changed.
func (d *Document) Migrate() (from int, changed bool, err error) {
	from, err = d.upgrade()
	if err != nil {
		return from, false, err
	}

	return from, from != CurrentVersion, nil
}
