-- 0001: esquema inicial (design, seção 5.6).
--
-- Convenções:
--   * datas/horas em INTEGER, segundos Unix em UTC;
--   * semana em TEXT no formato ISO 'AAAA-WW' (ex.: '2026-48');
--   * dia em TEXT 'AAAA-MM-DD' (UTC);
--   * idioma em código ISO 639-1 ('en', 'pt'...), 'und' quando indefinido;
--   * país estimado em ISO 3166-1 alfa-2 minúsculo, 'global' ou 'desconhecido'
--     (é sempre ESTIMATIVA, design 5.4);
--   * source: 'hn', 'bsky', 'gh' (sem CHECK, para novas fontes não exigirem
--     reconstruir a tabela).

-- Clusters de dor persistentes, criados e atualizados pela IA (ou import manual).
CREATE TABLE clusters (
    id                INTEGER PRIMARY KEY,
    pain              TEXT    NOT NULL,            -- nome da dor
    niche             TEXT    NOT NULL DEFAULT '', -- nicho
    summary           TEXT    NOT NULL DEFAULT '', -- resumo curto, enviado à IA no agrupamento
    first_seen_week   TEXT    NOT NULL,
    last_seen_week    TEXT    NOT NULL,
    total_mentions    INTEGER NOT NULL DEFAULT 0,
    last_score        INTEGER CHECK (last_score BETWEEN 0 AND 100),
    last_score_week   TEXT,
    last_score_detail TEXT,                        -- JSON: notas por critério, MVP, por que pode falhar
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
CREATE INDEX clusters_last_seen_week ON clusters (last_seen_week);

-- Histórico de menções por semana de cada cluster (para a tendência).
CREATE TABLE cluster_weeks (
    cluster_id INTEGER NOT NULL REFERENCES clusters (id) ON DELETE CASCADE,
    week       TEXT    NOT NULL,
    mentions   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (cluster_id, week)
) WITHOUT ROWID;
CREATE INDEX cluster_weeks_week ON cluster_weeks (week);

-- Candidatos (só eles guardam texto, truncado em ~2.000 caracteres).
CREATE TABLE items (
    id           INTEGER PRIMARY KEY,
    source       TEXT    NOT NULL,
    external_id  TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,                 -- quando o post foi publicado
    week         TEXT    NOT NULL,                 -- semana ISO de created_at
    collected_at INTEGER NOT NULL,
    text         TEXT    NOT NULL DEFAULT '',
    author_hash  TEXT    NOT NULL DEFAULT '',      -- nunca o autor em claro
    lang         TEXT    NOT NULL DEFAULT 'und',   -- idioma detectado
    lang_hint    TEXT    NOT NULL DEFAULT '',      -- idioma informado pela fonte
    country_est  TEXT    NOT NULL DEFAULT 'desconhecido',
    engagement   INTEGER NOT NULL DEFAULT 0,       -- pontos, likes ou reações
    pain_score   REAL    NOT NULL DEFAULT 0,
    signals      TEXT    NOT NULL DEFAULT '',      -- tipos de sinal casados, separados por vírgula
                                                   -- (ex.: 'request,payment,distribution')
    cluster_id   INTEGER REFERENCES clusters (id) ON DELETE SET NULL,
    UNIQUE (source, external_id)
);
-- Consultas do relatório: candidatos da semana por fonte, por idioma e por nota.
CREATE INDEX items_week_source ON items (week, source);
CREATE INDEX items_week_lang   ON items (week, lang);
CREATE INDEX items_week_score  ON items (week, pain_score DESC);
CREATE INDEX items_cluster     ON items (cluster_id) WHERE cluster_id IS NOT NULL;

-- Contagens agregadas (candidatos e não-candidatos). A chave primária começa
-- pelo dia, então as consultas por intervalo de dias (semana, 4 semanas
-- anteriores) usam a própria tabela, sem índice extra, o que poupa espaço.
CREATE TABLE daily_terms (
    day         TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    lang        TEXT    NOT NULL,
    country_est TEXT    NOT NULL,
    term        TEXT    NOT NULL,                  -- 1 a 3-grama normalizado
    count       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, source, lang, country_est, term)
) WITHOUT ROWID;

CREATE TABLE daily_brands (
    day         TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    lang        TEXT    NOT NULL,
    country_est TEXT    NOT NULL,
    brand       TEXT    NOT NULL,                  -- nome normalizado
    count       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, source, lang, country_est, brand)
) WITHOUT ROWID;

-- Cursor de retomada por fonte (timestamp ou ID, conforme a fonte).
CREATE TABLE cursors (
    source     TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Uma linha por execução (por fonte, no collect).
CREATE TABLE runs (
    id          INTEGER PRIMARY KEY,
    command     TEXT    NOT NULL,                  -- collect, report, import...
    source      TEXT    NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER,                           -- NULL = interrompida ou em andamento
    status      TEXT    NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'ok', 'partial', 'error')),
    items_seen  INTEGER NOT NULL DEFAULT 0,
    items_new   INTEGER NOT NULL DEFAULT 0,
    errors      INTEGER NOT NULL DEFAULT 0,
    error_text  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX runs_source_started ON runs (source, started_at);
CREATE INDEX runs_started        ON runs (started_at);

-- Uso de IA, para o limite semanal de tokens.
CREATE TABLE ai_usage (
    id                INTEGER PRIMARY KEY,
    created_at        INTEGER NOT NULL,            -- o limite semanal conta por aqui
    report_week       TEXT    NOT NULL,            -- semana do relatório processado
    stage             TEXT    NOT NULL,            -- group | score
    mode              TEXT    NOT NULL DEFAULT 'omniroute', -- omniroute | manual
    model             TEXT    NOT NULL DEFAULT '',
    provider          TEXT    NOT NULL DEFAULT '', -- quando o OmniRoute informar
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ai_usage_created     ON ai_usage (created_at);
CREATE INDEX ai_usage_report_week ON ai_usage (report_week);
