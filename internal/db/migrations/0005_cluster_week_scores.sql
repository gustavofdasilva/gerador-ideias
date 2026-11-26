-- 0005: nota de cada cluster por semana (design 5.2 passo 3 e seção 6).
--
-- A nota fica na semana em que foi dada, para que regerar um relatório antigo
-- não sobrescreva a nota mais recente (clusters.last_score continua sendo a
-- da semana mais nova pontuada).
ALTER TABLE cluster_weeks ADD COLUMN score INTEGER CHECK (score BETWEEN 0 AND 100);
ALTER TABLE cluster_weeks ADD COLUMN score_detail TEXT; -- JSON: notas, ajustes, canal, MVP, riscos
