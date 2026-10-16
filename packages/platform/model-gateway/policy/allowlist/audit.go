package allowlist

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
)

// O REGISTO DE GOVERNAÇÃO por chamada da allowlist regional (AOS-058, ADR-010/
// ADR-011): a decisão de allowlist (allow/deny) e a rota (modelo, região) são
// registadas POR CHAMADA, atribuíveis ao PRINCIPAL E ao BOARD — nunca um deny
// anónimo. É o mesmo eixo de atribuição de AOS-057 (metering/attribution), aqui
// aplicado à decisão de soberania: um deny cross-border ou fora-de-allowlist sela
// no audit WORM tamper-evident quem o motivou (principal) e sob que fronteira
// (board), respondendo a "quem foi recusado e porquê" sem revelar segredos.

// Erros fail-closed da selagem de governação.
var (
	// ErrNoBoard — a decisão não tem board resolvido. Fail-closed: um registo de
	// soberania sem board não é atribuível à fronteira e é recusado (a soberania é
	// por board — selar "sem fronteira" violaria a invariante de AOS-058).
	ErrNoBoard = errors.New("allowlist: decisao de governacao sem board (fail-closed)")
	// ErrNoAttribution — a decisão não tem principal (nem utilizador nem agente).
	// Fail-closed: um deny/allow anónimo é inaceitável (ADR-010).
	ErrNoAttribution = errors.New("allowlist: decisao de governacao sem principal (fail-closed)")
)

// Atributos de span (semconv-aligned) do registo de governação da allowlist.
const (
	// AttrAllowlistResult — resultado da decisão de allowlist ("allow"|"deny").
	AttrAllowlistResult = "aos.allowlist.result"
	// AttrAllowlistReason — razão estável da decisão (motivo do deny/allow).
	AttrAllowlistReason = "aos.allowlist.reason"
	// AttrAllowlistPolicyVersion — versão tamper-evident da allowlist em vigor.
	AttrAllowlistPolicyVersion = "aos.allowlist.policy_version"
	// AttrBoard — board (fronteira de soberania) da decisão.
	AttrBoard = "aos.board"
	// AttrRegion — região efectiva/pedida da decisão.
	AttrRegion = "aos.region"
	// AttrModel — modelo alvo da decisão.
	AttrModel = "aos.model"
)

// capabilityModelInvoke é a capability escopada exercida/negada: invocar um modelo.
const capabilityModelInvoke = "model:invoke"

// obligationGovernance é o tipo da obligation que sela board/razão/utilizador na
// cadeia (tamper-evident), tornando a decisão atribuível a partir de um único
// registo (não só pela partição).
const obligationGovernance = "governance"

// ObligationOrigin é o tipo da obligation que sela a ORIGEM de uma chamada feita por uma tool
// (AOS-530, ADR-042 §2.2): `Params["tool"]` é [GovRecord.OriginTool]. É o mesmo tipo e a mesma forma
// do selo de atribuição (metering/attribution), e é por este selo — o de governação por chamada — que
// a tool de origem fica no audit do nó, que não compõe o recorder de atribuição.
const ObligationOrigin = "model_call_origin"

// Hop é um elo (sub → act_as) da cadeia on-behalf-of, em forma primitiva.
type Hop struct {
	Sub   string
	ActAs string
}

