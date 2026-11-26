-- 0004: armazenamento econômico (design 5.1 passo 7).
--
-- Só os candidatos ficam em items. Para não contar duas vezes um
-- não-candidato que volta na sobreposição de janelas (ou quando uma janela
-- com erro é repetida), guarda-se só uma impressão de 64 bits do ID dele.
-- As linhas antigas são apagadas assim que o cursor da fonte passa delas.
CREATE TABLE seen (
    source     TEXT    NOT NULL,
    key        INTEGER NOT NULL, -- 64 bits do SHA-256 do external_id
    created_at INTEGER NOT NULL, -- data do post, usada na limpeza
    PRIMARY KEY (source, key)
) WITHOUT ROWID;
CREATE INDEX seen_source_created ON seen (source, created_at);

-- Quantos itens foram processados e quantos viraram candidatos, por dia
-- (do post), fonte, idioma e país estimado: base da taxa de candidatos.
CREATE TABLE daily_items (
    day         TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    lang        TEXT    NOT NULL,
    country_est TEXT    NOT NULL,
    items       INTEGER NOT NULL DEFAULT 0,
    candidates  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, source, lang, country_est)
) WITHOUT ROWID;
