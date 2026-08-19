# Plano — tornar a autonomia operável  ✅ FEITO (ver «Estado em 2026-08-19»)

Hoje o oráculo de autonomia (AOS-087) é o **único** mecanismo de governação deste sistema que se
muda editando um ficheiro no servidor: `AOS_AUTONOMY_LEVELS` no `.env`, seguido de recriar o nó.

O `/approve`, o `/pause`, o `/steer` e o `/promote` são todos assinados, com *nonce* de uso único
e selados no WORM com quem os emitiu. O mecanismo que decide **quanta supervisão humana se
aplica** não tem nada disso.

Faltar-lhe a rota é ao mesmo tempo o que o torna difícil de ligar e o que o torna difícil de
auditar. É o mesmo defeito visto de dois lados.


---

## Estado em 2026-08-19 — as quatro fases estão FEITAS

Este documento nasceu como plano e passa a ser **registo**. O que cada fase entregou, e o
artefacto onde vive:

| Fase | Entregue | Onde |
|---|---|---|
| 1 — a rota | `POST /autonomy`, `GET /autonomy` | `packages/cmd/aos/autonomy_route.go` |
| 2 — chave por CLASSE | `ClassPrefix`, `LevelForAgentOrClass` (instância → classe → piso) | `packages/control-plane/governance/autonomy/registry.go` |
| 3 — piso DECLARADO | `WithDefaultLevel`, `AOS_AUTONOMY_DEFAULT` | idem, + `autonomy_levels.go` |
| 4 — ensaio | `POST /autonomy/simular` | `packages/cmd/aos/autonomy_simular.go` |

As três rotas são **plano de CONTROLO** (`planoControlo` na tabela de `packages/cmd/aos/planos.go`):
admissão + mTLS de cliente aplicados no registo, assinatura ed25519 do corpo a decidir depois.
Nasceram **sem** essas barreiras, com o comentário da própria rota a afirmar o contrário — ver a
secção dos planos em `deploy/node/README.md`.

### O que cada fase PROVOU, e com que controlo

| Fase | Controlo exigido | Onde está exercido |
|---|---|---|
| 1 | emissor **não registado** recusado | `TestEmissorAssinadoEAceiteEOsOutrosNao` (caso 3) |
| 1 | o mesmo *nonce* duas vezes recusado | idem (caso 2) |
| 1 | assinatura amarrada ao **nível** e ao **motivo** | idem (caso 4) + `TestPayloadDeAutonomiaAmarraNivelEMotivo` |
| 1 | o selo **nomeia** quem mudou e o motivo | `TestSeloNomeiaQuemAssinouENaoOQueOCorpoDiz` |
| 1 | `GET` reflecte o `POST` | `TestGetReflecteOPost` |
| 2 | instância **ganha** à classe; classe aplica-se a agente nunca visto; sem nenhuma, cai no piso | `TestCascataInstanciaGanhaAClasse`, `TestClasseVaziaNaoHerdaNada` |
| 3 | piso L1 ⇒ par desconhecido dá **L1**; sem piso ⇒ **L0** | `TestPisoDeclaradoAplicaSeAPariAusentes`, `TestPisoInvalidoNaoBaixaAGuarda` |
| 4 | a simulação e a execução real **concordam** | `TestSimulacaoConcordaComOClassificadorReal` |

> Os dois controlos da Fase 1 sobre o **selo** e sobre o **`GET`** foram acrescentados em
> 2026-08-19, ao conferir esta tabela em vez de a recordar: os outros três estavam exercidos e
> estes dois não, apesar de o plano os ter exigido desde o início. Ambos foram verificados por
> **mutação** — selar um actor do corpo, e devolver `200` sem aplicar — e ambos os testes caem.

### O que falta, e não é código

O oráculo continua **desligado em produção**, por decisão do operador. Ligá-lo é escrever
`AOS_AUTONOMY_LEVELS` (e, se quiser, `AOS_AUTONOMY_DEFAULT`) no `.env` do servidor e recriar o nó
— a partir daí as mudanças passam pela rota, assinadas e seladas, sem editar ficheiros.

Ligar exige provar duas coisas no nó real: um run do agente **registado** completa sem escalar, e
um run com um id **novo** suspende. Sem esse par, o banner afirmaria uma postura que ninguém
observou.

---
## O que já existe, e é a razão de isto ser pequeno

| Peça | Estado |
|---|---|
| `LevelRegistry.SetLevel(ctx, agent, domain, level, reason, actor)` | **existe**, e já sela `autonomy.level_changed` na hash-chain |
| `autonomyWiring.registry` exposto no nó (`Bootstrap.Autonomy`) | **existe** |
| `steerAuth` — Ed25519 + *nonce store* durável, TTL de frescura | **existe**, é o que autentica `/pause` e `/steer` |
| `emitterWire` (id, assinatura, nonce, issued_at) | **existe** |
| `sealControlAction` — partição `governance.control` | **existe** (A3) |

Nada disto precisa de ser construído. **Falta a rota que os liga.**

---

## Fase 1 — `POST /autonomy` (a rota)

