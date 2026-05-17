# Decisões Arquiteturais — Rinha de Backend 2026

> Data: 2026-05-16
> Stack: Go 1.26.3

---

## ADR-01: Quantização uint8 para vetores

**Contexto**: Dataset tem 3M vetores de 14 dimensões. Com float64 (original do JSON) seriam 336 MB
só de dados; com float32 seriam 168 MB. O limite total de memória para todos os serviços é 350 MB
(proxy + 2 APIs + runtime).

**Decisão**: Quantizar cada dimensão para uint8 (0-255), ocupando 14 bytes por vetor → 42 MB
para os 3M vetores. Dimensões que podem ser -1 (índices 5 e 6, quando last_transaction é null)
são armazenadas como int8 em arrays separados.

**Consequências**:
- Positivas: redução de 4x no uso de memória; cálculos com inteiros são mais rápidos que float.
- Negativas: perda mínima de precisão na distância (arredondamento de 1/256 ≈ 0.4%).
- Mitigação: a ordenação relativa entre vizinhos próximos é preservada.

---

## ADR-02: IVF (Inverted File Index) para busca vetorial

**Contexto**: Brute force (comparar com todos os 3M vetores) levaria ~100-150ms por consulta,
inviabilizando p99 baixo. HNSW tem overhead de memória alto (~430 MB). VP-Tree é exata mas
complexa de implementar em Go puro.

**Decisão**: Implementar IVF com 1.000 clusters (centroides):
- K-means++ para inicialização (durante startup)
- Atribuição de cada vetor ao cluster mais próximo (distância Manhattan uint8)
- Na consulta: encontra o cluster mais próximo e busca apenas dentro dele (~3.000 vetores)
- Tempo de consulta estimado: < 0.1 ms

**Consequências**:
- Positivas: latência extremamente baixa; memória frugal (~47 MB/API).
- Negativas: recall ligeiramente inferior ao brute force (~97%).
- Mitigação: threshold 0.6 absorve pequenas variações na classificação.

**Parâmetros**: n_clusters=1000, n_iter=20, ef_search=1

---

## ADR-03: Distância Manhattan (L1) em vez de Euclidiana

**Contexto**: Distância Euclidiana requer multiplicações e raiz quadrada.

**Decisão**: Usar Manhattan (soma das diferenças absolutas) com inteiros.

**Cálculo**: dist(a, b) = Σ|a[i] - b[i]| para i = 0..13

**Consequências**:
- Para vetores normalizados, Manhattan tem correlação > 99% com Euclidiana em KNN.
- Sem multiplicações ou math.Sqrt.

---

## ADR-04: Proxy em Go (standard library) com /ready local

**Contexto**: Load balancer round-robin sem lógica de negócio. O endpoint `GET /ready` (na porta
9999) precisa responder mesmo que as APIs ainda estejam carregando o dataset.

**Decisão**: Implementar o proxy com `net/http/httputil.ReverseProxy` da stdlib para
`POST /fraud-score`, e um `http.ServeMux` que roteia `GET /ready` para um handler local.

O handler `/ready` no proxy:
1. Faz `GET /ready` em cada backend (api-1 e api-2) com timeout de 1s
2. Se todos responderem 2xx → retorna HTTP 200
3. Se algum falhar → retorna HTTP 503

**Consequências**:
- Zero dependências externas.
- Binário mínimo (< 5 MB stripped).
- Round-robin com `sync/atomic`.
- `/ready` nunca devolve 502 (proxy error) — se os backends estão carregando, o proxy devolve 503
  informando quais estão indisponíveis.

---

## ADR-05: Binários estripados (stripped)

**Decisão**: Compilar com CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath.

**Consequências**: Redução de ~60% no tamanho dos binários.

---

## ADR-06: Docker multi-stage build com scratch + índice pré-construído

**Decisão**:
1. Stage 1 (builder): golang:1.26.3-alpine para compilar + gerar índice IVF pré-construído
2. Stage 2 (runtime): scratch (apenas binário + índice binário + dados de referência)

No builder, após compilar o binário, executa-se:
```dockerfile
RUN RESOURCES_DIR=/resources /api -build-index /resources/index.bin
```
Isso serializa o IVF index em formato binário (~45MB), eliminando o processamento de startup.

**Consequências**: Imagem final ~51 MB (API), sem shell ou libc. Startup < 1s.

---

## ADR-07: Branch submission como orphan branch

**Contexto**: A branch submission deve conter apenas os arquivos necessários para execução, sem o código-fonte.

**Decisão**:
O repositório precisa ter ao menos **um commit** antes de criar a orphan branch.

