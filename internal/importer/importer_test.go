package importer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smue2012/sm-arthub/internal/datenbank"
	"github.com/smue2012/sm-arthub/internal/katalog"
)

const kopf = "Artikelnummer 2\tArtikelnummer\tBezeichung\tBezeichung 2\tLagerbestand\tGewicht\tPreis (inkl. MWSt)\tPreis (exkl. MWSt)\tWWW-Link\tWebtext\tWebtext 2\tHerstellernummer\tHersteller\tMWST Satz\tUVP\tGarantie (Monate)\tErfassungsdatum\tKategorie 1\tKategorie 2\tKategorie 3\tKategorie\tEAN-Code\tLieferdatum\tAusverkaufsartikel"

// Auszug aus ArtikelPreisdatenV2.csv (Alltron, Oktober 2026)
func liste(zeilen ...string) []byte {
	return []byte("\xef\xbb\xbf" + kopf + "\n" + strings.Join(zeilen, "\n") + "\n")
}

const (
	stehsammler = "1396848\tBU ESSELTE STEHSAMMLER 56001\tEsselte Stehsammler Vivida\tA4 Gelb\t6\t0.27\t7.4\t6.85\t\tEsselte Stehsammler Vivida A4 Gelb\tProdukttyp: Stehsammler, Detailfarbe: Gelb\t56001\tEsselte\t8.1\t14\t24\t27.08.22\tBüro & Freizeit\tBüro & Papeterie\tBüroablagen\tStehsammler\t5701216560016\tsofort\tNein"
	brixies     = "2108613\tSP BRIXIES PLUS  4260510514633\tBRIXIES Plus Blumenstrauss Sweethart\t896 bricks\t2\t0.6\t37.73\t34.9\t\tBrixies Plus Blumenstrauss Sweetheart\tAnzahl Teile: 896 Teile\t98.222106\tBrixies Plus\t8.1\t40.9\t24\t25.04.26\tBüro & Freizeit\tBaby & Spielwaren\tSpielwaren\tKlemmbausteine\t4260510514633\tsofort\tNein"
	sandwich    = "1644755\tHH PRINCESS SANDWICHM XXL\tPrincess SandwichMaker 01.127006.01.001\t1600W, XXL Backfläche, antihaft\t0\t3.16\t60.92\t56.36\t\tPrincess Sandwich-Toaster Supreme XXL 1600 W\tProdukttyp: Sandwich Toaster\t01.127006.01.001\tPrincess\t8.1\t82\t24\t23.11.23\tHaushalt & Gastro\tHaushaltskleingeräte\tKochen & Garen\tKontaktgrill\t8712836963023\t12.12.26\tNein"
)

func TestCompetecLesen(t *testing.T) {
	daten := liste(stehsammler, brixies, sandwich,
		"9999\tX\t\t\t1\t\t\t1.00\t",       // ohne Bezeichnung
		"8888\tY\tKein Preis\t\t1\t\t\t\t", // ohne Preis
		stehsammler)                        // doppelt
	z, ueber, err := CompetecLesen(daten)
	if err != nil {
		t.Fatal(err)
	}
	if len(z) != 3 || ueber != 2 {
		t.Fatalf("%d Zeilen, %d übersprungen", len(z), ueber)
	}
	s := z[0]
	if s.LieferantenNr != "1396848" || s.Bezeichnung != "Esselte Stehsammler Vivida A4 Gelb" || s.EK != "6.85" ||
		s.Lagerbestand != 6 || *s.GewichtG != 270 || s.EAN != "5701216560016" || s.Hersteller != "Esselte" ||
		s.HerstellerNr != "56001" || s.Kategorie != "Büro & Freizeit" || *s.LieferfristTg != 0 ||
		!strings.HasPrefix(s.Beschreibung, "Produkttyp") {
		t.Errorf("Stehsammler: %+v", s)
	}
	if z[2].LieferfristTg != nil || z[2].Lagerbestand != 0 {
		t.Errorf("Sandwich: %+v", z[2])
	}

	if _, _, err := CompetecLesen([]byte("Nr;Name;Preis\n1;a;2\n")); err == nil || !strings.Contains(err.Error(), "fehlt") {
		t.Errorf("falsche Datei: %v", err)
	}
}

