package importer

// Preisliste von Alltron/Competec: «ArtikelPreisdatenV2.csv», UTF-8, Tabulator,
// eine Kopfzeile, täglich ab 4:30 Uhr neu, rund 236'000 Artikel (Okt. 2026).
//
// Kopfzeile: Artikelnummer 2, Artikelnummer, Bezeichung, Bezeichung 2,
// Lagerbestand, Gewicht, Preis (inkl. MWSt), Preis (exkl. MWSt), WWW-Link,
// Webtext, Webtext 2, Herstellernummer, Hersteller, MWST Satz, UVP,
// Garantie (Monate), Erfassungsdatum, Kategorie 1–3, Kategorie, EAN-Code,
// Lieferdatum, Ausverkaufsartikel.
//
// «Bezeichung» ist in der Datei so geschrieben. «Artikelnummer 2» ist die
// Alltron-Artikelnummer (auch SKU der Bildliste), «Preis (exkl. MWSt)» der
// Händler-EK, «Gewicht» in kg.

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Zeile: ein Artikel der Preisliste, wie ihn der Abgleich braucht.
type Zeile struct {
	LieferantenNr string
	Bezeichnung   string
	Beschreibung  string
	Hersteller    string
	HerstellerNr  string
	EAN           string
	Kategorie     string // Ebene 1
	Kategorie2    string
	Kategorie3    string
	GewichtG      *int
	EK            string // Dezimalpunkt, z.B. "6.85"
	Lagerbestand  int
	LieferfristTg *int
}

// spaltenSchluessel: nur Buchstaben und Ziffern, klein – «Preis (exkl. MWSt)» = «preisexklmwst»
func spaltenSchluessel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var competecSpalten = map[string][]string{
	"nr":           {"artikelnummer2"},
	"bez":          {"bezeichung", "bezeichnung", "bezeichung1", "bezeichnung1"},
	"bez2":         {"bezeichung2", "bezeichnung2"},
	"bestand":      {"lagerbestand"},
	"gewicht":      {"gewicht"},
	"ek":           {"preisexklmwst", "preisexkl"},
	"text2":        {"webtext2"},
	"herstellernr": {"herstellernummer"},
	"hersteller":   {"hersteller"},
	"kategorie":    {"kategorie1"},
	"kategorie2":   {"kategorie2"},
	"kategorie3":   {"kategorie3"},
	"ean":          {"eancode", "ean"},
	"lieferdatum":  {"lieferdatum"},
}

var pflichtSpalten = map[string]string{"nr": "Artikelnummer 2", "bez": "Bezeichung", "ek": "Preis (exkl. MWSt)"}

var ersteZahl = regexp.MustCompile(`[0-9]+(?:[.,][0-9]+)?`)

func zahl(s string) (float64, bool) {
	s = strings.NewReplacer("'", "", "’", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, false
	}
	z := ersteZahl.FindString(s)
	if z == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(z, ",", "."), 64)
	return f, err == nil
}

// CompetecLesen liest die Preisliste. Zeilen ohne Artikelnummer, Bezeichnung
// oder gültigen Preis werden übersprungen und gezählt; doppelte Artikelnummern
// zählen nur einmal (erste gewinnt).
func CompetecLesen(daten []byte) (zeilen []Zeile, uebersprungen int, err error) {
	daten = bytes.TrimPrefix(daten, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(daten))
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	r.ReuseRecord = true

	kopf, err := r.Read()
	if err != nil {
		return nil, 0, Fehler("Preisliste: Kopfzeile nicht lesbar")
	}
	idx := map[string]int{}
	for i, name := range kopf {
		k := spaltenSchluessel(name)
		for feld, namen := range competecSpalten {
			if _, schon := idx[feld]; schon {
				continue
			}
			for _, n := range namen {
				if k == n {
					idx[feld] = i
				}
			}
		}
	}
	var fehlend []string
	for feld, name := range pflichtSpalten {
		if _, ok := idx[feld]; !ok {
			fehlend = append(fehlend, "«"+name+"»")
		}
	}
	if len(fehlend) > 0 {
		return nil, 0, Fehler("Preisliste: Spalte " + strings.Join(fehlend, ", ") +
			" fehlt – ist es die Datei ArtikelPreisdatenV2.csv (Tabulator-getrennt)?")
	}
	feld := func(z []string, f string) string {
		if i, ok := idx[f]; ok && i < len(z) {
			return strings.TrimSpace(z[i])
		}
		return ""
	}

	gesehen := map[string]bool{}
	for {
		z, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			uebersprungen++
			continue
		}
		nr := feld(z, "nr")
		bez := strings.TrimSpace(feld(z, "bez") + " " + feld(z, "bez2"))
		ek, ok := zahl(feld(z, "ek"))
		if nr == "" || bez == "" || !ok || len(nr) > 60 {
			uebersprungen++
			continue
		}
		if gesehen[nr] {
			continue
		}
		gesehen[nr] = true
		zeile := Zeile{
			LieferantenNr: nr,
			Bezeichnung:   kuerzen(bez, 300),
			Beschreibung:  kuerzen(feld(z, "text2"), 4000),
			Hersteller:    kuerzen(feld(z, "hersteller"), 200),
			HerstellerNr:  kuerzen(feld(z, "herstellernr"), 100),
			EAN:           ean(feld(z, "ean")),
			Kategorie:     kuerzen(feld(z, "kategorie"), 200),
			Kategorie2:    kuerzen(feld(z, "kategorie2"), 200),
			Kategorie3:    kuerzen(feld(z, "kategorie3"), 200),
			EK:            strconv.FormatFloat(math.Round(ek*10000)/10000, 'f', -1, 64),
		}
		if b, ok := zahl(feld(z, "bestand")); ok {
			zeile.Lagerbestand = int(b)
		}
		if g, ok := zahl(feld(z, "gewicht")); ok && g > 0 && g < 100000 {
			gr := int(math.Round(g * 1000))
			zeile.GewichtG = &gr
		}
		if strings.EqualFold(feld(z, "lieferdatum"), "sofort") {
			null := 0
			zeile.LieferfristTg = &null
		}
		zeilen = append(zeilen, zeile)
	}
	if len(zeilen) == 0 {
		return nil, uebersprungen, Fehler("Preisliste: keine Artikelzeilen gefunden")
	}
	return zeilen, uebersprungen, nil
}

