-- Etappe 2: Importe der Lieferanten-Preislisten (zuerst Alltron/Competec).
--
-- importe        Zugang und Zeitplan je Lieferant (Passwort nur hier, nie an den Browser)
-- import_laeufe  Protokoll jedes Laufs mit Bericht
-- artikel.quelle_lieferant_id  Lieferant, dessen Liste die Stammdaten des Artikels
--                pflegt (der, der ihn angelegt hat); andere Lieferanten liefern
--                nur Angebote dazu.

ALTER TABLE artikel ADD COLUMN quelle_lieferant_id INTEGER REFERENCES lieferanten (id);

CREATE TABLE importe (
    lieferant_id  INTEGER PRIMARY KEY REFERENCES lieferanten (id),
    format        TEXT NOT NULL DEFAULT 'competec' CHECK (format IN ('competec')),
    url           TEXT NOT NULL DEFAULT '',
    benutzer      TEXT NOT NULL DEFAULT '',
    passwort      TEXT NOT NULL DEFAULT '',
    fingerabdruck TEXT NOT NULL DEFAULT '',      -- SHA256 des SFTP-Hostschlüssels
    bildliste_url TEXT NOT NULL DEFAULT '',
    zeit          TEXT NOT NULL DEFAULT ''        -- täglich HH:MM, leer = nur von Hand
                  CHECK (zeit = '' OR zeit ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    aktiv         BOOLEAN NOT NULL DEFAULT false  -- Zeitplan eingeschaltet
);

CREATE TABLE import_laeufe (
    id           BIGSERIAL PRIMARY KEY,
    lieferant_id INTEGER NOT NULL REFERENCES lieferanten (id),
    art          TEXT NOT NULL DEFAULT 'abruf' CHECK (art IN ('abruf', 'datei', 'zeitplan')),
    start        TIMESTAMPTZ NOT NULL DEFAULT now(),
    ende         TIMESTAMPTZ,
    status       TEXT NOT NULL DEFAULT 'laeuft' CHECK (status IN ('laeuft', 'ok', 'fehler')),
    schritt      TEXT NOT NULL DEFAULT '',
    datei_stand  TIMESTAMPTZ,
    bericht      JSONB,
    meldung      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX import_laeufe_lieferant ON import_laeufe (lieferant_id, start DESC);

-- Beim Massenimport setzt der Importer die Stände selbst (eine Aktualisierung
-- je Artikel statt je Angebot): SET LOCAL arthub.massenimport = '1'.
CREATE OR REPLACE FUNCTION angebot_geaendert() RETURNS trigger AS $$
BEGIN
    IF current_setting('arthub.massenimport', true) = '1' THEN
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE artikel SET geaendert = now() WHERE id = OLD.artikel_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND (TG_OP = 'INSERT' OR NEW.artikel_id <> OLD.artikel_id) THEN
        UPDATE artikel SET geaendert = now() WHERE id = NEW.artikel_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
