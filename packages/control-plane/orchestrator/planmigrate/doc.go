// Package planmigrate persiste o plano APROVADO e governa a evolução/deprecação do
// seu `plan_version`, mantendo o replay DETERMINÍSTICO sem re-chamar o LLM nem
// re-atravessar o Reference Monitor (AOS-243, tecnica/18 §3.6).
//
// TRÊS EIXOS PINADOS, DISTINTOS (§3.6). Um run replayável fixa três coordenadas
// INDEPENDENTES no seu [Manifest] — conflatá-las é o erro que este pacote existe
// para prevenir:
//
//   - plan_version   — o SCHEMA do PlanDocument (SemVer estrito, [plan.PlanVersion]).
//     Governa a FORMA que o materializador consegue ler.
//   - prompt_version — o COMPORTAMENTO do prompt de decomposição (artefacto SemVer,
//     ADR-012). Governa COMO o plano foi proposto.
//   - capabilities_hash — o AMBIENTE (o snapshot pinado do REG). Governa CONTRA o
//     quê o plano foi validado.
//
// PROVENIÊNCIA DOS PINOS. `prompt_version` e `capabilities_hash` são persistidos na
// CAPTURA — o stream append-only `aos.planner.v1` (via `plan.proposed`, AOS-235):
// nada neste pacote os re-deriva. `plan_version` é pinado pelo READER — o
// PlanDocument aprovado e CONGELADO — que se liga à captura pelo HASH do documento
// aprovado ([HashPlan] vs. o hash em `plan.approved`). É por isso que a admissão de
// um run exige AMBOS: a captura (o que aconteceu) e o reader (o documento na versão
// em que foi aprovado). Ver o invariante do DoD, mais abaixo.
//
// CONGELADO, NUNCA AUTO-MIGRADO (§3.6). Um plano materializa-se EXACTAMENTE na
// versão em que foi aprovado. Este pacote NUNCA lê [plan.CurrentPlanVersion] para
// "actualizar" um plano aprovado — o [Manifest] reflecte a versão do reader e mais
// nenhuma. Um bump de MAJOR da linha corrente não toca em runs já aprovados.
//
// FAIL-CLOSED NA ADMISSÃO. Antes de materializar OU de reproduzir, a [Policy] é o
// gate soberano sobre o `plan_version` congelado:
//
//   - RETIRADA — se a versão foi retirada ([Policy.Retire]) antes da materialização,
//     o run é INVALIDADO ([ErrRetired]): exige re-plano + re-aprovação. Não há
//     migração silenciosa de um plano numa versão retirada.
//   - JANELA DE SUPORTE — os MAJORs com reader RETIDO formam uma janela declarada
//     ([SupportWindow], ADR-012). Um run cujo `plan_version.Major` cai FORA da janela
//     é INADMISSÍVEL ([ErrOutsideSupportWindow]) — tratado como um payload perdido
//     (a mesma inadmissibilidade de AOS-016): não se adivinha, não se auto-migra.
//
// Um bump de MAJOR passa por ADR-012 com o reader RETIDO (a janela mantém o MAJOR
// antigo, e o documento aprovado persiste) OU com deprecação DOCUMENTADA (a janela
// avança o MinMajor, tornando os runs antigos inadmissíveis de forma explícita) —
// nunca com uma migração automática por baixo da mesa.
//
// REPLAY ZERO-EFEITOS. [Migrator.Replay] reconstrói a captura via
// [plannerevents.Reconstruct] (que recebe só um EventReader — read-only por
// construção) e devolve a materialização CAPTURADA. NÃO consulta o REG
// ([CapabilityResolver]) nem o RM ([ReferenceMonitor]): as decisões já estão no log.
// O mesmo [Migrator] detém o REG e o RM porque a via de ESCRITA ([Migrator.Materialize])
// os usa de facto — mas a via de replay prova, por contadores a zero, que não lhes
// toca. É essa a fronteira que garante replay determinístico.
//
// INVARIANTE (DoD): um run é REPLAYÁVEL enquanto a CAPTURA (o stream) E o READER (o
// documento aprovado, na sua versão) forem AMBOS admissíveis. Se a versão saiu da
// janela ou foi retirada, o reader deixa de ser admissível e o run tem de voltar a
// planeamento — mesmo com a captura intacta.
//
// FRONTEIRA. Este pacote CONSOME [plan] (semver + documento) e
// [plannerevents]/[eventstore] (captura); não os edita e não declara tipos de evento
// novos — reutiliza as constantes de `aos.planner.v1`. Não faz rede nem executa o
// plano (ADR-005: o PlanDocument é dados untrusted).
package planmigrate
