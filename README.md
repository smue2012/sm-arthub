# sm-arthub – Artikelkatalog-Hub

Ein eigenständiger Dienst, der die Handelsware (Waren und Softwarelizenzen) der
Lieferanten zentral führt. Alle internen Applikationen lesen über dieselbe
Lese-API, statt die Lieferanten selbst anzubinden.

- **Intern:** ein Mandant, kein öffentlicher Endpunkt, Zugriff nur mit API-Schlüssel je App
- **Nur Handelsware:** Dienstleistungen, Verkaufspreise und Margen bleiben in den Apps
- **Bestpreis:** tiefster EK eines verfügbaren Angebots (Lagerbestand > 0);
  bei gleichem EK gewinnt der höhere Lagerbestand. Ist kein Angebot verfügbar,
  kommt das günstigste mit `verfuegbar: false`.
- **Bilder:** nur Links auf die URLs der Lieferanten (`bild_urls` am Angebot)

Stack: Go 1.24 + PostgreSQL 16, Betrieb mit Docker/Portainer.

## Stand

- Etappe 1: Grundgerüst, Datenmodell, Lese-API, API-Schlüssel mit Verwaltungsoberfläche.
- Etappe 2: Import der Alltron-Preisliste (Competec `ArtikelPreisdatenV2.csv`) mit Bildliste,
  von Hand, als Datei oder täglich nach Zeitplan.
- Später: Brack, eigene Artikel, Pax8; Export für SelectLine als CSV/XML.

## Importe der Preislisten

In der Verwaltung unter **Lieferanten und Importe**:

1. **+ Lieferant**, z.B. Kürzel `alltron`, Name `Alltron AG`, Preisliste «Alltron / Competec».
2. Adresse (z.B. `sftp://ftp.competec.ch/ArtikelPreisdatenV2.csv`), Benutzer und Passwort eintragen
   (Zugang bei Competec über «Request for electronic services»). Das Passwort bleibt in der
   Datenbank des Hubs und wird nie an den Browser zurückgegeben.
3. **Verbindung prüfen** holt den Fingerabdruck des SFTP-Servers; übernehmen und speichern. Ohne
   bestätigten Fingerabdruck verbindet der Hub nie (Schutz gegen einen untergeschobenen Server).
4. Optional die Adresse der **Bildliste** (`Artikelnummer;Bild-Link`, gleicher Zugang).
5. **Jetzt importieren** oder **Datei hochladen …**; täglich z.B. um `05:15` mit eingeschaltetem
   Zeitplan (Competec stellt die Liste ab 4:30 Uhr bereit).

