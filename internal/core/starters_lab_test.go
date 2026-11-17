package core

import (
	"strings"
	"testing"
)

// bb starter renders templates against the experiment's own Postgres
// (D-161): an existing service named db, with its own database and role,
// lands in pooling-modes' pgbouncer.ini and in the pool's upstream.
func TestApplyStarterRendersTheExperimentsInfra(t *testing.T) {
	h := newHarness(t)
	h.init()
	h.writeSpec("version: 1\nname: lab\nservices:\n  db:\n    type: postgres\n    config: {database: orders, user: shop}\n")
	if _, err := h.eng.ApplyStarter(h.ctx(), ApplyStarterRequest{Dir: h.dir, ID: "pgbouncer/pooling-modes"}); err != nil {
		t.Fatal(err)
	}
	ini := h.read("pooling-modes/pgbouncer.ini")
	for _, want := range []string{
		"\norders = host=db.bb.internal port=5432 dbname=orders pool_mode=transaction",
		"\norders_session = host=db.bb.internal port=5432 dbname=orders pool_mode=session",
		"\nadmin_users = shop\n",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("pgbouncer.ini lacks %q:\n%s", want, ini)
		}
	}
	spec := h.read("experiment.yaml")
	if !strings.Contains(spec, "  pool-central:\n    type: pgbouncer\n    upstreams: [db]\n") || strings.Contains(spec, "  postgres:\n") {
		t.Errorf("experiment.yaml does not point the pool at db:\n%s", spec)
	}

	// In an empty experiment the starter adds postgres with the defaults.
	h2 := newHarness(t)
	h2.init()
	if _, err := h2.eng.ApplyStarter(h2.ctx(), ApplyStarterRequest{Dir: h2.dir, ID: "pgbouncer/pooling-modes"}); err != nil {
		t.Fatal(err)
	}
	if ini := h2.read("pooling-modes/pgbouncer.ini"); !strings.Contains(ini, "\napp = host=postgres.bb.internal port=5432 dbname=app ") || !strings.Contains(ini, "\nadmin_users = app\n") {
		t.Errorf("default pgbouncer.ini:\n%s", ini)
	}
}
