package authoringsurface

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// As PORTAS do loop de autoria de skills (AOS-126). Fixam a fronteira entre esta
// camada de APRESENTAÇÃO e os subsistemas que já IMPÕEM a auto-modificação segura
// (sandbox/EPIC-07, registry/EPIC-05, eval-gate/EPIC-09, ratificação/AOS-096). A
// superfície COMPÕE estas portas — os adaptadores concretos ficam no WIRING, para não
// arrastar o sandbox/registry/model-gateway inteiro para o core.
//
// INVARIANTE ESTRUTURAL: NENHUMA porta tem caminho de COMMIT nem de RATIFY. O
// [DryRunner] só CAPTURA efeitos (nunca os comete); o [RatificationSubmitter] só
// CONSTRÓI+SUBMETE o artefacto e devolve o token — nunca chama Ratify. Assim, "a
// superfície não pode cometer efeitos nem ratificar" é uma propriedade da FORMA das
// portas, não uma convenção.

// CandidateRef identifica a skill candidata em todo o loop: o nome e a versão SemVer
// (do registry, EPIC-05). É o value-type opaco que as portas recebem — não transporta
// o conteúdo da skill nem segredos, só a identidade pinada.
type CandidateRef struct {
	// Skill é o nome da skill candidata (identidade no registry).
	Skill string
	// Version é a versão SemVer exacta (X.Y.Z) — nunca uma referência flutuante.
	Version string
}

// Valid indica se a referência está completa (nome + versão presentes). Fail-closed:
// uma candidata sem nome ou sem versão não é apresentável.
func (c CandidateRef) Valid() bool { return c.Skill != "" && c.Version != "" }

// DryRunner é a PORTA de execução SIMULADA (AC1). O adaptador do wiring monta a
// COMPOSIÇÃO de dry-run do EPIC-07 — [sandbox.MediatedLauncher] sobre o Launcher
// (overlay descartado) + EgressFilter com allowlist VAZIA para o principal da
// candidata (todo egress externo negado por default-deny) + SEM CredentialInjector
// (sem segredos reais) — e devolve o [DryRunResult] com os efeitos CAPTURADOS
// (untrusted) e Committed=false. A porta NÃO expõe nenhum caminho que comprometa
// efeitos no mundo: só corre isolado e devolve o que capturou.
type DryRunner interface {
	DryRun(ctx context.Context, candidate CandidateRef) (DryRunResult, error)
}

// AttributionReader é a PORTA de LEITURA da atribuição (AC2). O adaptador do wiring
// LÊ o [procedural.Manifest] (autor-agente, versão SemVer, run de origem, hash de
// conteúdo) de EPIC-05 e a [domain.Provenance] do registry, e projecta-os na
// [Attribution]. Leitura pura: a superfície nunca altera a atribuição. O hash é um
// DIGEST (pin de integridade) — nunca o conteúdo nem um segredo.
type AttributionReader interface {
	Attribution(ctx context.Context, skill, version string) (Attribution, error)
}

// EvalResultReader é a PORTA de LEITURA do resultado do eval-gate/canary (AC4). O
// adaptador do wiring LÊ o [otelgenai.EvaluationResult] (veredicto/score) de EPIC-09
// e o CanaryPassed de EPIC-08. A superfície LÊ e APRESENTA — nunca decide. Devolve o
// [otelgenai.EvaluationResult] (a fonte da verdade é o MESMO tipo que o eval-gate
// emite — composição, não cópia) e o segundo valor é o veredicto do CANARY
// (canaryPassed). A AUSÊNCIA de resultado sinaliza-se pelo value-zero do
// [otelgenai.EvaluationResult] (Verdict vazio) — a superfície trata-o como
// [ErrNoEvalResult] (fail-closed: não há veredicto a apresentar).
type EvalResultReader interface {
	EvalOutcome(ctx context.Context, skill, version string) (result otelgenai.EvaluationResult, canaryPassed bool, err error)
}

// RatificationSubmitter é a PORTA de ENCAMINHAMENTO ao gate de ratificação de AOS-096
// (AC3). O adaptador do wiring CONSTRÓI o [hitl.SelfModArtifact] (id/kind/versão/
// eval-result/canary/content-hash), SUBMETE-o ao gate e devolve o RatificationID (o
// token anti-transplante que o HUMANO assina). A porta NÃO tem caminho de Ratify: a
// DECISÃO assinada fica do lado do gate/ratificador — a superfície só apresenta o
// token e submete.
type RatificationSubmitter interface {
	Submit(ctx context.Context, candidate CandidateRef) (ratID string, err error)
}
