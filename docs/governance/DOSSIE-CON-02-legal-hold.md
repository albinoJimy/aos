# Dossiê de decisão — CON-02: legal hold e job de expiração (obrigação de produto vs responsabilidade do operador)

> **O que este documento é.** Um dossiê de **apoio à decisão do dono**, no mesmo molde do
> [`DOSSIE-Arbitragem-6.5.md`](DOSSIE-Arbitragem-6.5.md): reúne o facto verificável, o estado do
> código e as opções, para o dono decidir. **Não decide** — a folha de decisão (§6) fica por
> preencher. A `§7` traz a minha leitura técnica **não-vinculativa**, claramente separada.
>
> **Origem:** achado **CON-02** da auditoria v4 (`analises/08_Relatorio_Auditoria_Multiagente_v4.md:114`),
> CA em aberto de `specs/EPIC-18` §7 («**decisão do dono**»), e a lacuna nomeada em
> `tecnica/14_Matriz_Conformidade.md:175` («eixo POR ATRIBUIR»). É uma das **únicas** dívidas de
> conformidade **sem eixo/dono/data declarados**.

---

## 1. O facto (verificado em código, hoje)

| Peça | Existe? | Composta no nó? | Superfície de administração? |
|---|---|---|---|
| `audit.LegalHold` (suspende o shred de um titular) — AOS-092 | **sim** (`platform/audit`) | **sim** — `bootstrap.go:931`, exposto em `Node.DSARHolds` (`:428`) | **não** — nenhuma rota `/hold` em `api.go` (só `POST /dsar/erase`) |
| `audit.ExpirationJob` (varredor TTL-por-classe) — AOS-092 | **sim** (`platform/audit/expiration.go`) | **NÃO** — `grep NewExpirationJob` em `cmd/aos`/`integration` → **0 chamadores de produção** | **não** |

Ou seja: o **mecanismo existe e é fail-closed** (`ErrLegalHold` suspende o shred; `ErrRetentionActive`
barra o purge antes do TTL), o `LegalHold` está **cablado mas só programaticamente** (um operador não
tem como colocar/levantar um hold sem código), e o `ExpirationJob` **não corre no nó**.

## 2. Porque foi REBAIXADO no v4 (e porque isso importa para a decisão)

O v4 rebaixou CON-02 de ALTO com uma razão que é ela própria um facto técnico: **«não há apagamento
real para suspender»**. O crypto-shred do nó destrói hoje a **KEK por-titular do vault DEMO-GRADE**;
o conteúdo dos runs que o Event Store persiste está em **texto-claro** e **não** cifrado por titular
(a cifra por-titular do substrato é o **AOS-093**, deferido — ver `bootstrap.go` (7c) e a arbitragem
`A-DEF-301`). Consequência para CON-02: um **legal hold** sobre um apagamento que ainda **não torna o
conteúdo ilegível** tem valor prático limitado; e um **job de expiração** que varre para «expirar»
conteúdo que continua legível é meia-garantia. **A utilidade real de CON-02 está acoplada à de
AOS-093.** Isto não anula a decisão — informa-a (ver Opção C).

## 3. A decisão (é do dono — a CA de EPIC-18 já a enquadra com um «OU»)

A CA diz: «Legal hold e job de expiração recebem **eixo/dono/data declarados** OU **superfície de
administração**». Três opções reais, não duas:

### Opção A — RESPONSABILIDADE DO OPERADOR (o nó dá o mecanismo, não a administração)
O nó entrega os **mecanismos e portas** (`LegalHold`, `ExpirationJob`) — já existem — e o **operador**
conduz o hold/expiração pela sua própria ferramenta/política (chamando as portas, ou por um sidecar).
Formaliza-se **declarando eixo/dono/data** (uma entrada no `REGISTO-Deferimentos.md` com dono e
gatilho), sem nova superfície no nó. **Prós:** zero código novo; mantém o nó mínimo. **Contras:** uma
garantia de conformidade (legal hold) que depende de cada operador a reimplementar não é uma garantia
do *produto*; risco de inconsistência entre deployments.

### Opção B — OBRIGAÇÃO DE PRODUTO (o nó traz a administração)
O nó passa a expor uma **superfície de administração autenticada**: rotas `POST /dsar/hold` e
`/dsar/release` (no molde do plano de controlo — ed25519/credencial forte de AOS-160/205) e o
`ExpirationJob` **composto e conduzido** (agendado ou por rota). Nasce um **ticket** (AOS-NNN,
execução EPIC-09). **Prós:** legal hold e retenção passam a ser garantia do produto, uniformes e
auditáveis. **Contras:** código e superfície de ataque novos; e — ver §2 — governaria hoje um
apagamento que ainda não é real.

