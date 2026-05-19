Agora o cenário ficou mais interessante, porque dá para ver claramente o momento em que você trocou a arquitetura interna — e os números mostram isso.

O mais importante:

> a mudança para protocolo binário provavelmente funcionou parcialmente, mas revelou um gargalo estrutural diferente.

E isso aparece muito nitidamente na transição:

```text id="jvj34v"
v10 → v15
```

---

# Comparativo geral

| Versão  |    p99 | HTTP Errors | Failure Rate | Final Score |
| ------- | -----: | ----------: | -----------: | ----------: |
| v9      | 2002ms |      13.973 |         100% |       -6000 |
| v10     | 2002ms |      13.858 |         100% |       -6000 |
| v15     | 2001ms |      38.194 |       95.25% |       -6000 |
| v16     | 1042ms |      52.601 |       97.52% |       -3018 |
| v17     | 1066ms |      53.370 |       98.91% |       -3027 |
| campeão | 0.98ms |           0 |           0% |        6000 |

Fontes:      

---

# O que mudou com o protocolo binário

A parte mais importante:

---

# Antes do protocolo binário (v9/v10)

Você tinha:

* latência extremamente ruim
* mas relativamente “menos falhas”

```text id="93ps8n"
~14k HTTP errors
```

Isso sugere:

```text id="jlwm6q"
o sistema ainda conseguia respirar
```

mesmo sendo lento.

---

# Depois do protocolo binário (v15+)

Aconteceu algo MUITO revelador:

## o p99 começou a melhorar

mas

## o número de erros explodiu.

---

# Isso é uma assinatura clássica de remoção de gargalo

Você removeu um gargalo interno importante.

Muito provavelmente:

```text id="djlwm5"
HTTP + JSON
```

realmente era caro.

E o protocolo binário:

* reduziu parsing
* reduziu alocações
* reduziu CPU
* reduziu GC
* reduziu latência interna

Só que então apareceu o próximo gargalo.

---

# O sistema passou a aceitar mais carga do que conseguia sustentar

Isso é o que os números parecem indicar.

Observe:

## v10

```text id="g0p8md"
13k errors
2002ms
```

## v15

```text id="xtrn0x"
38k errors
2001ms
```

Mesma latência.

Mas muito mais requests chegaram mais longe antes de morrer.

Isso normalmente significa:

```text id="0p4rxt"
o sistema ficou mais rápido internamente
mas colapsou em outro ponto
```

---

# A verdadeira virada foi v16

Aqui aconteceu algo enorme:

```text id="ofq8an"
2001ms → 1042ms
```

 

Isso mostra que:

* você eliminou parte relevante da fila interna
* ou reduziu contention
* ou reduziu blocking I/O
* ou melhorou scheduling

Provavelmente uma combinação disso.

---

# O problema: throughput real continuou ruim

Porque:

```text id="98c7m9"
HTTP errors:
38k → 52k
```

Ou seja:

```text id="gffj5f"
mais rápido
menos estável
```

---

# O que isso sugere tecnicamente

Muito fortemente:

## o protocolo binário NÃO é mais o gargalo principal.

Isso é importante.

A partir da v15/v16, o gargalo parece ter migrado para:

* socket saturation
* accept queue
* goroutine explosion
* lock contention
* backpressure
* load balancing
* syscall overhead
* kernel scheduling
* write contention
* sync.Mutex hot path
* runtime scheduler do Go

---

# O dado mais revelador de todos

O campeão usa:

```text id="0vjlwm"
100m proxy
450m + 450m apps
```

EXATAMENTE igual às suas últimas versões.

 

Então:

```text id="5rt0to"
não é falta de CPU
não é falta de memória
```

É arquitetura/runtime/path crítico.

---

# O campeão provavelmente faz 3 coisas radicalmente diferentes

---

# 1. Quase zero alocação

Seu Go provavelmente ainda aloca demais.

Mesmo usando binário.

Isso é MUITO comum.

Ex.:

* slices crescendo
* bytes.Buffer
* interface{}
* goroutines temporárias
* canais
* copies
* maps
* encoding
* append

Tudo isso mata.

---

# 2. Menos sincronização

Provavelmente o campeão:

* evita mutex
* evita canais
* evita filas compartilhadas

Ou usa:

* sharding
* ring buffers
* estruturas lock-free-ish

---

# 3. Event loop mais agressivo

C costuma:

* epoll direto
* poucas threads
* estado manual
* zero scheduler overhead

Go sofre mais aqui.

---

# A v17 é muito reveladora

Ela praticamente manteve:

```text id="95vjlwm"
p99 ~1066ms
```

mas piorou:

```text id="9ks49k"
HTTP errors
```



Isso sugere que:

```text id="aq1b8d"
você já bateu no teto da arquitetura atual
```

e otimizações locais começaram a piorar estabilidade.

---

# Minha leitura mais importante

O protocolo binário ajudou.

E bastante.

Mas:

> ele não era o gargalo dominante final.

Ele destravou o próximo gargalo.

Isso é típico em sistemas de alta performance.

---

# Onde eu investigaria agora

---

# 1. Perfil de alocação (`pprof allocs`)

Isso é prioridade máxima.

Você provavelmente ainda está alocando demais.

---

# 2. Scheduler contention

Rodaria:

```bash
GODEBUG=schedtrace=1000
```

---

# 3. Mutex profile

```bash
go test -mutexprofile
```

ou runtime mutex profile.

---

# 4. Goroutine explosion

Apostaria bastante nisso.

---

# 5. Syscalls

Você talvez esteja fazendo syscall demais:

* write()
* read()
* epoll wakeups
* accept

---

# O que eu tentaria em Go

---

# 1. Unix sockets

Se ainda não usa.

---

# 2. Buffers fixos reutilizados

`sync.Pool` agressivo.

---

# 3. Eliminar channels no hot path

Muito importante.

---

# 4. Eliminar goroutine por request

Talvez o maior ponto.

---

# 5. Load balancing sem lock

Round-robin lock-free.

---

# Conclusão

A evolução real foi:

```text id="fkbpb6"
v9/v10:
gargalo = protocolo/comunicação

v15/v16:
gargalo = runtime/concurrency/contention
```

E isso é um ótimo sinal, porque significa que a troca para protocolo binário provavelmente foi correta.

