# Registo de decisões reabertas e de arbitragens — o SLI do processo de congelamento

| Campo | Valor |
|---|---|
| Documento | **Registo único** dos eventos que o tripwire da Carta §6.6 conta e das arbitragens do §6.5 |
| Autoridade | **Subordinado** à `specs/00_AOS_Carta.md`. Este ficheiro **não decide nada** — regista o que já foi decidido e torna-o contável. |
| Origem | AOS-200 (EPIC-18), achado **DEF-07** |
| Última actualização | 2026-07-26 |

---

## 1. Porque este ficheiro existe

A Carta §6.6 declara uma **condição de falsificação** da promessa anti-retrabalho:

> «se, numa janela de 30 dias, **≥ 2 decisões FIXAS forem reabertas** OU **≥ 2 invocações
> do §6.4 forem recusadas como re-litígio pelo árbitro**, o mecanismo de congelamento
> **falhou** e a Carta é revista na raiz (não emendada à margem). **Este contador é o SLI
> do próprio processo.**»

São **duas pernas distintas, e a distinção é de propósito**:

- a **1.ª perna** mede **churn factual** — decisões FIXAS reabertas. O texto **não menciona o
  árbitro**. Não pergunta se a reabertura teve mérito: uma reabertura justificada continua a
  ser uma reabertura, e é precisamente isso que o §6.6 quer apanhar («o congelamento não está
  a aguentar»);
- a **2.ª perna** mede **veredictos** — invocações do §6.4 recusadas **pelo árbitro** do §6.5.

E a §6.5 institui esse **árbitro** — **Arquitecto de Plataforma + Responsável de Segurança**
(dois papéis, não um) — que decide **por escrito** se uma invocação do §6.4 é *dívida
escondida* (aceite) ou *re-litígio* (recusada).

Até AOS-200, **nem o contador nem o registo de arbitragens existiam em lado nenhum do
corpus**. A consequência não é que a promessa esteja falsificada — é que era
**infalsificável**, exactamente a condição que o §6.6 foi escrito para evitar. Este ficheiro
é o substrato do contador: uma tabela estável, uma linha por evento, calculável por comando.

**Este ficheiro não altera a Carta.** Alterar a Carta exige emenda datada do dono (§7). As
lacunas encontradas ao construir este registo estão no §8 como **pendências para o dono**,
não como trabalho de engenharia.

---

## 2. Como se usa

### 2.1 Quem escreve o quê (as duas pernas têm autores diferentes)

| Coluna / valor | Quem o escreve | Porquê |
|---|---|---|
| Todas as colunas descritivas (ID, Data, Decisão tocada, Quem decidiu, Referência) | Quem redige a emenda, o ADR ou a decisão — **no mesmo commit** que a introduz. | Registo. |
| `Conta §6.6 = REABERTURA` | **Quem redige a emenda.** É uma **constatação FACTUAL**, verificável por `git show` sobre a Carta — **não** depende do árbitro. | A 1.ª perna do §6.6 não menciona o árbitro (§1). Fazê-la depender dele mataria a perna. |
| `Natureza = divida-escondida` ou `re-litigio`, e `Conta §6.6 = RECUSA` | **Só** o árbitro do §6.5 (Arquitecto de Plataforma **e** Responsável de Segurança, os dois). | São **veredictos**, e a 2.ª perna diz «recusadas … pelo árbitro». |

**Teste objectivo da 1.ª perna (aplicar antes de escrever a célula):** uma decisão FIXA do §4
da Carta foi reaberta quando uma emenda do §7

- **(a) alterou a linha dessa decisão no registo §4** — estado, ou conteúdo normativo (inscrever
  uma excepção, restringir, substituir, suspender); **ou**
- **(b) marcou-a para reavaliação, ou escopou/adiou a garantia que ela dá**, sem lhe editar a linha.

O caso **(a)** é `REABERTURA` **sem discussão possível** — o texto da decisão mudou, e isso
lê-se no `git diff`. O caso **(b)** é `PENDENTE`: a Carta **não define «reaberta»** (§8.4), e
não é este ficheiro que pode fechar a definição. Os dois números são reportados em separado
pelo §5.1 — o estrito (só (a)) e o limite superior (a)+(b) — para que **«não sei» não seja
operacionalmente igual a «não»**.

**Regra de honestidade que decorre do §6.5:** os rótulos `divida-escondida` e `re-litigio`
são **veredictos**, não auto-descrições. Quem propõe uma alteração **não pode** classificar-se
a si próprio como «dívida escondida» — se o fizesse, o §6.4 seria o *loophole* que o §6.5 foi
escrito para fechar. Enquanto o árbitro não se pronunciar, a Natureza fica `emenda-sobre-FIXA`
— e é essa a **dívida de arbitragem** que o §5.1 conta e avisa.

### 2.2 Quando se escreve — **PROPOSTA ao dono, não obrigação em vigor** (ver §8.1)

Este ficheiro é **subordinado** (§1) e a Carta **não o referencia**: não pode, por si, criar
obrigações de processo. O que se segue é a **regra proposta**, aplicada por convenção neste
registo e a submeter a emenda do dono (§8.1). Até essa emenda existir, é prática recomendada.

Propõe-se que uma linha nova passe a ser obrigatória sempre que:

1. se acrescenta uma emenda ao §7 da Carta;
2. se toca (altera, escopa, excepciona, marca para reavaliação) uma decisão **FIXA** do §4;
3. se invoca o §6.4 («isto é dívida escondida, não re-litígio»);
4. o árbitro do §6.5 se pronuncia — nesse caso **actualiza-se a linha existente** (Veredicto +
   Natureza + Conta), não se cria uma nova;
5. se emite um ADR que legitima uma excepção a um invariante já publicado.

### 2.3 Vocabulário controlado (obrigatório — o contador valida-o e falha se for violado)

