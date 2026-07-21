package authoringsurface

import "errors"

// ErrNilDryRunner — construção de um [AuthoringLoop] sem [DryRunner]. O dry-run é o
// cerne do loop (AC1): sem a porta de execução simulada não há nada a correr isolado.
var ErrNilDryRunner = errors.New("authoringsurface: loop exige um DryRunner")

// ErrEffectCommitted — o [DryRunResult] devolvido pelo [DryRunner] reporta
// Committed=true: um efeito foi (ou seria) COMETIDO no mundo. É a violação capital do
// dry-run. A superfície REJEITA-o fail-closed (nunca apresenta um resultado que
// cometeu efeitos) — o dry-run tem de ser, por invariante, "nada cometido" (AC1/AC5).
var ErrEffectCommitted = errors.New("authoringsurface: dry-run com efeito cometido (Committed=true) rejeitado — nada pode ser cometido")

// ErrNoAttributionReader — [AuthoringLoop.Attribution] sem [AttributionReader]
// configurado. A atribuição (autor/versão/proveniência) tem de ser visível em todo o
// loop (AC2); sem a porta de leitura não há de onde a ler. Fail-closed.
var ErrNoAttributionReader = errors.New("authoringsurface: atribuicao sem AttributionReader configurado")

// ErrNoEvalReader — [AuthoringLoop.EvalOutcome] sem [EvalResultReader] configurado. O
// resultado do eval-gate/canary tem de ser apresentado ANTES da decisão (AC4); sem a
// porta de leitura não há de onde o ler. Fail-closed.
var ErrNoEvalReader = errors.New("authoringsurface: eval sem EvalResultReader configurado")

// ErrNoSubmitter — [AuthoringLoop.SubmitForRatification] sem [RatificationSubmitter]
// configurado. O encaminhamento ao gate de ratificação (AC3) DELEGA à porta de
// submissão; sem ela não há a quem submeter. Fail-closed, nunca uma promoção local.
var ErrNoSubmitter = errors.New("authoringsurface: submissao sem RatificationSubmitter (a superficie nao ratifica)")

// ErrInvalidCandidate — a [CandidateRef] está incompleta (sem nome de skill ou sem
// versão SemVer). Uma candidata não-identificável não é apresentável nem submissível.
var ErrInvalidCandidate = errors.New("authoringsurface: candidata invalida (exige skill + versao SemVer)")

// ErrNoEvalResult — o [EvalResultReader] não tem resultado de eval para a candidata
// (present=false). O eval-gate/canary ainda não correu ou não é conhecido; não há
// veredicto a apresentar antes da decisão. Fail-closed.
var ErrNoEvalResult = errors.New("authoringsurface: sem resultado de eval para a candidata")
