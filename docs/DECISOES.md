# Decisões Arquiteturais — Rinha de Backend 2026

> Data: 2026-05-14
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

## ADR-04: Proxy em Go (standard library)

**Contexto**: Load balancer round-robin sem lógica de negócio.

**Decisão**: Implementar com net/http/httputil.ReverseProxy da stdlib.

**Consequências**:
- Zero dependências externas.
- Binário mínimo (< 5 MB stripped).
- Round-robin com sync/atomic.

---

## ADR-05: Binários estripados (stripped)

**Decisão**: Compilar com CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath.

**Consequências**: Redução de ~60% no tamanho dos binários.

---

## ADR-06: Docker multi-stage build com scratch

**Decisão**:
1. Stage 1: golang:1.26.3-alpine para compilar
2. Stage 2: scratch (apenas binário + dados de referência)

**Consequências**: Imagem final ~10-15 MB comprimida, sem shell ou libc.

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

## ADR-08

**Decisão**: No startup de cada API:
1. Carregar normalization.json e mcc_risk.json
2. Decompress + parse de references.json.gz
3. Executar K-means para construir IVF index
4. Só então responder 200 em GET /ready

**Consequências**: Startup mais lento (~5-15s), zero processamento durante o teste.

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

Pacotes: net/http, encoding/json, compress/gzip, math/rand/v2, slices, cmp, sync/atomic, net/http/httputil.

**Consequências**: go.mod com apenas module e go 1.26. Build reprodutível.

---

## Referências

- [REGRAS_DE_DETECCAO.md](./REGRAS_DE_DETECCAO.md) — fórmulas das 14 dimensões
- [DATASET.md](./DATASET.md) — formato dos arquivos de referência
- [ARQUITETURA.md](./ARQUITETURA.md) — limites de CPU/memória
- [BUSCA_VETORIAL.md](./BUSCA_VETORIAL.md) — introdução à busca vetorial