**Natureza:**

| Valor | Significado |
|---|---|
| `marco` | Evento de referência (ratificação, congelamento). Não conta. |
| `emenda` | Emenda ao §7 que **não** toca uma decisão FIXA do §4. |
| `emenda-sobre-FIXA` | Emenda que **toca** uma decisão FIXA. Classificação §6.4 **por arbitrar** — é a dívida de arbitragem que o §5.1 avisa. |
| `arbitragem` | Pronúncia registada sobre uma fronteira/invariante (§6.5 ou ADR equivalente). |
| `divida-escondida` | **Veredicto do árbitro:** facto novo verificável ⇒ aceite. |
| `re-litigio` | **Veredicto do árbitro:** sem facto novo ⇒ recusada. **Conta para a 2.ª perna** mesmo que a coluna Conta traga `REABERTURA` (um evento pode ser as duas coisas). |

**Conta §6.6:**

| Valor | Significado |
|---|---|
| `REABERTURA` | Teste objectivo **(a)** do §2.1 verificado: a linha da FIXA no §4 foi alterada por emenda. Conta para a **1.ª perna**. Escrito por quem redige a emenda. |
| `RECUSA` | O árbitro recusou uma invocação do §6.4 como re-litígio. Conta para a **2.ª perna**. Escrito só pelo árbitro. |
| `PENDENTE` | Teste **(b)** do §2.1: tocou uma FIXA sem lhe alterar a linha. **Indecidível quanto à 1.ª perna** até o dono definir «reaberta» (§8.4). Não conta no estrito; **conta no limite superior**. |
| `NAO` | Não toca nenhuma decisão FIXA do §4. |

> Nota: `PENDENTE` diz respeito à **1.ª perna** (falta a *definição*). A dívida de **arbitragem**
> (falta o *árbitro*, 2.ª perna) é sinalizada pela Natureza `emenda-sobre-FIXA`. São coisas
> diferentes e são contadas em separado.

**Invariantes de formato (o comando do §5.1 valida-os e aborta com código 2 se forem violados):**

- uma linha por evento, **sem quebras de linha** dentro da linha;
- **sem o carácter `|` dentro das células** (a justificação longa vai para as notas do §4);
- data em **ISO `AAAA-MM-DD`**;
- ID sequencial `REG-NNN`, **nunca reutilizado**. A tabela ordena-se por **data**, pelo que um
  ID mais alto pode aparecer acima de outro mais baixo quando o evento é registado
  retroactivamente (é o caso do REG-010);
- exactamente **8 colunas**, pela ordem da tabela do §3;
- as células `Natureza` e `Conta §6.6` só podem conter valores do vocabulário acima. Negrito,
  crases e espaços à volta são tolerados (o parser normaliza-os); **qualquer outro valor aborta
  o contador** — não é ignorado em silêncio.

---

## 3. O registo

| ID | Data | Decisão tocada | Natureza | Quem decidiu | Veredicto do árbitro (§6.5) | Conta §6.6 | Referência |
|---|---|---|---|---|---|---|---|
| REG-000 | 2026-07-22 | Carta v1.0 — ratificação e congelamento | marco | Dono do produto | n/a | NAO | commit `9d8050b`; Carta §7 v1.0 |
| REG-001 | 2026-07-22 | D4 — autoridade de identidade (estado ABERTA) | emenda | Dono do produto | n/a — D4 não estava FIXA | NAO | commit `42dc95d`; Carta §7 emenda 1.1; §4.2 |
| REG-002 | 2026-07-22 | DoD da v1 (Carta §5) — novo pré-requisito «D4 desbloqueado» | emenda | Dono do produto | n/a — o §5 não é decisão do registo §4 | NAO | commit `42dc95d`; Carta §7 emenda 1.1; §5 |
| REG-003 | 2026-07-22 | Afirmação «§4.1 todas FIXAS» falsa + emissão do ADR-017 + criação do árbitro §6.5 e do tripwire §6.6 | emenda | Dono do produto | n/a — correcção factual e decisões novas | NAO | commit `608e84c`; emenda 1.2 ponto E9; §4.1 §6.5 §6.6 |
| REG-004 | 2026-07-22 | System Spec §1 e non-goal datado single-host sem-HA | emenda | Dono do produto | n/a — documento subordinado | NAO | commit `608e84c`; emenda 1.2 ponto E3 |
| REG-005 | 2026-07-22 | **D3 — transporte SSE stdlib (FIXA)** marcada para REAVALIAR | divida-escondida | Dono do produto | **LEGITIMADA** — dívida-escondida (§6.5, 2026-07-29, N-011); ver N-005/N-011 | NAO | commit `608e84c`; emenda 1.2 ponto E10; §4.2 |
| REG-006 | 2026-07-22 | **D5 — BFF single-process (FIXA)** marcada para REAVALIAR | divida-escondida | Dono do produto | **LEGITIMADA** — dívida-escondida (§6.5, 2026-07-29, N-011); ver N-006/N-011 | NAO | commit `608e84c`; emenda 1.2 ponto E10; §4.2 |
| REG-010 | 2026-07-22 | **ADR-016 (FIXA)** — não-repúdio HITL e identidade fim-a-fim DEFERIDOS com D4; aprovação fica demo-grade | divida-escondida | Dono do produto | **LEGITIMADA** — dívida-escondida; garantia hoje em larga medida CUMPRIDA (§6.5, 2026-07-29, N-011); ver N-010/N-011 | NAO | commit `608e84c`; emenda 1.2 ponto E10; §4.1 §4.2 |
| REG-007 | 2026-07-23 | D4 — Opção A: provisionar a autoridade completa (Camada B) | emenda | Dono do produto | n/a — D4 não estava FIXA | NAO | commit `a16c0b6`; emenda 1.3; EPIC-16 |
| REG-008 | 2026-07-23 | **ADR-017 ponto 1 — binário zero-dep (FIXA)**: excepção escopada inscrita na própria linha do §4.1 | divida-escondida | Dono do produto | **LEGITIMADA** — dívida-escondida (§6.5, 2026-07-29, N-011); permanece REABERTURA na 1.ª perna; ver N-008/N-011 | REABERTURA | commit `a16c0b6`; emenda 1.3; §4.1 |
| REG-009 | 2026-07-25 | Invariante de sentido de dependências entre camadas (AGENTS.md §3) | arbitragem | Equipa AOS — **não** os dois papéis do §6.5 | LEGITIMADA como excepção intencional escopada — ver nota N-009 | NAO | commit `db5c19f`; ADR-019; EPIC-17 AOS-179 |
---

