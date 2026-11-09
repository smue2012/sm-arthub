-- Protokoll je Importlauf (Wunsch Stephan, 09.11.2026): welche Artikel neu sind,
-- welche bestehenden zugeordnet wurden, welche EK sich geändert haben (alt → neu),
-- welche Stammdaten geändert wurden, welche Angebote weggefallen oder wieder
-- gelistet sind. Reine Bestandsänderungen werden nur gezählt (Bericht).
-- Aufbewahrung 90 Tage, die Zusammenfassung in import_laeufe bleibt.

CREATE TABLE import_details (
    lauf_id        BIGINT NOT NULL REFERENCES import_laeufe (id) ON DELETE CASCADE,
    art            TEXT NOT NULL CHECK (art IN ('artikel_neu', 'zugeordnet', 'preis', 'stammdaten', 'entfernt', 'wieder')),
    lieferanten_nr TEXT NOT NULL,
    artikel_nummer TEXT NOT NULL DEFAULT '',
    bezeichnung    TEXT NOT NULL DEFAULT '',
    alt            TEXT NOT NULL DEFAULT '',
    neu            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX import_details_lauf ON import_details (lauf_id, art);
