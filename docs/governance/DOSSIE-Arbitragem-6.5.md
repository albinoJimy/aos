# Dossiê de arbitragem — Carta §6.5 (Arquitecto de Plataforma + Responsável de Segurança)

> **O que este documento é.** Um dossiê de **apoio à decisão**: reúne, para cada pendência, o
> facto verificável, o registo factual e as opções de veredicto, para que os **dois papéis** do
> §6.5 possam pronunciar-se **por escrito**. **Não é uma pronúncia** — a §7 «Decisão» está por
> preencher. Nenhuma linha aqui substitui o veredicto dos árbitros nem a definição de «reaberta»
> que compete ao dono (§8.4 do registo).
>
> **Fonte de autoridade:** `specs/00_AOS_Carta.md` §6 (regra de congelamento) e
> `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` (o SLI do processo). Onde este
> dossiê e a Carta divergirem, a Carta manda.

---

## 1. O mecanismo, num ecrã

- **§6.4 — a fronteira a arbitrar:** «dívida escondida» (legítima, executa-se) **vs** «re-litígio»
  (proibido). A distinção é subjectiva **por desenho** — daí precisar de árbitro.
- **§6.5 — o teste e quem o aplica:** cada invocação do §6.4 é arbitrada pelo **Arquitecto de
  Plataforma _e_ Responsável de Segurança** (dois papéis, não um), que decidem **por escrito**.
  Critério: uma «descoberta» é dívida escondida **só** se assenta em **facto novo verificável**
  (código/build/painel) que **não existia** à data da decisão FIXA; caso contrário é re-litígio e
  é **recusada**.
- **§6.6 — o tripwire (SLI falsificável):** se, numa janela de **30 dias**, **≥ 2 decisões FIXAS
  forem reabertas** OU **≥ 2 invocações do §6.4 forem recusadas como re-litígio**, o mecanismo de
  congelamento **falhou** e a Carta é **revista na raiz** (não emendada à margem).

**Consequência de processo (a razão de este dossiê existir agora):** as quatro pendências abaixo
foram classificadas na Carta com a fórmula «não re-litígio / reavaliação de contexto» **auto-
atribuída por quem propôs a emenda** (a linha de aprovação diz, nas emendas 1.2 e 1.3,
«Segurança/Arquitectura **pendente**»). É exactamente o *loophole* que o §6.5 existe para fechar:
**ninguém habilitado declarou ainda que isto não foi re-litígio.** Além disso, o **sign-off de
Segurança e de Arquitectura é pré-condição da aceitação da v1** (Carta §5) — pelo que estas
arbitragens bloqueiam a v1, não são higiene opcional.

---

## 2. Estado do tripwire §6.6 — porque a ordem das pronúncias importa

| Evento | Decisão FIXA tocada | Classe 1.ª perna (hoje) | Se a 1.ª perna virar REABERTURA |
|---|---|---|---|
| REG-008 | ADR-017 ponto 1 (zero-dep) | **REABERTURA** (diff `-`/`+` na linha do §4.1) | já conta |
| REG-005 | D3 (SSE stdlib) | PENDENTE | vira REABERTURA |
| REG-006 | D5 (single-process) | PENDENTE | vira REABERTURA |
| REG-010 | ADR-016 (não-repúdio/identidade) | PENDENTE | vira REABERTURA |

**Leitura:** já há **1 REABERTURA confirmada** (REG-008) na janela 2026-07-22/23. As outras três
são `PENDENTE` **apenas porque a Carta não define «reaberta»** (§8.4) — o texto das linhas de D3,
D5 e ADR-016 no §4 **não foi alterado** (facto verificado por `git show 608e84c`). **Duas decisões
inseparáveis, e não uma:**

1. **(Dono, §8.4)** definir «reaberta». Se «pôr uma FIXA de novo em cima da mesa» **contar** como
   reabri-la, então REG-005/006/010 passam a REABERTURA e, com REG-008, são **4 na janela** ⇒
   **§6.6 dispara** ⇒ Carta revista na raiz.
2. **(Árbitros, §6.5)** para cada uma, decidir dívida-escondida **vs** re-litígio. **≥ 2 recusas
   como re-litígio** também disparam o §6.6, por via independente.

O tripwire não é um castigo — é o SLI a fazer o seu trabalho. O dossiê apresenta os factos; o
disparo (ou não) é consequência das duas pronúncias, não uma escolha à margem.

---

## 3. As pendências (uma por secção)

Para cada uma: a decisão e o seu estado, a emenda que lhe tocou, o **facto novo verificável** (o
teste do §6.5), o desfecho factual até hoje, e as **posições já registadas** dos dois lados — sem
veredicto (esse é da §7).

