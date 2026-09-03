# Auditoria adversarial do Plano de Controlo — ORQ + SCH + PDP

| Campo | Valor |
|---|---|
| Documento | `analises/10_Auditoria_ORQ_SCH_PDP_Adversarial.md` |
| Data | 2026-09-03 |
| Estado auditado | HEAD `36c8ef5` (`chore: CODEOWNERS…`), branch `chore/codeowners`, árvore de trabalho limpa |
| Âmbito | **ORQ** (`packages/control-plane/orchestrator`, `runlifecycle`, `cmd/aos-orq`), **SCH** (`packages/control-plane/scheduler`, `budget`) e **PDP** (`packages/control-plane/pdp`), com as fronteiras que lhes tocam |
| Tipo | Sete lentes independentes → **refutação adversarial** → **medição executada no nó real** → **validação cruzada externa** (§8) |
| Auditoria anterior | `analises/09_Auditoria_RT_RM_Adversarial.md` (2026-09-02), cujo §7 declara: «O PDP, o orquestrador e o model-gateway só foram tocados nas fronteiras» |
| Contratos verificados contra | `_BRIEF.md` (linhas 50-52, 94), `specs/EPIC-01`, `EPIC-03`, `EPIC-09`, `EPIC-19`, ADR-011/013/015/018/019/020/022/023, `tecnica/01`, `03`, `12`, `14`, `16`, `17`, `18` |
| Remediação | `specs/EPIC-22_Remediacao_Auditoria_ORQ_SCH_PDP.md` (AOS-305..311) — os sete defeitos activos do §6 |

---

## 1. Método, e o preço que se paga por ele

Três passagens, cada uma com o ónus da prova invertido em relação à anterior.

**Passagem 1 — sete lentes independentes.** Uma por eixo: ORQ (grafo acíclico, delegação), SCH
(durabilidade, leases/fencing, backpressure), PDP (policy-as-code, p95, fail-closed), integração
(o que está montado no binário entregue), completude/rastreabilidade, resiliência/observabilidade,
segurança. Instrução comum: âncora `ficheiro:linha` para tudo; «não existe» só como **ausência
medida**; e verificar o **código**, não o comentário. Resultado: **63 achados**.

**Passagem 2 — quatro refutadores.** Regra única: *na dúvida, o achado cai*. Nenhum herdou a
evidência de quem acusou — todos refizeram os greps, e cada âncora citada foi aberta e confrontada
com o que lá está. Cada refutador teve de procurar activamente três coisas: a camada compensatória
noutro sítio, a decisão registada (ADR, `REGISTO-Deferimentos.md`, campo Estado do ticket), e o
alcance da frase construída sobre o facto.

**Passagem 3 — medição.** Dois medidores. Um levantou o **nó real** com bundle de política assinado,
oráculo de autonomia, dois operadores, WORM e um gateway de modelo de arreio, e atacou a superfície
HTTP. O outro atacou as duas alegações que **nasceram na refutação** — e que, por isso, nunca tinham
sido refutadas por ninguém.

### 1.1 O que cada passagem mudou

| | |
|---|---|
| Passagem 1 → 2 | Uma leitura de duas fontes de decisão **reenquadrou catorze achados de uma vez**; 5 caíram por inteiro; 2 alegações **novas** nasceram do lado do refutador |
| Passagem 2 → 3 | Um veredicto **reforçado além do alegado**, um **reescrito**, um **controlo negativo que não conseguiu falhar o alvo** |

---

## 2. A tese: a convergência mediu visibilidade, não verdade

Sete lentes que não falaram entre si convergiram na mesma frase: *o plano de controlo existe como
biblioteca e não está composto em binário nenhum*. Cinco delas trataram-no como descoberta.

Não é. É **decisão ratificada**, escrita nas palavras exactas que seria preciso citar para a refutar:

- **ADR-018 §4** (Aceite, 2026-07-23): «a decomposição real do DAG (ORQ) e o despacho multi-tarefa
  (SCH) ricos ficam por *cablar dentro do run* em tickets próprios; a v1 corre a forma mínima…,
  **declarada — não fingida**.»
- **ADR-023** (**RATIFICADO**, 2026-08-30, autoridade de dono), §«O QUE ESTE ADR NÃO FAZ»: «o nó `aos`
  continua single-host e o seu grafo de build continua **sem** um pacote sob `control-plane/orchestrator`
  ou `control-plane/scheduler`». §4: «Fica **desligado por omissão**: nenhum deployment v1 o arranca.»
- **`specs/EPIC-10`, AOS-281, §Contexto**: «O planeador, o gate de aprovação, a materialização e o
  dispatcher estão entregues e testados — e **nenhum deles corre**. **Não é wiring esquecido: é o
  ADR-018 a impedi-lo por desenho.**»
- E é **imposto por guard-test**: `packages/cmd/aos/boundary_orq_sch_test.go` proíbe o import directo
  e o grafo transitivo. Medido em duas passagens independentes: `go list -deps` nos cinco binários
  devolve **zero** pacotes sob `control-plane/scheduler`; `control-plane/orchestrator` aparece só em
  `aos-orq`, que o seu próprio `main.go:12-13` declara «DESLIGADO POR OMISSÃO na v1».

Isto muda o que conta como achado neste âmbito. **Um buraco que um ADR ratificado descreve não é
descoberta — é a leitura do ADR.** O que resta, e que esta auditoria persegue, são três classes:
(a) um artefacto que diga algo **falso** sobre o estado; (b) um defeito **dentro** de um caminho que
corre; (c) uma lacuna **não coberta** por ADR nem por `DEF-NNN`.

O contraste com a auditoria anterior é instrutivo. Na 09, o discriminador era o registo de
deferimentos. Aqui é o **ADR ratificado** — e a lição operacional repete-se com outro sujeito: as sete
lentes convergiram porque a decisão está escrita de forma visível, e a convergência mediu exactamente
isso.

---

## 3. Os sobreviventes

Vinte e um achados resistiram às três passagens. Estão ordenados pelo que a distinção
**activo/latente** faz a cada um: no topo, o que corre no binário que se instala.

### 3.1 Medido no nó real, e activo — o mais grave

**Uma assinatura remove o gate humano, e não há tecto nenhum.** `POST /autonomy` autoriza-se com a
assinatura ed25519 de **um** operador de `AOS_OPERATORS`
(`packages/cmd/aos/autonomy_route.go:111-118` → `packages/integration/steer_authenticator.go:187-228`,
que faz três coisas: pubkey registada, assinatura, nonce). Nem papel, nem escopo por-operador, nem
four-eyes. Medido ponta-a-ponta, com dois operadores compostos:

