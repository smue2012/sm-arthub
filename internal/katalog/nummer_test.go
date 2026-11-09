package katalog

import (
	"fmt"
	"testing"
)

func TestPruefziffer(t *testing.T) {
	for _, f := range []struct {
		nummer string
		ok     bool
	}{
		{"AH00000018", true},
		{"AH00000026", true},
		{"AH00000017", false}, // falsche Prüfziffer
		{"AH00000081", false}, // vertauscht
		{"AH0000001", false},  // ohne Prüfziffer
		{"ah00000018", false},
	} {
		if NummerGueltig(f.nummer) != f.ok {
			t.Errorf("%s: erwartet %v", f.nummer, f.ok)
		}
	}
	// Jede einzelne falsche Ziffer wird erkannt
	gut := "AH01234561"
	gut = gut[:9] + fmt.Sprint(Pruefziffer(gut[2:9]))
	for pos := 2; pos < 10; pos++ {
		for d := byte('0'); d <= '9'; d++ {
			if d == gut[pos] {
				continue
			}
			falsch := gut[:pos] + string(d) + gut[pos+1:]
			if NummerGueltig(falsch) {
				t.Fatalf("%s (statt %s) nicht erkannt", falsch, gut)
			}
		}
	}
}
