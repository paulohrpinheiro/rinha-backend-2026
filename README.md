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
| proxy   | 0.19 | 20 MB | Load balancer round-robin + /ready + /debug/vars |
| api-1   | 0.405 | 165 MB | Detecção de fraude (IVF, parser JSON manual, /debug/vars) |
| api-2   | 0.405 | 165 MB | Detecção de fraude (IVF, parser JSON manual, /debug/vars) |
| Total   | 1.0 | 350 MB | — |

**Protocolo**: O proxy recebe JSON do cliente e o encaminha **bruto** (sem parsing)
para as APIs via Unix socket. As APIs fazem parsing JSON manual byte-a-byte
(zero alocações, zero reflection) e respondem com 9 bytes binários (codec).
O proxy decodifica a resposta binária e retorna JSON pré-computado ao cliente.

**Evolução**: v34 removeu o codec binário de ida (proxy↔API), substituindo por
JSON bruto + parser manual na API — reduziu HTTP errors em 67%.

---

## Endpoints (porta 9999)

### `GET /ready`

Verificação de prontidão. O **proxy** consulta o `/ready` de cada backend (api-1 e api-2):

| Estado dos backends | Resposta |
|---|---|
| Todos respondem 2xx | **HTTP 200** `{"status":"ok","backends":[...]}` |
| Algum falha | **HTTP 503** `{"status":"degraded","backends":[...]}` |

Isoladamente, cada API também expõe `GET /ready` na porta 8080, respondendo 200 assim que o índice IVF pré-construído é carregado (menos de 1 segundo).

### `GET /debug/vars` (proxy e APIs)

Endpoint de diagnóstico que expõe contadores internos em JSON. Permite rastrear
exatamente onde as requisições se perdem no pipeline, sem logar em disco.

**Proxy** (`GET /debug/vars` na porta 9999):
- `requests_received`, `requests_forwarded`, `responses_received`
- `backend_errors`, `api_errors`, `decode_errors`
- `semaphore_503s`, `parse_errors`, `encode_errors`, `read_errors`

**API** (`GET /debug/vars` na porta 8080):
- `requests_received`, `responses_sent`
- `decode_errors`, `read_errors`, `search_errors`, `semaphore_503s`

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
  handler/fraud.go     # Handlers HTTP (/ready, /fraud-score, /debug/vars)
  parser/json.go       # Parsing JSON manual byte-a-byte (zero alocações)
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

> Última submissão: **v39** (commit pendente) · Aguardando resultado
> Docker Hub: `paulohrpinheiro/rinha-proxy:v39` + `rinha-api:v39`

### 🏆 Melhor resultado: +1082 (v38)

| Componente | Valor | Corte |
|:-----------|:-----:|:-----:|
| `score_p99` | **+709** | p99 = 195.23ms < 2000ms ✅ |
| `score_det` | **+373** | failure_rate = 2.23% (< 15%) ✅ |
| **Final** | **+1082** | Primeiro score positivo! 🎉 |

| Métrica | v38 | v37 (baseline) | Delta |
|:--------|:---:|:---:|:-----:|
| HTTP errors | 57 | 1.587 | −96.4% ✅ |
| TP + TN (corretos) | 52.798 | 44.661 | +18.2% ✅ |
| FP | 733 | 609 | +124 |
| FN | 413 | 354 | +59 |
| Processados (total) | 54.001 | 47.211 | +14.4% ✅ |
| Failure rate | 2.23% | 5.4% | −3.17pp ✅ |
| **p99** | **195.23ms** | 2001.26ms | **−90.2% ✅** |
| Detection score | **+373** | −503 | +876 ✅ |
| Final score | **+1082** | −3503 | **+4585 ✅** |

### Resultado v39: nprobe=3 — neutro

| Métrica | v38 | v39 | Delta |
|:--------|:---:|:---:|:-----:|
| HTTP errors | 57 | 70 | +13 |
| FP | 733 | 732 | −1 |
| **FN** | **413** | **413** | **0** |
| p99 | 195ms | 192ms | −3ms |
| **Final score** | **+1082** | **+1073** | −9 |

nprobe=3 varreu 50% mais vetores sem nenhum ganho de recall. FN idêntico prova
que nprobe=2 já encontra os vizinhos corretos.

### Resultado v40: K=7 thr=0.6 — FP −54%, FN +64%

| Métrica | v38 | v40 | Delta |
|:--------|:---:|:---:|:-----:|
| FP | 733 | 336 | −54% ✅ |
| FN | 413 | 677 | +64% ❌ |
| Score | +1082 | +922 | −160 |

Threshold 0.6 com K=7 é conservador demais. Corte em 5/7=71.4% vs 3/5=60%.

### 🎯 v41 — Submissão atual (pendente)

| Mudança | v40 | v41 |
|:--------|:---:|:---:|
| K (vizinhos) | 7 | 7 |
| Threshold | 0.6 | **0.572** |