```
=== POST /autonomy  (um só corpo, um só emitter, um só campo signature) ===
{"actor":"op:jimy","agent":"agt-1","domain":"fs","from":"L0","status":"applied","to":"L5"}
HTTP_STATUS=200

=== e o efeito onde importa: mesmo run, mesma tool, mesmo token ===
ANTES  (fs=L0):  status=waiting_on_human
       WORM seq=1 escalate cap=cap:fs.read denied_by=policy code=E_ESCALATED
                  reason="autonomia L0 x gray -> suggest (gate humano)"
DEPOIS (fs=L5):  status=paused
       WORM seq=1 deny     cap=cap:fs.read denied_by=dispatch code=E_TOOL_NOT_REGISTERED
```

`denied_by` passou de `policy` para `dispatch`: o `escalate` desapareceu e o run deixou de esperar
por um humano. Não há degraus — `L0 → L5` num salto, e um par que **nunca existiu** nasce
directamente em L5 (`from:"(nao registado)"`). Não há controlo de papel: `op:jimy` promoveu a
`class:agent-break-glass`, com que não tem relação nenhuma, e o nó devolveu 200.

O contraste está no mesmo binário: o roster de `/approve` **aborta o arranque** se duas entradas
partilharem pubkey (`packages/cmd/aos/main.go:2107`), precisamente para que «duas pessoas» esteja
ancorado em criptografia, e exige capability `approve:<classe>` de vocabulário fechado.

Há uma segunda metade, reproduzida em sonda: `governance/autonomy/registry.go:176` aplica
`r.levels[k] = level` e sela **depois** (`:182-186`), devolvendo o erro **sem reverter**; o handler
(`autonomy_route.go:120-123`) traduz isso em `400 "nivel recusado"`. Com o WORM em baixo, **a
promoção acontece, não fica selada, e a API diz que foi recusada**. O ponto fino: `registry.go:142-146`
**declara essa semântica de propósito** e nomeia a obrigação do consumidor («para que uma alteração
de nível sem changelog selado seja detectável por quem chama»). O defeito não é do registo — é do
único consumidor de produção a violar um contrato escrito a vinte linhas de distância. O RM, no mesmo
repositório, trata a condição equivalente como `CodeAuditUnavailable` e **nega**
(`kernel/reference-monitor/monitor.go:349-353`).

*Atenuante que tem de ficar escrita:* o acto é **totalmente atribuível**. Sela-se duas vezes no WORM
com hash-chain, em `autonomy` (`Capability "autonomy:set_level"`, obrigação `autonomy.level_changed`
com `old_level`/`new_level`/`actor`) e em `governance.control`. Ninguém muda a supervisão em segredo.
Muda-a **sozinho** — e o defeito é de controlo preventivo, não de rasto.

**`/runs/{id}/challenge` não autentica nada, e o comentário diz que autentica.** A rota está
classificada `planoControlo` (`packages/cmd/aos/planos.go:212`), a definição da classe (`:74-77`)
declara «a assinatura ed25519 do corpo (AOS-160) a decidir depois, dentro dele», e `api.go:1667` diz
«Autenticado pela MESMA admission do plano de controlo que o /approve». O handler (`api.go:1668-1695`)
não chama `SteerAuth.Authenticate` nenhum; `admitControl` é um token-bucket e `admitControlMTLS`
devolve `true` sem CA montada (`api.go:1911-1919`). Cinco vectores, todos `HTTP 200`:

```
sem assinatura, sem headers, contra run inexistente -> 200 {"challenge":"DU1uMktDZN4URZMtJNYdXN3yKA4…"}
aprovador arbitrário não pinado ("quem-eu-quiser")  -> 200
reemissão para o mesmo (request_id, approver)       -> 200, challenge DIFERENTE (não invalida o anterior)
rajada de 20                                        -> 200 ×20

WAL: foureyes.challenge.issued ×24
     producer nhi:foureyes-challenge-issuer   run_id:""   ← atribuição zero
```

Escrita não autenticada no Event Store durável, um stream por pedido, cujo `producer` é uma constante
do próprio nó e cujo `run_id` é vazio: nada no registo diz quem pediu.

*O que não ficou provado:* a consequência a jusante. Seis tentativas de consumir um challenge emitido
anonimamente numa cerimónia `/approve` real devolveram `403 "aprovacao recusada"` — uniformemente, e
**sem razão registada**, pelo que não é possível distinguir «o challenge anónimo foi rejeitado» de
«a cerimónia estava malformada». Fica em aberto. *(Observação lateral com valor operacional: um
operador com uma cerimónia legítima a falhar tem exactamente a mesma ausência de diagnóstico.)*

**O PDP é cego, e a explosão de denies não tem via na configuração entregue.** `pdp.WithTracer` tem
**zero chamadores** em todo o repositório fora dos testes do próprio pacote — verificado por três
agentes independentes — e o sítio de composição (`packages/cmd/aos/main.go:1222,1244`) monta só
`WithTrustAnchor` e `WithAutonomyOracle`. `PDP.Decide` (`pdp.go:234-284`) não abre span, não faz
`Append` e não incrementa métrica. `PDP.Reload` não tem chamador não-teste **nem rota HTTP**: o span
`aos.policy.reload` não é apenas no-op, é **inalcançável** — no nó entregue, trocar de política é
reiniciar o processo.

Medido, com onze mediações reais no WORM e OTLP desligado (que é o default e o do `driver.sh`):

```
aos_slo_spans_observed_total 0
31 séries aos_* — NENHUMA conta decisões, denies, escalates ou versão de política
SSE da trajectória de um run com 5 denies: 0 eventos de decisão
```

Com `AOS_OTLP_ENDPOINT` composto — a falsificação que **teve efeito** e obrigou a reescrever a
conclusão — há via: `override_rate` subiu a 0,67 contra um tecto de 0,1 e `aos_slo_breached` foi a 1.
Mas dos nove nomes de span exportados nenhum é `aos.policy.reload` ou `aos.autonomy.level`, e os
atributos do `execute_tool` que carrega a decisão são `aos.decision`, `aos.decision.denied_by`,
`aos.principal.nhi_id`, `aos.run_id`, `aos.step_id`, `aos.taint` — **sem `policy_version` e sem
`capability`**.

A conclusão certa não é «invisível». É: **visível sem causa, e só com OTLP composto.** Correlacionar
«os denies subiram» com «a política mudou» exige hoje cruzar à mão uma linha de log de arranque com N
partições WORM lidas offline, uma por run.

