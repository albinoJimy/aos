// Package agentruntime implementa o loop base do Agent Runtime (RT) do AOS —
// o "batimento cardíaco" descrito em tecnica/02 §3: cada iteração (turno) percorre
// quatro fases — montar (prompt cache-estável) → chamar (Model Gateway) →
// despachar (cada tool call via o Reference Monitor) → verificar — e grava o turno
// como evento no Event Store com o manifesto por trajectória.
//
// # Escopo (AOS-013)
//
// Este pacote entrega APENAS o esqueleto do loop:
//   - [PromptAssembler] cache-estável (ADR-009): prefixo IMUTÁVEL (system + tool
//     set congelado no run) + tail append-only; o prefixo é byte-idêntico entre
//     turnos com o mesmo tool set; o prompt materializado é hasheado por turno.
//   - [ModelClient] — a porta para o Model Gateway (o GW real é EPIC-06).
//   - [Runtime.Run] — o loop montar→chamar→despachar→verificar.
//   - [TurnRecorder] — grava cada turno ("turn.recorded") no Event Store com o
//     manifesto por trajectória (prompt_hash, model{id,params,seed},
//     assembly_version, tools/skills pinadas) (ADR-010).
//   - [Tracer] — a porta de observabilidade leve que emite spans OTel GenAI
//     (invoke_agent/chat/execute_tool) com os atributos da semconv (o SDK OTel
//     real é EPIC-08).
//   - taint (ADR-005): o resultado de cada tool é devolvido ao loop marcado
//     untrusted ([Tainted]).
//
// # Garantia estrutural de no-bypass (ADR-002)
//
// O Runtime detém um *[referencemonitor.Monitor], NUNCA uma
// referencemonitor.ToolFunc. Não há qualquer via na API pública que execute uma
// tool sem passar por [referencemonitor.Monitor.Mediate]: o dispatcher que
// executa tools é não-exportado no RM e o permit é não-forjável. O RT limita-se
// a construir o [referencemonitor.Call] e a invocar Mediate. Uma prova estrutural
// (archlint + reflexão) vive nos testes.
//
// # Fidelidade de replay: o que o log de turno garante (AOS-013 vs AOS-016)
//
// O evento "turn.recorded" grava o manifesto por trajectória (incl. prompt_hash =
// sha256 do prompt materializado COMPLETO do turno). Isto é suficiente para
// DETECTAR divergência num replay — se a re-materialização não bater certo com o
// prompt_hash gravado, o replay divergiu — mas NÃO é, por si só, suficiente para
// RECONSTRUIR o prompt de um turno a partir do zero: o tail de cada turno é semeado
// com o texto do modelo e com os outputs untrusted (não-determinísticos) das tools
// do turno anterior (ex.: web_search, fs.read, relógio), e esses bytes só existem
// em memória — não são journaled aqui. Consequência de contrato: o replay FIEL
// exige RE-EXECUÇÃO DETERMINISTA das tools (mesmas entradas ⇒ mesmas saídas) OU o
// journaling durável desses outputs. Esse journaling (blob referenciado por hash no
// envelope, para reconstrução independente da re-execução) é âmbito de AOS-016
// (replay) e é dívida técnica explícita deste ticket — o loop base de AOS-013 está
// correcto; o gap é puramente de registo, não de correcção.
//
// # Manifesto inline vs. manifesto congelado por referência (AOS-013-F1)
//
// tecnica/13 descreve um manifesto de dependências CONGELADO no início do run
// (frozen_at_seq:1) e referenciado em cada evento por um campo de envelope
// dependency_manifest_ref. O envelope do Event Store entregue por AOS-002 NÃO expõe
// esse campo; alterá-lo sairia do âmbito de AOS-013 (o RT não altera módulos
// existentes). Por isso o RT opta, deliberadamente, por um manifesto INLINE e
// auto-contido em cada "turn.recorded": cada evento carrega o manifesto completo do
// run (que é invariante entre turnos, à parte o prompt_hash por turno). É mais
// verboso mas auto-suficiente para auditoria por turno. A indirecção canónica
// (evento único de manifesto no seq 1 + dependency_manifest_ref por turno) fica
// registada como dívida técnica ligada a AOS-016, a introduzir quando o envelope a
// suportar.
//
// # Fora de âmbito (apenas hooks expostos)
//
// Idempotência de passo (AOS-014), checkpoint intra-iteração (AOS-015), máquina
// de estados durável (AOS-017) e activities (AOS-021) NÃO são implementados aqui.
// Os pontos de ligação estão expostos como interfaces opcionais com default
// no-op: [StepIdentity] (AOS-014) e [Checkpointer] (AOS-015).
package agentruntime
