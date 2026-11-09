-- Suchindex in eine eigene Tabelle (09.11.2026): Bisher lag der Trigramm-Index
-- auf artikel und angebote. Jede tägliche Bestandsänderung schreibt Artikel
-- (neuer Stand) und Angebot neu – und damit jedes Mal alle Trigramme. Auf
-- Stephans Server verlangsamte das den Import stark.
--
-- artikel_suche hält den Suchtext (Bezeichnung, Hersteller, Herstellernummer,
-- EAN, Hub-Nummer und die Lieferanten-Nummern der Angebote) und ändert sich nur,
-- wenn sich einer dieser Werte ändert. Trigger halten sie aktuell; beim
-- Massenimport frischt der Importer sie selbst auf (eine Anweisung).

DROP INDEX IF EXISTS artikel_suchtext;
DROP INDEX IF EXISTS angebote_lieferanten_nr_trgm;
ALTER TABLE artikel DROP COLUMN IF EXISTS suchtext;

CREATE TABLE artikel_suche (
    artikel_id BIGINT PRIMARY KEY REFERENCES artikel (id) ON DELETE CASCADE,
    suchtext   TEXT NOT NULL
);

CREATE FUNCTION artikel_suche_auffrischen(ids BIGINT[]) RETURNS void AS $$
    INSERT INTO artikel_suche (artikel_id, suchtext)
    SELECT a.id, lower(a.bezeichnung || ' ' || a.hersteller || ' ' || a.hersteller_nr || ' ' || a.ean || ' ' ||
           a.nummer || ' ' || coalesce((SELECT string_agg(an.lieferanten_nr, ' ' ORDER BY an.lieferanten_nr)
                                        FROM angebote an WHERE an.artikel_id = a.id), ''))
    FROM artikel a WHERE a.id = ANY (ids)
    ON CONFLICT (artikel_id) DO UPDATE SET suchtext = EXCLUDED.suchtext
        WHERE artikel_suche.suchtext IS DISTINCT FROM EXCLUDED.suchtext;
$$ LANGUAGE sql;

CREATE FUNCTION artikel_suche_artikel() RETURNS trigger AS $$
BEGIN
    IF current_setting('arthub.massenimport', true) = '1' THEN
        RETURN NULL;
    END IF;
    PERFORM artikel_suche_auffrischen(ARRAY[NEW.id]);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER artikel_suche AFTER INSERT OR UPDATE OF bezeichnung, hersteller, hersteller_nr, ean, nummer
    ON artikel FOR EACH ROW EXECUTE FUNCTION artikel_suche_artikel();

CREATE FUNCTION artikel_suche_angebot() RETURNS trigger AS $$
BEGIN
    IF current_setting('arthub.massenimport', true) = '1' THEN
        RETURN NULL;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM artikel_suche_auffrischen(ARRAY[OLD.artikel_id]);
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM artikel_suche_auffrischen(ARRAY[NEW.artikel_id]);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER angebote_suche AFTER INSERT OR DELETE OR UPDATE OF lieferanten_nr, artikel_id
    ON angebote FOR EACH ROW EXECUTE FUNCTION artikel_suche_angebot();

-- Bestand füllen, dann den Index in einem Zug bauen (schneller als Zeile für Zeile)
INSERT INTO artikel_suche (artikel_id, suchtext)
SELECT a.id, lower(a.bezeichnung || ' ' || a.hersteller || ' ' || a.hersteller_nr || ' ' || a.ean || ' ' ||
       a.nummer || ' ' || coalesce(o.nrn, ''))
FROM artikel a
LEFT JOIN (SELECT artikel_id, string_agg(lieferanten_nr, ' ' ORDER BY lieferanten_nr) AS nrn
           FROM angebote GROUP BY artikel_id) o ON o.artikel_id = a.id;

CREATE INDEX artikel_suche_trgm ON artikel_suche USING gin (suchtext gin_trgm_ops);