Abgleich (in einer Transaktion; gemessen mit 236'000 Zeilen: ~14 s beim ersten Mal, ~4 s täglich – auf langsameren Servern entsprechend länger):

- Bestehende Angebote (Lieferant + Lieferanten-Nr.): EK, Lagerbestand, Lieferfrist nachführen.
- Neue Angebote: Artikel über die EAN suchen (nur wenn eindeutig), sonst über Hersteller +
  Herstellernummer, sonst neuen Artikel anlegen. So landen gleiche Produkte verschiedener
  Lieferanten auf einem Artikel, und der Bestpreis vergleicht sie.
- Die Stammdaten (Bezeichnung usw.) pflegt der Lieferant, dessen Liste den Artikel angelegt hat.
- Nicht mehr gelistete Angebote werden inaktiv; Artikel ohne aktives Angebot ebenfalls.
- **Schutz:** Enthält die Liste weniger als die Hälfte der bisher aktiven Angebote (z.B. abgebrochene
  Übertragung), wird nichts geändert.

**Lagerbestandsliste** (nur Bestand, Competec: Spalten `Nummer 2`, `Lagerbestand`): eigene Adresse,
«Bestand alle … Minuten» (15–1440, 0 = aus) in einem Zeitfenster (Vorgabe 07:00–19:00) oder
«Bestand jetzt laden». Sie führt nur den Bestand bestehender Angebote nach (negativ = 0), deaktiviert
nichts und braucht wenige Sekunden. «Datei hochladen …» erkennt an der Kopfzeile, ob es die Preisliste
oder die Lagerbestandsliste ist.

Jeder Lauf steht unter **Letzte Importe** mit Ergebnis oder Fehlermeldung. Ein Klick auf einen
Lauf öffnet das **Protokoll**: neue Artikel, bestehenden Artikeln zugeordnet, EK geändert (alt → neu),
Stammdaten geändert, nicht mehr gelistet, wieder gelistet – je als Reiter, die ersten 500 Zeilen in der
Oberfläche, alle als CSV (Excel, Semikolon, UTF-8). Reine Bestandsänderungen werden nur gezählt.
Die Details werden 90 Tage aufbewahrt, die Zusammenfassung bleibt.

## Lese-API (Version 1)

Alle Aufrufe unter `/api/v1/` brauchen `Authorization: Bearer <schlüssel>`.
Unbekannte Parameter werden abgelehnt (400). Geändert wird nur rückwärtskompatibel.

| Aufruf | Antwort |
| --- | --- |
| `GET /api/v1/artikel?seit=<stand>&limit=<n>` | `{artikel, stand, mehr}` – Delta-Abgleich, `limit` 1–2000 (Vorgabe 500) |
| `GET /api/v1/artikel/{nummer}` | ein Artikel, 404 wenn unbekannt |
| `GET /api/v1/suche?q=<wörter>&limit=<n>&verfuegbar=1` | `{artikel, mehr}` – aktive Artikel, in denen jedes Wort vorkommt (Bezeichnung, Hersteller, Herstellernummer, EAN, Hub-Nummer, Lieferanten-Artikelnummer; Trigramm-Index); `limit` 1–200 (Vorgabe 50), `verfuegbar=1` nur an Lager |
| `GET /api/v1/lieferanten` | `{lieferanten}` |
| `GET /health` | `ok` (ohne Schlüssel, für Docker) |

Artikelnummern: `AH` + 7 Ziffern + Luhn-Prüfziffer, z.B. `AH00000018` (bis 9,9 Mio. Artikel).
Die Nummer ist eindeutig, ändert sich nie (auch nicht, wenn das beste Angebot den Lieferanten
wechselt) und dient als SelectLine-Artikelnummer; die Prüfziffer erkennt Tippfehler.
Prüfen in Go: `katalog.NummerGueltig`.

Die Suche nutzt die Tabelle `artikel_suche` mit Trigramm-Index (pg_trgm); sie ändert sich nur, wenn sich Bezeichnung, Hersteller, Nummern oder Angebote ändern – nicht bei Bestandsänderungen.

Ein Artikel enthält die Stammdaten, `bestes` (das massgebende Angebot oder
`null`) und `angebote` (alle aktiven, sortiert nach der Bestpreis-Regel).
Der EK kommt als Text (`"12.5000"`), damit nichts gerundet wird.

### Delta-Abgleich

Jede Änderung an einem Artikel oder an einem seiner Angebote gibt dem Artikel
einen neuen, steigenden `stand`. Eine App

1. holt beim ersten Mal mit `seit=0`,
2. fragt weiter mit `seit=<stand>`, solange `mehr` true ist,
3. merkt sich den letzten `stand` und fragt beim nächsten Lauf nur Änderungen ab.

Artikel werden nie gelöscht, nur `aktiv: false` gesetzt – so erfährt die App
den Wegfall. Auf Belegen speichern die Apps die Artikeldaten als Kopie, nie als
Verweis auf den Hub (GeBüV).

## Katalog & Auswahl je App

In der Verwaltung unter **Katalog & Auswahl**: Kategoriebaum (Competec «Kategorie 1–3») und
Artikelliste mit Suche, «nur an Lager» und bestem Angebot.

Je App (API-Schlüssel) lassen sich **Kategorien** (ganzer Zweig ab der gewählten Ebene) und
**Einzelartikel** anhaken. Mit **«Nur die Auswahl liefern»** erhält die App über die Lese-API nur diese
Artikel (Suche, Artikel per Nummer, Delta-Abgleich); ohne sieht sie den ganzen Katalog. Die Auswahl
wirkt sofort, die Apps brauchen nichts dafür. Hinweis Delta-Abgleich: Eine geänderte Auswahl gibt den
Artikeln keinen neuen Stand – Apps mit lokaler Kopie holen danach einmal mit `seit=0`.

## Verwaltungsoberfläche und API-Schlüssel

Die Verwaltung läuft auf einem eigenen Port (`ADMIN_PORT`, Vorgabe 8111),
getrennt von der Lese-API. Anmeldung mit `ARTHUB_ADMIN_PASSWORT`
(mindestens 12 Zeichen; ohne Passwort startet die Verwaltung nicht).

Dort werden die API-Schlüssel verwaltet: Jede App bekommt ihren eigenen
Schlüssel. Er wird beim Erzeugen genau einmal angezeigt (zum Kopieren in die
serverseitige Konfiguration der App); gespeichert ist nur der SHA-256-Hash.
«Neu erzeugen» ersetzt den alten Schlüssel, «Sperren» entzieht den Zugriff.

Schutz: Sitzungscookie `HttpOnly` und `SameSite=Strict` (hinter HTTPS auch
`Secure`, über `X-Forwarded-Proto`), Kopf `X-Arthub: 1` auf allen Aufrufen,
strenge CSP ohne Inline-Skript, falsches Passwort bremst 1 s.
Sitzungen liegen im Speicher und enden nach 8 Stunden ohne Aktivität oder mit
einem Neustart.

Notfall ohne Oberfläche (Portainer: Container → Console, `/bin/bash`):

```
/sm-arthub schluessel anlegen smerp
/sm-arthub schluessel liste
/sm-arthub schluessel sperren smerp
```

## Betrieb

`docker-compose.yml` als Stack in Portainer (Stacks → Add stack):

1. Im Stack die Variablen `ARTHUB_DB_PASSWORT` und `ARTHUB_ADMIN_PASSWORT`
   (beide frei wählen) und optional `ARTHUB_VERSION`, `ARTHUB_ADMIN_PORT` setzen.
   Passwörter ohne `$` wählen oder jedes `$` als `$$` schreiben – Compose liest
   `$` als Variable, sonst kommt ein anderes Passwort im Container an.
2. Der Stack legt das Netz `arthub` selbst an. Beide Netze haben feste
   Subnetze (`ARTHUB_NETZ_INTERN`, Vorgabe `10.250.250.0/28`, und
   `ARTHUB_NETZ`, Vorgabe `10.250.250.16/28`), weil die Standard-Adresspools
   von Docker aufgebraucht sein können («all predefined address pools have
   been fully subnetted»). Überschneidet sich das mit dem LAN oder einem VPN,
   andere Subnetze setzen.
3. Die Stacks der Apps hängen sich als externes Netz `arthub` daran und
   erreichen den Hub unter `http://sm-arthub:8110`

Das Image ist privat (ghcr.io): Portainer braucht die Registry ghcr.io mit
Zugang, wie für smerp.

4. Verwaltung unter `http://<host>:8111` öffnen, besser über NPM mit HTTPS

Umgebung des Programms: `DATABASE_URL` (Pflicht), `PORT` (Lese-API, Vorgabe
8110), `ADMIN_PORT` (Verwaltung, Vorgabe 8111), `ARTHUB_ADMIN_PASSWORT`.
Migrationen laufen beim Start automatisch (`internal/datenbank/migrationen`,
bestehende Dateien nie ändern, nur neue Nummern anlegen).

Images: Jeder Push auf `main` baut `ghcr.io/smue2012/sm-arthub:latest`
(und `main-<commit>`); ein Git-Tag `v26.11.1` baut `:26.11.1`.

## Entwicklung

```
go test ./...                                   # ohne Datenbank: DB-Tests werden übersprungen
ARTHUB_TEST_DB="postgres://postgres@localhost:5432/postgres?sslmode=disable" go test ./... -count=1
```

`ARTHUB_TEST_DB` zeigt auf eine bestehende Datenbank; der Benutzer muss
Datenbanken anlegen dürfen (jeder Test legt eine eigene an und löscht sie wieder).
Die CI (`.github/workflows/image.yml`) testet mit PostgreSQL 16 und baut erst
danach das Image.

## Aufbau

| Pfad | Inhalt |
| --- | --- |
| `cmd/sm-arthub` | Start, Healthcheck, Befehle für API-Schlüssel |
| `internal/datenbank` | Verbindung, Migrationen |
| `internal/katalog` | Artikel und Angebote lesen, Bestpreis-Regel, Auswahl je App, Kategoriebaum, Blättern |
| `internal/schluessel` | API-Schlüssel (nur Hash gespeichert) |
| `internal/api` | Lese-API v1 |
| `internal/importer` | Preislisten holen (sftp mit Fingerabdruck, ftps, https), Competec lesen, Abgleich, Zeitplan |
| `internal/verwaltung` | Verwaltungsoberfläche (Anmeldung, API-Schlüssel, Lieferanten/Importe, Status) |