## 4. Notas de classificação (a fundamentação, evento a evento)

Dois critérios distintos, um por perna:

- **1.ª perna (`Conta §6.6`)** — o teste objectivo do §2.1, verificável no `git diff` da Carta.
- **2.ª perna (`Natureza`)** — o do §6.4 + §6.5: uma «descoberta» é **dívida escondida** só se
  assenta em **facto novo verificável** (código, build ou painel) que **não existia à data da
  decisão FIXA**; caso contrário é **re-litígio**. Só o **árbitro** (dois papéis) o pode declarar.

O mérito pertence à 2.ª perna. **Não absolve a 1.ª:** uma reabertura com mérito continua a ser
uma reabertura.

### N-001 / N-002 — Emenda 1.1 (D4 passa a pré-requisito da v1)

**Não é reabertura.** A D4 estava **ABERTA-deferida** no §4.2, não FIXA. Resolver uma decisão
ABERTA é o caminho normal do §6.3 (dono + escalada), não o §6.1. O facto que a motivou — o
achado ALTO «forma sobre-reivindicada» do painel adversarial `wamnbffrk` — é um facto novo
verificável e o painel é uma fonte que o §6.5 admite explicitamente.

O **REG-002** é registado à parte porque a emenda 1.1 também **acrescentou um requisito ao
DoD da v1 (§5)**, que tinha sido ratificado no mesmo dia. O §6 fala de «decisões FIXAS» do
registo §4; o §5 não é uma decisão desse registo, pelo que o tripwire não o cobre. Regista-se
na mesma, e a densidade que isso revela está no §8.

### N-003 / N-004 — Emenda 1.2, pontos E9 e E3

**Não são reaberturas.** O E9 **corrige uma afirmação falsa** («§4.1 — todas FIXAS», quando o
ADR-017 estava reservado-mas-não-redigido), **emite** o ADR-017 e **cria** o §6.5 (árbitro) e o
§6.6 (tripwire) — decisões novas, não reabertas. O E3 reconcilia o `00_System_Spec.md` §1,
documento **subordinado** à Carta (§1 da hierarquia): alinhar um subordinado com a autoridade
não é re-litigar a autoridade.

Nota de honestidade que o próprio REG-003 tem de carregar: **a mesma emenda 1.2 é a que cria o
árbitro e o tripwire** — e é a que produz REG-005, REG-006 e REG-010. Não havia, à data,
mecanismo para arbitrar os seus próprios pontos. O mecanismo nasce já em dívida consigo mesmo.

### N-005 / N-006 — Emenda 1.2, ponto E10 (D3 e D5) — **o caso desconfortável**

**D3** (transporte SSE stdlib) e **D5** (BFF single-process) estão listadas como **FIXAS** no
§4.2. A emenda 1.2 marcou ambas para **REAVALIAR** face ao modelo de ameaça do nó-serviço, e
a Carta acrescenta, na mesma frase, a auto-absolvição: *«não re-litígio — reavaliação de
contexto»*.

**Facto verificado no `git show 608e84c -- specs/00_AOS_Carta.md`:** as **linhas de D3 e D5 no
§4.2 não foram tocadas**; a marcação para reavaliar entrou num bloco de prosa novo («Notas de
coerência») logo abaixo da tabela. É por isso — e só por isso — que caem no caso **(b)** do
§2.1 e ficam `PENDENTE` em vez de `REABERTURA`: o estado ficou FIXA e o texto da decisão ficou
intacto. Se o dono fechar a definição de «reaberta» no sentido de que *pôr uma FIXA de novo em
cima da mesa é reabri-la*, estas duas passam a `REABERTURA` e o §6.6 dispara (§7).

Quanto à 2.ª perna:

- **A favor de não ser re-litígio (mérito):** existe **facto novo verificável** — a forma do
  produto «nó `aos` deployável, exposto como serviço de rede» foi fixada no §2 em 2026-07-22,
  ao passo que D3/D5 tinham sido fixadas no contexto **BFF-atrás-de-SPA** (EPIC-13 §25). O
  artefacto sobre o qual a regra passou a aplicar-se **não existia** à data em que foram
  fixadas. Isto satisfaz, no mérito, o teste do §6.5.
- **Desfecho factual:** nenhuma das duas foi revertida. D3 **manteve-se e foi endurecida**
  (SSE stdlib com backfill, resume-from-seq, dedup e backpressure — AOS-167, EPIC-15); D5
  manteve-se (o nó da v1 é single-process). O estado das duas continua **FIXA** no §4.2.
- **Contra (o que não se pode varrer para debaixo do tapete):** o §6.5 exige que a fronteira
  «dívida escondida vs re-litígio» seja decidida **por escrito pelos dois papéis**. A linha de
  aprovação da própria emenda 1.2 diz **«Dono do produto; Segurança/Arquitectura pendente»**.
  Ou seja: **a arbitragem exigida não foi feita**, e a classificação «não re-litígio» foi
  **auto-atribuída por quem propôs a alteração** — precisamente o *loophole* que o §6.5 existe
  para fechar. Ninguém está, hoje, habilitado a declarar que isto não foi re-litígio.

