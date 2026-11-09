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

Etappe 1: Grundgerüst, Datenmodell, Lese-API, API-Schlüssel.
Noch keine Importer – Daten kommen in den nächsten Etappen (Alltron, Brack,
eigene Artikel, Pax8; Export für SelectLine als CSV/XML später).

## Lese-API (Version 1)

Alle Aufrufe unter `/api/v1/` brauchen `Authorization: Bearer <schlüssel>`.
Unbekannte Parameter werden abgelehnt (400). Geändert wird nur rückwärtskompatibel.

| Aufruf | Antwort |
| --- | --- |
| `GET /api/v1/artikel?seit=<stand>&limit=<n>` | `{artikel, stand, mehr}` – Delta-Abgleich, `limit` 1–2000 (Vorgabe 500) |
| `GET /api/v1/artikel/{nummer}` | ein Artikel, 404 wenn unbekannt |
| `GET /api/v1/lieferanten` | `{lieferanten}` |
| `GET /health` | `ok` (ohne Schlüssel, für Docker) |

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

## API-Schlüssel

Jede App bekommt ihren eigenen Schlüssel. Gespeichert wird nur der SHA-256-Hash;
der Schlüssel wird beim Anlegen einmal angezeigt und gehört danach nur in die
serverseitige Konfiguration der App.

In Portainer: Containers → `sm-arthub` → Console → Connect (Befehl `/bin/bash`), dann:

```
/sm-arthub schluessel anlegen smerp
/sm-arthub schluessel liste
/sm-arthub schluessel sperren smerp
```

Erneutes `anlegen` für dieselbe App ersetzt den alten Schlüssel.

## Betrieb

`docker-compose.yml` als Stack in Portainer (Stacks → Add stack):

1. Im Stack die Variablen `ARTHUB_DB_PASSWORT` (frei wählen) und optional
   `ARTHUB_VERSION` setzen
2. Der Stack legt das Netz `arthub` selbst an
3. Die Stacks der Apps hängen sich als externes Netz `arthub` daran und
   erreichen den Hub unter `http://sm-arthub:8110`

Das Image ist privat (ghcr.io): Portainer braucht die Registry ghcr.io mit
Zugang, wie für smerp.

Umgebung des Programms: `DATABASE_URL` (Pflicht), `PORT` (Vorgabe 8110).
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
| `internal/katalog` | Artikel und Angebote lesen, Bestpreis-Regel |
| `internal/schluessel` | API-Schlüssel (nur Hash gespeichert) |
| `internal/api` | Lese-API v1 |
