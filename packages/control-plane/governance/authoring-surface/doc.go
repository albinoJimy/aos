// Package authoringsurface é o LOOP DE AUTORIA DE SKILLS (AOS-126, EPIC-12): a
// SUPERFÍCIE que torna LEGÍVEL e ACCIONÁVEL o fluxo pelo qual quem escreve/revê uma
// skill candidata a (1) EXECUTA em DRY-RUN — em modo simulado, vendo o efeito SEM o
// cometer —, (2) vê a ATRIBUIÇÃO — autor (agente/humano), versão SemVer e proveniência
// —, (3) vê o resultado do EVAL-GATE/CANARY ANTES da decisão, e (4) ENCAMINHA a
// candidata ao gate de ratificação de AOS-096.
//
// # COMPÕE — não reimplementa, não comete efeitos, não ratifica
//
// Esta camada é APRESENTAÇÃO pura (padrão porta+adaptador). CONSOME o que outros
// subsistemas já impõem e NÃO reimplementa nada — e, por construção das portas, NÃO
// pode cometer efeitos nem ratificar:
//
//   - EPIC-07 (sandbox / dry-run): o dry-run reutiliza o sandbox e o taint existentes
//     via [DryRunner]. NÃO há "modo dry-run" novo: é a COMPOSIÇÃO de invariantes
//     estruturais que o adaptador do wiring monta — [sandbox.MediatedLauncher] sobre o
//     Launcher (overlay descartado; nada persiste) + EgressFilter com allowlist VAZIA
//     para o principal da candidata (todo egress externo negado por default-deny) + SEM
//     CredentialInjector (sem segredos reais). Os efeitos são CAPTURADOS ([CapturedEffect],
//     untrusted), nunca cometidos; [DryRunResult.Committed] TEM de ser false e a
//     superfície valida-o fail-closed ([ErrEffectCommitted]).
//   - EPIC-05 (registry / SemVer / proveniência): a ATRIBUIÇÃO é LIDA via
//     [AttributionReader] do [procedural.Manifest] (autor-agente, versão SemVer, run de
//     origem, hash de conteúdo) e da [domain.Provenance] do registry. A superfície LÊ —
//     nunca reimplementa a atribuição. O hash é um DIGEST (pin), nunca conteúdo/segredo.
//   - EPIC-09/08 (eval-gate / canary): o veredicto/score e o canary são LIDOS via
//     [EvalResultReader] do [otelgenai.EvaluationResult] + CanaryPassed e apresentados
//     ANTES da decisão. A superfície MOSTRA — nunca decide.
//   - AOS-096 (ratificação): a candidata é ENCAMINHADA via [RatificationSubmitter], que
//     no adaptador constrói o [hitl.SelfModArtifact] e devolve o RatificationID (o token
//     anti-transplante que o HUMANO assina). A superfície APRESENTA+SUBMETE; NÃO chama
//     Ratify nem promove — não há caminho de Ratify aqui (ver ADR-012: o pipeline de
//     admissão não é duplicado).
//
// Ver specs/EPIC-12 (## AOS-126), specs/EPIC-09 (AOS-096), specs/EPIC-05, EPIC-07
// (sandbox) e ADR-012 (SemVer + pipeline de admissão). A superfície emite o seu próprio
// vocabulário de spans aos.authoring.surface.* (AC5), ligado à trajectória por AttrRunID,
// sem segredos.
//
// # Mapa dos critérios de aceitação
//
//   - AC1: [AuthoringLoop.DryRun] → [DryRunResult] com Committed=false validado
//     fail-closed (Committed=true ⇒ [ErrEffectCommitted]); efeitos capturados untrusted;
//     egress default-deny.
//   - AC2: [AuthoringLoop.Attribution] → [Attribution] (Author/Version SemVer/
//     OriginRunID/Provenance) LIDA de [AttributionReader], apresentada em todo o loop.
//   - AC3: [AuthoringLoop.SubmitForRatification] DELEGA a [RatificationSubmitter] e
//     devolve o RatificationID — sem caminho de Ratify na superfície.
//   - AC4: [AuthoringLoop.EvalOutcome] → [EvalView] (Verdict/Score/CanaryPassed) LIDA de
//     [EvalResultReader], apresentada ANTES da decisão.
//   - AC5: cada passo emite o span aos.authoring.surface.kind (dry_run/attribution_view/
//     submit) ligado à trajectória, sem cometer efeitos nem expor segredos.
package authoringsurface