### 3.2 Medido, e novo — as duas alegações que nasceram na refutação

**As obrigações são código, não política.** `pdp/engine_cedar.go:177-184` (`obligationsFor`, chamada
só de `pdp.go:276`) deriva as obrigações **em Go**: `redact_pii` com campos fixos quando a
sensibilidade é `confidential`, e `audit` sempre. Medido com um bundle divergente, assinado pelo mesmo
caminho de `cmd/policy-sign`:

```
BUNDLE-REFERENCIA v1.0.0 (region=eu)         -> permit  obligations=[redact_pii{email,phone}, audit{full}]
BUNDLE-DIVERGENTE v9.9.9 (region=eu)         -> deny    obligations=null
BUNDLE-DIVERGENTE v9.9.9 (region=antarctica) -> permit  obligations=[redact_pii{email,phone}, audit{full}]
```

O bundle **manda na decisão** e **não toca nas obrigações**. Quatro vias de anotação Cedar
(`@obligation`, `@advice`, `@redact_pii`, `@audit`) compilaram sem erro e foram ignoradas por inteiro.

O que o torna material: o **golden Rego** de `tecnica/12:333-341` — a referência de que ADR-011:22 diz
que o Cedar tem «semântica idêntica» — **exprime as obrigações na linguagem de política**. Em Rego
viajariam no bundle assinado; em Cedar-Go não viajam. A equivalência falha neste eixo.

Alcance: o vocabulário do PEP tem **5** obrigações (`reference-monitor/obligations.go`); o PDP sabe
emitir 4, **nenhuma vinda do bundle** (2 hardcoded, `region` de um registo Go, `autonomy` de um
oráculo Go); `ttl` nunca é emitida por caminho nenhum. Uma obrigação que o PEP não reconhece dá deny
fail-closed — o que fecha o tecto, com duas fechaduras Go e nenhuma em policy-as-code. E o gate 7 não
mitiga: `TestDecide_GoldenTruthTable` **afirma** as obrigações, isto é, **fixa as constantes Go**.

Consequência prática: apertar a protecção de dados — redigir `ssn`/`iban`, ou redigir também em
`sensitivity=internal` — exige **libertar binário**, fora da cadeia de assinatura e ratificação que o
ADR-011 promete para a política.

**A região do recurso é uma etiqueta que o operador escreve.** Cadeia traçada âncora a âncora:
`resource_region` no JSON do operador (`deploy/server/model-tools/tools.json:16,34`) →
`packages/cmd/aos/modeltools.go:50,235,274,280` → `agent-runtime/model.go:36` → `loop.go:744` →
`Input.Resource.Region` → `engine_cedar.go:126` → `resource.region == "eu"` (`aos_authz.cedar:32`).
`validateResourceBinding` valida a sintaxe dos slots de `resource_value` — **nunca a região**. Medido,
mesmo recurso físico, só a etiqueta a mudar:

```
resource_region="eu"         -> permit      resource_region="us"          -> deny
resource_region="antarctica" -> deny        resource_region=""            -> deny
```

Fail-closed na omissão: a configuração só pode **alargar** afirmando «eu» falsamente, nunca falhar
aberta por ausência. A combinação está viva num deployment expedido
(`deploy/server/docker-compose.prod.yml:86,331`).

> **Corrigido em §8.1:** esta medição construiu o `Input` à mão com `Taint` no valor-zero. Pelo caminho
> cablado o taint é sempre `"untrusted"` e a única regra que lê região exige `!= "untrusted"` — o
> `permit` medido não é alcançável no nó. O facto estrutural (etiqueta não atestada, CON-04 a emendar)
> mantém-se; a consequência desce para observação documental.

E **corrige o registo**: CON-04 (`analises/08:116`, ABERTO) afirma que `Resource.Region` «vem de
`inv.ResourceRegion`, campo produzido pela fronteira **untrusted**», avisando que ligar o bundle
deixaria a região «ditada pela saída do modelo». Isso é hoje falso —
`platform/model-gateway/runtime_adapter.go:178-181` constrói `ToolInvocation{ToolID, Input}` e mais
nada; nenhum adaptador desserializa `ResourceRegion` da resposta do modelo. **O achado registado
descreve o mecanismo errado**, pelo que a remediação planeada (proteger contra o modelo) não fecharia
o defeito real (atestar a residência). O mesmo eixo repete-se na allowlist assinada do model-gateway:
a região que entra em `Input` vem de `AOS_MODEL_REGION` com default `"eu"` — **a assinatura autentica
a tabela, não a região afirmada**.

### 3.3 Confirmado, com consequência delimitada

