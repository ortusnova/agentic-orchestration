package workers

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var (
	fpA = strings.Repeat("a", 64)
	fpB = strings.Repeat("b", 64)
)

func TestValidate(t *testing.T) {
	for name, l := range map[string]List{
		"a bad name":        {Workers: []Worker{{Name: "Taichi", Fingerprint: fpA, Flows: Flows}}},
		"a name twice":      {Workers: []Worker{{Name: "p1", Fingerprint: fpA, Flows: Flows}, {Name: "p1", Fingerprint: fpB, Flows: Flows}}},
		"a shared cert":     {Workers: []Worker{{Name: "p1", Fingerprint: fpA, Flows: Flows}, {Name: "p2", Fingerprint: fpA, Flows: Flows}}},
		"a bad fingerprint": {Workers: []Worker{{Name: "p1", Fingerprint: strings.ToUpper(fpA), Flows: Flows}}},
		"no flow":           {Workers: []Worker{{Name: "p1", Fingerprint: fpA}}},
		"an unknown flow":   {Workers: []Worker{{Name: "p1", Fingerprint: fpA, Flows: []string{"ssh"}}}},
	} {
		if err := l.Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

func TestSaveLoadAllowed(t *testing.T) {
	f := filepath.Join(t.TempDir(), "workers.json")
	l, err := Load(f)
	if err != nil || len(l.Workers) != 0 {
		t.Fatalf("a missing list: %v %v", l, err)
	}
	l.Put(Worker{Name: "taichi", Fingerprint: fpB, Flows: []string{"control"}})
	l.Put(Worker{Name: "p1", Fingerprint: fpA, Flows: Flows})
	if err := l.Save(f); err != nil {
		t.Fatal(err)
	}
	l, err = Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if l.Workers[0].Name != "p1" {
		t.Errorf("the list is not sorted: %v", l.Workers)
	}
	if got := l.Allowed("control"); !slices.Equal(got, []string{fpA, fpB}) {
		t.Errorf("control allows %v", got)
	}
	if got := l.Allowed("stdio"); !slices.Equal(got, []string{fpA}) {
		t.Errorf("stdio allows %v", got)
	}
	if !l.Remove("taichi") || l.Remove("taichi") {
		t.Error("Remove does not report whether it removed")
	}
	if got := l.Allowed("control"); !slices.Equal(got, []string{fpA}) {
		t.Errorf("after the removal control allows %v", got)
	}
}