O que muda: ligar, desligar ou ajustar um nível passa a ser uma operação de governação —
autenticada, selada, sem *downtime*, reversível pelo mesmo caminho.

**Contrato**

```json
POST /autonomy
{ "emitter": {"id":"...","signature":"...","nonce":"...","issued_at":"..."},
  "agent": "agt-rotina-01", "domain": "fs", "level": "L4",
  "reason": "leitura de rotina corre sozinha; egress continua a escalar" }
```

- **`reason` é obrigatório.** O `SetLevel` já o aceita e sela. Uma mudança de nível sem motivo
  escrito é uma decisão de governação sem justificação no registo, e é exactamente o que a
  auditoria de um sistema destes tem de conseguir responder.
- **`actor`** vem do emissor **verificado**, nunca do corpo — a mesma disciplina de todo o resto:
  quem assina é quem fica no selo.
- Mesma admissão e mesmo `mTLS` de controlo que `/approve` e `/pause`.
- **Fail-closed:** nível fora de `L0–L5` ⇒ `400`; emissor não verificado ⇒ `403`; registo de
  níveis não composto ⇒ `501` (e não um `200` que não fez nada).

**`GET /autonomy`** devolve o estado **em vigor**. Sem ele, a rota é escrever sem poder ler — e o
banner de arranque deixa de ser verdade no instante em que alguém muda um nível.

> ⚠️ **Consequência que a Fase 1 obriga a corrigir:** o banner afirma a postura de autonomia
> **do arranque**. Com mudanças em runtime, essa linha passa a poder mentir. Tem de passar a dizer
> que é o estado *no arranque*, e apontar o `GET /autonomy` como fonte de verdade.

---

## Fase 2 — a chave certa (por CLASSE, não por instância)

O registo é chaveado por `(agente, domínio)`, com o agente na **instância** (`agt-rotina-01`).
Mas os `agent_id` deste sistema são cunhados por run: o WORM de produção tem 13, todos *ad hoc*,
nenhum repetido. Registar instâncias é registar coisas que ainda não existem.

A unidade estável é a **classe** — `agent-worker`, `researcher` — que o NHI já carrega e que a
política de classe (`ClassPolicy`) já usa para limitar o escopo.

**Resolução em cascata, do mais específico para o mais geral:**

```
(agente, domínio)  →  (classe, domínio)  →  piso declarado  →  L0
```

Assim uma instância continua a poder ser tratada à parte — quando há razão para isso — sem obrigar
a enumerar o que é irrepetível.

---

## Fase 3 — piso DECLARADO em vez de herdado

Hoje um par não registado cai em L0 **em silêncio**. É fail-closed, e é correcto; mas é uma
decisão de governação que ninguém tomou explicitamente, e é a razão pela qual "ligar a autonomia"
significa hoje "todo o agente novo bloqueia".

`AOS_AUTONOMY_DEFAULT` (ou o mesmo campo via `POST /autonomy` sem `agent`) torna o piso uma
**declaração**. Continua fail-closed no valor por omissão: ausente ⇒ L0, como hoje.

A diferença não é de comportamento — é de quem responde pela decisão.

---

## Fase 4 — ensaio antes de virar o interruptor

`POST /autonomy/simular` (ou `GET` com parâmetros): dada uma configuração hipotética, responde
**o que teriam feito os últimos N runs** — quantos correriam, quantos escalariam, e quais.

É o que falta para a mudança deixar de ser um salto. Hoje liga-se e descobre-se; e descobrir-se em
produção que a L4 escala tudo é o cenário que passámos o dia a evitar.

Sem estado novo: os selos das tool calls no WORM já têm capability, recurso, taint e — desde a
v0.1.4 — a classe de risco. A simulação é uma releitura desses registos contra a tabela proposta.

---

## Ordem, e porquê esta

1. **Fase 1** primeiro porque é a que remove o passo manual em produção — e é a que fecha o buraco
   de auditoria. As outras são conforto; esta é postura.
2. **Fase 3** a seguir, porque é pequena e é o que torna a Fase 1 utilizável sem a Fase 2.
3. **Fase 2** depois, porque muda a semântica de resolução e merece o seu próprio conjunto de
   controlos.
4. **Fase 4** por último, porque é a única que não desbloqueia nada — só torna a decisão menos
   assustadora.

## O que cada fase tem de provar, e não só fazer

| Fase | O controlo que a torna verificada |
|---|---|
| 1 | um emissor **não registado** é recusado; o mesmo *nonce* duas vezes é recusado; o selo **nomeia** quem mudou e o motivo; `GET` reflecte o `POST` |
| 2 | a instância **ganha** à classe quando ambas existem; a classe aplica-se a um agente **nunca visto**; sem nenhuma das duas, cai no piso |
| 3 | com piso declarado a L1, um par desconhecido dá **L1** e não L0; sem piso declarado, continua **L0** |
| 4 | a simulação e a execução real **concordam** para a mesma configuração — senão a simulação é um oráculo que mente |

O último é o mais importante de todos: uma simulação que diverge do comportamento real é pior do
que não ter simulação nenhuma, porque produz confiança em vez de a medir.
