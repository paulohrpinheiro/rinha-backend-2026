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
| proxy   | 0.15 | 20 MB | Load balancer round-robin + /ready + JSON→binário |
| api-1   | 0.425 | 165 MB | Detecção de fraude (IVF, protocolo binário) |
| api-2   | 0.425 | 165 MB | Detecção de fraude (IVF, protocolo binário) |
| Total   | 1.0 | 350 MB | — |

**Protocolo**: O proxy recebe JSON do cliente, faz o parsing, codifica em formato
binário compacto (~80-130 bytes), e envia para as APIs via Unix socket. As APIs
leem o binário diretamente (zero alocações de parsing) e respondem com 9 bytes
binários. O proxy decodifica e serializa a resposta JSON para o cliente.

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

## Resultados do Teste Oficial (Evolução)

> Última submissão: v24 (commit `c3edb96`) · Score final: **−6000** (pior possível)

### Resultados reais (v24)

| Componente | Valor | Corte |
|:-----------|:-----:|:-----:|
| `score_p99` | **−3000** | ✅ p99 = 2001.85ms > 2000ms |
| `score_det` | **−3000** | ✅ failure_rate = 99.28% > 15% |
| **Final** | **−6000** | ⛔ Piso absoluto |

### Evolução completa

| Versão | Abordagem | Erros | OK | p99 | Score |
|:------|:----------|:-----:|:--:|:---:|:-----:|
| v10 | TCP, httputil, sem timeouts | 13.858 | 0 | 2002ms | −6000 |
| v16 | codec binário | 52.601 | 1.370 | 1042ms | −3018 |
| v20 | proxy custom, semáforo 16 (503) | 50.368 | 3.691 | **501ms** | **−2700** |
| v21 | semáforo removido | 49.706 | 326 | 2002ms | −6000 |
| v22 | semáforo bloqueante 128 | 42.949 | 294 | 2002ms | −6000 |
| v24 | + DecodeBytes fix | 44.800 | 329 | 2002ms | −6000 |
| v25 | não-bloqueante 1024 + respostas pré-aloc + warmup | ~0* | ~54k* | ~10-50ms* | >+5000* |
| v26 | v25 + K-means corrigido + nprobe=3 | 0** | 5000** | 97ms** | — |

**Benchmark local v26 (commit c751a1e): 5000 reqs, concurrency 20, zero falhas**

### Comparativo com o melhor concorrente

| Métrica | Best (MXLange C) | v24 (último real) | v25 (esperado*) |
|:--------|:----------------:|:----------------:|:----------------:|
| p99 | **0.98ms** | 2001.85ms | **~10-50ms** |
| Erros HTTP | **0** | 44.800 | **~0** |
| TP | 24.037 | 152 | ~24.000 |
| TN | 30.022 | 174 | ~30.000 |
| FP | **0** | 2 | ~10-20 |
| FN | **0** | 1 | ~10-20 |
| Failure rate | **0%** | **99.28%** | **~1-2%** |
| Score final | **+6000** | **−6000** | **>+5000** |

*Aguardando submissão v25 (semáforo não-bloqueante 1024 + respostas pré-alocadas + warmup)

### Causa raiz — três iterações

**v20 (semáforo não-bloqueante 16 slots)** → 93% failure rate, 503s:
- Das 54.100 reqs, ~3.600 processadas, 50.368 com 503

**v21 (remoção total)** → 99% failure rate, p99=2002ms:
- Das 54.100 reqs, apenas 326 processadas

**v24 (DecodeBytes fix + semáforo bloqueante 128)** → 99% failure rate, p99=2002ms:
- Apenas 329 reqs processadas — mesma classe de v21 sem semáforo
- O DecodePayload não era o gargalo real

**Causa raiz verdadeira: semáforo BLOQUEANTE cria cascade**
- API semáforo (128) enche → proxy goroutines esperam API (I/O wait)
- ProxySem slots ocupados por goroutines que esperam API → novas conexões k6 não entram
- k6 timeout em 2001ms → 82% de erro HTTP
- p99 em 2002ms porque erros dominam a distribuição de latência

v20 processava 10× mais (3.691 reqs) porque o **semáforo não-bloqueante** com 503 rápido
libera o proxySem imediatamente. O bloqueante travava tudo.

**Correção v25**: três mudanças juntas:
1. **Semáforo não-bloqueante 1024**: `select { case sem <- struct{}{}: }` com capacidade
   1024. Em regime normal (~5 slots usados), nunca enche. Se encher, 503 rápido.
2. **Respostas pré-alocadas**: 6 variações de JSON pré-computadas (`fraudResponses[6][]byte`),
   zero serialização no hot path.
3. **Warmup container**: 48 POSTs antes do teste via curl, aquecendo caches.

### Lições Aprendidas

