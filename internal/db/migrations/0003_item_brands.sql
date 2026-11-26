-- 0003: marcas associadas a cada item, separadas por vírgula.
-- Na F2 vem só do GitHub (owner/repo); a F3 completa com a extração do texto.
ALTER TABLE items ADD COLUMN brands TEXT NOT NULL DEFAULT '';
