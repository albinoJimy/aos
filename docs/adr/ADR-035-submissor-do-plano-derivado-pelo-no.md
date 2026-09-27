# ADR-035 — O submissor de um plano viaja até ao run filho, derivado pelo NÓ; os dados do run filho são dele

- **Estado:** Aceite
- **Data:** 2026-09-27
- **Deciders:** Dono do produto (decisões B+ do AOS-439 e titular do AOS-440, 2026-09-26) · executor
  de AOS-439/440 (implementação)
- **Tickets:** AOS-439, AOS-440
- **Emenda:** ADR-033 §2.1 (o mandato enumera `requesters`) — a emenda vive no próprio ADR-033, §7.
- **Relacionados:** ADR-003 (cadeia `on-behalf-of` com raiz humana), ADR-018 (o nó não conhece a
  semântica do orquestrador), ADR-027 (cada nó do plano é um run do nó), ADR-030 (a fila e a
  reclamação), ADR-031 (a titularidade do pedido), ADR-010 (a hash-chain do WORM e a versão
  por-registo)

## 1. Contexto

Medido em produção (2026-09-25, `plan-e2e-docread-1790340990`, v0.1.33): um plano submetido pelo
service account `aos-reader` e drenado pelo timer correu com a cadeia `human:<humano do mandato>`
→ `agt-drenador`, e o conteúdo dos runs filhos foi selado sob a KEK do `aos-reader` — quem **chamou**
o nó, e não quem **pediu** o plano. O submissor ficava gravado no `planrequest.submitted` e
perdia-se aí: a reclamação não o devolvia, o `aos-orq` submetia cada run filho com o NHI do
mandato, e o nó derivava o titular do chamador HTTP.

Três consequências: o humano que assinou o mandato respondia por pedidos que nunca viu (ADR-033 §5,
resíduo 7); um apagamento DSAR de quem pediu não apagava o conteúdo; e apagar a KEK do service
account apagava o conteúdo de **todos** os planos, de todos os submissores.

E uma quarta, que só a medição do desenho encontrou: qualquer identidade autenticada da região
podia **reclamar** pedidos alheios e receber o objectivo **decifrado** (`plan_claim.go`).

## 2. Decisão

### 2.1 Quem drena é uma lista fechada

`POST /plans/claim` e `POST /plans/outcome` só servem os principals de `AOS_PLAN_DRAINERS` (o `sub`
do ID-token). **Fail-closed**: vazia ⇒ ninguém drena, e o banner di-lo. A recusa é a mesma 403 das
outras recusas da rota, e acontece antes de qualquer escrita — uma reclamação recusada não gasta
uma geração do pedido.

### 2.2 O submissor é DERIVADO pelo nó, nunca aceite do corpo (decisão B+)

O `POST /runs` aceita um campo opcional `plan_request: {run_id, generation}`. O corpo **não diz
quem é o submissor**: diz de que pedido o run é trabalho. O nó verifica, contra o **seu próprio
log** da fila:

1. existe `planrequest.submitted` para esse plano — é dele que sai o submissor;
2. o chamador é drenador e tem a reclamação **viva** da **última** geração (sem desfecho, dentro do
   TTL), e é essa a geração nomeada;
3. a região do pedido é a do chamador;
4. o `run_id` tem a forma `<plano>~<nó>` — o separador `~` passa a **contrato do nó**
   (`separadorDoRunFilho`, com um teste a amarrá-lo à constante do `aos-orq`).

Se tudo bate, o `requested_by` do run é o principal gravado no pedido; se falha, 403 uniforme e a
causa no log. Sem o campo, nada muda.

**O que o vínculo prova, e o que NÃO prova** (corrigido pela revisão adversarial). Prova que o run é
trabalho do PLANO nomeado, submetido por quem o nó leu do log, e que o chamador tem a reclamação
viva desse plano. **Não prova que `<nó>` é um nó do documento aprovado**: o nó não conhece o
documento do plano (ADR-018 — o plano é do orquestrador), pelo que uma reclamação viva serve para
runs `<plano>~<qualquer-coisa>` arbitrários, todos atribuídos ao mesmo submissor. Declarado no §5.

**Porque não um campo `requested_by` no corpo:** quem chama o `POST /runs` é o drenador. Um campo no
corpo seria o drenador a afirmar por quem age; um drenador comprometido afirmaria o que quisesse.
Com o vínculo, só consegue invocar um submissor que pediu mesmo um plano, cujo pedido ele tem
reclamado **agora**, e cujo nome o nó leu do seu log.