### Opção C — DEFERIR COM EIXO, ACOPLADO A AOS-093 (a via que a §2 sugere)
Declara-se o eixo/dono/data (fecha a CA de EPIC-18 e a lacuna de `tecnica/14`), **atando CON-02 ao
AOS-093**: a superfície de administração (Opção B) só se constrói **depois** de a cifra por-titular
tornar o apagamento real — antes disso seria governança sobre uma erasure incompleta. A decisão
**produto-vs-operador fica registada** (recomendo produto), mas a **execução** dispara com o gatilho
«AOS-093 entregue». **Prós:** honesto quanto à dependência; fecha a dívida-sem-eixo já; sequencia
certo. **Contras:** adia a superfície (aceitável se o apagamento real também está adiado).

## 4. O que não está em causa (para não inflacionar)

- O **DSAR de apagamento** existe e está exposto (`POST /dsar/erase`, `dsar.go`) — CON-02 **não** é
  «o nó não apaga»; é «o nó não tem como **suspender** (hold) nem **expirar por TTL** de forma
  administrável».
- Os mecanismos são **fail-closed** e testados (AOS-092) — a decisão é de **composição/superfície e
  de dono**, não de correcção do mecanismo.
- CON-02 **não** duplica AOS-093 (cifra do substrato) — é a camada de **administração** sobre a
  erasure, não a erasure em si.

## 5. Relações e dependências

- **AOS-093** (crypto-shredding / cifra por-titular do substrato) — a utilidade de CON-02 acopla-se a
  ele (§2). Opção C ata-os explicitamente.
- **AOS-160/AOS-205** (canal autenticado / credencial forte) — se Opção B, as rotas de hold reutilizam
  esta autenticação, não inventam nova.
- **EPIC-09** (governança/conformidade) — epic de execução em qualquer das opções.

## 6. Folha de decisão (a preencher pelo dono)

| Campo | Valor |
|---|---|
| Opção escolhida (A / B / C) | `______` |
| Produto ou operador? | `______` |
| Eixo/dono/data declarados | eixo: `______`  dono: `______`  data/gatilho: `______` |
| Cria ticket AOS-NNN agora? | `SIM (nº ____) / NÃO` |

**O que a escolha aciona:**
- **A** → entrada no `REGISTO-Deferimentos.md` (dono + gatilho «operador»); CA de EPIC-18 §7 fecha
  como «eixo declarado»; `tecnica/14:175` deixa de dizer «POR ATRIBUIR».
- **B** → novo ticket AOS-NNN (EPIC-09) com CA falsificáveis (rotas de hold autenticadas + job
  composto + prova de suspensão/expiração); CA de EPIC-18 §7 fecha como «superfície de administração».
- **C** → entrada no `REGISTO-Deferimentos.md` com gatilho «AOS-093 entregue» + a decisão
  produto-vs-operador registada; CA de EPIC-18 §7 fecha como «eixo declarado (acoplado a AOS-093)».

---

## 7. Leitura técnica (não-vinculativa — input do assistente, não decisão)

> Mesmo estatuto da `LEITURA-TECNICA-Merito-6.5.md`: avalio o que o código diz; a escolha é do dono.

**Recomendo a Opção C, com a decisão de princípio «produto» registada.** Razões:

1. **Sequência.** Construir a administração agora (Opção B pura) é montar governança sobre uma erasure
   que a §2 mostra ainda **não ser real** — o legal hold suspenderia um shred que não torna o conteúdo
   ilegível. O valor entrega-se quando AOS-093 entrega; atar os dois é a leitura honesta do facto.
2. **Produto, não operador (em princípio).** Legal hold é uma **garantia de conformidade**; deixá-la a
   cada operador reinventar contraria a forma do produto («corres o nó AOS», não «montas a tua própria
   retenção»). Por isso descarto a Opção A como estado final — mas ela é aceitável como **ponte** até
   AOS-093 se o dono quiser fechar a dívida-sem-eixo imediatamente.
3. **Custo/risco.** A Opção C fecha já a única classe de dívida sem eixo (o objectivo de conformidade),
   sem gastar código numa superfície prematura nem alargar a superfície de ataque antes de haver o que
   proteger.

**Nota de honestidade:** eu compus o `LegalHold` no nó (AOS-172) e conheço o estado do AOS-093 desta
sessão — sou **fonte de facto**, não decisor. Os comandos de verificação estão na §1 para o dono
confirmar por si.

*Documento de apoio. Não altera a Carta, o registo nem a matriz de conformidade — alimenta a decisão.
A pronúncia do dono entra por commit próprio.*
