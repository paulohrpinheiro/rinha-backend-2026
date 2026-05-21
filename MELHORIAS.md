# Melhorias Potenciais — Rinha de Backend 2026

Análise do repositório [`joycegodinho/rinha-2026`](https://github.com/joycegodinho/rinha-2026) — melhor implementação em Go disponível publicamente.

> Data: 2026-05-19

---

## 1. File Descriptor Passing (SCM_RIGHTS)

O load balancer é C++ (~300 linhas) que aceita conexões TCP e **passa o file descriptor** para a API via `sendmsg(SCM_RIGHTS)`. O LB nunca toca no payload.

**Fluxo deles:**
```
k6 → LB (aceita TCP) → passa fd → API lê HTTP direto do socket do cliente
```

**Nosso fluxo:**
```
k6 → Proxy (parse JSON → encode binário → HTTP) → API (decode → processa → responde)
```

Cada requisição no proxy consome ~337μs de CPU. Com fd-passing, o proxy consome <50μs (só aceitar + passar fd).

```cpp
// fd_lb.cpp — aceita e passa fd
int cfd = accept4(listen_fd, nullptr, nullptr, SOCK_NONBLOCK | SOCK_CLOEXEC);
send_passed_fd(backend.ctrl_fd, cfd);  // fd vai para API via SCM_RIGHTS
close_fd(cfd);
```

**Impacto**: elimina o proxy como gargalo. API lê JSON puro sem codec.

---

## 2. Parsing JSON Manual sem Alocações

`buildVectorUltraGo()` faz parsing byte-a-byte do JSON — zero `json.Unmarshal`, zero reflection, zero alocações de string.

```go
func buildVectorUltraGo(body []byte, r *runtime.RuntimeData, out *ivf.Vector) {
    // byte-at-a-time scanning, campos resolvidos por switch(len(chave))
    out[0] = clamp(amount / n.MaxAmount)
    // ...
}
```

Técnicas:
- Resolução de campos por tamanho da chave (ex: 6→"amount", 8→"customer")
- `parseFloatFast()` customizada (sem strconv)
- Array fixo para known_merchants (`var known [32][]byte`)
- Dias da semana via lookup table hardcoded para março 2026

**Comparação**: nosso codec binário tem o mesmo efeito (zero alocações na API), mas adiciona 391 linhas de manutenção. O parsing manual faz o mesmo sem codec.

---

## 3. Respostas Pré-alocadas ✅ Implementado

```go
var fraudResponses = [6][]byte{
    []byte(`{"approved":true,"fraud_score":0.0}`),
    // ... 0.2, 0.4, 0.6, 0.8, 1.0
}

func writeFraudCountResponse(ctx *fasthttp.RequestCtx, fraudCount int) {
    ctx.Response.SetBodyRaw(FraudResponse(fraudCount)) // zero alocação
}
```

6 respostas possíveis pré-computadas como `[]byte` estático. Zero serialização, zero alocação.

**Implementação simples no nosso código** (substitui a montagem manual com `strconv.AppendFloat`):

```go
var fraudResponses = [6][]byte{...}
w.Write(fraudResponses[fraudCount])
```

---

## 4. Fasthttp vs stdlib net/http

Usam `github.com/valyala/fasthttp` — framework zero-alocação.

```go
server := &fasthttp.Server{
    Concurrency: 4096,              // sem semáforo artificial
    ReadBufferSize:  1024,          // buffers mínimos
    WriteBufferSize: 1024,
    DisableHeaderNamesNormalizing: true,
    NoDefaultDate: true, NoDefaultServerHeader: true,
    NoDefaultContentType: true,
}
```

Benefícios vs `net/http`:
- `RequestCtx` reutilizado via pool (zero alocação por request)
- Body como `[]byte` direto: `ctx.Request.Body()` (sem `io.ReadAll`)
- Response raw: `ctx.Response.SetBodyRaw(b)`

---

## 5. Raw TCP/Unix Mode

Quando `SERVICE_MODE=raw`, eliminam QUALQUER framework HTTP:

```go
func serveRawConn(conn net.Conn, classifier *handler.Classifier) {
    r := bufio.NewReaderSize(conn, 4096)
    reqLine, _ := r.ReadSlice('\n')         // "POST /fraud-score HTTP/1.1"
    // headers manualmente (Content-Length, Connection)
    io.ReadFull(r, bodyBuf[:contentLength])  // body exato
    fraudCount := classifier.FraudCount(bodyBuf[:contentLength])
    writeFraudHTTP(w, fraudCount, keepAlive) // resposta HTTP manual
}
```

Overhead do fasthttp vs raw: ~0.5μs. Só vale com fd-passing.

---

## 6. IVF Multi-Probe ✅ Parcial (nprobe=3 implementado)

| Característica | Nós (1 cluster) | Eles (8-20 clusters) |
|:--------------|:---------------:|:--------------------:|
| Vetores por busca | ~3.000 | ~3.000-7.500 |
| Recall | ~95-97% | >99.9% |
| FP/FN (v20) | 34/42 | 0/0 |

Eles usam `quickProbe=8` (top-8 centroides) e `expandedProbe=20` (expansão para recall). Blocos de 16 vetores com distância min/max ao centroide permitem pular blocos irrelevantes. Vetores em `int16` (escala 10000) para mais precisão.

**Nosso status**: Implementado nprobe=3 com maxScanPerCluster=5000 (ADR-44). Varre ~15.000 vetores (3 clusters x 5000), latencia ~130us. Block bounds e int16 ainda nao implementados.

---

## 7. Warmup Pré-teste ✅ Implementado

Container separado com curl faz 48 POSTs simulados antes do teste real:

```sh
i=1; while [ "$i" -le 48 ]; do
    curl -fsS -d "$payload" http://lb:9999/fraud-score >/dev/null
    i=$((i + 1))
done
```

Aquece caches de CPU, resolve page faults, compila hot paths.

---

## 8. Ajustes de Memória

| Parâmetro | Eles | Nós |
|-----------|:----:|:---:|
| GOMEMLIMIT | 60MiB | 150MiB |
| GC_MODE | off (SetGCPercent(-1)) | GOGC=off |
| Startup | debug.FreeOSMemory() | — |

---

## 9. Tabela Comparativa

| Decisão | Eles | Nós | Ganho potencial |
|---------|------|-----|:---------------:|
| Proxy/LB | C++ fd-passing | Go parse+encode+fwd | **Alto** |
| Parsing | Manual byte-a-byte | Codec binário | **Médio** |
| Respostas | Pre-alocadas | Pre-alocadas ✅ | Implementado |
| HTTP Server | fasthttp | stdlib net/http | **Médio** |
| IVF | 8-20 clusters + block pruning | 1 cluster | **Médio** |
| Semáforo | Concurrency: 4096 | Nao-bloqueante 1024 ✅ | Implementado |
| Warmup | 48 POSTs | Nenhum | **Médio** |
| GOMEMLIMIT | 60MiB | 150MiB | **Baixo** |

---

## 10. Prioridade de Implementação

| Prioridade | Melhoria | Esforço | Impacto |
|:----------:|:---------|:-------:|:-------:|
| 1 | Respostas pre-alocadas | 10min | Baixo | ✅ v25 |
| 2 | Warmup container | 30min | Medio | ✅ v25 |
| 3 | Fasthttp | 2-4h | Médio |
| 4 | Parsing JSON na API (eliminar codec) | 4-8h | Alto |
| 5 | IVF multi-probe + block bounds | 8-16h | Medio | ✅ Parcial (nprobe=3 v26) |
| 6 | C++ fd-passing LB | 16-32h | **Muito alto** |

**Recomendação**: comecar com respostas pre-alocadas e warmup (✅ feito). K-means corrigido (✅ v26). Proximos: fasthttp + parsing JSON manual na API. Visao de longo prazo: fd-passing.

---

## 11. Evolução de Resultados (v27 → v38)

| Versão | Estratégia | HTTP Errs | Failure | p99 | Score |
|:------:|:-----------|:---------:|:-------:|:---:|:-----:|
| v27 | baseline | 10.006 | 53.9% | 2002ms | −6000 |
| v28 | timeouts 200ms | 12.321 | 80.0% | 2002ms | −6000 |
| v29 | proxy 0.15 CPU | 9.840 | 46.8% | 2002ms | −6000 |
| v30 | semáforo 1024 | 5.550 | 16.0% | 2001ms | −6000 |
| v31 | nprobe=3 | 6.873 | 29.3% | 2002ms | −6000 |
| v32 | revert nprobe=2 | 8.951 | 29.7% | 2002ms | −6000 |
| v34 | parser JSON manual | 3.783 | 36.6% | 2002ms | −6000 |
| v35 | correção keyLen | **1.812** | 5.7% | 2001ms | **−3565** |
| v36 | timeouts 100ms | 7.709 | 17.7% | 2001ms | −6000 |
| v37 | timeouts 200ms + proxy 0.17 | 1.587 | 5.4% | 2001ms | −3503 |
| v38 | proxy 0.19 + client 200ms | **57** | **2.2%** | **195ms** | **+1082 🏆** |
| v39 | nprobe=3 (neutro) | 70 | 2.3% | 192ms | +1073 |
| v40 | K=7, thr=0.6 | 112 | 2.1% | 201ms | +922 |
| v41 | K=7, thr=0.572 | 161 | 2.2% | 206ms | +866 |
| v42 | reverte K=5 + diag | 70 | 2.3% | 192ms | +1075 |
| v43 | int16+Euclidiana ❌ | 56 | 35.9% | 198ms | −2296 |
| v44 | reverte int8 (platô) | 67 | 2.3% | 192ms | +1076 |

### Análise de tendência

1. **Parser JSON manual (v34→v35)**: O maior salto de qualidade. Substituir o codec
   binário por parsing JSON manual na API + proxy forward bruto reduziu HTTP errors
   em 67% (5.550→1.812) e tirou o detection_score do corte (−3000→−565).

2. **CPU do proxy (v35→v37)**: Aumentar proxy de 0.15→0.17 reduziu HTTP errors em
   12.4% (1.812→1.587) e melhorou o score em 61 pontos. Ganho modesto mas consistente.

3. **Timeouts (v35→v36→v37)**: 100ms foi desastroso (+325% HTTP errors). 200ms é
   o piso estável. O client timeout de 500ms (proxy→API) era inconsistente com o
   server timeout de 200ms — reduzido na v38.

4. **p99 estagnado**: Desde a v30, o p99 oscila entre 2000.92ms e 2002.08ms, sempre
   acima do corte de 2000ms. A latência de processamento real é <1μs — o p99 é
   dominado por scheduling/queueing delays do kernel e do runtime Go.

5. **v38 — Rompeu a barreira**: Proxy 0.19 CPU + client timeout 200ms derrubou
   o p99 de 2001ms para 195ms (−90%). HTTP errors colapsaram 96% (1587→57).
   Primeiro score positivo da série (+1082). O detection_score também ficou
   positivo (+373).

6. **v39 — nprobe=3 não ajudou**: FN ficou idêntico (413), FP variou 1 (ruído).
   nprobe=2 já encontra os vizinhos corretos. O problema não está no recall
   da busca, mas no algoritmo KNN=5 com threshold 0.6.

7. **v40 — K=7**: Com 7 vizinhos, cada um tem peso de ~14% (vs 20% com K=5).
   Threshold 0.6 fica mais conservador: 4/7=0.571 → approve (antes 3/5=0.6 → deny).
   Hipótese: reduz FP por ser mais difícil negar uma transação.

8. **Próximo desafio**: Se K=7 não resolver, investigar parser JSON (possíveis bugs
   sutis), vetorização (fórmulas), ou considerar K adaptativo.

### Lições aprendidas

- **Proxy é o gargalo principal**, não as APIs. Dar mais CPU ao proxy consistentemente
  melhora o score (v35→v37: +61 pontos).
- **Timeout de 100ms é contraproducente** com GOMAXPROCS=1 — o scheduling jitter do
  kernel facilmente ultrapassa 100ms, causando falsos timeouts em cascata.
- **Parser JSON manual + forward bruto** foi a melhor decisão arquitetural da série,
  responsável pelo salto de −6000 para −3565.
- **nprobe=2 é suficiente** — nprobe=3 (v31) piorou HTTP errors (+24%) sem melhorar
  significativamente a detecção.
- **Client timeout deve ser ≤ server timeout** — Proxy client 500ms com server 200ms
  causava espera fantasma. Alinhar ambos em 200ms (v38) foi crucial para o salto.
- **Depois de romper a barreira do p99, o foco muda para detecção** — Com p99=195ms,
  a folga de latência permite investir em recall (nprobe=3) sem medo de degradação.