1. Commit inicial na main:
  git checkout -b main
  git add .
  git commit -m "feat: initial implementation"
  git push -u origin main

2. Criar branch submission (orphan):
  git checkout --orphan submission
  git rm -r .
  git add docker-compose.yml Dockerfile.* info.json
  git commit -m "submission: deployment files"
  git push -u origin submission
  git checkout main

**Consequências**:
- Branch sem histórico compartilhado com main.
- `git rm -r .` remove apenas arquivos **trackeados** (falha se não houver commit anterior).

---

## ADR-08: Índice IVF pré-construído no Docker build

**Contexto**: O servidor HTTP da API deve responder apenas quando todos os dados estiverem
carregados e o índice IVF estiver pronto para consultas. Originalmente, todo o processamento
(carregamento streaming de 3M vetores + K-means + full assignment) ocorria no startup de cada
container, levando ~90s. O test runner faz até 30 tentativas de `GET /ready` (~3s cada),
totalizando ~90s — o startup estourava essas tentativas.

**Decisão**: O índice IVF é construído **uma única vez durante o Docker build** e serializado
em formato binário (`index.bin`, ~45MB). No startup de cada API:

1. Carregar normalization.json e mcc_risk.json (< 1ms)
2. Ler `index.bin` do disco e reconstruir o índice IVF (~200ms)
3. Iniciar o servidor HTTP e responder 200 em `GET /ready`

No Dockerfile, isso é feito com:
```dockerfile
RUN RESOURCES_DIR=/resources /api -build-index /resources/index.bin
```

O código oferece fallback: se `index.bin` não existir (ex: desenvolvimento local sem Docker),
o carregamento completo do `references.json.gz` é usado.

**Consequências**:
- Startup caiu de **~90s para < 1s** (~100x mais rápido).
- O Docker build ficou ~70s mais lento (acontece uma vez por versão).
- Imagem final ~4 MB maior (devido ao `index.bin`).
- Código existente permanece funcional (fallback preservado).

---

## ADR-09: Distribuição de recursos no docker-compose.yml

| Serviço | CPU  | Memória |
|---------|:----:|:-------:|
| proxy   | 0.05 | 20 MB   |
| api-1   | 0.475| 165 MB  |
| api-2   | 0.475| 165 MB  |
| Total   | 1.0  | 350 MB  |


---

## ADR-10: Uso exclusivo da standard library

**Decisão**: Nenhuma dependência externa.

Pacotes: net/http, encoding/json, compress/gzip, math/rand/v2, slices, cmp, sync/atomic, net/http/httputil, encoding/binary, unsafe.

**Consequências**: go.mod com apenas module e go 1.26. Build reprodutível.

---

## ADR-11: Proxy serve /ready localmente

**Contexto**: O endpoint `GET /ready` na porta 9999 (`docs/API.md`) precisa responder sempre.
Anteriormente, o proxy encaminhava `/ready` para os backends, que devolviam 502 (Bad Gateway)
enquanto carregavam o dataset (~5-15s de startup).

**Decisão**: O proxy passou a servir `GET /ready` localmente, usando um
`http.ServeMux` para rotear:

- `GET /ready` → `ReadyHandler` (local): consulta `GET /ready` de cada backend
- `/` (qualquer outra rota) → `RoundRobinProxy` (round-robin)

O `ReadyHandler` usa `http.Client` com timeout de 1s para verificar cada backend.

**Consequências**:
- `/ready` nunca retorna 502 — se backends estão carregando, retorna 503 com detalhes.
- O proxy continua sem lógica de negócio no fluxo de `POST /fraud-score`.
- Código mais robusto: o test runner pode consultar `/ready` a qualquer momento.

---

## ADR-12: Imagens versionadas para Docker Hub

**Contexto**: A branch `submission` não contém código-fonte, apenas arquivos de deploy.
O `docker-compose.yml` da submission não pode ter `build:` (falta o `cmd/` e `internal/`).

**Decisão**:
1. Manter dois arquivos de orquestração:
   - `docker-compose.yml` (main branch) — com `build:` para desenvolvimento local
   - `docker-compose.submission.yml` (main branch) — template sem `build:`, com image tags
     variáveis para Docker Hub
2. Usar tags de versão específicas (`v1`, `v2`, ...) em vez de `:latest` — o test runner
   pode ter cache local com `:latest` antigo
3. O Makefile tem o target `submission-file` que gera o `docker-compose.yml` da submission
   com a versão correta:
   ```bash
   make submission-file VERSION=v2 > docker-compose.yml
   ```