### N-010 — Emenda 1.2, ponto E10, 1.ª alínea (não-repúdio HITL / identidade fim-a-fim)

O **ADR-016 é FIXA** (§4.1): *fronteira de confiança da UI — BFF non-signing, WYSIWYS, 4-eyes*.
A emenda 1.2 declara que **não-repúdio HITL e identidade fim-a-fim ficam DEFERIDOS com o eixo
D4** e que a superfície de aprovação fica *«estruturalmente completa mas criptograficamente
demo-grade até AOS-160 (assinatura ed25519) e AOS-162 (4-eyes atestado)»*.

- **Porque é registada:** isto **escopa no tempo a garantia** de uma decisão FIXA — o 4-eyes e o
  não-repúdio do ADR-016 passam a valer *demo-grade* por declaração de emenda. Pela regra do
  §2.2 ponto 2, é um toque numa FIXA e tem de ter linha. Omiti-la seria escolher a leitura
  favorável: é a **4.ª** `emenda-sobre-FIXA` **dentro da mesma janela** de 30 dias.
- **Porque é `PENDENTE` e não `REABERTURA`:** cai no caso **(b)** do §2.1. O `git show 608e84c`
  confirma que a **linha do ADR-016 no §4.1 não foi alterada** — o invariante mantém-se e o que
  a emenda faz é declarar honestamente o estado da sua *realização*. O argumento contrário, que
  fica registado: uma garantia de segurança que passa a *demo-grade* por emenda é, em substância,
  uma decisão FIXA suspensa. A escolha entre as duas leituras é a mesma pendência do §8.4.
- **2.ª perna:** por arbitrar, como as anteriores («Segurança/Arquitectura pendente»).

### N-008 — Emenda 1.3 (excepção escopada ao zero-dep do ADR-017) — **a reabertura sem discussão**

O **ADR-017 é FIXA** (§4.1) e a emenda 1.3 **editou a sua linha no registo §4.1** para inscrever
uma excepção escopada (lib WebAuthn/CBOR no componente de autoridade de identidade **externo**).

**Isto é o caso (a) do §2.1 e conta `REABERTURA`.** O `git show a16c0b6 -- specs/00_AOS_Carta.md`
mostra a linha do ADR-017 a ser substituída: o conteúdo normativo da decisão FIXA passou a
conter «**Exceção ESCOPADA (emenda 1.3)**». Não é interpretação — é um `-`/`+` no diff da
autoridade. Registá-la como `PENDENTE` seria esconder a única reabertura que não admite dúvida.

O mérito — que pertence à 2.ª perna e **não** apaga a 1.ª — está registado dos dois lados:

- **A favor:** o **invariante** do ADR-017 ponto 1 — *o binário do nó é zero-dep (stdlib +
  cedar-go)* — mantém-se **literalmente intacto**; a dep vive fora do artefacto do nó e passa
  pelos gates (sca/govulncheck, `go.sum` pinado, SBOM). O facto novo verificável é que a frente 4
  do EPIC-16 (attestation WebAuthn/AAGUID) não é implementável só com a stdlib.
- **Contra:** a aprovação da emenda 1.3 diz outra vez **«Segurança/Arquitectura pendente»**. A
  excepção a uma decisão FIXA de **supply-chain** foi aprovada sem o Responsável de Segurança —
  que é, por desenho do §6.5, um dos dois árbitros obrigatórios.

### N-009 — Arbitragem que originou o ADR-019 (fronteiras de camada)

O gate `layer-lint` (AOS-178) detectou inversões ao sentido canónico de dependências
(`AGENTS.md` §3). O AOS-179 (EPIC-17) previa a saída: *«Se uma inversão for intencional e
permanente, emitir ADR/emenda que a autorize»*. A decisão foi **legitimar** cinco famílias de
inversão como excepções intencionais escopadas, em vez de refactorizar (custo/risco
desproporcionado para a v1), com a excepção declarada como **tecto e não cartão branco**.

- **Conta `NAO`** porque o invariante tocado vive no `AGENTS.md` §3 e não é uma **decisão FIXA
  do registo §4 da Carta** — o §6.6 conta decisões desse registo. O facto novo verificável
  existia (o output do gate, que não existia antes do AOS-178).
- **Ressalva registada:** o campo *Deciders* do ADR-019 diz **«Equipa AOS»**, não «Arquitecto
  de Plataforma + Responsável de Segurança». O ADR-019 é uma arbitragem **em substância** e
  está por escrito (é mais do que existia), mas **não segue o procedimento nominal do §6.5**.
  O padrão é o mesmo dos N-005/006/008/010: decide-se por escrito, sem os dois papéis nomeados.

### N-011 — Arbitragem §6.5 das quatro pendências (2026-07-29)

Os **dois papéis do §6.5** (Arquitecto de Plataforma + Responsável de Segurança) pronunciaram-se
sobre REG-005 (D3), REG-006 (D5), REG-010 (ADR-016) e REG-008 (excepção zero-dep do ADR-017) e
**legitimaram as quatro como dívida-escondida** — ver o dossiê `DOSSIE-Arbitragem-6.5.md` e a
leitura técnica `LEITURA-TECNICA-Merito-6.5.md` que instruíram a decisão. Fecha-se a **2.ª perna**
que N-005/006/008/010 marcavam «por arbitrar»: a classificação «não re-litígio» deixou de ser
auto-atribuída por quem propôs e passou a ter a pronúncia dos dois papéis. Para REG-010, a
pronúncia regista ainda que a garantia do ADR-016 está **hoje em larga medida cumprida** (verifier
real + ed25519 + 4-eyes compostos no nó; residuais WebAuthn/tenant IdP deferidos).

