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

## 3. Respostas Pré-alocadas

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

## 6. IVF Multi-Probe com Block Bounds

| Característica | Nós (1 cluster) | Eles (8-20 clusters) |
|:--------------|:---------------:|:--------------------:|
| Vetores por busca | ~3.000 | ~3.000-7.500 |
| Recall | ~95-97% | >99.9% |
| FP/FN (v20) | 34/42 | 0/0 |

Eles usam `quickProbe=8` (top-8 centroides) e `expandedProbe=20` (expansão para recall). Blocos de 16 vetores com distância min/max ao centroide permitem pular blocos irrelevantes. Vetores em `int16` (escala 10000) para mais precisão.

---

## 7. Warmup Pré-teste

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
| Respostas | Pré-alocadas | Montagem manual | **Baixo** |
| HTTP Server | fasthttp | stdlib net/http | **Médio** |
| IVF | 8-20 clusters + block pruning | 1 cluster | **Médio** |
| Semáforo | Concurrency: 4096 | 128 bloqueante | **Baixo** |
| Warmup | 48 POSTs | Nenhum | **Médio** |
| GOMEMLIMIT | 60MiB | 150MiB | **Baixo** |

---

## 10. Prioridade de Implementação

| Prioridade | Melhoria | Esforço | Impacto |
|:----------:|:---------|:-------:|:-------:|
| 1 | Respostas pré-alocadas | 10min | Baixo |
| 2 | Warmup container | 30min | Médio |
| 3 | Fasthttp | 2-4h | Médio |
| 4 | Parsing JSON na API (eliminar codec) | 4-8h | Alto |
| 5 | IVF multi-probe + block bounds | 8-16h | Médio |
| 6 | C++ fd-passing LB | 16-32h | **Muito alto** |

**Recomendação**: começar com respostas pré-alocadas e warmup. Depois fasthttp + parsing JSON manual na API. Visão de longo prazo: fd-passing.