- **A soberania por board não alcança o caminho de efeito (AOS-094, AC#2).** `pdp.WithBoardRegions`
  sem chamador de produção ⇒ `applySovereignty` retorna imediato ⇒ a obrigação `region` nunca é
  emitida ⇒ `reference-monitor/obligations.go:86-89` → `enforceRegion` é código inalcançável. A
  alegação inicial («a soberania por board não é imposta») **caiu**: quatro dos cinco CA de AOS-094
  estão cobertos pela allowlist default-deny por `(board, modelo, região)` do model-gateway (AOS-058),
  assinada com trust anchor pinado por fingerprint em código e **composta no binário entregue**; o
  read-path está coberto por AOS-182/205. O que sobrevive é o AC#2 para tool calls — e, combinado com
  a região auto-declarada acima, o eixo não tem apenas o wiring desligado: **falta-lhe a fonte de
  verdade do lado do recurso**. Sem entrada no registo: percorridas as onze entradas de soberania
  (DEF-201..212, 280-REGIAO, 301), nenhuma nomeia `WithBoardRegions`.
- **Nunca é emitido `policy.changed`.** `Reload` sem chamador e sem rota; a troca real passa por `Open`,
  que não emite changelog. O CA de AOS-088 é satisfeito só pela suite. *Atenuante:* `policy_version`
  viaja em cada `MediationRecord` selado no WORM, pelo que a mudança é detectável no trilho de
  mediação. Falta o evento dedicado, com `OldVersion`/`ContentHash`/`At`.
- **O exemplo normativo do contrato C1 mente.** `tecnica/12:98-131` devolve `"decision":"permit"` para
  um pedido que o código nega por três razões independentes, e inclui uma obrigação
  `{"type":"ttl","seconds":3600}` que **nenhum caminho do PDP emite**. Medido. A remediação P0-D
  (`analises/00:287`) prometia «tornar o exemplo C1 consistente com a política (com teste de contrato
  no gate 7)»; `grep -rn "RIG-02" specs/ docs/ tecnica/` devolve vazio.
- **AOS-090 não tem chamador.** `autonomy.NewController` — promoção só por fiabilidade sustentada e
  **despromoção automática em anomalia** — tem zero chamadores em todo o repositório e zero entradas no
  registo. É a metade de segurança da autonomia: quando esta é ligada, um par promovido a L5 fica a L5
  até alguém reparar. *(O resto da alegação original caiu: a ausência de produtor de `escalate` está
  declarada à letra na primeira coisa que o operador lê ao arrancar — `posture_banner.go:259`.)*
- **`aos-orq serve --plan-doc` materializa o não-validado.** Medido duas vezes com o binário construído
  do source: plano com ciclo condicional `a↔b` e plano com `capabilities_hash` que não casa com o
  snapshot pinado — ambos `materializado: … EXIT=0`, com `plan.materialized` e `task.node.created`
  gravados. `planvalidate.Validate` não tem chamador no caminho (`main.go:346` faz só `plan.Decode`);
  `planvalidate` rejeitaria exactamente estas duas formas (`verdict.go:104-108`, `validate.go:157-159`).
  É a única metade do lote da composição que **não é sobre composição** — e não está coberta por DEF
  nem por ADR. Delimitação honesta: `aos-orq` está desligado por omissão (ADR-023 §4).
- **O CA2 de AOS-238 está marcado `[x]` sobre uma costura vazia.** Com o grep completado a todo o
  `packages/` (o acusador tinha declarado âmbito parcial): zero implementações de produção de
  `plandispatch.Headroom` — as quatro `Acquire` que existem são duplos de teste. O `deriveMaxSpawn` real
  vive em `scheduler/spawn_admission.go:243`, noutro módulo que o **guard-test do próprio AOS-238**
  proíbe importar. Um critério de aceitação sobre um *tecto de concorrência* está fechado quando o
  tecto, por construção, só pode existir num terceiro módulo que ainda não existe.
- **O lock do dispatcher é linear na profundidade da fila.** `priority.go:572-573` mantém `d.mu` durante
  o laço de candidatos, que chama `Admit` — e este faz `Read` do stream inteiro mais `Append` com
  `WithExpectedSeq`, em laço de retry. Reproduzido **sobre o substrato durável real** (fsync medido:
  p50 522 µs, p95 1,05 ms), sem latência sintética:

  ```
  [durable] N= 30  Dispatch=26.4ms   Submit bloqueado=25.4ms
  [durable] N=100  Dispatch=84.3ms   Submit bloqueado=83.7ms
  ```

  O sinal está invertido: quanto mais saturado, mais tempo o lock preso, e menos trabalho novo consegue
  **entrar** — realimentação positiva no regime que o SCH existe para gerir. **Latente** (o módulo não
  está em binário nenhum), sem dívida declarada.
- **Cada admissão relê o stream desde a seq 1.** `admission.go:649`, dentro do laço de CAS, sem
  `fromSeq` nem snapshot nem compactação. Confirmado ao evento: 99,5 eventos lidos por admissão nas
  primeiras 200, **699,5** nas 601-800. A magnitude caiu: `x5,9` in-memory tornou-se **`x1,68`** sobre o
  substrato real, porque o fsync amortiza a releitura no regime medido. O eixo é real e é o único do
  lote **sem dívida escrita em lado nenhum** — nem no código, nem no README §«Limites conhecidos», nem
  no registo.
- **A negação por prazo esgotado é atribuída ao sítio errado.** `PDP.Decide` ignora o `context.Context`
  (medido: contexto cancelado ⇒ `permit`, `err=nil`). Mas o fail-open alegado é **inalcançável**: o
  `audit-before-effect` do RM (`monitor.go:348`) usa o mesmo contexto, o `Append` verifica `ctx.Err()`,
  e o resultado é `deny / E_AUDIT_UNAVAILABLE`. O «timeout fail-closed» que `tecnica/17` §4.3-D declara
  entregue **cumpre-se ponta-a-ponta**. O resíduo: quem lê o evento vê «Event Store indisponível» onde
  houve prazo esgotado. *(**Invertido em §8.1:** esta cadeia foi medida com o sink de eventstore; o
  `audit.FileStore` de produção não consulta `ctx`, pelo que o fail-closed é condicional ao sink.)*
- **A matriz de conformidade subdeclara um controlo entregue.** `tecnica/14:111,122` afirma que o
  binário «não expõe variável de ambiente que carregue uma política» e «Falha o critério (c)». A via
  existe (`main.go:686`, `bootstrap.go:2262`, teste `aos220_pdp_bundle_surface_test.go`), entrou em
  **2026-07-31** — um dia depois do último commit a tocar a matriz — e está **453 commits** atrás. E é
  pior do que a data sugere: como as obrigações não vivem no bundle, carregar **qualquer** bundle acende
  a perna. A célula está substancialmente errada, não mal redigida. A baseline
  `scripts/ci/baseline/deferrals.txt` continua a certificar como dívida reconhecida exactamente o que
  AOS-220 entregou.
- **O gate de deferimentos descarta cinco linhas em silêncio.** 103 linhas `| DEF-` no registo, 98
  parseadas. `scripts/ci/deferrals.py:176` exige `^\|\s*(DEF-\d{3})\s*\|`, e as cinco `DEF-280-*`
  (`:201,203,204,205,206`) não casam; a verificação de colunas (`:257-259`) corre **depois** do
  `continue`, pelo que a linha descartada nunca chega ao reporte de erro. `RE_NOTE` tem o mesmo defeito.
  **Dano hoje: potencial** — os três ficheiros ancorados nessas linhas não têm marcador `DEFERIDO`, pelo
  que nada se perde no cruzamento código↔registo. O defeito é estrutural: o padrão `DEF-NNN-SUFIXO` já
  está estabelecido com cinco exemplares, e a próxima entrada sufixada entra sem eixo verificado e sem
  ninguém dar por isso.
- **O plano de controlo está fora do gate de cobertura.** `COVERAGE_GATED_MODULES`
  (`scripts/ci/lib.sh:412`) lista RM, agent-runtime, testkit e oito sub-pacotes de `governance/`;
  `orchestrator`, `scheduler` e `pdp` estão fora. Medido: 90,0% / 91,9% / 90,8% — números bons hoje que
  podem descer para 40% com os gates verdes. O comentário imediatamente acima (`:400-411`) documenta que
  este modo de falha **já mordeu uma vez**, com o `agent-runtime` a 93,5% fora da lista, achado a
  2026-08-23. *(Os números citados pela lente — 85,9/89,9/86,5 — caem: vinham de `coverage/lcov.info`,
  que é `gitignored` e local, não prova de CI.)*
- **A RTM afirma cobertura que as suas próprias secções geradas contradizem.** `tecnica/16:196` diz
  «20/20 ADRs e 12/12 NFRs»; a §4 gerada tem 19 linhas e a §5 tem 10, e `:123`/`:143` dizem 19/19 e
  10/10 — a setenta linhas de distância, no mesmo ficheiro. `:241` descreve uma alteração («+ADR-020 …
  no §4») que o ficheiro não contém. A §7 está **duplamente sem guarda**: `rtm-regenerate.py` regenera
  §§1,4,5,6 e pára; `ref-lint.py:323` tem a RTM na lista de `skip`. *A consequência alegada caiu*:
  NFR-11 e NFR-12 **têm** ticket de verificação (AOS-242 e AOS-232, `specs/EPIC-19:420,132`). Falta-lhes
  a linha, não a prova.
- **Um número falso, conhecido, datado e por corrigir.** `scheduler/README.md:465` afirma «vencendo
  individualmente em **≥90%**». Corrido: `least-loaded venceu 27/33 ordens` = **81,8%**;
  `routing_test.go:452` assere `wins*4 < n*3`, isto é **≥75%**. Foi reportado em
  `docs/reports/desafio-A5-escalonador.md:52` a **2026-08-08**, com âncoras e item de remediação escrito.
  Vinte e seis dias depois está intacto e sem entrada no registo. Baixo em impacto, alto em sinal: o
  programa tem a disciplina para o fechar e não a aplicou aqui.
- **O detector de rotas partilha o modo de falha do defeito.**
  `packages/cmd/aos/rotas_de_controlo_test.go:175-252` verifica classificação, mTLS e admissão —
  **nunca a assinatura por-rota**. É o achado de processo por trás do `/challenge`, e vale mais do que o
  achado de rota: `planos.go:14-33` diz que a tabela veio precisamente fechar esta classe de erro.
- **Contagem manual num artefacto de segurança.** DEF-012 (`REGISTO-Deferimentos.md:155` e `:480`) e
  `deploy/node/README.md:148,755` — **quatro** sítios — enumeram «as 10 rotas do plano de CONTROLO». São
  11 desde AOS-288 (`planos.go:235`, `POST /nhi/revoke`, 2026-09-02). Inócuo no comportamento (a barreira
  deriva da classificação da tabela, não da prosa) e material na auditoria: quem inventaria a superfície
  de controlo a partir do registo omite a rota que **revoga identidades**.

---

## 4. O que caiu, e porquê o modo importa

Oito alegações destruídas. Quatro merecem registo porque o **modo** como caíram ensina mais do que o
facto.

**A citação truncada é a nova citação inventada.** A auditoria 09 apanhou um achado que citava linhas
inexistentes. Aqui o padrão mudou de forma: as citações existem, e o corte fabrica a contradição.

- «`pdp/capabilities.go:25-27` declara *SEM WILDCARDS PERIGOSOS POR OMISSÃO*» — e o bundle de referência
  tem uma classe `{"cap":"*"}`. A frase continua: «Uma entrada `"*"` **só concede se trouxer uma
  `justification` não-vazia**». A entrada tem justificação, dono nomeado e cadência de revisão. Medido:
  a classe break-glass obtém `permit` **apenas** para as duas capabilities que as regras Cedar já
  concedem a qualquer classe; `cap:fs.write` e `cap:qualquer.coisa` são negadas por default-deny. O
  wildcard atravessa o gate de allowlist e não concede coisa nenhuma. **Código e bundle concordam; a
  contradição foi fabricada pelo corte.**
- «`runbooks/registry.go:132-135` diz ao operador que o escalonador corre *noutro processo*» — o campo
  chama-se literalmente `SemProdutorReason` e as linhas seguintes dizem «cujo **Meter de producao nao
  existe**… **NUNCA dispara neste binario**». O sítio existe **para** dizer que não há produtor.

**Uma alegação de p95 destruída por medição, com margem de três ordens.** Alegava-se que o p95 do PDP é
calculado sobre médias de lotes de 200 e que a cauda é aplanada por construção. Refeito **sem lotes
nenhuns**, 200 000 `Decide` cronometrados individualmente: `p999 = 528 µs`, **`MAX = 10,57 ms`** — o pior
de duzentos mil está abaixo do alvo de 15 ms. O lote não pode esconder um fosso de 3000×, e o comentário
de `bench_test.go:66-72` declara o motivo do lote (granularidade do relógio do Windows, que os p50/p95 a
0 s confirmam). Caiu também a alegação de que o input do benchmark é irrealizável: cruzava dois planos de
taint distintos — `pdp.Input` não tem campo de taint de autorização.

**A sobre-admissão x2,0 do relógio era o tecto que o desenho já declara.** A sonda reproduziu-se, mas
duas medições novas desmontaram-na: a sobre-admissão exige desvio superior à **janela inteira** (a 59 s
de desvio, zero), e o **x2,0 é alcançável com relógios perfeitos**, porque é inerente a esquemas de
janela deslizante — e `scheduler/README.md:156-163`, secção «Limites conhecidos (semântica declarada)»,
escreve-o. O acusador mediu o tecto declarado e atribuiu-o ao relógio. *(O que excede o declarado exige
três ou mais nós mutuamente desviados de mais de uma janela.)*

**Uma alegação assente numa citação que já não resiste ao ficheiro.** COMP-04 (`analises/01_Completude.md`)
cita «o `_BRIEF` §1 eleva "IPC→mensagem agente-a-agente" a primitivo». `grep -in "ipc|a2a|agente-a-agente"`
sobre `_BRIEF.md` **e** `specs/00_AOS_Carta.md` devolve **zero ocorrências**. A lente herdou a citação sem
a refazer — o mesmo erro que a metodologia proíbe. *(A lacuna documental subjacente é real: `tecnica/12`
tem C1…C5 e não tem contrato ORQ↔SCH nem SCH↔RT. Mas `tecnica/17` §4.8 **é** «Mensagens inter-agente
(A2A)»: o vector está analisado, o contrato é que falta. E há colisão de identificadores — `tecnica/16:19`
declara fechar «COMP-04», mas fecha o do `v1_baseline`, outro achado com o mesmo ID.)*

As restantes: o valor-zero do `HookDecision` permitir é inalcançável (o `switch` testa `err != nil` antes
de ler `res.Decision`); o `MachineParker` sem fencing token vive fora do grafo de build e resolve máquinas
por um registo **em memória do mesmo processo**, pelo que «incarnação superada» não tem objecto; e as
caixas de CA por marcar do EPIC-03 não medem nada — EPIC-04, 05, 06, 07, 08 e 11 têm **todos** zero
marcadas, e o **EPIC-21, declarado encerrado**, tem zero em 62. Não são um tracker; são um molde por
preencher, e a RTM já o registou como GAP-05.

---

## 5. Meta-achados

**A refutação também acusa — e ninguém a auditava.** Dois dos achados mais consequentes desta auditoria
(as obrigações hardcoded, a região auto-declarada) não vieram de lente nenhuma: nasceram na secção «o que
o acusador não olhou» de dois refutadores. Um refutador que produz um achado novo está a agir como
acusador, e um acusador não se audita a si próprio. Foi preciso uma passagem extra só para os atacar — e
ambos sobreviveram. **A refutação não é apenas subtractiva; o pipeline tinha de prever isso e não previa.**

**A medição inverteu em ambas as direcções, outra vez.** Reforçou o achado do `/autonomy` **além** do que
a leitura alegava — a leitura previa o efeito, a medição mostrou que não há sequer um degrau, e que a
promoção funciona para uma classe com que o operador não tem relação nenhuma. E **reescreveu** a conclusão
do PDP cego: compor um sink OTLP mostrou que o `override_rate` é uma via real, e a conclusão passou de
«invisível» para «visível sem causa, e só com OTLP composto». O controlo negativo funcionou nos dois
sentidos: das três alegações que o medidor tentou falhar, uma sobreviveu a cinco vectores e outra mudou o
veredicto.

**A gravidade inflaciona-se pela sonda, não só pela frase.** Os dois factores mais impressionantes da
passagem 1 — «`Allow` bloqueado 269,97 ms» e «degradação x5,9» — eram artefactos de `time.Sleep`
sintético. Contra o Event Store real (fsync p50 = 522 µs) tornaram-se **1,13 ms** (≈530× de inflação) e
**x1,68**. Uma sonda que corre não prova o que o seu autor diz que prova: a primeira coisa a medir é a
latência daquilo que se está a substituir por um duplo.

**Um achado registado pode ficar estale e desviar a correcção.** CON-04 está ABERTO no relatório v4 e
descreve a região do recurso como vinda da fronteira untrusted, ditada pelo modelo. Medido: falso. O
resíduo real é outro — declaração de operador sem atestação. Um achado que envelhece mal é pior do que um
achado ausente: mobiliza trabalho **na direcção errada**, com a autoridade de estar registado.

**Números escritos à mão derivam exactamente onde nenhum gate os lê.** Verificadas 61 asserções numéricas
em prosa; **27 batem, 34 não** (~56%). A distribuição não é aleatória: onde uma máquina escreve (§§1,4,5,6
da RTM) bate; onde uma máquina compara (§3.1 do registo de deferimentos) bate; onde ninguém lê, deriva —
21 dos 34 falhanços vivem nos dois `INDICE.md`, que afirmam «189 tickets em 17 epics» e «118 tickets em 11
epics» contra os **304 tickets em 21 epics** reais. O exemplar mais limpo é auto-contido: a §7 da RTM
afirma 20/20 a setenta linhas de secções geradas no mesmo ficheiro que dizem 19/19 — e é a única secção
excluída *tanto* da regeneração *quanto* do ref-lint. **A remediação de maior retorno não é corrigir os
trinta e quatro; é um gate que extraia asserções numéricas dos `.md` e as confronte com a fonte.** Sem
isso, cada um volta.

**A honestidade do banner é o melhor controlo deste programa, e por isso as excepções contam.** Três
alegações da passagem 1 caíram porque a consequência que denunciavam está escrita, à letra, na primeira
coisa que o operador lê ao arrancar o nó (`posture_banner.go:259`, `bootstrap.go:2262`). Contra isso, a
única omissão de ramo encontrada — o mTLS de controlo, que não produz linha nenhuma quando está desligado
(`main.go:1096-1098`, sem `else`, ao contrário de todos os outros) — é uma nota menor, e o refutador
argumentou de forma defensável que a barreira primária declarada (a assinatura ed25519) **está** ligada e
**é** anunciada.

---

## 6. Classificação e destino

O critério é o do §1 do `REGISTO-Deferimentos.md`: um **defeito a corrigir** não entra no registo —
registá-lo seria convertê-lo em dívida aceite por decreto. Um **limite aceite** entra, com eixo.

| Sobrevivente | Estado | Destino |
|---|---|---|
| `/autonomy`: uma assinatura, sem papel, sem tecto, sem four-eyes | **activo, medido** | **AOS-305** (EPIC-22) |
| `/autonomy`: nível aplicado com selagem falhada, API responde 400 | **activo, medido** | **AOS-306** (EPIC-22) |
| `/challenge` sem autenticação, com o comentário a declarar o contrário | **reclassificado (§8)** — emissão inerte para autoridade | **AOS-308** (EPIC-22); **e** `/approve` nega sem selo → **AOS-309** (EPIC-22) |
| Obrigações hardcoded em Go, fora do bundle assinado | **reclassificado (§8)** | **Limitação ratificada** — ADR-011:58,143 documenta a derivação em Go; emenda de precisão ao ADR sobre a equivalência com o golden Rego; linha de registo para o `ttl` sem emissor |
| Região do recurso auto-declarada, sem atestação | **rebaixado (§8)** — inalcançável pelo caminho cablado | **Observação documental** — a única regra com região exige `taint != "untrusted"`, e o caminho real fixa sempre `untrusted`; **emenda a CON-04** mantém-se |
| PDP sem tracer, `Reload` sem via, decisão sem métrica | **reformulado (§8)** | «Cego» cai (WORM leva `policy_version` por decisão); ficam 3 lacunas: `WithTracer` por ligar, span sem `policy_version`, `aos audit-trail` omite-o |
| `policy.changed` nunca emitido em produção (AOS-088) | activo | **AOS-310** (EPIC-22) |
| Exemplo normativo C1 contradiz o código (obrigação `ttl` inexistente) | documental | **Defeito** — remediação P0-D por executar, com teste de contrato no gate 7 |
| AOS-090 (`autonomy.NewController`) sem chamador | activo por omissão | **DEF novo** — limite aceite, eixo AOS-090 |
| AOS-094 AC#2: obrigação `region` inerte para tool calls | latente | **DEF novo** — eixo AOS-094 (os outros 4 CA estão cobertos por AOS-058/182/205) |
| `aos-orq serve --plan-doc` materializa o não-validado | latente (binário desligado) | **Defeito** — ticket; `PlanHash` vazio passa a `ErrInvalidRequest` |
| CA2 de AOS-238 marcado `[x]` sobre porta sem implementação | rastreabilidade | **Defeito** — desmarcar o CA e nomear o módulo que falta |
| Lock do dispatcher linear em N (medido no substrato real) | latente | **DEF novo** — eixo AOS-032, com a medição anexada |
| O(N) por admissão, sem compactação nem snapshot | latente | **DEF novo** — eixo AOS-027 |
| Matriz de conformidade subdeclara AOS-220; baseline certifica dívida paga | documental | **Defeito** — emenda a `tecnica/14:111,122` + remoção da entrada de baseline |
| `deferrals.py` descarta IDs sufixados em silêncio | gate | **Defeito** — uma linha na regex, e o mesmo em `RE_NOTE` |
| `orchestrator`/`scheduler`/`pdp` fora de `COVERAGE_GATED_MODULES` | gate | **Defeito** — três linhas em `lib.sh:412` |
| RTM §7: 20/20 contra 19/19 no mesmo ficheiro; §7 sem guarda | documental | **Defeito** — corrigir a §7 e alargar o âmbito do regenerador ou do ref-lint |
| `scheduler/README.md:465` «≥90%» contra 81,8% medido | documental | **Defeito** — uma linha, conhecida desde 2026-08-08 |
| «10 rotas do plano de controlo» em quatro sítios (são 11) | documental | **Defeito** — e substituir a enumeração manual por derivação da tabela |
| Deny por prazo esgotado atribuído ao `audit-sink` | **eliminado (§8)** — eixo novo no lugar | O «timeout fail-closed» de `tecnica/17` §4.3-D é **condicional ao sink**: o `audit.FileStore` de produção não consulta `ctx`. **AOS-311** (EPIC-22) |
| `/autonomy` não sobrevive a reinício: `provision` reaplica `AOS_AUTONOMY_LEVELS` sobre registo novo | **novo (§8)** | **AOS-307** (EPIC-22) |

**Não entram como achado próprio**, por serem a leitura de uma decisão ratificada: a não-composição do ORQ
e do SCH (ADR-018 §4, ADR-023, AOS-281 §Contexto), o stub de `orchestrator.Submit` (**DEF-803**, ABERTO,
eixo AOS-025), o orçamento por árvore sem CAS (**DEF-907**), o `RiskGate` não montado (**DEF-905**), e o
`Meter` sem exportador OTLP (`metrics.go:167`, eixo EPIC-08).

---

## 7. Limites desta auditoria

- **O caminho até ao PDP no nó real é de arreio, não do produto.** Sem gateway de modelo real o nó de
  referência não faz tool calls; o gateway falso e o `tools.json` são do medidor. Os **veredictos de
  política** medidos são genuínos (política 1.0.0 assinada, oráculo real, RM real, WORM real), mas a tool
  de teste não tem executor de sandbox. Está demonstrado que **a decisão de governação que bloqueava
  desapareceu**; não está demonstrado um efeito real a executar sem gate humano.
- **A consequência a jusante da emissão anónima de challenges fica em aberto.** Seis tentativas de
  cerimónia `/approve` devolveram `403` uniforme e sem razão registada; não é possível distinguir a
  rejeição do challenge de uma cerimónia malformada.
- **Um só nó, uma só réplica, mTLS de controlo desligado.** Com CA montada, `/autonomy` e `/challenge`
  ganham uma barreira de **transporte**; nenhuma das duas ganha autenticação de **identidade**. Não foi
  medido.
- **Nenhuma sonda correu dentro do repositório.** Todas viveram em módulos externos com `replace` ou em
  cópias no scratchpad; `git status` foi confirmado limpo por todos os agentes no fim. O que exigisse um
  ficheiro dentro de um pacote (símbolo não-exportado) ficou por correr.
- **Onze dos vinte e um sobreviventes não foram executados.** Estão escritos como afirmações falsificáveis,
  formuladas para cair com um teste e não com uma discussão.
- **A varredura de asserções numéricas é uma amostra enviesada** para o que é barato de contar. A taxa real
  pode diferir nos dois sentidos.
- **O âmbito foi ORQ, SCH e PDP.** O model-gateway, a memória, o registry e o sandbox só foram tocados nas
  fronteiras — e duas dessas fronteiras (a allowlist assinada de AOS-058 e o read-path soberano de
  AOS-182/205) acabaram por ser decisivas para rebaixar um achado ALTO. Uma auditoria do GW com o mesmo
  método é o passo seguinte natural.

---

## 8. Passagem 4 — validação cruzada externa, e o que ela fez ao relatório

Depois da entrega, uma síntese externa (9 refutadores + 10 validadores, com âncoras reabertas) atacou
os nove achados que este relatório classificara como «defeito activo». **Não foi aceite por
autoridade**: cada alegação nova foi reconfrontada com o código antes de entrar aqui. Resultado —
**confirma-se em quase tudo, corrige três coisas deste relatório, e erra em duas sub-alegações que
são inconsistentes entre si.**

### 8.1 O que corrige neste relatório — e foi verificado

**A região auto-declarada (§3.2) não é alcançável pelo caminho cablado.** A única regra Cedar que lê
`resource.region` é `allow_http_post`, e exige **também** `context.taint != "untrusted"`
(`pdp/policies/aos_authz.cedar:24-34`); `allow_fs_read` não lê região nenhuma. No caminho real,
`loop.go:756` fixa `Taint: authorizationTaintOf(inv)`, que devolve o literal `"untrusted"` para toda a
invocação não marcada — e `AuthorizeTrusted`, a única função que a marcaria, **não tem chamador de
produção**. A sonda de N-02 construiu o `Input` à mão, com `Taint` no valor-zero `""` (que é
`!= "untrusted"`), e por isso viu `permit`: mediu um estado que nenhum caminho cablado produz. O
facto estrutural mantém-se (a etiqueta não é atestada e a emenda a CON-04 é devida); a consequência
desce de «activo, medido» para **observação documental**. *Nota de endurecimento:* o PDP em isolamento
trata `taint=""` como não-untrusted (`engine_cedar.go:141`) — inacessível hoje, mas é o valor-zero a
permitir.

**As obrigações em Go são limitação ratificada, não defeito de desenho.** ADR-011:58 descreve
`obligationsFor` com a regra exacta, e a linha 143 da sua tabela de conformidade repete-a. O golden
Rego de `tecnica/12` emite as **mesmas duas** obrigações e nenhuma `ttl`. Fica o destino que o §6 já
propunha — emenda de precisão ao ADR sobre o que «semântica idêntica» cobre — mais uma linha de
registo para o `ttl` do PEP, que não tem emissor em lado nenhum.

**«O PDP é cego» não resiste como frase.** Toda a mediação é selada na hash-chain com
`policy_version` e atribuição, e `tecnica/08:75` contrata o span `execute_tool` a carregar a decisão.
O que sobrevive são três lacunas não-deferidas: `pdp.WithTracer` sem chamador; o span sem
`policy_version`; e `aos audit-trail` (`packages/cmd/aos/audit_trail.go:71-96`) a imprimir
`seq/decision/tool/cap/denied_by/code/reason` — **sem versão de política**. A via decisão↔versão
continua a exigir leitura crua do WORM.

**O «deny por prazo esgotado mal atribuído» cai — e o que aparece no lugar é maior.** A refutação da
passagem 2 mediu a cadeia fail-closed com o sink de **eventstore**, cujo `Append` verifica `ctx.Err()`
(`substrate/eventstore/store.go:312`). O sink de produção é `audit.NewMediationSink(cfg.WORM)` sobre o
`audit.FileStore` (`bootstrap.go:1096`), cujo `Append` (`platform/audit/filestore.go:151-189`) só passa
o `ctx` a `autorizadoAEscrever` — e a posse é `nil` no nó, pelo que **nenhuma linha consulta o contexto**.
Um prazo que expire entre o check de entrada de `Monitor.evaluate` e a selagem produz *permit* e tenta o
efeito sob contexto morto. O «timeout fail-closed» de `tecnica/17` §4.3-D é **condicional ao sink
injectado, e a condição não está declarada**.

### 8.2 O que reforça

- **`/autonomy`:** o corpus condena esta classe por princípio — `specs/EPIC-20:983`: «decisão humana
  MAIS FRACO do que o four-eyes já entregue — regressão de postura» (decisão do dono, 2026-08-12). E o
  PR #71 (`7e073de`), que criou a rota, reutilizou as chaves de `AOS_OPERATORS` via `AutonomyScope` e
  deixou «chave por CLASSE — merece controlos próprios» como fase 3 por escrito. O que existe hoje é a
  fase 1 com a autoridade da fase 3.
- **`policy.changed`:** o precedente AOS-248 fechou exactamente esta classe **para os níveis de
  autonomia** — `provision` sela cada nível do ambiente no WORM ao arrancar e `ErrAutonomyProvisioning`
  (`autonomy_levels.go:154`) recusa arrancar «com uma autonomia que não consegue auditar». O mesmo
  composition-root não faz o mesmo com o bundle de política em `pdp.Open`.
- **`/challenge`:** a emissão anónima é inerte para autoridade (o consumo exige assinatura sobre o
  tuplo canónico com a pubkey pinada, `approve:<classe>`, e uso único durável) — mas a validação
  encontrou o achado que a medição da passagem 3 não conseguiu distinguir: **`FourEyesGate.Authorize`
  (`integration/foureyes.go:338-620`) não sela nem regista nenhuma negação**, e o handler
  (`api.go:1868-1874`) só chama `sealControlAction` no sucesso, com um comentário a afirmar que «o
  audit tem o erro dedicado». Foi isto que impediu a própria auditoria de distinguir rejeição de
  cerimónia malformada.
- **DEF-2 (400 que mente):** confirmado a 100% dos casos alcançáveis. O handler pré-valida
  `agent`/`domain`/`reason` não-vazios, o nível e o emissor **antes** de `SetLevel`
  (`autonomy_route.go:78-116`), pelo que `ErrInvalidLevel`/`ErrEmptyPair`/`ErrMissingReason`/
  `ErrMissingActor` são inalcançáveis; o único erro que chega ao `400 "nivel recusado"` é o do sink.

### 8.3 Onde a síntese externa erra — e as duas erram em direcções que se anulam

**«A janela de DEF-2 abre-se sem indisponibilidade do WORM — basta o contexto do pedido HTTP morrer
durante o Append.»** Falso para o caminho composto. O sink de autonomia liga-se ao **mesmo**
`audit.FileStore` (`autonomy_levels.go:182` → `autonomy.NewAuditSink(worm)` → `store.Append`) que
o §8.1 acabou de mostrar **não consultar o contexto**. A morte do `ctx` não falha a selagem; a janela
exige uma falha real de `persist` (fsync/disco) — exactamente como este relatório a descrevera. A
síntese usou, na mesma entrega, «o FileStore ignora `ctx`» para agravar DEF-9 e «o `ctx` morto falha o
Append» para agravar DEF-2. **Um validador de consistência cruzada tinha de apanhar isto e não apanhou.**

**«O registo é memória-only sem replay, pelo que o nível aplicado-sem-selo permanece em vigor ao
restart.»** A premissa está certa e a conclusão invertida. `LevelRegistry` não tem `Rebuild` (métodos:
`LevelFor`, `Get`, `SetLevel`, `History`, `HistoryFor`, `LevelForAgentOrClass`) — mas `provision`
(`autonomy_levels.go:270-279`) constrói um registo **novo** e reaplica `AOS_AUTONOMY_LEVELS` em cada
arranque. Um nível posto por `/autonomy` **não sobrevive a um reinício**, selado ou não: reverte para o
ficheiro. O que isto expõe é um achado que nenhuma das duas análises tinha: **o WORM regista L5, o nó
reiniciado serve o nível do ambiente, e trilho e efeito divergem** sem nenhum evento a dizê-lo.

### 8.4 O que fica

Dos nove «defeitos activos» originais: **três mantêm-se** (`/autonomy` sem autoridade proporcional;
`/autonomy` aplica sem selo e mente; `policy.changed` nunca emitido), **quatro reclassificam-se**
(`/challenge`, obrigações, região, AOS-090) para limites aceites com resíduos ticketáveis, **um
reformula-se** (PDP «cego») e **um é eliminado** (atribuição do timeout) — com **três achados novos** a
entrar no lugar: o `FileStore` sem `ctx`, o `/approve` a negar sem selo, e o `/autonomy` não-durável.

A lição de método repete-se pela terceira vez nesta auditoria: **a refutação também acusa**. E acrescenta
uma: **duas refutações independentes podem estar cada uma certa no seu facto e erradas em conjunto** —
a consistência cruzada não é uma passagem opcional, é a que apanha o erro que nenhum refutador
individual consegue ver.