// ean: nur Ziffern, 8 bis 14 Stellen, sonst leer (z.B. «0» oder Text)
func ean(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 || len(s) > 14 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if strings.Trim(s, "0") == "" {
		return ""
	}
	return s
}

func kuerzen(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// BildlisteLesen: Datei «SKU;BildUrl» (eine Zeile je Bild). Liefert je
// Lieferanten-Artikelnummer die Links (höchstens 10, nur http/https).
func BildlisteLesen(daten []byte) (map[string][]string, error) {
	daten = bytes.TrimPrefix(daten, []byte("\xef\xbb\xbf"))
	trenn := ';'
	erste, _, _ := bytes.Cut(daten, []byte("\n"))
	if !bytes.ContainsRune(erste, ';') && bytes.ContainsRune(erste, '\t') {
		trenn = '\t'
	}
	r := csv.NewReader(bytes.NewReader(daten))
	r.Comma = trenn
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	links := map[string][]string{}
	for {
		z, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(z) < 2 {
			continue
		}
		nr, url := strings.TrimSpace(z[0]), strings.TrimSpace(z[1])
		if nr == "" || !(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) || len(url) > 1000 {
			continue // auch die Kopfzeile
		}
		if len(links[nr]) < 10 {
			links[nr] = append(links[nr], url)
		}
	}
	if len(links) == 0 {
		return nil, Fehler("Bildliste: keine Zeilen «Artikelnummer;Bild-Link» gefunden")
	}
	return links, nil
}

// ---------- Lagerbestandsliste ----------
//
// Competec liefert zusätzlich eine kurze Liste nur mit dem Bestand, die
// mehrmals täglich neu ist: Tabulator, Kopfzeile «Nummer 2», «Lagerbestand»
// (z.B. «723591 -1», «1649149 0»). Negative Werte = nicht an Lager.

// IstBestandsliste erkennt die Lagerbestandsliste an der Kopfzeile (keine
// Bezeichnung, kein Preis, aber Nummer und Lagerbestand).
func IstBestandsliste(daten []byte) bool {
	daten = bytes.TrimPrefix(daten, []byte("\xef\xbb\xbf"))
	kopf, _, _ := bytes.Cut(daten, []byte("\n"))
	var hatNr, hatBestand, hatMehr bool
	for _, f := range strings.FieldsFunc(string(kopf), func(r rune) bool { return r == '\t' || r == ';' }) {
		switch spaltenSchluessel(f) {
		case "nummer2", "artikelnummer2":
			hatNr = true
		case "lagerbestand":
			hatBestand = true
		case "bezeichung", "bezeichnung", "preisexklmwst":
			hatMehr = true
		}
	}
	return hatNr && hatBestand && !hatMehr
}

// BestandLesen liest die Lagerbestandsliste: Lieferanten-Nr. → Bestand (≥ 0).
func BestandLesen(daten []byte) (map[string]int, error) {
	daten = bytes.TrimPrefix(daten, []byte("\xef\xbb\xbf"))
	trenn := '\t'
	erste, _, _ := bytes.Cut(daten, []byte("\n"))
	if !bytes.ContainsRune(erste, '\t') && bytes.ContainsRune(erste, ';') {
		trenn = ';'
	}
	r := csv.NewReader(bytes.NewReader(daten))
	r.Comma = trenn
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	kopf, err := r.Read()
	if err != nil {
		return nil, Fehler("Lagerbestandsliste: Kopfzeile nicht lesbar")
	}
	nr, best := -1, -1
	for i, f := range kopf {
		switch spaltenSchluessel(f) {
		case "nummer2", "artikelnummer2":
			if nr < 0 {
				nr = i
			}
		case "lagerbestand":
			best = i
		}
	}
	if nr < 0 || best < 0 {
		return nil, Fehler("Lagerbestandsliste: Spalten «Nummer 2» und «Lagerbestand» erwartet")
	}
	bestand := map[string]int{}
	for {
		z, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || nr >= len(z) || best >= len(z) {
			continue
		}
		n := strings.TrimSpace(z[nr])
		if n == "" || len(n) > 60 {
			continue
		}
		wert := 0
		if b, ok := zahl(z[best]); ok { // «-1» liefert zahl() als nicht ok → 0
			wert = int(b)
		}
		bestand[n] = wert
	}
	if len(bestand) == 0 {
		return nil, Fehler("Lagerbestandsliste: keine Zeilen gefunden")
	}
	return bestand, nil
}