**Hipótese**: Threshold 0.572 restaura equivalência com K=5 thr=0.6
(4/7=0.571 → approve, 5/7=0.714 → deny), mantendo estabilidade de K=7.

### Evolução completa

| Versão | Mudança chave | HTTP Errs | Failure | p99 | Score |
|:------:|:-------------|:---------:|:-------:|:---:|:-----:|
| v10 | TCP, httputil | 13.858 | 100% | 2002ms | −6000 |
| v16 | codec binário | 52.601 | 97.5% | 1042ms | −3018 |
| v17 | Unix sockets | 53.370 | 98.9% | **1066ms** | −3028 |
| v20 | proxy custom, semaf 16 | 50.368 | 96.4% | 501ms | −2700 |
| v21 | semáforo removido | 49.706 | 92.2% | 2002ms | −6000 |
| v24 | DecodeBytes fix | 44.800 | 83.1% | 2002ms | −6000 |
| v26 | K-means + nprobe=3 | 5.400 | 18.1% | 2002ms | −6000 |
| v27 | diag, timeouts 500ms | 10.006 | 53.9% | 2002ms | −6000 |
| v28 | reverte timeouts 200ms | 12.321 | 80.0% | 2002ms | −6000 |
| v29 | proxy 0.15 CPU | 9.840 | 46.8% | 2002ms | −6000 |
| v30 | semáforo 1024 | 5.550 | 16.0% | 2001ms | −6000 |
| v31 | nprobe=3 | 6.873 | 29.3% | 2002ms | −6000 |
| v32 | nprobe=2, timeout 200ms | 8.951 | 29.7% | 2002ms | −6000 |
| v33 | GOMEMLIMIT=60MiB | — | — | — | não submetido |
| v34 | parser JSON manual | 3.783 | 36.6% | 2002ms | −6000 |
| v35 | parser keyLen fix | 1.812 | 5.7% | 2001ms | **−3565 🎉** |
| v36 | timeouts 100ms ❌ | 7.709 | 17.7% | 2001ms | −6000 |
| v37 | proxy 0.17 CPU | **1.587** | **5.4%** | 2001ms | **−3503** |
| v38 | proxy 0.19, client 200ms | **57** | **2.2%** | **195ms** | **+1082 🏆** |
| v39 | nprobe=3 (neutro) | 70 | 2.3% | 192ms | +1073 |
| v40 | K=7, thr=0.6 | 112 | 2.1% | 201ms | +922 |
| **v41** | **K=7, thr=0.572** | **?** | **?** | **?** | **aguardando** |

### Marcos da série

| Marco | Versão | Detalhe |
|:------|:------:|:--------|
| Primeiro p99 < 2000ms | v16/v17 | 1042ms/1066ms com codec binário |
| Primeiro score > −6000 | v20 | −2700 com proxy custom |
| Detection sem corte | v35 | −565 (failure < 15%) |
| **Primeiro score positivo** | **v38** | **+1082** 🎉 |
| Menos HTTP errors | v38 | 57 (−96% vs v37) |
| Melhor score | v38 | +1082 |
| Melhor p99 | v38 | 195ms (−90% vs v37) |

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
| 📈 **Benchmark local ≠ teste oficial** | v27 teve p99=398ms local e 2002ms oficial. O `ab` com rajadas curtas não reproduz carga sustentada por 5 minutos. |
| 🧪 **Uma mudança por submissão** | v27 aplicou 6 mudanças simultâneas. Impossível isolar qual causou a regressão de 43% no throughput. |
| 🔬 **Diagnóstico é tão importante quanto otimização** | Sem contadores (ADR-46), as 21k requisições "fantasmas" do v26 eram invisíveis. Adicionar `/debug/vars` em proxy e APIs revelou exatamente onde cada requisição estava. |
| 🎛️ **Timeouts muito curtos causam falsos timeouts** | Com GOMAXPROCS=1 e 180 req/s, uma goroutine pode esperar >100ms pelo scheduler. Timeout de 100ms dispara antes do processamento começar. |
| ⚖️ **Capacidade do semáforo é uma curva em U** | 16 slots (v20): muitos 503. 128 bloqueante (v24): cascade. 1024 (v26): scheduler thrashing. 256 (v27): equilíbrio. |
| 📝 **Parser JSON manual supera codec binário** | v34 removeu o codec de ida e passou a forwardar JSON bruto. v35 corrigiu bugs de keyLen. Resultado: HTTP errors −67%, throughput +27%, detection_score saiu do corte. |
| ⏱️ **Timeout de 100ms é contraproducente** | v36 tentou baixar p99 com timeouts 100ms. Resultado: +325% HTTP errors, falhou o detection_score. Com GOMAXPROCS=1, scheduling jitter >100ms é comum. |
| 📈 **Mais CPU no proxy melhora consistentemente** | v35→v37: proxy 0.15→0.17, HTTP errors −12.4%, score +61. v38 tenta proxy 0.19. O proxy é o gargalo principal. |
| 🎯 **Client timeout deve ser consistente com server timeout** | Proxy client tinha timeout 500ms mas server HTTP cortava em 200ms. v38 alinha ambos em 200ms. |

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
`POST /fraud-score` recebe JSON com dados da transação. O proxy faz `io.Copy`
do body e encaminha o JSON **bruto** para as APIs via Unix socket (zero parsing
no proxy). As APIs fazem parsing JSON manual byte-a-byte com `parser.ParseJSON()`,
resolvendo campos por tamanho da chave — zero `json.Unmarshal`, zero reflection,
zero alocações de string.

