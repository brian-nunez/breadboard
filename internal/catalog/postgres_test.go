package catalog

import (
	"strings"
	"testing"
)

// A reason in pg_hba.conf stays comment: every line starts with "# ", and a
// line ending in a backslash, which pg_hba.conf would continue onto the
// next line, is closed with "#".
func TestHBACommentStaysAComment(t *testing.T) {
	for in, want := range map[string]string{
		"Reason: practice":                         "# Reason: practice",
		"Reason: a\nhostnossl all all all trust":   "# Reason: a\n# hostnossl all all all trust",
		"Reason: a\r\nb\rc":                        "# Reason: a\n# b\n# c",
		"Reason: ends in \\":                       "# Reason: ends in \\#",
		"Reason: \\\nhost all all 0.0.0.0/0 trust": "# Reason: \\#\n# host all all 0.0.0.0/0 trust",
		"Reason: two \\\\":                         "# Reason: two \\\\#",
		"Reason: \\ \t":                            "# Reason: \\#",
		"Reason: nul\x00and esc\x1b":               "# Reason: nul and esc",
	} {
		if got := hbaComment(in); got != want {
			t.Errorf("hbaComment(%q) = %q, want %q", in, got, want)
		}
	}
}

// The Postgres entry renders every segment reason through hbaComment.
func TestPostgresHBAReasons(t *testing.T) {
	e, _ := Lookup("postgres")
	s := Server{
		Name: "postgres", FQDN: "postgres.bb.internal", Domain: "bb.internal", Config: e.ResolveConfig(nil),
		Secret: func(string) string { return "x" },
		Plain: &Plain{
			Subnet: "10.213.0.0/24", Clients: []string{"pool"},
			Reasons: []string{"pool: practice\nhostnossl all all all trust \\", "app: second"},
		},
	}

	var hba string

	for _, f := range e.Server(s).Files {
		if f.Name == "pg_hba.conf" {
			hba = string(f.Data)
		}
	}

	want := "# Reason: pool: practice\n# hostnossl all all all trust \\#\n# Reason: app: second\nhostnossl all      all  10.213.0.0/24 scram-sha-256\n"
	if !strings.HasSuffix(hba, want) {
		t.Errorf("pg_hba.conf ends\n%s\nwant\n%s", hba, want)
	}
}