**A raiz da cadeia não muda.** O humano do mandato continua a ser a raiz `human:` da cadeia
on-behalf-of (ADR-003): é ele quem autoriza a máquina. O submissor fica **selado ao lado** — não é
um elo da cadeia, é atribuição.

### 2.3 O mandato nomeia por quem o emissor pode agir

Emenda ao ADR-033 §2.1 (ver lá, §7): o mandato enumera `requesters`, obrigatórios, sem curingas,
assinados sob um domínio **novo** (`aos.identity.mandate.v2`). O nó recusa o `POST /runs` — e a
retoma — de um run cujo `requested_by` não conste dos `requesters`; um run **sem** submissor sob um
mandato v2 também. Um service account só submete se estiver nomeado.

Quando o vínculo passou e só o mandato recusa, a resposta leva o código `E_MANDATE_REQUESTER`: o
chamador já provou ser o drenador com a reclamação viva, e o código não lhe diz nada que ele não
tenha. É o que permite ao `aos-orq` — que conhece os seus códigos de saída, e o nó não (ADR-018) —
fechar o pedido como **terminal** (saída `11`, `requerente_fora_do_mandato`) em vez de o retentar;
a saída 11 larga a posse, como as outras recusas terminais.

**O drenador recusa ANTES de planear** (revisão adversarial). A recusa do nó chega depois de o
`serve` decompor o objectivo — o modelo corria com o NHI do mandato por um submissor que o humano
não nomeou. A resposta do `POST /plans/claim` passou a trazer o `requested_by` do pedido (o drenador
já recebe o objectivo decifrado; não lhe revela nada novo), e o `consume --mandate <mandato.json>`
compara-o com os `requesters` do mandato: não constando, fecha logo com a saída 11, sem `serve`.
Um mandato ilegível aborta a drenagem antes de reclamar (fail-closed); um mandato v1 não compara (a
janela é do nó). Sob um v2, um pedido **sem** `requested_by` também fecha com 11 (2.ª ronda: o nó
recusá-lo-ia com a 403 uniforme, classificada transitória, e o pedido voltaria à fila para sempre).
E antes de reclamar, o `consume` cruza o id do mandato embebido no NHI em uso — lido **sem**
verificar a assinatura, é só consistência; quem decide é o nó — com o do `mandato.json`: se os dois
se lêem e divergem (a janela entre re-assinar e o timer trocar o NHI), aborta sem reclamar, e os
pedidos ficam na fila. Um NHI sem mandato legível (emissor manual, NHI de teste) não cruza. O
`drenar-planos.sh` passa o mesmo `mandato.json` que o timer de cunhagem lê, e recusa arrancar sem ele.

**Mandatos v1** (sem `requesters`) são aceites durante uma **janela de migração**
(`AOS_MANDATE_V1_UNTIL`), para o deploy não parar a drenagem em produção, e recusados depois
(`E_MANDATE_V1_CLOSED`). Fechada por omissão no código; no compose de produção a omissão é o fim do
mandato v1 em vigor. O tecto da janela é a validade máxima de um mandato (90 dias do arranque).
Enquanto está aberta, um v1 continua a verificar: depois de assinar o v2, o humano revoga o v1
(`revoke-sign --jti mandate:<id>`) ou fecha a janela.

### 2.4 Selado no WORM (quando o operador o liga) e no evento de mediação

O registo de decisão ganha `requested_by` e `mandate_id` — este último fecha o resíduo 3 do ADR-033.
Novo `SchemaVersion` (v4), domínio novo (`aos.audit.v4`), campos no **fim** do conteúdo canónico: um
registo v2 ou v3 produz os mesmos bytes e continua a verificar. O evento `tool.call.*` leva os dois
campos, opcionais, **sempre**.

**Expand/contract** (revisão adversarial). Um binário anterior não conhece o v4 e recusa arrancar
sobre um WORM que o contenha — e o `stampSchema` sela na versão corrente TODO o registo novo (selos
de residência, `gov.*`, recusas), não só as mediações. Escrever v4 por omissão cortava o rollback
no primeiro registo depois do deploy. Por isso esta release **lê e verifica** v4 sempre e **escreve**
v3 por omissão; o v4 escreve-se com `AOS_AUDIT_WRITE_V4=1`, ligado pelo operador depois de confirmar
o deploy saudável (o banner declara a versão e o custo). Num registo v3 os dois campos **não** ficam
no WORM — nem selados nem ao lado —, e o `aos audit-trail` só os mostra em v4.