func TestBildlisteLesen(t *testing.T) {
	b, err := BildlisteLesen([]byte("SKU;BildUrl\n1396848;https://img.example/a.jpg\n1396848;https://img.example/b.jpg\n2108613;ftp://x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b["1396848"]) != 2 || len(b["2108613"]) != 0 {
		t.Errorf("%v", b)
	}
}

func TestEAN(t *testing.T) {
	for in, aus := range map[string]string{"5701216560016": "5701216560016", "0": "", "00000000": "", "abc12345": "", "1234567": "", "12345678": "12345678"} {
		if ean(in) != aus {
			t.Errorf("ean(%q) = %q", in, ean(in))
		}
	}
}

// ---------- Abgleich (mit Datenbank) ----------

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("ARTHUB_TEST_DB")
	if url == "" {
		t.Skip("ARTHUB_TEST_DB nicht gesetzt")
	}
	admin, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := fmt.Sprintf("arthub_imp_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec("DROP DATABASE " + name + " WITH (FORCE)") })
	i := strings.LastIndex(url[:strings.IndexAny(url+"?", "?")], "/")
	j := strings.IndexAny(url+"?", "?")
	db, err := datenbank.Verbinden(url[:i+1] + name + url[j:])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := datenbank.Migrieren(db, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return db
}

func lieferant(t *testing.T, db *sql.DB, kuerzel string) int {
	t.Helper()
	var id int
	if err := db.QueryRow(`INSERT INTO lieferanten (kuerzel, name) VALUES ($1, $1) RETURNING id`, kuerzel).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func wert[T any](t *testing.T, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestAbgleich(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	alltron := lieferant(t, db, "alltron")
	andere := lieferant(t, db, "andere")

	// Ein anderer Lieferant führt den Stehsammler schon (gleiche EAN) → kein neuer Artikel
	var vorhanden int64
	db.QueryRow(`INSERT INTO artikel (bezeichnung, ean, hersteller, quelle_lieferant_id)
		VALUES ('Stehsammler (anderer)', '5701216560016', 'Esselte', $1) RETURNING id`, andere).Scan(&vorhanden)
	db.Exec(`INSERT INTO angebote (artikel_id, lieferant_id, lieferanten_nr, ek, lagerbestand) VALUES ($1, $2, 'A-1', 7.10, 3)`, vorhanden, andere)

	z, _, _ := CompetecLesen(liste(stehsammler, brixies, sandwich))
	bilder := map[string][]string{"2108613": {"https://img.example/brix.jpg"}}
	b, err := Abgleichen(ctx, db, alltron, z, bilder, time.Date(2026, 11, 9, 4, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if b.AngeboteNeu != 3 || b.ArtikelNeu != 2 || b.Zugeordnet != 1 || b.Deaktiviert != 0 {
		t.Fatalf("erster Lauf: %+v", b)
	}
	if n := wert[int](t, db, `SELECT count(*) FROM angebote WHERE artikel_id = $1`, vorhanden); n != 2 {
		t.Fatalf("Stehsammler hat %d Angebote, erwartet 2", n)
	}
	// Stammdaten des fremden Artikels bleiben, neue gehören Alltron und tragen eine gültige Nummer
	if s := wert[string](t, db, `SELECT bezeichnung FROM artikel WHERE id = $1`, vorhanden); s != "Stehsammler (anderer)" {
		t.Errorf("fremde Stammdaten überschrieben: %s", s)
	}
	if n := wert[int](t, db, `SELECT count(*) FROM artikel WHERE quelle_lieferant_id = $1 AND nummer ~ '^AH[0-9]{8}$'`, alltron); n != 2 {
		t.Errorf("neue Artikel: %d", n)
	}
	if s := wert[string](t, db, `SELECT array_to_string(bild_urls, ',') FROM angebote WHERE lieferanten_nr = '2108613'`); s != "https://img.example/brix.jpg" {
		t.Errorf("Bilder: %q", s)
	}
	stand1 := wert[int64](t, db, `SELECT max(stand) FROM artikel`)

	// Zweiter Lauf unverändert: nichts geändert, kein neuer Stand
	b, err = Abgleichen(ctx, db, alltron, z, bilder, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if b.AngeboteNeu+b.AngeboteGeaendert+b.ArtikelNeu+b.StammdatenGeaendert+b.Deaktiviert+b.BilderGeaendert != 0 {
		t.Fatalf("unveränderter Lauf: %+v", b)
	}
	if s := wert[int64](t, db, `SELECT max(stand) FROM artikel`); s != stand1 {
		t.Errorf("Stand ohne Änderung erhöht: %d → %d", stand1, s)
	}

	// Dritter Lauf: Preis Brixies geändert, Name Sandwich geändert, Stehsammler fehlt
	brixNeu := strings.Replace(brixies, "\t34.9\t", "\t33.5\t", 1)
	sandNeu := strings.Replace(sandwich, "Princess SandwichMaker", "Princess Sandwich-Maker", 1)
	z, _, _ = CompetecLesen(liste(brixNeu, sandNeu))
	b, err = Abgleichen(ctx, db, alltron, z, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if b.AngeboteGeaendert != 1 || b.StammdatenGeaendert != 1 || b.Deaktiviert != 1 {
		t.Fatalf("dritter Lauf: %+v", b)
	}
	if ek := wert[string](t, db, `SELECT ek::text FROM angebote WHERE lieferanten_nr = '2108613'`); ek != "33.5000" {
		t.Errorf("EK: %s", ek)
	}
	// Stehsammler: Alltron-Angebot inaktiv, Artikel bleibt aktiv (anderer Lieferant), aber neuer Stand
	if aktiv := wert[bool](t, db, `SELECT aktiv FROM artikel WHERE id = $1`, vorhanden); !aktiv {
		t.Error("Stehsammler darf aktiv bleiben (Angebot des anderen Lieferanten)")
	}
	if s := wert[int64](t, db, `SELECT stand FROM artikel WHERE id = $1`, vorhanden); s <= stand1 {
		t.Error("Stehsammler braucht einen neuen Stand")
	}
	// Brixies ohne Bildliste: Bilder bleiben
	if s := wert[string](t, db, `SELECT array_to_string(bild_urls, ',') FROM angebote WHERE lieferanten_nr = '2108613'`); s == "" {
		t.Error("Bilder ohne Bildliste gelöscht")
	}

	// Vierter Lauf: Sandwich fällt weg → Artikel ohne aktives Angebot wird inaktiv
	z, _, _ = CompetecLesen(liste(brixNeu))
	if _, err := Abgleichen(ctx, db, alltron, z, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if aktiv := wert[bool](t, db, `SELECT a.aktiv FROM artikel a JOIN angebote an ON an.artikel_id = a.id
		WHERE an.lieferanten_nr = '1644755'`); aktiv {
		t.Error("Sandwich ohne Angebot muss inaktiv sein")
	}
	// Kommt er wieder, wird er wieder aktiv (gleiches Angebot, gleicher Artikel)
	z, _, _ = CompetecLesen(liste(brixNeu, sandNeu))
	b, _ = Abgleichen(ctx, db, alltron, z, nil, time.Time{})
	if b.ArtikelNeu != 0 || b.AngeboteGeaendert != 1 {
		t.Errorf("Rückkehr: %+v", b)
	}
	if aktiv := wert[bool](t, db, `SELECT a.aktiv FROM artikel a JOIN angebote an ON an.artikel_id = a.id
		WHERE an.lieferanten_nr = '1644755'`); !aktiv {
		t.Error("Sandwich wieder aktiv erwartet")
	}
}

func TestAbgleichSchutz(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	l := lieferant(t, db, "alltron")
	var zeilen []Zeile
	for i := 0; i < 1200; i++ {
		zeilen = append(zeilen, Zeile{LieferantenNr: fmt.Sprint(100000 + i), Bezeichnung: fmt.Sprint("Artikel ", i), EK: "1", Lagerbestand: 1})
	}
	b, err := Abgleichen(ctx, db, l, zeilen, nil, time.Time{})
	if err != nil || b.ArtikelNeu != 1200 {
		t.Fatalf("%+v %v", b, err)
	}
	_, err = Abgleichen(ctx, db, l, zeilen[:500], nil, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "unvollständig") {
		t.Fatalf("Schutz greift nicht: %v", err)
	}
	if n := wert[int](t, db, `SELECT count(*) FROM angebote WHERE aktiv`); n != 1200 {
		t.Errorf("trotz Schutz geändert: %d aktiv", n)
	}
}

func TestDienstDateiUndZeitplan(t *testing.T) {
	db := testDB(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	d := Neu(ctx, db, slog.New(slog.DiscardHandler))
	if err := d.LieferantAnlegen(ctx, "alltron", "Alltron", "competec"); err != nil {
		t.Fatal(err)
	}
	if err := d.LieferantAnlegen(ctx, "alltron", "Doppelt", ""); err == nil {
		t.Fatal("doppeltes Kürzel angenommen")
	}
	if err := d.ImportSpeichern(ctx, "alltron", Eingabe{Format: "competec", URL: "sftp://ftp.example.ch/ArtikelPreisdatenV2.csv",
		Benutzer: "u", Passwort: "geheim", Zeit: "05:15", Aktiv: true}); err != nil {
		t.Fatal(err)
	}
	// Passwort leer = unverändert
	if err := d.ImportSpeichern(ctx, "alltron", Eingabe{Format: "competec", URL: "sftp://ftp.example.ch/ArtikelPreisdatenV2.csv",
		Benutzer: "u", Zeit: "05:15", Aktiv: true}); err != nil {
		t.Fatal(err)
	}
	if pw := wert[string](t, db, `SELECT passwort FROM importe`); pw != "geheim" {
		t.Fatalf("Passwort: %q", pw)
	}
	q, _ := d.Lieferanten(ctx)
	if len(q) != 1 || !q[0].PasswortGesetzt || q[0].passwort != "" {
		t.Fatalf("Liste darf das Passwort nicht enthalten: %+v", q)
	}
	if err := d.ImportSpeichern(ctx, "alltron", Eingabe{Format: "competec", Zeit: "25:00"}); err == nil {
		t.Error("ungültige Zeit angenommen")
	}

	id, err := d.Starten("alltron", "datei", liste(stehsammler, brixies))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Starten("alltron", "datei", liste(stehsammler)); err == nil {
		t.Error("zweiter gleichzeitiger Lauf angenommen")
	}
	var lauf Lauf
	for i := 0; i < 100; i++ {
		l, _ := d.Laeufe(ctx, "alltron", 1)
		if len(l) == 1 && l[0].ID == id && l[0].Status != "laeuft" {
			lauf = l[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lauf.Status != "ok" || lauf.Bericht == nil || lauf.Bericht.ArtikelNeu != 2 {
		t.Fatalf("Lauf: %+v", lauf)
	}

	// Zeitplan: vor 05:15 nichts, danach einmal. Der Abruf scheitert hier, weil
	// kein Fingerabdruck bestätigt ist – ohne ihn verbindet der Hub nie.
	tag := time.Now()
	d.faelligeStarten(ctx, time.Date(tag.Year(), tag.Month(), tag.Day(), 5, 0, 0, 0, time.Local))
	time.Sleep(100 * time.Millisecond)
	if n := wert[int](t, db, `SELECT count(*) FROM import_laeufe`); n != 1 {
		t.Fatalf("vor der Zeit gestartet: %d Läufe", n)
	}
	// Der Datei-Lauf von eben war nach 05:15 → heute schon gelaufen, kein weiterer Start
	d.faelligeStarten(ctx, time.Date(tag.Year(), tag.Month(), tag.Day(), 23, 59, 0, 0, time.Local))
	time.Sleep(100 * time.Millisecond)
	if n := wert[int](t, db, `SELECT count(*) FROM import_laeufe`); n != 1 && time.Now().Hour() >= 6 {
		t.Fatalf("heute schon gelaufen, trotzdem gestartet: %d Läufe", n)
	}
	// Am nächsten Tag nach 05:15: Start, Fehler beim Abruf wird protokolliert
	morgen := tag.AddDate(0, 0, 1)
	d.faelligeStarten(ctx, time.Date(morgen.Year(), morgen.Month(), morgen.Day(), 5, 20, 0, 0, time.Local))
	var fehler Lauf
	for i := 0; i < 100; i++ {
		l, _ := d.Laeufe(ctx, "alltron", 1)
		if len(l) == 1 && l[0].Art == "zeitplan" && l[0].Status != "laeuft" {
			fehler = l[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if fehler.Status != "fehler" || !strings.Contains(fehler.Meldung, "Fingerabdruck") {
		t.Fatalf("Zeitplan-Lauf: %+v", fehler)
	}
}

// TestLast: Abgleich in der Grösse der Alltron-Liste. Nur mit ARTHUB_LASTTEST=1.
func TestLast(t *testing.T) {
	if os.Getenv("ARTHUB_LASTTEST") != "1" {
		t.Skip("ARTHUB_LASTTEST=1 setzen")
	}
	db := testDB(t)
	ctx := context.Background()
	l := lieferant(t, db, "alltron")
	var b strings.Builder
	for i := 0; i < 236000; i++ {
		fmt.Fprintf(&b, "%d\tX\tArtikel %d\tVariante\t%d\t0.5\t10\t%d.95\t\tWeb\tDetails zum Artikel %d\tHN-%d\tHersteller %d\t8.1\t15\t24\t01.01.26\tKategorie %d\t\t\t\t%013d\tsofort\tNein\n",
			1000000+i, i, i%50, 5+i%300, i, i, i%900, i%20, 7610000000000+int64(i))
	}
	daten := []byte(kopf + "\n" + b.String())
	t0 := time.Now()
	z, _, err := CompetecLesen(daten)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("lesen: %d Zeilen in %v (%d MB)", len(z), time.Since(t0).Round(time.Millisecond), len(daten)>>20)
	r, err := Abgleichen(ctx, db, l, z, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("erster Import: %+v", r)
	for i := 0; i < len(z); i += 7 { // jeder 7. Artikel neuer Bestand
		z[i].Lagerbestand++
	}
	r, err = Abgleichen(ctx, db, l, z, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("täglicher Import: %+v", r)
	repo := katalog.Repository{DB: db}
	for _, q := range []string{"artikel 12345", "hersteller 77", "7610000123456", "gibtsnicht"} {
		t0 := time.Now()
		liste, _, err := repo.Suche(ctx, q, 50, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("suche %q: %d Treffer in %v", q, len(liste), time.Since(t0).Round(time.Millisecond))
	}
}
