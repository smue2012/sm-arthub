-- Lagerbestandsliste und Auswahl je App (Wünsche Stephan, 09.11.2026)

-- 1. Lagerbestandsliste: eigene, schnelle Läufe nur für den Bestand, mehrmals
--    täglich in einem Zeitfenster (Competec: «Nummer 2», «Lagerbestand»).
ALTER TABLE importe
    ADD COLUMN bestand_url       TEXT NOT NULL DEFAULT '',
    ADD COLUMN bestand_intervall INTEGER NOT NULL DEFAULT 0      -- Minuten, 0 = kein Zeitplan
        CHECK (bestand_intervall = 0 OR bestand_intervall BETWEEN 15 AND 1440),
    ADD COLUMN bestand_von       TEXT NOT NULL DEFAULT '07:00'
        CHECK (bestand_von ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    ADD COLUMN bestand_bis       TEXT NOT NULL DEFAULT '19:00'
        CHECK (bestand_bis ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$');

ALTER TABLE import_laeufe DROP CONSTRAINT import_laeufe_art_check;
ALTER TABLE import_laeufe ADD CONSTRAINT import_laeufe_art_check
    CHECK (art IN ('abruf', 'datei', 'zeitplan', 'bestand', 'bestand_zeitplan'));

-- 2. Kategorien in drei Ebenen (Competec «Kategorie 1–3»); kategorie = Ebene 1
ALTER TABLE artikel
    ADD COLUMN kategorie2 TEXT NOT NULL DEFAULT '',
    ADD COLUMN kategorie3 TEXT NOT NULL DEFAULT '';
CREATE INDEX artikel_kategorien ON artikel (kategorie, kategorie2, kategorie3) WHERE aktiv;

-- 3. Auswahl je App: Ohne «nur_auswahl» sieht eine App den ganzen Katalog.
--    Mit «nur_auswahl» nur die angehakten Kategorien (ganzer Zweig ab der
--    gewählten Ebene, Pfad «Ebene 1 > Ebene 2 > Ebene 3») und Einzelartikel
--    (Hub-Nummer).
ALTER TABLE api_schluessel ADD COLUMN nur_auswahl BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE auswahl (
    app  TEXT NOT NULL REFERENCES api_schluessel (name) ON DELETE CASCADE,
    art  TEXT NOT NULL CHECK (art IN ('kategorie', 'artikel')),
    wert TEXT NOT NULL,
    PRIMARY KEY (app, art, wert)
);