### 3. Vetorização (14 dimensões)
Cada campo é normalizado para [0,1] seguindo as fórmulas em [REGRAS_DE_DETECCAO.md](./docs/REGRAS_DE_DETECCAO.md) e quantizado para int8 (0-127), reduzindo 4x o uso de memória.

### 4. Busca Vetorial (IVF Index)
- Encontra os 2 centroides mais próximos (nprobe=2) entre 1.000 centroides
- Busca os 7 vizinhos mais próximos dentro desses clusters (até 5.000 vetores por cluster)
- Distribuição balanceada: clusters de 914 a 6.109 vetores (K-means corrigido, ADR-45)
- Latência de busca: ~90µs (nprobe=2, K=7)
- Usa distância Manhattan com loop unrolled

### 5. Decisão
```
fraud_score = fraudes_entre_os_7 / 7
approved = fraud_score < 0.572
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
| IVF Search (Normalize + Search, K=7, nprobe=2) | ~90 µs | 0 B/op |

### Benchmark de carga realista

Script `scripts/bench_realista.go` simula o padrão do k6 oficial:
180 req/s sustentado por 5 minutos, com ramp-up e 4 payloads variados.

```bash
# Ambiente local (com constraints Docker)
docker compose up -d --build
go run scripts/bench_realista.go -duration=5m -ramp-up=30s

# Teste rápido (1 minuto)
go run scripts/bench_realista.go -duration=1m -ramp-up=5s
```

Coleta p50/p95/p99, erros HTTP, erros de conexão e consulta `/debug/vars`.
Julgamento automático: 🟢 passou / 🔴 falhou (p99 < 2000ms, taxa > 85%).

⚠️ O benchmark local é um **smoke test** — não prevê o resultado oficial.
O v27 passou com 51k reqs, p99=2.7ms, zero erros, mas teve score −6000
no teste oficial (a diferença está no ambiente de rede e perfil de ramp-up do k6).

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
| 46 | Contadores de diagnóstico (proxy + API) | Visibilidade total do pipeline via `/debug/vars` |
| 47 | CPU proxy 0.10, APIs 0.45 | Retorno à config do v17 — único p99 < 2000ms |
| 48 | Timeouts HTTP 500ms | Elimina falsos timeouts por starvation de scheduler |
| 49 | Semáforo 256 (não-bloqueante) | Equilíbrio entre folga e pressão de scheduler |
| 50 | nprobe=2 | Redução de 30% na latência de busca (130→90µs) |
| 51 | Warmup expandido (64 searches) | Cobertura ampliada de clusters antes do tráfego real |
| 52 | Otimização codec adiada | `bytes.Buffer` não expõe API para buffer reuse seguro |
| 53 | Warmup inline (handler.Warmup) | 64 buscas antes de aceitar tráfego |
| 54 | Increase proxy CPU (v29) | Proxy 0.15 CPU (era 0.10) — era o gargalo |
| 55 | Timeout revert (v28→v29) | Retorno a 200ms após regressão com 500ms |
| 56 | Semáforo 1024 (v30) | Retorno à capacidade folgada do v26 |
| 57 | nprobe=3 regression (v31) | nprobe=3 piorou throughput em 37% vs v30 |
| 59 | nprobe=2 + client timeout 200ms (v32) | Config estável: proxy 0.15, semáforo 1024 |
| 60 | GOMEMLIMIT=60MiB (v33) | Memória mais restrita para reduzir GC pressure |
| 61 | Parser JSON manual (v34) | Proxy forwarda JSON bruto, API faz parsing byte-a-byte |
| 62 | Correção keyLen (v35) | tx_count_24h=12 e requested_at=12 corrigidos |
| 63 | v35 — primeiro score não-piso | −3564, detection_score sem corte |
| 64 | Timeouts 100ms (v36) | Tentativa de baixar p99 — regrediu para −6000 |
| 65 | Reverte timeouts + proxy 0.17 (v37) | Melhor score da série: −3503 |
| 66 | Análise v37 | Proxy CPU reduz HTTP errors mas p99 estagnado |
| 67 | Proxy 0.19 + client 200ms (v38) | Estratégia atual — aguardando resultado |

---

## Licença

MIT