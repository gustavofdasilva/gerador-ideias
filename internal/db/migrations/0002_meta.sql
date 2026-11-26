-- 0002: valores internos do banco, como a chave secreta do hash de autores.
-- A chave fica no próprio banco (e vai junto nos backups), então os hashes
-- continuam consistentes depois de restaurar, mas não dá para recuperar o
-- nome de um autor testando nomes conhecidos sem ter o banco.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