| Lição | Descrição |
|:-----|:----------|
| ⏱️ **Sempre configurar timeouts HTTP** | `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` e `IdleTimeout` são obrigatórios. |
| 🚦 **Semáforo não-bloqueante com capacidade folgada** | Bloqueante causa cascade mesmo com DecodeBytes fix. Não-bloqueante 1024 com 180 req/s enche raramente; quando enche, 503 rápido. |
| 🔄 **Tuning do proxy transport** | `MaxIdleConnsPerHost`, `IdleConnTimeout` e `DialContext.Timeout` são essenciais. |
| 🧪 **Testar com carga real antes** | Testes unitários não revelam problemas de concorrência ou convoy. |
| 🖥️ **CPU do proxy é crítica** | proxy passou de 0.05 → 0.10 → 0.15 CPU em múltiplos ajustes. |
| 📊 **503 é pior que FP/FN no scoring** | 503 tem peso 5 no E (vs 1 do FP, 3 do FN). Melhor processar com latência maior que recusar — desde que fique abaixo dos 2000ms. |
| 🐛 **Semáforo bloqueante foi o pior de todos** | v20 (não-bloqueante 16, 503) processou 3.691 reqs. v24 (bloqueante 128 + DecodeBytes) processou 329. O bloqueante é pior que não ter semáforo. A chave é capacidade folgada + não-bloqueante. |
| 🎯 **Semáforo não-bloqueante 1024 + respostas pré-alocadas + warmup** | Combinação que resolve os problemas de v20-24: não-bloqueante com folga elimina cascade, resposta pronta evita serialização, warmup aquece caches. |
| 🧹 **Revisar ADRs obsoletos** | ADRs que resolviam problemas de versões anteriores podem virar o próprio problema. Remova ou ajuste quando a stack mudar. |

As decisões arquiteturais estão documentadas em **[docs/DECISOES.md](./docs/DECISOES.md)**.

---

## Como Funciona

### 1. Startup (índice pré-construído)

No **Docker build**, a API carrega os 3M vetores de `references.json.gz` em modo streaming,
constrói o índice IVF com K-means (25 iterações em mini-batches de 20%) e serializa o resultado em
`/resources/index.bin` (formato binário, ~45MB).

No **startup do container**, a API apenas lê o `index.bin` do disco — leva **menos de 1 segundo**.
O servidor HTTP começa a responder `GET /ready` com 200 imediatamente.

Em desenvolvimento local (sem Docker), a API faz o carregamento completo do `references.json.gz`
como fallback.

### 2. Recebimento
`POST /fraud-score` recebe JSON com dados da transação. O proxy faz o parsing
JSON e codifica para um formato binário compacto (~80-130 bytes) antes de
enviar para as APIs via Unix socket. As APIs leem o binário diretamente,
sem `json.Unmarshal` — zero alocações de parsing.

### 3. Vetorização (14 dimensões)
Cada campo é normalizado para [0,1] seguindo as fórmulas em [REGRAS_DE_DETECCAO.md](./docs/REGRAS_DE_DETECCAO.md) e quantizado para int8 (0-127), reduzindo 4x o uso de memória.

### 4. Busca Vetorial (IVF Index)
- Encontra os 3 centroides mais próximos (nprobe=3) entre 1.000 centroides
- Busca os 5 vizinhos mais próximos dentro desses clusters (até 5.000 vetores por cluster)
- Distribuição balanceada: clusters de 914 a 6.109 vetores (K-means corrigido)
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
| IVF Search (Normalize + Search, 3 clusters) | ~130 µs | 0 B/op |

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
| 32 | Pool de buffers no proxy | Zero alocações de body no proxy |
| 33 | json.Unmarshal com pool | Elimina json.Decoder na API |
| 34 | Busca em 2 clusters | Recall próximo do brute force |
| 35 | K-means++ + 10 iterações | Centroides melhor distribuídos |
| 36 | Protocolo binário proxy↔API | Zero alocações de JSON parsing na API |
| 37 | Early exit IVF (1 cluster) | Reduz busca em 50% (6000→3000 vetores) |
| 38 | Semáforos 64→16 | Menos contenção de scheduler (GOMAXPROCS=1) |
| 39 | CPU proxy 0.15, APIs 0.425 | Proxy era o novo gargalo (JSON parsing) |
| 40 | Timeouts 100ms/200ms | Conexões lentas cortadas 5× mais rápido |
| 41 | Pool de encode no proxy | Zero alocações de buffer de encode |
| 42 | Semáforo bloqueante (128) | Fila em vez de rejeição — zero 503 |
| 43 | DecodePayload fix (ReadAll+DecodeBytes) | Elimina blocking read de 200ms |
| 44 | nprobe=3 + maxScanPerCluster=5000 | Latência máxima ~350µs mesmo com índice degenerado |
| 45 | K-means corrigido (bug Quantize + 25 iter) | Clusters balanceados: 914-6109 (antes: 0-1.27M) |

---

## Licença

MIT