Conforme o §2.2 ponto 4, a pronúncia **actualiza as linhas existentes** (Veredicto + Natureza +
Conta de REG-005/006/008/010) — **não** cria uma linha nova.

**As duas pendências que a arbitragem tinha deixado ao dono foram despachadas na mesma data:**

1. **Definição de «reaberta» (§8.4) — o dono fechou-a no sentido ESTRITO** (2026-07-29): «reaberta»
   = a **linha da FIXA foi textualmente alterada** (caso (a) do §2.1). Consequência no contador:
   REG-005/006/010 tocaram FIXAS **sem** lhes alterar o texto (facto verificado por `git show
   608e84c`) ⇒ resolvem-se como **não-reabertura** (`Conta = NAO`); só REG-008 alterou a linha do
   §4.1 ⇒ mantém-se `REABERTURA`. **Contagem final: 1 reabertura na janela ⇒ abaixo do limiar do
   §6.6 ⇒ o tripwire NÃO dispara.** A perna (b) já estava a **0** (nenhuma recusada). **O mecanismo
   de congelamento aguenta** — não é revisto na raiz.
2. **Sign-off de v1 (§5) — dado.** Os mesmos dois papéis assinaram o sign-off de Segurança/
   Arquitectura das emendas **1.2 e 1.3** (2026-07-29); a linha de aprovação de cada uma no §7 da
   Carta passa de «pendente» a **assinado**. A pré-condição de aceitação da v1 do §5 quanto a estas
   emendas fica **satisfeita**.

O registo mantém-se honesto e agora **fechado** nestes eixos: mérito arbitrado (as quatro,
dívida-escondida), contador do §6.6 resolvido (1 reabertura, não dispara) e sign-off de v1 dado.

---

## 5. O contador (calculável por comando)

O §6.6 fala de «**uma** janela de 30 dias» — qualquer janela deslizante, não só a janela
corrente. O comando do §5.1 é a **definição operacional canónica** do SLI. **Correr a partir da
raiz do repositório.** Requer `python3` (só stdlib).

### 5.1 Comando canónico — todas as janelas deslizantes de 30 dias, fail-closed

```bash
python3 - <<'PY'
import datetime, pathlib, re, sys

P = pathlib.Path("docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md")
NAT = {"marco", "emenda", "emenda-sobre-FIXA", "arbitragem", "divida-escondida", "re-litigio"}
CNT = {"REABERTURA", "RECUSA", "PENDENTE", "NAO"}
norm = lambda s: s.strip().strip("*`").strip()

# 1) Ler TODAS as linhas de evento. Nenhuma pode desaparecer em silencio.
raw = [l for l in P.read_text(encoding="utf-8").splitlines() if l.startswith("| REG-")]
ev, erros = [], []
for l in raw:
    c = [x.strip() for x in l.split("|")]
    if len(c) != 10:
        erros.append("colunas=%d (esperado 8): %s" % (len(c) - 2, l[:56])); continue
    rid, dt, nat, cnt = norm(c[1]), norm(c[2]), norm(c[4]), norm(c[7])
    if not re.fullmatch(r"REG-\d{3}", rid):
        erros.append("ID invalido: '%s'" % rid); continue
    try:
        d = datetime.date.fromisoformat(dt)
    except ValueError:
        erros.append("%s: data nao-ISO: '%s'" % (rid, dt)); continue
    if nat not in NAT: erros.append("%s: Natureza fora do vocabulario: '%s'" % (rid, nat))
    if cnt not in CNT: erros.append("%s: Conta fora do vocabulario: '%s'" % (rid, cnt))
    ev.append((d, rid, cnt, nat))

# 2) Fail-closed: formato invalido NUNCA produz um numero.
if erros or len(ev) != len(raw):
    print("ERRO DE FORMATO: linhas '| REG-'=%d  parseadas=%d" % (len(raw), len(ev)))
    for e in erros: print("  -", e)
    print("contador NAO calculavel -- corrigir o registo (fail-closed, ver 2.3)")
    sys.exit(2)

ev.sort()
perna1 = lambda e: e[2] == "REABERTURA"                          # constatacao factual (2.1a)
perna2 = lambda e: e[3] == "re-litigio" or e[2] == "RECUSA"      # veredicto do arbitro (6.5)
ampla  = lambda e: e[2] in ("REABERTURA", "PENDENTE")            # leitura ampla de "reaberta"

def janelas(pred):
    out, visto = [], set()
    for d0, _, _, _ in ev:
        fim = d0 + datetime.timedelta(days=29)                   # 30 dias inclusivos
        hits = [e for e in ev if d0 <= e[0] <= fim and pred(e)]
        chave = tuple(h[1] for h in hits)
        if len(hits) >= 2 and chave not in visto:
            visto.add(chave); out.append((d0, fim, hits))
    return out

est = [("REABERTURA", w) for w in janelas(perna1)] + [("RECUSA", w) for w in janelas(perna2)]
amp = janelas(ampla)
for nome, (d0, fim, hits) in est:
    print("TRIPWIRE DISPARADO  janela %s..%s  perna=%s  n=%d  [%s]"
          % (d0, fim, nome, len(hits), ", ".join(h[1] for h in hits)))

pend = [e for e in ev if e[2] == "PENDENTE"]
arb = [e for e in ev if e[3] == "emenda-sobre-FIXA"]
print("eventos=%d  reaberturas=%d  recusas=%d  indecidiveis-1a-perna=%d%s"
      % (len(ev), sum(1 for e in ev if perna1(e)), sum(1 for e in ev if perna2(e)), len(pend),
         (" [%s]" % ", ".join(p[1] for p in pend)) if pend else ""))