### 3.1 REG-005 — D3 (transporte SSE stdlib)

- **Decisão / estado:** D3 «Transporte SSE stdlib (não gRPC/WS/GraphQL)» — **FIXA** (§4.2), dono
  Plataforma, origem EPIC-13 §25.
- **Emenda que tocou:** 1.2, ponto E10 (`608e84c`) — marcou **REAVALIAR** face ao modelo de ameaça
  do nó-serviço.
- **Facto novo verificável (teste §6.5):** a forma do produto «nó `aos` deployável, exposto como
  **serviço de rede**» foi fixada no §2 em 2026-07-22; D3 fora fixada no contexto **BFF-atrás-de-
  SPA**. O artefacto sobre o qual a regra passou a aplicar-se **não existia** à data de D3. No
  mérito, satisfaz o teste.
- **Desfecho factual:** D3 **não foi revertida** — manteve-se e foi **endurecida** (SSE stdlib com
  backfill, resume-from-seq, dedup, backpressure — AOS-167, EPIC-15). Estado continua **FIXA**.
- **1.ª perna:** PENDENTE (linha de D3 no §4.2 intacta) — vira REABERTURA sob a definição «pôr de
  novo em cima da mesa».
- **A arbitrar:** dívida-escondida (facto novo real ⇒ legitimar a reavaliação) **vs** re-litígio.

### 3.2 REG-006 — D5 (BFF single-process)

- **Decisão / estado:** D5 «BFF single-process» — **FIXA (postura)** com gatilho de graduação
  **CONDICIONAL** a SLO/utilizadores (§4.2), dono Plataforma, origem EPIC-13 §25.
- **Emenda que tocou:** 1.2, ponto E10 (`608e84c`) — marcou **REAVALIAR**.
- **Facto novo verificável (teste §6.5):** o mesmo de D3 — o nó-serviço de rede não existia à data
  de D5; a separação «dois canais» passa de física a de protocolo/taint.
- **Desfecho factual:** D5 **não foi revertida** — o nó da v1 é **single-process**. Estado continua
  **FIXA**. Nota: D5 é a única com **gatilho CONDICIONAL** já inscrito (SLO/utilizadores) — pelo
  §6.2, «abre» só quando o gatilho ocorre; convém o veredicto distinguir «reavaliar a postura» de
  «accionar o gatilho condicional».
- **1.ª perna:** PENDENTE — como D3.
- **A arbitrar:** dívida-escondida **vs** re-litígio (e, se legitimada, confirmar que o gatilho
  condicional de D5 **não** foi accionado — não houve o facto SLO/utilizadores).

### 3.3 REG-010 — ADR-016 (não-repúdio HITL / identidade fim-a-fim)

- **Decisão / estado:** ADR-016 «fronteira de confiança da UI — BFF non-signing, WYSIWYS, 4-eyes»
  — **FIXA** (§4.1).
- **Emenda que tocou:** 1.2, ponto E10, 1.ª alínea (`608e84c`) — declarou **não-repúdio HITL e
  identidade fim-a-fim DEFERIDOS com o eixo D4**; a superfície de aprovação fica «estruturalmente
  completa mas **criptograficamente demo-grade**» até AOS-160 (ed25519) e AOS-162 (4-eyes
  atestado).
- **Facto verificável:** o eixo D4 (token spine, AOS-156) não estava desbloqueado à data; a
  emenda **escopa no tempo a garantia** de uma FIXA de segurança. **Actualização factual para o
  árbitro:** desde então, a Opção A do D4 foi enactada (EPIC-16: AOS-174/175/176/177) e a cadeia
  real de identidade/assinatura foi composta no nó — ou seja, **a condição temporal que
  justificava o «demo-grade» mudou**; o árbitro deve avaliar se o deferimento ainda subsiste ou se
  a garantia do ADR-016 já está realizada.
- **1.ª perna:** PENDENTE (linha do ADR-016 no §4.1 intacta) — a posição contrária registada é que
  «uma garantia de segurança que passa a demo-grade por emenda é, em substância, uma FIXA
  suspensa».
- **A arbitrar:** (a) o deferimento temporal foi dívida-escondida legítima **vs** re-litígio; e
  (b) dado o estado actual pós-EPIC-16, a garantia do ADR-016 está **realizada, ainda deferida, ou
  parcialmente** — com evidência de código.

### 3.4 REG-008 — ADR-017 ponto 1 (excepção escopada ao zero-dep) — **a única REABERTURA confirmada**

- **Decisão / estado:** ADR-017 ponto 1 «o binário do nó é **zero-dep** (stdlib + cedar-go)» —
  **FIXA** (§4.1), decisão de **supply-chain**.
