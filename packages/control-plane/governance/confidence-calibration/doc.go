// Package confcalib é a SUPERFÍCIE DE CALIBRAÇÃO DE CONFIANÇA (AOS-124, EPIC-12, UX/DX).
// O over-trust é tão perigoso como o under-trust: quem confia cegamente aprova
// alucinações; quem desconfia de tudo anula o valor do agente. Esta superfície faz
// calibração ACTIVA de duas formas, CONSUMINDO os sinais que JÁ existem:
//
//	(1) LINGUAGEM DE INCERTEZA SELECTIVA (AC1/AC3) — sinaliza SÓ quando há sinal de baixa
//	    confiança/ambiguidade, nunca um disclaimer genérico em toda a resposta (que geraria
//	    ruído e fadiga). O sinal vem do otelgenai.EvaluationResult de EPIC-08 (AOS-084): um
//	    Score abaixo do limiar OU um Verdict != EvalPass. Ver uncertainty.go.
//	(2) HISTÓRICO DE CORRECÇÕES (AC2) — quantas/que tipo de correcções via steer (AOS-119)
//	    ocorreram em CONTEXTOS SEMELHANTES (agente/capability/domínio). Ver history.go.
//
// # COMPÕE e CONSOME — não recalcula, não inventa, não expõe PII
//
//   - CONSOME o sinal (AC3): a incerteza deriva do Score/Verdict do EvaluationResult
//     DADO. A superfície NÃO recalcula confiança, NÃO reavalia a trajectória, NÃO importa o
//     replay/eval-engine. Calibrate é função PURA do resultado de eval dado.
//   - SEM PII (AC5): o texto de cada correcção passa pelo redaction.Engine (AOS-091,
//     RemoveAllPolicy) — o histórico apresentado passa sempre Engine.ScanText == []. Os
//     spans nunca contêm o texto da correcção (só a contagem/tipo agregados).
//   - INFORMA, não decide (AC4): a Calibration ANEXA-SE ao approval-card/superfície de
//     aprovação (AOS-120/122) para informar a decisão humana — é um artefacto passivo, não
//     substitui nem toma a decisão.
//
// # Padrão porta+adaptador (core mínimo)
//
// O core importa SÓ o LEVE: otel-genai (EvaluationResult — o sinal; Tracer/AttrRunID) e
// redaction (Engine — sem PII). A FONTE das correcções fica como PORTA (CorrectionSource):
// o adaptador que lê os eventos control.steer (AOS-119) do Event Store vive no WIRING, para
// o core não arrastar o agent-runtime/control. Build offline; integração por path local
// (replace). ZERO dependências externas (molde: progress-surface/AOS-123).
//
// # Observabilidade (AC5)
//
// Emite um span de interacção (aos.calibration.interaction) ligado ao run por AttrRunID,
// com aos.calibration.{uncertainty_shown, uncertainty_reason, correction_count, context} —
// SEM segredos/PII. Ver span.go.
//
// Referências: EPIC-08 (evals/gen_ai.evaluation.result, Score/Verdict — o sinal consumido);
// AOS-119 (correcções via steer — a fonte do histórico); ADR-013 (canal de controlo
// out-of-band, não-repúdio das correcções).
package confcalib