// GovRecord é a decisão de governação da allowlist de UMA chamada: o board, o
// principal (utilizador, agente) e a cadeia, o modelo e a região, o veredicto e a
// razão. Junta o eixo da soberania (board) ao eixo da identidade (principal).
type GovRecord struct {
	// Board é a fronteira de soberania da decisão (SEMPRE presente).
	Board string
	// Principal (utilizador, agente) responsável.
	PrincipalUser  string
	PrincipalAgent string
	AgentClass     string
	// HumanRoot é o humano responsável na raiz da cadeia on-behalf-of (ADR-003).
	HumanRoot string
	// DelegationChain é a cadeia on-behalf-of verificada.
	DelegationChain []Hop
	// Model e Region identificam a rota da decisão.
	Model  string
	Region string
	// Decision é o veredicto (allow|deny).
	Decision audit.Decision
	// Reason é a razão estável da decisão (ex.: "modelo fora da allowlist").
	Reason string
	// PolicyVersion é a versão tamper-evident da allowlist em vigor.
	PolicyVersion string
	// Operation é a operação GenAI ("chat"|"embeddings").
	Operation string
	// RunID/StepID correlacionam com a trajectória (opcional).
	RunID  string
	StepID string
	// OriginTool é a tool que fez a chamada (AOS-530, ADR-042 §2.2); vazio num turno do executor.
	// Quando não vazio, sela-se numa obligation própria [ObligationOrigin], a seguir à de governação;
	// vazio, o selo é, byte a byte, o de sempre.
	OriginTool string
	// Timestamp é observacional (a ordem no WORM é o AuditSeq).
	Timestamp time.Time
}

// principalID devolve o identificador NÃO-SECRETO do principal (a resposta a
// "quem"). Prefere o agente (NHI); cai para o utilizador.
func (r GovRecord) principalID() string {
	if r.PrincipalAgent != "" {
		return r.PrincipalAgent
	}
	return r.PrincipalUser
}

// Sink recebe, opcionalmente, cada [GovRecord] emitido (introspecção em memória; os
// testes de governação inspeccionam-no). A selagem WORM é separada.
type Sink interface {
	Emit(ctx context.Context, rec GovRecord)
}

// SinkFunc adapta uma função a [Sink].
type SinkFunc func(ctx context.Context, rec GovRecord)

// Emit implementa [Sink].
func (f SinkFunc) Emit(ctx context.Context, rec GovRecord) { f(ctx, rec) }

// Recorder produz o registo de governação por chamada: anota o span e sela no
// audit WORM (partição POR BOARD — a cadeia tamper-evident da soberania). Construir
// com [NewRecorder].
type Recorder struct {
	store audit.Store
	sink  Sink
}

// Option configura o [Recorder].
type Option func(*Recorder)

// WithSink liga um [Sink] em memória que recebe cada registo (além da selagem WORM).
func WithSink(s Sink) Option { return func(r *Recorder) { r.sink = s } }

// NewRecorder constrói o recorder sobre um audit [audit.Store] (WORM). Sem store, a
// selagem é no-op (só a anotação de span/sink corre) — produção liga um store real.
func NewRecorder(store audit.Store, opts ...Option) *Recorder {
	r := &Recorder{store: store}
	for _, o := range opts {
		o(r)
	}
	return r
}

// partitionOf agrupa a cadeia de audit POR BOARD (fronteira de soberania): a
// responsabilização de soberania é contígua por board.
func partitionOf(board string) string { return "modelgw-gov:" + board }

// Annotate anota o span com a decisão de governação (resultado, board, modelo,
// região, versão da política). NUNCA emite segredos. Seguro com span nil.
func (r *Recorder) Annotate(span agentruntime.Span, rec GovRecord) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrAllowlistResult, string(rec.Decision))
	span.SetAttribute(AttrAllowlistReason, rec.Reason)
	span.SetAttribute(AttrAllowlistPolicyVersion, rec.PolicyVersion)
	span.SetAttribute(AttrBoard, rec.Board)
	span.SetAttribute(AttrModel, rec.Model)
	span.SetAttribute(AttrRegion, rec.Region)
}

// Seal sela a decisão no audit WORM hash-chain (ADR-010/AOS-011) e devolve o
// registo selado. Sem store é no-op. Fail-closed: um registo sem board ou sem
// principal é RECUSADO (a decisão de soberania tem de ser atribuível a ambos).
func (r *Recorder) Seal(ctx context.Context, rec GovRecord) (audit.AuditRecord, error) {
	return r.seal(ctx, rec, nil)
}

