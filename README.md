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
| proxy   | 0.05 | 20 MB | Load balancer round-robin + /ready |
| api-1   | 0.475 | 165 MB | Detecção de fraude (IVF) |
| api-2   | 0.475 | 165 MB | Detecção de fraude (IVF) |
| Total   | 1.0 | 350 MB | — |

---

## Endpoints (porta 9999)

### `GET /ready`

Verificação de prontidão. O **proxy** consulta o `/ready` de cada backend (api-1 e api-2):

| Estado dos backends | Resposta |
|---|---|
| Todos respondem 2xx | **HTTP 200** `{"status":"ok","backends":[...]}` |
| Algum falha | **HTTP 503** `{"status":"degraded","backends":[...]}` |

Isoladamente, cada API também expõe `GET /ready` na porta 8080, respondendo 200 assim que o índice IVF pré-construído é carregado (menos de 1 segundo).

### `POST /fraud-score`

Processa a transação e retorna a decisão de fraude. O proxy distribui as requisições em round-robin entre api-1 e api-2. Consulte [docs/API.md](./docs/API.md) para o contrato completo.

---

## Estrutura do Projeto

```
cmd/
  api/main.go          # Servidor HTTP da API
  proxy/main.go        # Load balancer round-robin com /ready local
internal/
  model/types.go       # Tipos: payload, resposta, normalização
  vector/normalize.go  # Vetor 14-dim + quantização int8 + Manhattan
  index/index.go       # IVF Index (Inverted File Index)
  handler/fraud.go     # Handlers HTTP (/ready, /fraud-score)
  loader/loader.go     # Carregamento streaming + clustering IVF + serialização binária do índice
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
resources/             # Dataset + índice pré-construído
  references.json.gz   # 3M vetores rotulados (apenas para build)
  mcc_risk.json        # Risco por MCC
  normalization.json   # Constantes de normalização
  index.bin            # Índice IVF pré-construído (gerado no docker build)
Dockerfile.api         # Multi-stage: golang -> scratch
Dockerfile.proxy       # Multi-stage: golang -> scratch
docker-compose.yml     # Orquestração local (com build)
docker-compose.submission.yml # Orquestração para submission (Docker Hub)
Makefile               # build, test, docker, push, submission-prep
go.mod / go.sum        # Zero dependências externas
info.json              # Metadados da submissão
README.md              # Este arquivo
```

---

## Como Funciona

### 1. Startup (índice pré-construído)

No **Docker build**, a API carrega os 3M vetores de `references.json.gz` em modo streaming,
constrói o índice IVF com K-means (5 iterações em mini-batches) e serializa o resultado em
`/resources/index.bin` (formato binário, ~45MB).

No **startup do container**, a API apenas lê o `index.bin` do disco — leva **menos de 1 segundo**.
O servidor HTTP começa a responder `GET /ready` com 200 imediatamente.

Em desenvolvimento local (sem Docker), a API faz o carregamento completo do `references.json.gz`
como fallback.

### 2. Recebimento
`POST /fraud-score` recebe JSON com dados da transação.

### 3. Vetorização (14 dimensões)
Cada campo é normalizado para [0,1] seguindo as fórmulas em [REGRAS_DE_DETECCAO.md](./docs/REGRAS_DE_DETECCAO.md) e quantizado para int8 (0-127), reduzindo 4x o uso de memória.

### 4. Busca Vetorial (IVF Index)
- Encontra o cluster mais próximo entre 1.000 centroides
- Busca os 5 vizinhos mais próximos dentro desse cluster (~3.000 vetores)
- Usa distância Manhattan com loop unrolled

### 5. Decisão
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
| `make docker-build VERSION=v2` | Constrói imagens Docker com a versão |
| `make docker-push VERSION=v2` | Envia imagens para o Docker Hub |
| `make docker-tag-latest VERSION=v2` | Tagueia como latest e envia |
| `make docker-up` | docker compose up -d (build local) |
| `make docker-down` | docker compose down |
| `make docker-logs` | docker compose logs -f |
| `make docker-up-submission` | Sobe com docker-compose.submission.yml |
| `make submission-file VERSION=v2` | Gera docker-compose.yml para submission |
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
- Dockerfile.api: binário API (~5 MB) + dataset + índice IVF pré-construído (~45 MB)
- Dockerfile.proxy: binário Proxy (~3 MB)

Imagens finais: ~51 MB (API), ~3 MB (proxy), sem shell ou libc.

### Índice pré-construído
Durante o Docker build, a API executa `api -build-index /resources/index.bin` para gerar o índice IVF a partir do `references.json.gz`. Esse índice binário é copiado para a imagem final, eliminando o processamento de startup (~90s → <1s). Se o `index.bin` não existir (desenvolvimento local sem Docker), o carregamento completo do JSON é usado como fallback.

### Publicação no Docker Hub

```bash
make docker-build VERSION=v2
make docker-push VERSION=v2
make docker-tag-latest VERSION=v2  # opcional
```

---

## Submissão

> ⚠️ O repositório precisa ter ao menos **um commit** antes de criar a branch submission.

A branch `submission` é uma orphan branch independente — alterações na `main` **não** propagam automaticamente para ela. Siga o fluxo abaixo a cada nova versão.

### Fluxo completo (a partir da main)

```bash
# 1. Altera o código, testa, commita na main
git add .
git commit -m "feat: descrição da mudança"

# 2. Sobe nova versão para o Docker Hub
make docker-build VERSION=v2
make docker-push VERSION=v2

# 3. Gera o docker-compose.yml com a versão correta
make submission-file VERSION=v2 > /tmp/dc-submission.yml

# 4. Cria/atualiza a branch submission
git checkout --orphan submission            # primeira vez
# git checkout submission && git rm -r .    # atualização

cp /tmp/dc-submission.yml docker-compose.yml
git add docker-compose.yml Dockerfile.api Dockerfile.proxy info.json
git commit -m "submission: v2"
git push -u origin submission
git checkout main  # volta para a main
```

> O `docker-compose.yml` na branch submission **não** contém `build:` — ele referencia diretamente as imagens do Docker Hub (`paulohrpinheiro/rinha-api:v2`, etc.). Isso é necessário porque a submission não tem o código-fonte.

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
| 08 | Índice pré-construído | Startup <1s (index.bin gerado no Docker build) |
| 09 | 350 MB total | Proxy 20MB + 2xAPI 165MB |
| 10 | Só stdlib | Nenhuma dependência externa |
| 11 | Proxy serve /ready | Evita 502 enquanto APIs carregam |
| 12 | Imagens versionadas | Tag fixa evita cache no test runner |
| 13 | Streaming JSON loading | Evita OOM durante build do índice (114MB pico vs 165MB limite) |
| 14 | IVF index binário | Serialização/deserialização direta em disco (~45MB) |

---

## Licença

MIT