O hook de identidade continua a substituir a identidade inteira a partir do token (incluindo o
humano da raiz, `UserID`, e o `MandateID`); **preserva** o `RequestedBy` e o `Subject`, que não são
claims do token nem autorizam nada.

### 2.5 O titular dos dados do run filho é o submissor (AOS-440)

`Goal.Subject`, **separado** de `Principal.NHIID`: o titular sob cuja KEK o conteúdo do run é selado
— captura do turno, output da tool no step-ledger, registo de retoma, selo terminal. Derivado pelo
nó pelo **mesmo** vínculo da §2.2; vazio ⇒ `Principal.NHIID`, o que todos os runs anteriores usaram.
O produtor dos eventos e o atributo do span continuam o principal (quem chamou).

O titular atravessa a via durável dentro do `Principal` da call (`Principal.Subject`, copiado pelo
loop), sem mudar a porta da Activity.

**A retoma compara agente, humano e mandato.** Em modo soberano o `Principal.NHIID` é o principal
OIDC de quem chama o nó, e a retoma comparava-o com o `AgentID` da credencial — um run escalado era
irretomável com a sua própria credencial (medido). O `POST /runs` grava agora no registo o
`AgentID`, o humano da raiz (`UserID`) e o `MandateID` da credencial que verificou, e a retoma
compara os três (revisão adversarial: só o agente deixava o mesmo agente, cunhado para outro humano
ou sob outro mandato, continuar o run). Um campo vazio no registo (registo anterior, emissor manual
sem mandato) mantém o comportamento de antes.

## 3. Alternativas rejeitadas

- **(a) O submissor como raiz da cadeia**, com o mandato a autorizar a máquina a agir por ele.
  Rejeitada pelo dono: muda a semântica do ADR-003 (o humano da raiz autoriza), e um service account
  não tem humano a pôr na raiz.
- **(b) O submissor só registado, sem limite no mandato.** Mantinha o resíduo 7 do ADR-033: o humano
  do mandato continuava a responder por qualquer submissor.
- **(c) Um mandato por submissor.** Um mandato por humano que pede planos, renovado de 90 em 90
  dias, e o emissor a escolher qual usar por pedido — cerimónias a mais para o mesmo limite que os
  `requesters` dão numa lista.
- **O `requested_by` no corpo** — §2.2.
- **O v4 escrito por omissão** — §2.4: cortava o rollback no deploy.

## 4. Consequências

**Ganha-se:** o selo de cada decisão diz quem pediu e sob que mandato (com o v4 ligado); o humano do
mandato só responde pelos submissores que nomeou, e o drenador não planeia pelos outros; o
apagamento de quem pediu torna ilegível o conteúdo selado dos seus runs filhos e não toca no de
outros; a fila deixa de entregar objectivos a quem não drena.

**Paga-se:**

- **Um `serve` manual com o NHI de um mandato v2 deixa de correr** — não tem pedido, logo não tem
  submissor. O operador que corre planos à mão usa um token do emissor **manual** — mas só para runs
  NOVOS: **retomar** um run mandatado com um token do emissor manual é recusado (o `MandateID`
  vazio do token não é o do registo).
- **O humano tem de re-assinar o mandato** com `--requesters` antes do fim da janela, e revogar o v1
  ou fechar a janela depois.
- **Re-assinar o mandato em v2 também corta o rollback** (2.ª ronda). O binário anterior não o lê:
  o `VerifySignature` dele assina sob o domínio v1 e recusa um v2, e o `mint-mandated` dele recusa
  o `mandato.json` v2 (`DisallowUnknownFields`, campo `requesters`). A ordem é por isso: deploy →
  confirmar a release → re-assinar, **guardando o `mandato.json` v1** e sem revogar o v1 até a
  release estar confirmada → revogar o v1 ou fechar a janela.
- **Renovar o mandato** (outro id) torna os runs suspensos sob o anterior irretomáveis com os tokens
  do novo: a retoma exige o mesmo mandato. Re-assina-se com `aos_runs_suspended = 0`.
