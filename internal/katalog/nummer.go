package katalog

import "regexp"

var nummerMuster = regexp.MustCompile(`^AH[0-9]{8}$`)

// Pruefziffer berechnet die Luhn-Prüfziffer zu einer Ziffernfolge
// (gleiches Verfahren wie luhn_pruefziffer in Migration 002).
func Pruefziffer(ziffern string) int {
	summe := 0
	for i := 0; i < len(ziffern); i++ {
		z := int(ziffern[len(ziffern)-1-i] - '0')
		if i%2 == 0 {
			z *= 2
			if z > 9 {
				z -= 9
			}
		}
		summe += z
	}
	return (10 - summe%10) % 10
}

// NummerGueltig prüft Format (AH + 8 Ziffern) und Prüfziffer einer Artikelnummer.
func NummerGueltig(nummer string) bool {
	if !nummerMuster.MatchString(nummer) {
		return false
	}
	return int(nummer[9]-'0') == Pruefziffer(nummer[2:9])
}
