package hostinfo

import "testing"

func TestParseVMStat(t *testing.T) {
	out := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10.
Pages active:                            999.
Pages inactive:                           20.
Pages speculative:                         2.
Pages purgeable:                           0.
`
	got, err := parseVMStat([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if got != 32*16384 {
		t.Errorf("got %d, want %d", got, 32*16384)
	}
}
