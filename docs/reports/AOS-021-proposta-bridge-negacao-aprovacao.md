# Proposta de desenho — bridge negação → aprovação humana → reexecução

> **Estado:** PROPOSTA (nenhum código escrito) · **Data:** 2026-08-07 · **Origem:** gap 3 do relatório de prontidão agêntica, após revisão adversarial (o único sub-item integralmente por fazer).
> **Decisão pedida ao dono:** aprovar a FORMA (§4) antes de qualquer implementação.

## 1. O problema

Hoje, uma tool call originada pelo modelo que peça uma capability privilegiada (ex.: `cap:http.post`) é **negada pelo taint-gate** — «untrusted não comanda» (P4/AOS-069). Isso está **correcto** e não é um defeito.

O que falta é a **via legítima de destravar**: um humano responsável assumir a acção. Hoje não existe:

- `FourEyesGate` é um endpoint `/approve` **autónomo** para acções irreversíveis; **não é um hook** da cadeia de mediação (`secured.go` compõe identity → revalidation → policy → taint → scope → budget → egress; four-eyes não está lá).
- Nenhum código apanha `Decision.DeniedBy` e encaminha para aprovação.
- Consequência: um `deny|policy` termina o efeito **sem via de escalada**. O agente não tem como pedir ajuda a um humano.

## 2. A armadilha que este desenho tem de evitar

A solução ingénua é: aprovado ⇒ `AuthorizationTaint = "trusted"` ⇒ o taint-gate deixa passar.

**Isto seria a pior mudança de segurança do sistema.** O próprio código o diz (`agent-runtime/model.go:52-64`):

> `AuthorizationTaint` é **CONVENÇÃO — não estrutura** (ao contrário do `referencemonitor.Permit`, mintado e infalsificável). INVARIANTE INEGOCIÁVEL: nenhum adaptador pode preenchê-lo a partir de dados do modelo. O único mecanismo que impede a escalada quando esta convenção é violada é o fail-closed de `authorizationTaintOf`.

Ou seja: a marca `trusted` é **uma string**. Fazer a aprovação humana escrever essa string transformaria o mecanismo mais fraco do sistema no **guardião da barreira mais importante**. Qualquer bug, adaptador descuidado ou componente comprometido que ponha `"trusted"` numa struct passa a ter bypass total do taint-gate — e o log mostraria uma call perfeitamente legítima.

**Rejeito essa via.** O resto desta proposta existe para obter o mesmo resultado funcional sem tocar na convenção de taint.

## 3. O que JÁ existe e deve ser reutilizado

Levantamento (nada disto é para reescrever):

| Peça | Onde | O que dá |
|---|---|---|
| `EffectEscalate` / `HookEscalate` | `reference-monitor/decision.go:17`, `hooks.go:14` | O RM **já modela** «requer gate humano; nenhum efeito ocorre até aprovação» e sela-o no audit (`tool.call.escalated`) |
| `FourEyesGate.Authorize` | `integration/foureyes.go:338` | Dual-control, distinção estrutural por 3 eixos (principal/sessão/credencial), challenges anti-replay, attestation WebAuthn, `approve:<classe>` por perna |
| `FourEyesRequest.Preview` | `foureyes.go:149` | **WYSIWYS**: digest canónico do efeito EXIBIDO, coberto pela assinatura de cada perna — uma perna que assinou outro preview **não valida** |
| `RiskClass` no tuplo assinado | `foureyes.go:157` | Não rebaixável por relay; valor-zero = `ClassDanger` (fail-closed) |
| `risk.Classifier` / `RiskGate` | `reference-monitor/risk/` | Classifica a acção (safe/gray/danger) — quem decide *se* precisa de humano |
| `state.WaitingOnHuman` + `Breaker.EscalateToHuman` | `agent-runtime/state`, `breaker.go:281` | Estado durável de espera por humano, **já implementado** |
| Feedback de negação no tail | `prompt.go` (commit `60f5d64`) | O modelo já **vê** que foi negado e sob que código |

