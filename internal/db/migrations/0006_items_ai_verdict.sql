-- 0006: veredito da IA sobre cada candidato julgado (base do "ideias calibrate").
--   NULL = não foi enviado à IA (além de ai.max_items) ou a semana não passou pela IA;
--   1    = a IA ligou o candidato a uma dor;
--   0    = a IA descartou como "não é dor" (null na resposta).
ALTER TABLE items ADD COLUMN ai_verdict INTEGER CHECK (ai_verdict IN (0, 1));