print("limite superior (leitura ampla: tocar uma FIXA e' reabri-la -- pendencia 8.4)")
for d0, fim, hits in amp:
    print("  janela %s..%s  FIXAs tocadas=%d  [%s]  -> TERIA DISPARADO pela 1.a perna"
          % (d0, fim, len(hits), ", ".join(h[1] for h in hits)))
if not amp:
    print("  nenhuma janela de 30 dias com >=2 FIXAs tocadas")
if arb:
    print("AVISO: %d invocacao(oes) do 6.4 sobre FIXAS por arbitrar pelos dois papeis do 6.5 [%s]"
          % (len(arb), ", ".join(a[1] for a in arb)))

if est:
    sys.exit(1)
if amp:
    print("leitura estrita NAO dispara; leitura ampla DISPARA -- definir 'reaberta' e' pendencia do dono (8.4)")
    sys.exit(3)
print("tripwire: NAO disparado em nenhuma das duas leituras")
sys.exit(0)
PY
```

**Códigos de saída (qualquer valor ≠ 0 exige acção humana):**

| Código | Significado |
|---|---|
| `0` | Não dispara em nenhuma das duas leituras. |
| `1` | **Tripwire disparado** na leitura estrita — o §6 desencadeia-se. |
| `2` | **Registo malformado** — o contador recusa-se a produzir um número (fail-closed). |
| `3` | A leitura estrita não dispara mas a **ampla dispara**: o resultado depende de uma definição que a Carta não dá (§8.4). *«Não sei» não é «não».* |

### 5.2 Comando rápido — janela corrente de 30 dias (grep/awk) — **auxiliar, não canónico**

```bash
LIM=$(date -d '30 days ago' +%F) && \
awk -F'|' -v lim="$LIM" '
  $2 ~ /REG-[0-9][0-9][0-9]/ {
    d=$3; c=$8; gsub(/[[:space:]*`]/,"",d); gsub(/[[:space:]*`]/,"",c);
    if (d >= lim) n[c]++
  }
  END {
    printf "janela %s..hoje  REABERTURA=%d  RECUSA=%d  PENDENTE=%d\n",
           lim, n["REABERTURA"], n["RECUSA"], n["PENDENTE"];
    print (n["REABERTURA"] >= 2 || n["RECUSA"] >= 2) \
          ? "TRIPWIRE DISPARADO (Carta 6.6)" : "tripwire: nao disparado"
  }' docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md
