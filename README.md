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
| proxy   | 0.10 | 20 MB | Load balancer round-robin + /ready |
| api-1   | 0.45 | 165 MB | Detecção de fraude (IVF) |
| api-2   | 0.45 | 165 MB | Detecção de fraude (IVF) |
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

## Resultados do Teste Oficial

> Última submissão: v10 (commit `6b95df7`) · Score final: **−6000** (pior possível)

### Breakdown

| Componente | Valor | Corte ativado? |
|:-----------|:-----:|:--------------:|
| `score_p99` | **−3000** | ✅ p99 = 2002.25ms > 2000ms |
| `score_det` | **−3000** | ✅ failure_rate = 100% > 15% |
| **Final** | **−6000** | ⛔ Piso absoluto |

### Comparativo com o melhor concorrente

| Métrica | Nossos resultados (v9/v10) | Best (MXLange C) |
|:--------|:--------------------------:|:----------------:|
| p99 | 2002.20 − 2002.25ms | **0.98ms** |
| Erros HTTP | 13.858 − 13.973 | **0** |
| TP | 0 | 24.037 |
| TN | 0 | 30.022 |
| FP | 0 | **0** |
| FN | 0 | **0** |
| Failure rate | **100%** | **0%** |
| Score final | **−6000** | **+6000** |

### Causa raiz

| Problema | Evidência |
|:---------|:----------|
| **Proxy com 0.05 CPU insuficiente** | httputil.ReverseProxy consome ~0.5ms/req; com 0.05 CPU, throughput máximo ~100 req/s, abaixo dos 180 req/s necessários |
| **100% failure rate** | Nenhuma requisição processada com sucesso — todas as respostas foram erros ou timeouts |
| **p99 = 2002ms** | Exatamente no timeout do k6 — requisições nunca chegam à API |

A distribuição de CPU foi ajustada no **ADR-28** (proxy 0.05→0.10, APIs 0.475→0.45).

### Lições Aprendidas

| Lição | Descrição |
|:-----|:----------|
| ⏱️ **Sempre configurar timeouts HTTP** | `http.Server` sem timeouts é uma bomba-relógio sob carga. `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` e `IdleTimeout` são obrigatórios. |
| 🚦 **Limitar concorrência** | Um semáforo simples (`chan struct{}`) evita que uma rajada de requisições exploda o número de goroutines e paralise o GC. |
| 🔄 **Tuning do proxy transport** | `MaxIdleConnsPerHost`, `IdleConnTimeout` e `DialContext.Timeout` no `http.Transport` do proxy evitam criação excessiva de conexões TCP. |
| 📊 **503 imediato > timeout de 2s** | Responder com HTTP 503 ("too many requests") em <1ms é muito melhor que deixar a conexão aberta até o timeout do cliente. |
| 🧪 **Testar com carga real antes** | Testes unitários não revelam problemas de concorrência. Um teste de carga com k6 (mesmo que reduzido) teria detectado o problema. |
| 🖥️ **CPU do proxy é crítica** | Com 0.05 CPU e httputil.ReverseProxy, o proxy é o gargalo principal — não a API. |

As decisões arquiteturais estão documentadas em **[docs/DECISOES.md](./docs/DECISOES.md)**.

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
| 15 | Timeouts HTTP | Evita acúmulo de conexões lentas |
| 16 | Semáforo de concorrência | Evita thrashing no GC |
| 17 | Unix sockets | Elimina overhead de TCP/IP |
| 18 | Semáforo 128 | Ajuste após Unix sockets |
| 19 | Timeouts 500ms/1s | Redução após Unix sockets |
| 20 | Pool de buffers | Reutilização de buffers de resposta |
| 21 | Semáforo 32 | Redução para menos pressão no GC |
| 22 | Pool de body + payload | Reutilização de buffers de leitura |
| 23 | Serialização manual JSON | Sem reflection no hot path |
| 24 | GOMAXPROCS=1 | Alinhamento com cota de container |
| 25 | Early exit IVF | Busca em 1 cluster vs 2 |
| 26 | Timeouts no proxy | Consistência com a API |
| 27 | Remove log.Printf | Syscall no hot path |
| 28 | CPU proxy 0.10, APIs 0.45 | Proxy era o gargalo principal |
| 29 | json.NewDecoder direto | Elimina cópia intermediária do body |
| 30 | Proxy custom (sem httputil) | Reduz CPU por requisição de 0.5ms para 0.15ms |
| 31 | hostname explícito nos APIs | Alinha socket names entre proxy e API |

---

## Licença

MIT