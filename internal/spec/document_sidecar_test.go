package spec

import (
	"reflect"
	"testing"
)

// AddApp writes sidecars so that decoding the result gives them back, with
// unset fields left out (their defaults apply).
func TestAddAppSidecarsRoundTrip(t *testing.T) {
	retries := 0
	app := App{Name: "api", Path: "./api", Uses: []Use{{Service: "postgres"}, {Service: "s3"}}, Replicas: 2, Sidecars: []Sidecar{
		{Type: SidecarEnvoy, Envoy: &EnvoySidecar{
			Inbound:     &EnvoyInbound{Port: 8443, AppPort: 8080},
			Outbound:    []EnvoyOutbound{{Service: "web", Port: 9001, LB: "least_request", Timeout: "2s", Retries: &retries, Outlier: &Outlier{Consecutive5xx: 3, Ejection: "30s", MaxEjectionPercent: 50, Interval: "5s"}}},
			Transparent: &Transparent{Egress: EgressDeny},
		}},
		{Type: SidecarPgBouncer, Name: "pool", PgBouncer: &PgBouncerSidecar{Service: "postgres", PoolMode: "session", PoolSize: 5}},
		{Type: SidecarFluentBit, FluentBit: &FluentBitSidecar{To: "s3", Bucket: "logs"}},
	}}
	d := New("lab")
	for _, svc := range []struct{ name, typ string }{{"postgres", "postgres"}, {"s3", "s3-compatible"}} {
		if err := d.AddService(svc.name, svc.typ); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddApp(App{Name: "web", Path: "./web", Sidecars: []Sidecar{{Type: SidecarEnvoy, Envoy: &EnvoySidecar{Inbound: &EnvoyInbound{Port: 8443, AppPort: 8080}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.AddApp(app); err != nil {
		t.Fatal(err)
	}
	out, _ := d.Bytes()
	s, err := loadWith(t, string(out))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, _ := s.App("api")
	if got.Replicas != 2 || !reflect.DeepEqual(got.Sidecars, app.Sidecars) {
		t.Fatalf("round trip:\n got %+v\nwant %+v\n%s", got.Sidecars, app.Sidecars, out)
	}

	plain := New("lab")
	if err := plain.AddApp(App{Name: "web", Path: "./web", Sidecars: []Sidecar{{Type: SidecarEnvoy, Envoy: &EnvoySidecar{Transparent: &Transparent{}}}}}); err != nil {
		t.Fatal(err)
	}
	out, _ = plain.Bytes()
	if s, err := loadWith(t, string(out)); err != nil || !s.Apps[0].Sidecars[0].Transparent() {
		t.Fatalf("transparent: true did not round trip: %v\n%s", err, out)
	}
}
