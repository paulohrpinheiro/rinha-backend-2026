# Rinha de Backend 2026 — Detecção de Fraude por Busca Vetorial

API de detecção de fraude usando **busca vetorial com IVF index** em **Go 1.26.3** com **zero dependências externas**.

> **Stack**: Go 1.26.3 · IVF · Manhattan · int8 quantization
> **Limites**: 1 CPU · 350 MB RAM · 3 serviços (proxy + 2 APIs)
> **Porta**: 9999 (proxy)

---

## Arquitetura

Client -> Proxy -> API #1 e API #2 (round-robin)

| Serviço | CPU | Memória | Função |
|---------|:---:|:-------:|--------|
| proxy   | 0.05 | 20 MB | Load balancer round-robin |
| api-1   | 0.475 | 165 MB | Detecção de fraude (IVF) |
| api-2   | 0.475 | 165 MB | Detecção de fraude (IVF) |
| Total   | 1.0 | 350 MB | — |

---

## Estrutura do Projeto

```
cmd/
  api/main.go          # Servidor HTTP da API
  proxy/main.go        # Load balancer round-robin
internal/
  model/types.go       # Tipos: payload, resposta, normalização
  vector/normalize.go  # Vetor 14-dim + quantização int8 + Manhattan
  index/index.go       # IVF Index (Inverted File Index)
  handler/fraud.go     # Handlers HTTP (/ready, /fraud-score)
  loader/loader.go     # Carregamento do dataset + clustering
  *_test.go            # Testes unitários e benchmarks
docs/
  DECISOES.md          # Decisões arquiteturais (contexto para IA)
  REGRAS_DE_DETECCAO.md# Fórmulas das 14 dimensões
  API.md               # Contrato da API
  ARQUITETURA.md       # Limites de CPU/memória
  AVALIACAO.md         # Fórmula de pontuação
  BUSCA_VETORIAL.md    # Introdução à busca vetorial
  DATASET.md           # Formato dos arquivos de referência
  SUBMISSAO.md         # Passo a passo da submissão
resources/             # Dataset (copiado no build Docker)
  references.json.gz   # 3M vetores rotulados
  mcc_risk.json        # Risco por MCC
  normalization.json   # Constantes de normalização
Dockerfile.api         # Multi-stage: golang -> scratch
Dockerfile.proxy       # Multi-stage: golang -> scratch
docker-compose.yml     # Orquestração: proxy + 2 APIs
Makefile               # build, test, docker, etc.
go.mod / go.sum        # Zero dependências externas
info.json              # Metadados da submissão
README.md              # Este arquivo
```

---

## Como Funciona

### 1. Recebimento
`POST /fraud-score` recebe JSON com dados da transação.

### 2. Vetorização (14 dimensões)
Cada campo é normalizado para [0,1] seguindo as fórmulas em [REGRAS_DE_DETECCAO.md](./docs/REGRAS_DE_DETECCAO.md) e quantizado para int8 (0-127), reduzindo 4x o uso de memória.

### 3. Busca Vetorial (IVF Index)
- Encontra o cluster mais próximo entre 1.000 centroides
- Busca os 5 vizinhos mais próximos dentro desse cluster (~3.000 vetores)
- Usa distância Manhattan com loop unrolled

### 4. Decisão
```
fraud_score = fraudes_entre_os_5 / 5
approved = fraud_score < 0.6
```

---

## Pré-requisitos

- Go 1.26.3+
- Docker Engine + Compose
- make

### Recursos do Dataset
Baixe do [repositório oficial da Rinha](https://github.com/zanfranceschi/rinha-de-backend-2026) e coloque em `resources/`:
- references.json.gz
- mcc_risk.json
- normalization.json

---

## Comandos Make

| Comando | Descrição |
|---------|-----------|
| `make build` | Compila binários estripados em bin/ |
| `make test` | Roda todos os testes |
| `make bench` | Roda benchmarks |
| `make docker-build` | Constrói imagens Docker |
| `make docker-up` | docker compose up -d |
| `make docker-down` | docker compose down |
| `make docker-logs` | docker compose logs -f |
| `make clean` | Remove bin/ |
| `make all` | build + test |

---

## Testes e Benchmarks

### Testes (12 testes, 3 pacotes)
- internal/vector: Quantize, ManhattanDistance, Normalize
- internal/index: IVF Search, empty index, exact match
- internal/handler: GET /ready, POST /fraud-score, invalid JSON

### Benchmarks (zero alocações)
| Operação | Tempo | Alocações |
|----------|:-----:|:---------:|
| ManhattanDistance (14 dims) | ~14 ns | 0 B/op |
| Normalize (payload -> vetor) | ~100 ns | 0 B/op |

---

## Docker

Multi-stage build com scratch:
- Dockerfile.api: binário API (~5 MB) + dataset
- Dockerfile.proxy: binário Proxy (~3 MB)

Imagens finais: ~10-15 MB, sem shell ou libc.

---

## Submissão

> ⚠️ O repositório precisa ter ao menos **um commit** antes de criar a branch submission.

### 1. Commit inicial na main

```bash
git checkout -b main
git add .
git commit -m "feat: initial implementation"
git push -u origin main
```

### 2. Criar branch submission (orphan)

```bash
git checkout --orphan submission
git rm -r .
git add docker-compose.yml Dockerfile.* info.json
git commit -m "submission: deployment files"
git push -u origin submission
git checkout main  # volta para a main
```

> **Importante**: `git rm -r .` falha com *"pathspec '.' did not match any files"* se não houver commit anterior. O comando só remove arquivos **trackeados** (que estão no índice git). Primeiro commite na main, depois crie a orphan branch.

---

## Decisões Arquiteturais

Documentadas em **[docs/DECISOES.md](./docs/DECISOES.md)** — arquivo de contexto para interações com IA.

| ADR | Decisão | Motivo |
|:---:|---------|--------|
| 01 | Quantização int8 | 4x menos memória que float64 |
| 02 | IVF Index | 1000x mais rápido que brute force |
| 03 | Distância Manhattan | Sem multiplicações, correlação > 99% |
| 04 | Proxy na stdlib | Zero dependências, binário ~3 MB |
| 05 | Stripped binaries | -ldflags="-s -w" -trimpath — 60% menor |
| 06 | Docker scratch | Imagem ~10 MB, sem shell/libc |
| 07 | Orphan branch | submission sem histórico com main |
| 08 | Pré-carregamento | Startup lento, zero CPU durante teste |
| 09 | 350 MB total | Proxy 20MB + 2xAPI 165MB |
| 10 | Só stdlib | Nenhuma dependência externa |

---

## Licença

MIT
