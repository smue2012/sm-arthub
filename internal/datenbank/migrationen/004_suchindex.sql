-- Schnelle Suche bei 200'000+ Artikeln: Trigramm-Index (pg_trgm, in PostgreSQL
-- enthalten) über einen Suchtext aus Bezeichnung, Hersteller, Herstellernummer,
-- EAN und Hub-Nummer; dazu die Lieferanten-Artikelnummern der Angebote.
-- Die lange Beschreibung wird nicht durchsucht (langsam, viele Fehltreffer).

CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE artikel ADD COLUMN suchtext TEXT GENERATED ALWAYS AS (
    lower(bezeichnung || ' ' || hersteller || ' ' || hersteller_nr || ' ' || ean || ' ' || nummer)
) STORED;

CREATE INDEX artikel_suchtext ON artikel USING gin (suchtext gin_trgm_ops);
CREATE INDEX angebote_lieferanten_nr_trgm ON angebote USING gin (lieferanten_nr gin_trgm_ops);