```

Saída verificada em 2026-07-26: `janela 2026-06-26..hoje  REABERTURA=1  RECUSA=0  PENDENTE=3`
/ `tripwire: nao disparado`.

**Portabilidade:** `date -d` é GNU coreutils (Linux, Git Bash, WSL). Em BSD/macOS a sintaxe é
`LIM=$(date -v-30d +%F)`.

**Aviso — este comando caduca com o tempo e não valida nada.** Mede **só a janela corrente**:
quando os eventos de Julho de 2026 saírem dos 30 dias, imprimirá tudo a `0` enquanto o §5.1
continua a apanhá-los (o §6.6 fala de *uma* janela qualquer, não da actual). Também não valida
vocabulário nem contagem de colunas. **Para efeitos do §6.6 o único comando canónico é o §5.1.**

### 5.3 Critério de disparo (Carta §6.6, literal)

O tripwire **dispara** quando, numa janela de 30 dias, se verificar **qualquer** das pernas:

| Perna | Condição | O que a alimenta | Quem escreve |
|---|---|---|---|
| 1.ª | **≥ 2** decisões FIXAS reabertas | `Conta §6.6 = REABERTURA` | Quem redige a emenda (constatação factual, §2.1) |
| 2.ª | **≥ 2** invocações do §6.4 recusadas como re-litígio pelo árbitro | `Natureza = re-litigio` ou `Conta §6.6 = RECUSA` | Só o árbitro do §6.5 |

As pernas **não se somam** entre si: 1 `REABERTURA` + 1 `RECUSA` **não** dispara. Um mesmo
evento **pode** contar para as duas (uma reabertura factual que o árbitro depois recusa) — por
isso a 2.ª perna lê a Natureza e não depende de a coluna Conta estar livre.

`PENDENTE` não conta na leitura estrita; conta no **limite superior** do §5.1.

### 5.4 Estado do contador em 2026-07-26 — **os dois números**

```
eventos=11  reaberturas=1  recusas=0  indecidiveis-1a-perna=3 [REG-005, REG-006, REG-010]
limite superior (leitura ampla: tocar uma FIXA e' reabri-la -- pendencia 8.4)
  janela 2026-07-22..2026-08-20  FIXAs tocadas=4  [REG-005, REG-006, REG-010, REG-008]  -> TERIA DISPARADO pela 1.a perna
AVISO: 4 invocacao(oes) do 6.4 sobre FIXAS por arbitrar pelos dois papeis do 6.5 [REG-005, REG-006, REG-010, REG-008]
leitura estrita NAO dispara; leitura ampla DISPARA -- definir 'reaberta' e' pendencia do dono (8.4)
$ echo $?
3
```

**Leitura honesta — e o que fica dito sem atenuação:**

1. **Limite inferior (estrito) = 1 reabertura.** Só o REG-008 satisfaz o teste (a) do §2.1: a
   linha do ADR-017 no §4.1 foi literalmente reescrita pela emenda 1.3. Falta **uma** para o
   tripwire disparar pela 1.ª perna.
2. **Limite superior (amplo) = 4 FIXAS tocadas na janela 2026-07-22..2026-08-20.** Sob a leitura
   literal da 1.ª perna — *tocar uma FIXA por emenda é reabri-la* — **o tripwire já teria
   disparado retroactivamente**, nessa janela, com REG-005, REG-006, REG-010 e REG-008, e **sem
   depender de árbitro nenhum**, porque a 1.ª perna não o menciona. Qual das duas leituras vale é
   **pendência do dono (§8.4)**; até lá o SLI reporta as duas e sai com código `3`.
3. **A 2.ª perna é hoje estruturalmente inavaliável.** Quatro invocações do §6.4 sobre decisões
   FIXAS foram feitas e **nenhuma foi arbitrada** pelos dois papéis do §6.5 — que o §8.2 declara
   **não constituídos**. Não pode haver «recusas do árbitro» quando não há árbitro.
4. **«Não disparou» não é «o congelamento funcionou».** Três emendas em dois dias, quatro toques
   em decisões FIXAS, zero arbitragens, e a emenda que cria o mecanismo é a mesma que produz três
   dos quatro toques. O contador está agora **calculável e capaz de dar mau resultado** — e já dá
   um resultado incómodo.

### 5.5 Prova negativa — o contador tem de conseguir dar mau resultado

Um contador que nunca dispara não é um SLI; um parser que lê `0` sobre um registo malformado é
pior do que não existir. Cinco cenários executados em 2026-07-26 **sobre cópias** em directório
temporário (`git status` confirmou o ficheiro versionado intacto em todos):

| # | Mutação sobre a cópia | Resultado | Código |
|---|---|---|---|
| A | as 3 linhas `PENDENTE` → `RECUSA` (o desfecho «re-litígio» do §7) | `TRIPWIRE DISPARADO janela 2026-07-22..2026-08-20 perna=RECUSA n=3` | `1` |
| B | as 3 linhas `PENDENTE` → `REABERTURA` (o desfecho «leitura ampla» do §7) | `TRIPWIRE DISPARADO janela 2026-07-22..2026-08-20 perna=REABERTURA n=4` | `1` |
| C | `PENDENTE` → `**REABERTURA**` (negrito, como as células vizinhas) | normalizado e **contado**, não ignorado: `n=4` | `1` |
| D | uma linha `REG-` reduzida a 7 colunas | `ERRO DE FORMATO: linhas '\| REG-'=11 parseadas=10` + `colunas=7 (esperado 8)` — a linha **não** desaparece em silêncio | `2` |
| E | `Conta` = `TALVEZ` (fora do vocabulário) | `ERRO DE FORMATO ... REG-005: Conta fora do vocabulario: 'TALVEZ'` | `2` |

Os cenários **A** e **B** demonstram que **as duas pernas estão vivas** — a 2.ª pela via do
árbitro, a 1.ª sem ele. Os cenários **C**, **D** e **E** demonstram que o parser **não
subnotifica em silêncio**: negrito é normalizado e contado; formato inválido aborta em vez de
imprimir `0`.

Comandos usados (as cópias vivem **fora** do repositório; corre-se depois o bloco do §5.1 com
`P` apontado a cada cópia):

```bash
D=$(mktemp -d); R=docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md
sed 's/| PENDENTE |/| RECUSA |/'          "$R" > "$D/A.md"   # cenario A
sed 's/| PENDENTE |/| REABERTURA |/'      "$R" > "$D/B.md"   # cenario B
sed 's/| PENDENTE |/| **REABERTURA** |/'  "$R" > "$D/C.md"   # cenario C
sed 's/| PENDENTE | commit/| TALVEZ | commit/'                "$R" > "$D/E.md"   # cenario E
# cenario D (retirar uma celula a uma linha REG-) -- sed nao serve por causa dos acentos:
python3 -c 'import sys,pathlib
o=[]
for l in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines():
    if l.startswith("| REG-009 |"):
        c=l.split("|"); del c[3]; l="|".join(c)
    o.append(l)
pathlib.Path(sys.argv[2]).write_text("\n".join(o)+"\n",encoding="utf-8")' "$R" "$D/D.md"
```

---

## 6. O que acontece quando o tripwire dispara

A Carta §6.6 é explícita e não admite meio-termo:

> «o mecanismo de congelamento **falhou** e a Carta é revista **na raiz** (não emendada à
> margem)»

**O que a Carta manda** (citação, não acrescento):

1. **A promessa do §0 — «isto acaba o retrabalho» — está FALSIFICADA.** Não se argumenta com o
   contador nem se reclassificam linhas *a posteriori* para o baixar; a reclassificação de uma
   linha exige o teste objectivo do §2.1 (1.ª perna) ou veredicto do árbitro (2.ª perna),
   registado na própria linha.
2. **Revisão na raiz da Carta**, conduzida pelo dono do produto **com os dois papéis do §6.5**
   presentes, respondendo a: porque é que estas decisões não aguentaram? O defeito está na
   decisão (foi fixada cedo demais, sem facto suficiente), no mecanismo (o §6.4 é largo demais)
   ou na sua aplicação (auto-classificação sem árbitro)?
3. **O resultado da revisão é uma nova versão da Carta** (§7), não uma emenda incremental.

**PROPOSTAS ao dono — não estão em vigor** (a Carta não as contém; exigem emenda, §8.1):

- **(P1) Congelar a emissão de novas emendas sobre decisões FIXAS até à revisão.** Emendar à
  margem enquanto a revisão está pendente parece contrariar o espírito do §6.6, mas o §6.6 não
  o diz — e não é este ficheiro que o pode instituir.
- **(P2) Registar a revisão aqui como um `marco` e reiniciar a leitura do contador a partir
  dessa data.** Uma regra de **reset do contador** é matéria da autoridade, não de um ficheiro
  subordinado: sem emenda do dono, o §5.1 continua a varrer **todas** as janelas desde 2026-07-22.

---

## 7. Conversão dos indecidíveis e dos por-arbitrar (o que fica em aberto)

Duas coisas distintas estão em aberto e têm donos distintos. **Actualizam-se as linhas
existentes**, não se criam novas (§2.2 ponto 4), e recalcula-se com o §5.1.

**(A) A definição de «reaberta» — decide o DONO por emenda (§8.4). Afecta a 1.ª perna:**

| Decisão do dono | Efeito nas linhas `PENDENTE` | Conta §6.6 | Efeito no tripwire |
|---|---|---|---|
| Leitura **ampla**: marcar/escopar uma FIXA **é** reabri-la | REG-005, REG-006, REG-010 | `REABERTURA` | 4 na janela 2026-07-22..2026-08-20 ⇒ **dispara retroactivamente pela 1.ª perna**, sem árbitro |
| Leitura **estrita**: só conta alterar a linha da decisão no §4 | REG-005, REG-006, REG-010 | `NAO` | 1 (só REG-008) ⇒ não dispara; falta uma |

**(B) O veredicto do §6.4 — decide o ÁRBITRO do §6.5, quando constituído (§8.2). Afecta a 2.ª:**

| Veredicto sobre cada `emenda-sobre-FIXA` | Natureza | Conta §6.6 | Efeito no tripwire |
|---|---|---|---|
| **Dívida escondida** (facto novo verificável) | `divida-escondida` | inalterada | 2.ª perna fica em 0 |
| **Re-litígio** (sem facto novo) | `re-litigio` | `RECUSA` (ou mantém `REABERTURA` se a 1.ª já conta) | ≥ 2 recusas ⇒ **dispara pela 2.ª perna** |

**O cenário que tem de ficar dito:** REG-005, REG-006 e REG-010 têm a **mesma data**
(2026-07-22) e REG-008 cai a 2026-07-23 — **todos dentro da mesma janela de 30 dias**
(2026-07-22..2026-08-20). Basta **uma** de duas coisas para o §6.6 disparar retroactivamente:
o dono fechar a definição no sentido amplo (via A), **ou** o árbitro recusar duas das quatro
(via B). Registar isto agora é o objectivo do artefacto: o contador só vale se puder dar mau
resultado — e hoje já sai com código `3`.

---

## 8. Pendências para o dono (exigem emenda — §7 da Carta — não trabalho de engenharia)

Encontradas ao construir este registo. **Nenhuma foi alterada por AOS-200**, porque alterar a
Carta ou o registo de decisões exige emenda datada do dono (§7).

1. **A Carta não referencia este registo.** O §6.6 diz «este contador é o SLI do próprio
   processo» sem apontar para onde ele vive. Uma emenda deveria ligar o §6.5/§6.6 a
   `docs/governance/REGISTO-Decisoes-Reabertas-e-Arbitragens.md` e tornar a linha nova
   obrigatória em cada emenda. **Enquanto essa emenda não existir, o §2.2 deste ficheiro é
   proposta, não obrigação**, e nada garante que a próxima emenda seja registada.
2. **O árbitro do §6.5 não está constituído.** «Arquitecto de Plataforma» e «Responsável de
   Segurança» são papéis sem titular nomeado no corpus. As emendas 1.2 e 1.3 estão ambas
   aprovadas com «Segurança/Arquitectura **pendente**». Enquanto durar, a 2.ª perna do tripwire
   é inavaliável — o que torna a 1.ª perna a **única** que pode disparar hoje.
3. **O §4.1 está incompleto face aos ADRs existentes.** O registo «único» de decisões lista até
   ao ADR-017, mas existem `docs/adr/ADR-018-fronteira-no-orq-sch.md` (2026-07-23) e
   `docs/adr/ADR-019-fronteiras-camada-excecoes.md` (2026-07-25), ambos **Aceites** e nenhum
   inscrito no §4.1. Um registo único que não regista tudo deixa de ser único.
4. **O §6.6 não define «reaberta» — e é a pendência mais cara.** É ela que separa
   `reaberturas=1` de `reaberturas=4` e, com isso, «não disparou» de «disparou
   retroactivamente». O teste objectivo do §2.1 deste ficheiro é a **proposta** de definição
   (caso (a) estrito, caso (b) amplo); só o dono a pode fixar por emenda. Até lá o §5.1 reporta
   os dois números e sai com código `3` — «não sei» deixa de ser igual a «não», mas continua
   por decidir.
5. **O §6.6 não diz o que acontece ao contador depois da revisão na raiz.** Se a Carta for
   revista, o contador reinicia? As propostas P1/P2 do §6 estão por decidir.

---

## 9. Referências

- `specs/00_AOS_Carta.md` — §4 (registo de decisões), §5 (DoD da v1), §6.4/§6.5/§6.6 (regra de
  congelamento, árbitro, tripwire), §7 (emendas 1.0 a 1.3).
- `specs/EPIC-18_Remediacao_Auditoria_Multiagente_v4.md` — AOS-200, achado DEF-07.
- `specs/EPIC-17_Remediacao_Auditoria_Multiagente_v3.md` — AOS-178 (gate `layer-lint`),
  AOS-179 (inversões canónicas).
- `specs/EPIC-15_No_AOS_Runtime_Deployavel.md` — ponto E10 e AOS-167 (desfecho da reavaliação de D3).
- `docs/adr/ADR-016-*`, `docs/adr/ADR-017-supply-chain-node.md`,
  `docs/adr/ADR-018-fronteira-no-orq-sch.md`, `docs/adr/ADR-019-fronteiras-camada-excecoes.md`.
- `docs/reports/D4-escalacao-autoridade-identidade.md` — escalada da D4.
- `AGENTS.md` §3 — invariantes de fronteira e remissão para o ADR-019.
- Diffs que fundamentam a classificação: `git show 608e84c -- specs/00_AOS_Carta.md` (emenda
  1.2 — linhas de D3/D5/ADR-016 **não** alteradas) e `git show a16c0b6 -- specs/00_AOS_Carta.md`
  (emenda 1.3 — linha do ADR-017 **alterada**).
