-- Grundlage des Artikelkatalog-Hubs: Lieferanten, Artikel, Lieferantenangebote,
-- API-Schlüssel der Applikationen.
--
-- Delta-Abgleich: Jede Änderung an einem Artikel oder an einem seiner Angebote
-- gibt dem Artikel einen neuen, steigenden «stand» (Sequenz aenderung_seq).
-- Eine App merkt sich den höchsten Stand, den sie gesehen hat, und fragt beim
-- nächsten Mal nur «seit=<stand>» ab. Artikel werden nie gelöscht, nur
-- inaktiv gesetzt, damit die Apps den Wegfall über den Abgleich erfahren.

CREATE SEQUENCE aenderung_seq;
CREATE SEQUENCE artikel_nummer_seq;

CREATE TABLE lieferanten (
    id       SERIAL PRIMARY KEY,
    kuerzel  TEXT NOT NULL UNIQUE,          -- z.B. alltron, brack, pax8, eigen
    name     TEXT NOT NULL,
    aktiv    BOOLEAN NOT NULL DEFAULT true,
    erstellt TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE artikel (
    id            BIGSERIAL PRIMARY KEY,
    nummer        TEXT NOT NULL UNIQUE
                  DEFAULT ('AH' || lpad(nextval('artikel_nummer_seq')::text, 7, '0')),
    art           TEXT NOT NULL DEFAULT 'ware' CHECK (art IN ('ware', 'software')),
    bezeichnung   TEXT NOT NULL,
    beschreibung  TEXT NOT NULL DEFAULT '',
    hersteller    TEXT NOT NULL DEFAULT '',
    hersteller_nr TEXT NOT NULL DEFAULT '',
    ean           TEXT NOT NULL DEFAULT '',
    kategorie     TEXT NOT NULL DEFAULT '',
    gewicht_g     INTEGER,                   -- für den Postversand
    laenge_mm     INTEGER,
    breite_mm     INTEGER,
    hoehe_mm      INTEGER,
    aktiv         BOOLEAN NOT NULL DEFAULT true,
    stand         BIGINT NOT NULL DEFAULT nextval('aenderung_seq'),
    erstellt      TIMESTAMPTZ NOT NULL DEFAULT now(),
    geaendert     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX artikel_stand ON artikel (stand);
CREATE INDEX artikel_ean ON artikel (ean) WHERE ean <> '';
CREATE INDEX artikel_hersteller_nr ON artikel (hersteller, hersteller_nr) WHERE hersteller_nr <> '';

-- Ein Angebot je Artikel und Lieferant. Bilder bleiben beim Lieferanten:
-- gespeichert werden nur die Links (bild_urls).
CREATE TABLE angebote (
    id               BIGSERIAL PRIMARY KEY,
    artikel_id       BIGINT NOT NULL REFERENCES artikel (id),
    lieferant_id     INTEGER NOT NULL REFERENCES lieferanten (id),
    lieferanten_nr   TEXT NOT NULL,
    ek               NUMERIC(12, 4) NOT NULL CHECK (ek >= 0),
    waehrung         TEXT NOT NULL DEFAULT 'CHF',
    lagerbestand     INTEGER NOT NULL DEFAULT 0,
    lieferfrist_tage INTEGER,
    bild_urls        TEXT[] NOT NULL DEFAULT '{}',
    aktiv            BOOLEAN NOT NULL DEFAULT true,
    stand_lieferant  TIMESTAMPTZ,            -- Datum der Lieferantendatei
    geaendert        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (lieferant_id, lieferanten_nr)
);
CREATE INDEX angebote_artikel ON angebote (artikel_id);

-- Jede Änderung am Artikel gibt einen neuen Stand.
CREATE FUNCTION artikel_neuer_stand() RETURNS trigger AS $$
BEGIN
    NEW.stand := nextval('aenderung_seq');
    NEW.geaendert := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER artikel_stand BEFORE UPDATE ON artikel
    FOR EACH ROW EXECUTE FUNCTION artikel_neuer_stand();

-- Ändert sich ein Angebot, bekommt sein Artikel einen neuen Stand
-- (über das UPDATE greift der Trigger oben).
CREATE FUNCTION angebot_geaendert() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE artikel SET geaendert = now() WHERE id = OLD.artikel_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND (TG_OP = 'INSERT' OR NEW.artikel_id <> OLD.artikel_id) THEN
        UPDATE artikel SET geaendert = now() WHERE id = NEW.artikel_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER angebote_stand AFTER INSERT OR UPDATE OR DELETE ON angebote
    FOR EACH ROW EXECUTE FUNCTION angebot_geaendert();

-- API-Schlüssel je Applikation. Gespeichert wird nur der SHA-256-Hash,
-- der Schlüssel selbst wird beim Anlegen einmal angezeigt.
CREATE TABLE api_schluessel (
    id       SERIAL PRIMARY KEY,
    name     TEXT NOT NULL UNIQUE,           -- Applikation, z.B. smerp, smshop
    hash     BYTEA NOT NULL UNIQUE,
    aktiv    BOOLEAN NOT NULL DEFAULT true,
    erstellt TIMESTAMPTZ NOT NULL DEFAULT now(),
    zuletzt  TIMESTAMPTZ
);
