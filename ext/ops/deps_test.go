package ops

import (
	"strings"
	"testing"
)

func TestKnownDepsFiltersUnknownAndSelf(t *testing.T) {
	t.Parallel()
	p := Profile{Services: []Service{
		{Name: "api", DependsOn: []string{"database", "ghost", "  ", "api", "cache"}},
		{Name: "database"},
		{Name: "cache"},
	}}
	got := knownDeps(p, p.Services[0])
	if len(got) != 2 || got[0] != "database" || got[1] != "cache" {
		t.Fatalf("got=%v (want declared order, unknown/self/blank dropped)", got)
	}
}

func TestKnownDepsEmpty(t *testing.T) {
	t.Parallel()
	p := Profile{Services: []Service{{Name: "api"}}}
	if got := knownDeps(p, p.Services[0]); got != nil {
		t.Fatalf("got=%v want nil", got)
	}
}

func TestHasDeps(t *testing.T) {
	t.Parallel()
	// Only an unknown dependency: nothing usable, so no guidance.
	p := Profile{Services: []Service{{Name: "api", DependsOn: []string{"ghost"}}}}
	if p.hasDeps() {
		t.Fatal("unknown-only dependency should not count")
	}
	p.Services = append(p.Services, Service{Name: "ghost"})
	if !p.hasDeps() {
		t.Fatal("resolvable dependency should count")
	}
}

func TestSystemAppendRendersDepsAndCorrelation(t *testing.T) {
	root := t.TempDir()
	cfg := `
services:
  - name: api
    depends_on: [database, cache]
    actions:
      status: [echo, s]
  - name: database
  - name: cache
`
	writeProfileDir(t, root, "fleet", cfg)
	p, err := loadProfile("fleet", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	sys := p.systemAppend()
	if !strings.Contains(sys, "[needs:database,cache]") {
		t.Fatalf("dependency edge missing: %s", sys)
	}
	// The edge is only useful if the model is told what to do with it.
	if !strings.Contains(sys, "likely cause") {
		t.Fatalf("correlation guidance missing: %s", sys)
	}
}

func TestSystemAppendOmitsDepGuidanceWhenNoDeps(t *testing.T) {
	root := t.TempDir()
	writeProfileDir(t, root, "plain", "services:\n  - name: api\n")
	p, err := loadProfile("plain", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	sys := p.systemAppend()
	if strings.Contains(sys, "needs:") || strings.Contains(sys, "likely cause") {
		t.Fatalf("dependency text leaked into a profile with no deps: %s", sys)
	}
}

func TestDependsOnYAMLRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeProfileDir(t, root, "f", "services:\n  - name: api\n    depends_on: [db]\n  - name: db\n")
	p, err := loadProfile("f", PackConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := p.service("api")
	if !ok {
		t.Fatal("api missing")
	}
	if len(svc.DependsOn) != 1 || svc.DependsOn[0] != "db" {
		t.Fatalf("DependsOn=%v", svc.DependsOn)
	}
}