**A `Preview` é a peça-chave.** É exactamente o mecanismo de binding de que preciso: se o Preview for o digest canónico *daquela* tool call, a aprovação fica criptograficamente ligada a ela e não pode ser reencaminhada para outra.

## 4. Desenho proposto

### 4.1 Princípio

> A aprovação humana **não altera o taint**. O taint continua `untrusted` para sempre — porque a call **foi mesmo** originada pelo modelo, e o registo tem de continuar a dizer isso.
>
> O que muda é que passa a existir uma **PROVA VERIFICADA, ligada a esta call concreta**, de que um humano com autoridade assumiu a acção. O gate que negava consulta essa prova.

Isto mantém P4 exactamente tão forte como hoje e torna a excepção **explícita e auditável** em vez de lavada através de uma string.

### 4.2 Peças

**(a) `ApprovalGate` — novo hook, DENTRO do pacote `referencemonitor`**

Vive no kernel (como `TaintGate`/`ScopeGate`) por uma razão estrutural: só código do próprio pacote pode escrever um **campo não-exportado** em `Call`. É o mesmo truque que torna `Decision.permit` infalsificável.

```
referencemonitor.NewApprovalGate(v ApprovalVerifier) ApprovalGate
```

Comportamento:
1. Lê `Call.ApprovalEvidence` — bytes **opacos e untrusted** fornecidos pelo chamador.
2. Entrega-os ao `ApprovalVerifier` (porta; o adaptador real vive em `integration`, sobre o `FourEyesGate`).
3. O verificador confirma: assinaturas das pernas, dual-control quando exigido, distinção dos 3 eixos, challenge não-reutilizado, attestation de dispositivo, `approve:<classe>` de cada aprovador — **e** que a `Preview` assinada é igual ao digest canónico **desta** call.
4. Só em sucesso o gate escreve `call.humanApproved = true` (**não-exportado**) + a identidade dos aprovadores para o audit.
5. **Descarta** qualquer coisa que o chamador tenha alegado — exactamente como o `IdentityCheck` substitui `call.Principal` após `Verify`.

Sem evidência ⇒ no-op silencioso (não nega; é a cadeia normal que decide).

**(b) `TaintGate` ganha UMA excepção explícita**

A regra passa de:
> autorização untrusted não pode originar capability privilegiada

para:
> autorização untrusted não pode originar capability privilegiada **salvo se existir aprovação humana VERIFICADA ligada a esta call** (`call.humanApproved`, escrito só pelo `ApprovalGate` do próprio pacote)

O campo é não-exportado ⇒ **nenhum pacote externo o consegue forjar**. A excepção fica num único sítio, legível e testável.

**(c) Binding: a `Preview` é o digest canónico da call**

```
Preview = sha256(canonical(RunID, StepID, ToolID, Capability, Resource{Type,Value,Region}, sha256(Input), Principal.NHIID, RiskClass))
```

Propriedades: um humano aprova **aquela** acção com **aqueles** argumentos; mudar um byte do Input invalida a aprovação; a aprovação não é reencaminhável para outro run/step/tool; e é o que foi **exibido** ao humano (WYSIWYS já garantido pelo FourEyes).

**(d) Fluxo end-to-end**

```
1. Modelo emite tool call privilegiada
2. RM: RiskGate classifica (gray/danger) → devolve ESCALATE (não Deny)
      └─ audit sela tool.call.escalated; NENHUM efeito ocorreu
3. Loop: escalate na fronteira de fim-de-turno → run entra em waiting_on_human
      └─ reutiliza o estado durável e o Breaker.EscalateToHuman JÁ existentes
      └─ o tail regista o facto (o marcador de negação do commit 60f5d64)
4. Operador vê o pendente (preview + classe de risco) e aprova:
      POST /runs/{id}/approve  com as pernas assinadas sobre a Preview
      └─ 1 perna se reversível; 2 estruturalmente distintas se irreversível
5. Aprovação persistida no WORM, ligada ao fingerprint da call
6. Resume do run: a MESMA call volta a atravessar o RM COMPLETO
      └─ ApprovalGate verifica e marca; TaintGate consulta e deixa passar;
         PDP, scope, egress continuam a decidir normalmente
7. Efeito executa sob permit, com audit-before-effect e os aprovadores no registo
```