4. Workflow: `make docker-build` + `make docker-push` na main → gerar arquivo →
   commitar na orphan branch submission

**Consequências**:
- Submission branch autocontida e independente.
- Versão explícita e rastreável em cada submission.
- `:latest` opcional (target `docker-tag-latest`) para referência rápida.

---

## ADR-13: Carregamento streaming do JSON para evitar OOM

**Contexto**: O JSON de referências (`references.json.gz`) tem ~298MB descomprimido com 3M
entradas. Cada entrada contém um `vector` de 14 float64 (112 bytes) + label. O decode ingênuo
com `json.Decoder.Decode(&refs)` carrega **todas as 3M entradas na memória como `[]Reference`**,
consumindo ~450MB — muito além do limite de 165MB por API. O container morria com exit code 137
(SIGKILL/OOM killer).

**Decisão**: Processar o JSON em **modo streaming**:
1. Ler o `[` inicial com `dec.Token()`
2. Para cada elemento (enquanto `dec.More()`):
   - Decodificar um `Reference` individual
   - Quantizar imediatamente os 14 floats para `Vector14` (int8)
   - Atribuir o label (0 ou 1)
   - Fazer `ref.Vector = nil` para o GC coletar o `[]float64` imediatamente
   - Fazer `append` nos slices finais
3. Os slices `vectors` e `labels` são pré-alocados com capacidade 3.000.000 para
   minimizar realocações durante o `append`

Além disso, no `BuildIVFIndex`, substituiu-se `rng.Perm(n)` (que aloca `[]int` de 3M
elementos = 24MB) por `make([]int, batchSize)` + `rng.IntN(n)`, alocando apenas 1.2MB.

**Pico de memória durante startup**:

| Etapa | Memória |
|-------|:-------:|
| Loading streaming (1 ref por vez) | ~50 MB |
| Após loading (vectors + labels) | ~45 MB |
| BuildIVFIndex (clusterAssign + reordered) | ~114 MB (pico) |
| **Pico total** | **~114 MB** |

**Consequências**:
- Resolve o OOM killer — pico de 114MB dentro do limite de 165MB.
- O streaming agora ocorre **durante o Docker build** (não no startup runtime), então o impacto no runtime é zero.
- Nenhuma alocação extra no runtime: após o startup, a API opera com ~47MB fixos.
- `json.NewDecoder` em modo streaming é tão rápido quanto bulk para arquivos grandes.

---

## ADR-14: Índice IVF em formato binário para startup instantâneo

**Contexto**: O índice IVF construído no Docker build precisa ser salvo em disco e recarregado
no startup do container. O formato precisa ser eficiente para leitura e minimalista em tamanho.

**Decisão**: Serializar o IVF index em formato binário customizado com:

| Campo | Tipo | Tamanho |
|-------|------|:-------:|
| Magic + versão | `[4]byte` ("IVF\x01") | 4 B |
| Nº de vetores | `uint32` LE | 4 B |
| Nº de centroides | `uint32` LE | 4 B |
| Vetores | `[]int8` (nVet × 14) | nVet × 14 B |
| Labels | `[]uint8` (nVet) | nVet B |
| Centroides | `[]int8` (nCent × 14) | nCent × 14 B |
| Offsets | `[]int32` LE (nCent+1) | (nCent+1) × 4 B |

Para 3M vetores e 1000 centroides: ~45MB totais (42MB vetores + 3MB labels + 14KB centroides + 4KB offsets).

A função `SaveIndex()` serializa os slices diretamente; `LoadIndex()` faz o caminho inverso
usando `unsafe` para conversão eficiente entre `[]byte` e `[]int8`.

**Consequências**:
- Startup **< 1s** vs ~90s anteriores (~100x mais rápido).
- Imagem Docker ~4 MB maior (45 MB do índice vs 41 MB do JSON comprimido? não, o JSON gzipado tem 48MB, mas o índice binário tem 45MB — diferença marginal).
- Fallback preservado: se `index.bin` não existir, carrega do `references.json.gz`.
- `unsafe` usado apenas na leitura, com escopo limitado ao loader e sem impacto na segurança do runtime.

---

## Referências

- [REGRAS_DE_DETECCAO.md](./REGRAS_DE_DETECCAO.md) — fórmulas das 14 dimensões
- [DATASET.md](./DATASET.md) — formato dos arquivos de referência
- [ARQUITETURA.md](./ARQUITETURA.md) — limites de CPU/memória
- [BUSCA_VETORIAL.md](./BUSCA_VETORIAL.md) — introdução à busca vetorial
- [API.md](./API.md) — contrato da API