// SealRouteVariance sela no audit WORM uma VARIÂNCIA DE ROTA (AOS-505): o modelo que o proxy
// declarou ter servido não é o do perfil da rota pedida, ou não foi reportado. É o registo de
// governação de [Recorder.Seal] — a mesma partição por board, o mesmo principal, o mesmo run e
// passo —, com dois parâmetros a mais na obligation: o modelo esperado e o modelo servido. A
// razão ([GovRecord.Reason]) é a causa, em vocabulário fechado, e [GovRecord.Decision] diz se o
// turno seguiu (allow, em observação) ou falhou (deny, em imposição).
//
// Só o NOME do modelo servido entra, já saneado por quem o leu. O endpoint não entra, e o
// identificador do deployment que o proxy emite nunca é lido.
func (r *Recorder) SealRouteVariance(ctx context.Context, rec GovRecord, expectedModel, servedModel string) (audit.AuditRecord, error) {
	return r.seal(ctx, rec, map[string]string{
		"variance":       "served_route",
		"expected_model": expectedModel,
		"served_model":   servedModel,
	})
}

// seal é o corpo de [Recorder.Seal]. `extra` acrescenta parâmetros à obligation de governação;
// nil deixa o registo com os bytes de sempre.
func (r *Recorder) seal(ctx context.Context, rec GovRecord, extra map[string]string) (audit.AuditRecord, error) {
	if err := rec.validate(); err != nil {
		return audit.AuditRecord{}, err
	}
	if r.store == nil {
		return audit.AuditRecord{}, nil
	}
	params := map[string]string{
		"board":          rec.Board,
		"reason":         rec.Reason,
		"principal_user": rec.PrincipalUser,
	}
	for k, v := range extra {
		params[k] = v
	}
	ar := audit.AuditRecord{
		Partition:     partitionOf(rec.Board),
		Timestamp:     rec.Timestamp,
		Decision:      rec.Decision,
		Principal:     audit.Principal{NHIID: rec.principalID(), DelegationChain: toAuditHops(rec.DelegationChain)},
		Capability:    capabilityModelInvoke,
		PolicyVersion: rec.PolicyVersion,
		RunID:         rec.RunID,
		StepID:        rec.StepID,
		ToolID:        "model." + rec.Model,
		Resource:      audit.Resource{Type: "model", Value: rec.Model, Region: rec.Region},
		// A obligation sela BOARD + razão + utilizador na cadeia (tamper-evident):
		// a decisão é atribuível a partir de UM registo, não só pela partição.
		Obligations: []audit.Obligation{{
			Type:   obligationGovernance,
			Params: params,
		}},
	}
	// AOS-530: uma chamada feita por uma tool sela a tool numa obligation à parte. Num turno do
	// executor não há obligation nova e o registo é, byte a byte, o de antes.
	if rec.OriginTool != "" {
		ar.Obligations = append(ar.Obligations, audit.Obligation{Type: ObligationOrigin, Params: map[string]string{"tool": rec.OriginTool}})
	}
	return r.store.Append(ctx, ar)
}

// changelogPartition é a cadeia dedicada ao CHANGELOG da allowlist (ADR-011): cada
// activação de uma versão da policy sela aqui, tornando a evolução da allowlist
// tamper-evident e auditável independentemente das decisões por chamada.
const changelogPartition = "modelgw-gov:allowlist-changelog"

// SealChangelog sela no audit WORM a ACTIVAÇÃO de uma versão da allowlist
// (policy-as-code versionada + assinada) — o CHANGELOG exigido pelo ADR-011. Regista
// a versão tamper-evident (`Policy.Version()`) numa cadeia dedicada. Chamado no
// arranque/rotação da policy. Sem store é no-op. A versão vazia é recusada.
func (r *Recorder) SealChangelog(ctx context.Context, policyVersion string, at time.Time) (audit.AuditRecord, error) {
	if policyVersion == "" {
		return audit.AuditRecord{}, ErrPolicyMalformed
	}
	if r.store == nil {
		return audit.AuditRecord{}, nil
	}
	ar := audit.AuditRecord{
		Partition:     changelogPartition,
		Timestamp:     at,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: "allowlist-policy"},
		Capability:    "policy:activate",
		PolicyVersion: policyVersion,
		ToolID:        "allowlist.regional",
		Resource:      audit.Resource{Type: "policy", Value: "allowlist-regional"},
		Obligations: []audit.Obligation{{
			Type:   obligationGovernance,
			Params: map[string]string{"changelog": "activate", "policy_version": policyVersion},
		}},
	}
	return r.store.Append(ctx, ar)
}