**O passo 6 é o coração**: a reexecução **não** é um atalho. A call volta a atravessar a cadeia **inteira**. A aprovação humana remove *um* obstáculo (o taint), não todos — se entretanto o token expirou, o escopo mudou, a política mudou ou o egress não permite, continua a ser negada.

## 5. Propriedades de segurança

| Propriedade | Como se mantém |
|---|---|
| P4 «untrusted não comanda» | O taint **nunca** muda. A excepção é uma prova verificada, não uma reetiquetagem |
| Não-forja | Campo não-exportado, escrito só pelo gate do próprio pacote, após verificação criptográfica |
| Sem replay / relay | `Preview` liga à call exacta; challenge de uso único; `RiskClass` e `dual_required` no tuplo assinado (fecham o downgrade) |
| Dual-control real | Reutiliza a distinção por 3 eixos já implementada (principal/sessão/credencial) |
| Sem bypass da cadeia | A reexecução atravessa **todos** os hooks; a aprovação não é um permit |
| Atribuição | Os aprovadores entram no registo de mediação: fica gravado *quem* assumiu |
| Fail-closed | Sem evidência, evidência inválida, preview divergente ou classe não computada (⇒ danger) ⇒ nada passa |

## 6. O que NÃO proponho (e porquê)

- **Flip do `AuthorizationTaint`** — §2. Transformaria uma convenção-string no guardião da barreira principal.
- **Four-eyes como hook que PERMITE** — a aprovação não deve emitir um permit; deve remover **um** obstáculo e deixar a cadeia decidir o resto.
- **Aprovação por-capability ou por-sessão** («o humano aprova `http.post` para este run») — seria uma procuração aberta. A aprovação é **por-call**, com preview.
- **Auto-reexecução sem humano no resume** — o resume é conduzido por quem opera, não pelo agente.

## 7. Decisões que precisam do dono

1. **Âmbito de arranque:** implementar só o caminho `escalate` (risco gray/danger via `RiskGate`), ou também permitir escalada de um `deny|policy` do Cedar? *(Recomendo: só `escalate` primeiro — é o caminho que o RM já modela.)*
2. **Expiração da aprovação:** TTL de uma aprovação pendente (ex.: 15 min) e o que fazer no fim — descartar e prosseguir negado, ou abortar o run?
3. **Quem vê o pendente:** basta o endpoint (polling do operador), ou é preciso notificação activa? *(A superfície `/approve` já existe.)*
4. **Reversível vs irreversível:** confirmar que `DualControlRequired` deriva de `risk.Class == danger`, ou o dono quer um mapa próprio?
5. **Modo dev:** com `AOS_APPROVERS_FILE` ausente, `/approve` responde 501. O escalate deve então degradar para deny (comportamento actual) — confirmar.

## 8. Esforço estimado

| Peça | Tamanho |
|---|---|
| `ApprovalGate` + campo não-exportado + excepção no TaintGate (kernel) | pequeno |
| Porta `ApprovalVerifier` + adaptador sobre `FourEyesGate` (integration) | pequeno |
| Digest canónico da Preview + persistência da aprovação no WORM | médio |
| Escalate → `waiting_on_human` no loop + resume (reutiliza o que existe) | médio |
| Wiring no nó + superfície `/approve` + env documentada (gate AOS-203) | médio |
| Testes: binding, replay, downgrade dual→single, preview divergente, reexecução atravessa a cadeia | **o maior item** |

Nada disto é grande **isoladamente**; o risco está na composição, e é por isso que peço a validação da forma antes de escrever código.
