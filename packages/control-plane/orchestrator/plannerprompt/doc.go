// Package plannerprompt versiona o PROMPT DE DECOMPOSIÇÃO (o artefacto
// COMPORTAMENTAL do planeador) e monta o EVAL-GATE de golden-sets (AOS-241,
// tecnica/18 §3.6). O prompt é o input que faz o LLM produzir um
// [plan.PlanDocument]; a sua evolução é uma mudança de comportamento e por isso é
// GOVERNADA — SemVer próprio (`prompt_version`, ADR-012) e cache-estável (ADR-009).
//
// Fronteiras (o que este pacote NÃO faz — DELIBERADO):
//
//   - NÃO chama nenhum LLM vivo. O eval corre OFFLINE e DETERMINÍSTICO: as K
//     amostras por objectivo são FIXTURES ([plan.PlanDocument] pré-gerados em
//     testdata), nunca uma amostragem em run-time. Este pacote é uma biblioteca
//     pura que um job de CI de STAGING invoca — o handoff para esse CI real é a
//     FRONTEIRA (ver [Report] / [Regression]); não se liga a nenhum gate de
//     produção nem ao caminho por-run (AOS-231/232/236 continuam intocados).
//   - NÃO reimplementa a validação estrutural: as asserções ESTRUTURAIS delegam no
//     validador puro de AOS-231 ([planvalidate.Validate], regras 1–4). As
//     SEMÂNTICAS são uma rubrica de predicados declarados ([Rubric]).
//   - NÃO declara tipos de evento: reutiliza o domínio de `plannerevents` por via
//     do [planvalidate.Verdict] (que carrega [plannerevents.Rule]).
//
// Disciplina de segurança do gate (§3.6, DoD):
//
//   - Amostragem K× por objectivo. As asserções de SEGURANÇA têm de passar a 100%
//     de K (fail-closed: uma única amostra insegura BLOQUEIA — nunca um limiar). As
//     de QUALIDADE passam por limiar >= M/K (política [Policy]).
//   - Trace-diffing = REGRESSÃO DISTRIBUCIONAL sobre pass-rate agregado por
//     categoria ([Regression]) — NÃO igualdade de plano cru. SEM regressão de
//     segurança admitida (a segurança tem de continuar a 100%).
//   - Mutar o prompt é GATED por ADR-012 ([ValidatePromptMutation]); mutar o
//     golden-set é GATED contra ENVENENAMENTO ([ValidateGoldenMutation]): cegar um
//     caso DIFÍCIL — quer removendo-o quer esvaziando as suas asserções (mudança da
//     sua assinatura de invariantes) — exige aprovação explícita. O que a assinatura
//     NÃO cobre é o corpo de um predicado semântico sob o mesmo id/severidade (ver
//     [Case.assertionSignature]).
//
// O sinal de pass-rate ([Report.PassRate]) é o que a promoção AOS-242 consome.
package plannerprompt