- **Emenda que tocou:** 1.3 (`a16c0b6`) — **editou a própria linha do §4.1** para inscrever uma
  **excepção escopada** (lib WebAuthn/CBOR `go-webauthn/webauthn` no componente de autoridade de
  identidade **externo** ao nó, frente 4 do EPIC-16).
- **1.ª perna — NÃO admite dúvida:** é o caso (a) do §2.1 — o `git show a16c0b6` mostra a linha do
  ADR-017 a ser **substituída** (`-`/`+` no diff da autoridade). Conta **REABERTURA**. É a única das
  quatro que já conta para o §6.6 sem depender da definição de «reaberta».
- **Posições registadas:**
  - **A favor (mérito, 2.ª perna):** o **invariante** — *o binário do nó é zero-dep* — mantém-se
    **literalmente intacto**; a dep vive **fora** do artefacto do nó e passa pelos gates
    (sca/govulncheck, `go.sum` pinado, SBOM). Facto novo verificável: a frente 4 (attestation
    WebAuthn/AAGUID) **não é implementável só com a stdlib**.
  - **Contra:** a excepção a uma FIXA de **supply-chain** foi aprovada **sem o Responsável de
    Segurança** — que é, por desenho do §6.5, **um dos dois árbitros obrigatórios**. A pronúncia
    em falta é precisamente a dele.
- **A arbitrar:** legitimar a excepção escopada como dívida-escondida (invariante do nó intacto,
  facto novo real) **vs** recusá-la. **Nota de contagem:** como já é REABERTURA, basta que **uma**
  das PENDENTES (D3/D5/ADR-016) vire REABERTURA para o §6.6 disparar pela perna «≥2 reabertas».

---

## 4. Pré-condição ligada — sign-off de v1 (Carta §5)

Independentemente do §6.4, a Carta §5 faz o **sign-off de Segurança e de Arquitectura** das
emendas **pré-condição da aceitação da v1**. As linhas de aprovação das emendas **1.2** e **1.3**
dizem «Dono do produto; **Segurança/Arquitectura pendente**». Enquanto os dois papéis não
assinarem, a v1 **não é aceitável** pelo §5, mesmo com todos os gates verdes. Este dossiê serve as
**duas** pronúncias de uma vez: as arbitragens do §6.4 **e** o sign-off do §5.

---

## 5. Folha de decisão (a preencher pelos dois papéis)

> Preencher **cada linha** com veredicto + fundamentação escrita. O veredicto de §6.4 é um de:
> `DÍVIDA-ESCONDIDA` (legitimada) ou `RE-LITÍGIO` (recusada). Ambos os papéis assinam.

**Pré-passo (dono, §8.4):** definição de «reaberta» adoptada: `________________________________`
(determina se REG-005/006/010 são PENDENTE ou REABERTURA, e portanto o contador do §6.6).

| Pendência | Veredicto §6.4 | Fundamentação (facto novo? sim/não + qual) | Arquitecto Plataforma | Resp. Segurança |
|---|---|---|---|---|
| REG-005 (D3) | ______ | | ______ | ______ |
| REG-006 (D5) | ______ | | ______ | ______ |
| REG-010 (ADR-016) | ______ | | ______ | ______ |
| REG-008 (ADR-017 exc.) | ______ | | ______ | ______ |

**Sign-off de v1 (§5):** emenda 1.2 — Arquitectura `____` Segurança `____`; emenda 1.3 —
Arquitectura `____` Segurança `____`.

**Contador §6.6 após as pronúncias:** reaberturas na janela = `____`; recusas por re-litígio =
`____`. Dispara o tripwire? `SIM / NÃO`. Se SIM: a Carta é revista na raiz (§6.6).

---

## 6. O que cada desfecho aciona (para o registo)

- **Cada veredicto** preenche a coluna «Veredicto do árbitro (§6.5)» da linha REG-NNN em
  `REGISTO-Decisoes-Reabertas-e-Arbitragens.md` §3 (hoje «NÃO REALIZADA»).
- **Se o dono definir «reaberta»** no sentido lato, actualizar a coluna «Conta §6.6» de
  REG-005/006/010 de `PENDENTE` para `REABERTURA` e recontar.
- **Se o §6.6 disparar**, abrir a revisão-na-raiz da Carta (não emenda à margem) — é o desfecho
  **desenhado**, não uma falha do dossiê.
- **Os sign-offs de v1** desbloqueiam (ou não) o critério de aceitação do §5.

*Este dossiê não altera a Carta nem o registo — só os alimenta. As pronúncias entram por commit
próprio, com autoria dos dois papéis.*