// routeProfilesPartition é a cadeia dedicada aos PERFIS DE ROTA carregados de registos de entrada
// assinados (AOS-519): cada carregamento sela aqui, numa cadeia à parte das decisões por chamada.
const routeProfilesPartition = "modelgw-gov:route-profiles"

// ErrRouteProfileSeal — o selo do carregamento de um perfil de rota não tem o que identifica o
// perfil: o nome pedido ou um dos três digests. Fail-closed: não se sela um carregamento anónimo.
var ErrRouteProfileSeal = errors.New("allowlist: selo de perfil de rota recusado — sem nome pedido, sem um dos tres digests, ou sem audit onde selar (fail-closed)")

// SealRouteProfileLoaded sela no audit WORM o CARREGAMENTO de um perfil de rota que veio de um
// registo de entrada assinado (AOS-519): o nome pedido, o modelo esperado, e os três digests — o
// do perfil, o do registo (o que a assinatura do dono cobre) e o do relatório de qualificação que
// o registo cita. É o que deixa reconstruir do log que perfil serviu que nome, e desde quando.
// Chamado uma vez por perfil em cada composição do gateway, DEPOIS de ele estar composto.
//
// SEM STORE NÃO É NO-OP, ao contrário dos outros selos deste recorder: devolve
// [ErrRouteProfileSeal]. Um perfil que mudou o que um nome significa e não deixou prova é
// exactamente o que este selo existe para impedir.
//
// Só identificadores e digests: nenhum conteúdo do registo nem do relatório.
func (r *Recorder) SealRouteProfileLoaded(ctx context.Context, at time.Time, requested, expectedModel, profileDigest, entryDigest, reportDigest string) (audit.AuditRecord, error) {
	if requested == "" || profileDigest == "" || entryDigest == "" || reportDigest == "" {
		return audit.AuditRecord{}, ErrRouteProfileSeal
	}
	if r.store == nil {
		return audit.AuditRecord{}, fmt.Errorf("%w: nao ha audit onde selar", ErrRouteProfileSeal)
	}
	ar := audit.AuditRecord{
		Partition:     routeProfilesPartition,
		Timestamp:     at,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: "route-profile-entry"},
		Capability:    "route-profile:load",
		PolicyVersion: "route-profile/" + profileDigest,
		ToolID:        "model." + requested,
		Resource:      audit.Resource{Type: "route-profile", Value: requested},
		Obligations: []audit.Obligation{{
			Type: obligationGovernance,
			Params: map[string]string{
				"route_profile":  "loaded",
				"requested":      requested,
				"expected_model": expectedModel,
				"profile_digest": profileDigest,
				"entry_digest":   entryDigest,
				"report_digest":  reportDigest,
			},
		}},
	}
	return r.store.Append(ctx, ar)
}

// ErrCanaryClosedSeal — o selo do fim de um canary não tem o que o identifica (o nome pedido ou um
// dos dois digests), tem contagens sem sentido, ou não há audit onde selar. Fail-closed.
var ErrCanaryClosedSeal = errors.New("allowlist: selo do fim do canary recusado — sem nome pedido, sem um dos dois digests, com contagens invalidas, ou sem audit onde selar (fail-closed)")

