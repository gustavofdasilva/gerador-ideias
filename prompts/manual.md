# Gerador de ideias — semana {{SEMANA}} (modo manual)

Você vai processar os candidatos da semana {{SEMANA}} em duas etapas e
devolver **uma única resposta JSON** com o resultado das duas.

{{PARTES}}

## O que fazer

1. **Agrupar** todos os candidatos, seguindo a etapa "Agrupar dores" abaixo.
2. **Pontuar** as até {{TOP}} dores mais relevantes da semana, seguindo a etapa
   "Pontuar dores" abaixo. Mais relevantes = as que receberam mais candidatos
   nesta semana (empate: maior soma de `score` dos candidatos). Podem ser
   clusters existentes (`c12`, …) ou novos (`novo1`, …). Use também o histórico
   dos clusters existentes (menções e semanas) para a nota de frequência.

## Resposta

Responda **só** com um bloco de código JSON, sem texto antes ou depois,
juntando as duas etapas:

```json
{
  "novos_clusters": [ … ],
  "atribuicoes": [ … ],
  "pontuacoes": [ … ]
}
```

O JSON será validado por um programa: siga exatamente os formatos das duas
etapas.

---

{{AGRUPAR}}

---

{{PONTUAR}}