- **Ligar o v4 corta o rollback** para binários anteriores a esta release — do nó **e** do `aos-issuer
  worm-seal` que corre fora do servidor: o de antes lê um WORM v4 como «hash-chain adulterada …
  mutation», pára a âncora diária e aponta para adulteração. Pré-requisito de ligar o v4:
  reconstruir o `aos-issuer` na máquina do operador a partir desta release.
- **O conteúdo JÁ selado sob o `aos-reader` fica onde está.** A migração (M1: destruir a KEK do
  `aos-reader` uma vez, depois de inventariar as partições `~`) é operação do dono —
  `deploy/server/README.md`.

## 5. Resíduos declarados

1. **Conteúdo sobre terceiros dentro de um run fica sob o submissor** (`tecnica/14`): um run que
   lê dados de outra pessoa sela-os sob a KEK de quem pediu. O apagamento do terceiro não os alcança.
2. **`POST /runs` directo por um service account:** o titular é a SA. Não há submissor humano a
   derivar; é um limite declarado, não um defeito.
3. **Fora do crypto-shredding** (ticket a abrir, sem número): os documentos de plano em claro no
   `aos-orq`, os veredictos no WAL do orquestrador, e o objectivo na linha de comando do `serve`
   (`--goal`).
4. **O objectivo do run filho sobrevive ao DSAR.** O `IngestObjective` grava o objectivo REDIGIDO
   em `memory.episodic`, em claro (`integration/ingestion.go`, composto no `bootstrap.go`), para
   todos os runs — pré-existente, não introduzido aqui. O `/dsar/erase` do submissor torna ilegível
   o conteúdo selado do run filho (capturas, step-ledger, registo de retoma), não este registo de
   memória. Ticket a abrir.
5. **Uma reclamação viva serve para runs `<plano>~*` arbitrários** (§2.2): o nó não conhece o
   documento do plano (ADR-018), logo não sabe que nós existem. Um drenador com a reclamação viva
   pode abrir runs com nomes de nó inventados, todos com o `requested_by` do mesmo submissor — mas
   só sob o mandato que o nomeia, e só enquanto a reclamação vive.
6. **O `requested_by` fica em claro no WORM e no evento de mediação, e sobrevive ao DSAR** — é o
   `sub` do submissor, um pseudónimo, como o NHIID dos humanos que o WORM já guarda. O WORM é
   append-only por desenho; o crypto-shredding não o alcança.
7. **Um run soberano escalado ANTES deste binário continua irretomável** com a sua credencial: o
   registo não tem o `AgentID`, e a comparação cai no NHIID como antes. E um run sem submissor
   (anterior, ou que não é de um plano) não se retoma com um NHI de um mandato v2.
8. **Modo não-soberano:** sem gate de leitura o `plan_request` é recusado e o `POST /runs` não
   verifica a credencial na porta, pelo que os `requesters` não são impostos aí. Produção exige o
   gate soberano.
9. **O hook de identidade do RM não re-verifica os `requesters` em cada tool call** — a decisão é
   na porta do `POST /runs` e da retoma, que são as duas vias por onde um run ganha credencial. Não
   se mediu o efeito de a impor também no hook sobre outros consumidores do mesmo NHI (o planeador
   do `aos-orq` usa-o sem submissor), e por isso não se impôs.
10. **Uma saída 11 do `serve` com irmãos já em voo deixa runs filhos órfãos no nó** — o caso de um
    mandato trocado a meio de um plano: o primeiro nó recusado fecha o pedido, e os que já corriam
    continuam no nó sem quem os recolha.
11. **Depois de um rollback do binário, os runs filhos em voo ou suspensos não se retomam**: foram
    selados sob o submissor (`Subject`), e o binário anterior abre as capturas pelo NHIID —
    fail-closed, não há leitura com a chave errada.

**Fechados pela revisão adversarial (2026-09-27):** o TTL da reclamação (30 min) abaixo do pior caso
de uma geração — subiu para **60 min** (pior caso: prazo 40 + 3 tentativas do planeador × egress
2 + re-hidratação 2 = 48 min), com um teste que soma as parcelas das fontes (ADR-030 §4); o rollback
cortado pelo v4 — passou a expand/contract (§2.4), e o `Append` recusa um registo preposto numa
versão acima da que o store escreve, para nenhum produtor o contornar.