// SealCanaryClosed sela no audit WORM o FIM do canary de um modelo candidato (AOS-520, ADR-041
// §2.6): o nome pedido do candidato, o digest do seu perfil, o digest do registo de entrada
// assinado que abriu o canary, quantos planos o candidato serviu e o limite. Fica na mesma cadeia
// do carregamento dos perfis assinados, ao lado do selo que diz desde quando o perfil serviu o nome.
//
// É chamado por quem gravou o fim no livro durável do canary, uma vez. O livro é a fonte da
// contagem; este selo é a prova, tamper-evident, de que o fim foi registado.
//
// SEM STORE NÃO É NO-OP: devolve [ErrCanaryClosedSeal]. Só identificadores, digests e contagens.
func (r *Recorder) SealCanaryClosed(ctx context.Context, at time.Time, requested, profileDigest, entryDigest string, assigned, limit int) (audit.AuditRecord, error) {
	if requested == "" || profileDigest == "" || entryDigest == "" || limit <= 0 || assigned < limit {
		return audit.AuditRecord{}, ErrCanaryClosedSeal
	}
	if r.store == nil {
		return audit.AuditRecord{}, fmt.Errorf("%w: nao ha audit onde selar", ErrCanaryClosedSeal)
	}
	ar := audit.AuditRecord{
		Partition:     routeProfilesPartition,
		Timestamp:     at,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: "model-canary"},
		Capability:    "model-canary:close",
		PolicyVersion: "route-profile/" + profileDigest,
		ToolID:        "model." + requested,
		Resource:      audit.Resource{Type: "route-profile", Value: requested},
		Obligations: []audit.Obligation{{
			Type: obligationGovernance,
			Params: map[string]string{
				"model_canary":   "closed",
				"requested":      requested,
				"profile_digest": profileDigest,
				"entry_digest":   entryDigest,
				"plans_assigned": strconv.Itoa(assigned),
				"plan_limit":     strconv.Itoa(limit),
			},
		}},
	}
	return r.store.Append(ctx, ar)
}

// CanaryClosedSeals devolve quantos selos do fim do canary do registo de entrada dado ESTE audit
// tem (AOS-520). É o que torna o selo reconciliável: quem tem de garantir que o fim ficou selado
// pergunta ao audit, e não à sua memória — o processo que escreveu o fim no livro pode ter morrido
// antes de selar, ou o selo pode ter falhado.
//
// O audit é LOCAL a quem o tem (um por réplica do nó, salvo um audit partilhado): a resposta é
// sobre este audit, e não sobre os das outras réplicas.
func (r *Recorder) CanaryClosedSeals(ctx context.Context, entryDigest string) (int, error) {
	if r.store == nil {
		return 0, fmt.Errorf("%w: nao ha audit onde ler", ErrCanaryClosedSeal)
	}
	head, err := r.store.Head(ctx, routeProfilesPartition)
	if err != nil || head == 0 {
		return 0, err
	}
	recs, err := r.store.Read(ctx, routeProfilesPartition, 1, head)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range recs {
		if rec.Capability != "model-canary:close" {
			continue
		}
		for _, o := range rec.Obligations {
			if o.Type == obligationGovernance && o.Params["model_canary"] == "closed" && o.Params["entry_digest"] == entryDigest {
				n++
			}
		}
	}
	return n, nil
}

// Record ANOTA o span, emite para o [Sink] (se configurado) e SELA no WORM. É o
// ponto único que o estágio de allowlist chama por chamada. Fail-closed ANTES de
// qualquer efeito observável: uma decisão sem board/principal nunca chega a anotar
// nem a selar.
func (r *Recorder) Record(ctx context.Context, span agentruntime.Span, rec GovRecord) error {
	if err := rec.validate(); err != nil {
		return err
	}
	r.Annotate(span, rec)
	if r.sink != nil {
		r.sink.Emit(ctx, rec)
	}
	if _, err := r.Seal(ctx, rec); err != nil {
		return err
	}
	return nil
}

// validate impõe as invariantes de atribuição (fail-closed): board presente e
// pelo menos um dos identificadores do principal.
func (r GovRecord) validate() error {
	if r.Board == "" {
		return ErrNoBoard
	}
	if r.PrincipalUser == "" && r.PrincipalAgent == "" {
		return ErrNoAttribution
	}
	return nil
}

// toAuditHops projecta os hops de atribuição para o modelo do audit.
func toAuditHops(hops []Hop) []audit.DelegationHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]audit.DelegationHop, len(hops))
	for i, h := range hops {
		out[i] = audit.DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}
