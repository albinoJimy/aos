// Package pipeline é a PIPELINE DETERMINÍSTICA do Model Gateway (AOS-055,
// tecnica/06 §3): a cadeia ORDENADA e FIXA de estágios que cada model call
// atravessa antes (e depois) de sair para um provedor.
//
//	auth-principal → allowlist-regional → roteamento → validação-de-layout-de-cache → metering
//
// Cada estágio é uma INTERFACE [Stage] com uma implementação de referência
// PASS-THROUGH. Os tickets posteriores substituem cada impl pela real SEM mexer
// na ordem nem no contrato:
//   - auth-principal  → AOS-057 (identidade vs chaves pooled)
//   - allowlist       → AOS-058 (allowlist regional + soberania)
//   - roteamento      → AOS-059 (cost/load-aware + tiering)
//   - cache-layout    → AOS-060 (prefixo imutável cache-estável) — ver nota abaixo
//   - metering        → AOS-062 (custo USD por chamada)
//
// NOTA sobre cache-layout (AOS-060): ao contrário dos restantes, o estágio
// cache-layout mantém-se PASS-THROUGH por desenho. A guarda de layout de AOS-060
// (cache/layout+freeze+compaction) é consumida na HOT PATH DA MONTAGEM pelo
// runtime/assembler (freeze.RunPrefix.Turn -> layout.Guard.Admit), não pela fachada
// do GW: o [Exchange] não transporta a PromptView/runID/turno que a validação
// byte-a-byte exige. Por isso não há WithCacheLayoutStage. Ver
// [PassthroughCacheLayout] e tecnica/06 §7.
//
// # Fail-closed
//
// A ordem é determinística. Um estágio que RECUSA (devolve erro) FALHA-FECHA a
// chamada: os estágios seguintes não correm e o provider não é invocado. É a
// mesma disciplina default-deny do resto do AOS.
//
// # Determinismo
//
// Nenhum estágio de referência usa relógio nem aleatoriedade na decisão. O
// [Exchange] transporta um relógio injectável para quem precise de timestamps
// (nunca na decisão de roteamento/allowlist).
package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/aos-ref/platform/model-gateway/port"
)

// Op identifica a operação que atravessa a pipeline.
type Op string

const (
	// OpChat — chat/completions (síncrono ou streaming).
	OpChat Op = "chat"
	// OpEmbeddings — embeddings.
	OpEmbeddings Op = "embeddings"
)

// Decision é o registo determinista de um estágio: o que decidiu e porquê. É a
// base para o registo por chamada (modelo, região, resultado) que os tickets de
// governação/custo consomem — aqui apenas acumulado, não emitido.
type Decision struct {
	Stage  string
	Result string
	Reason string
}

// Exchange é o estado mutável que atravessa a pipeline de uma chamada. Os
// estágios lêem os campos "Requested*" e preenchem os "Resolved*". O provider é
// invocado ENTRE o estágio de cache-layout e o de metering (ver [Pipeline.Execute]),
// pelo que [Exchange.Usage] só está preenchido quando o metering corre.
type Exchange struct {
	Op        Op
	Principal string
	Board     string

	// RequestedModel/Provider/Region é o que o pedido pediu (o Provider pedido é
	// o do adaptador configurado por default; o roteamento pode divergir dele).
	RequestedModel    string
	RequestedProvider string
	RequestedRegion   string

	// ResolvedModel/Provider/Region é o que o roteamento escolheu. Num swap,
	// Resolved* != Requested* em qualquer das dimensões — o GW regista variância
	// explícita (model/provider/region), nunca silenciosa.
	ResolvedModel    string
	ResolvedProvider string
	ResolvedRegion   string

	// Usage é preenchido pela invocação do provider antes do estágio de metering.
	Usage port.Usage

	// --- Identidade resolvida pelo estágio de authn (AOS-057) ---
	//
	// Campos PRIMITIVOS por desenho: o pacote pipeline NÃO importa a identidade
	// (platform/identity) — o estágio de authn (routing/keypool à parte) preenche
	// estes campos a partir do token NHI validado. São o eixo da ATRIBUIÇÃO
	// (principal/modelo/região por chamada), SEPARADO do eixo da chave de infra
	// (KeyID). Ficam vazios no caminho pass-through (AOS-055) e só o estágio de
	// authn real (AOS-057) os popula.
	PrincipalUser  string
	PrincipalAgent string
	AgentClass     string
	// HumanRoot é o humano responsável na raiz da cadeia on-behalf-of (ADR-003):
	// "quem autorizou" em última instância. Vazio até o authn resolver a cadeia.
	HumanRoot string
	// EffectiveAuthority é a autoridade efectiva = utilizador ∩ classe de agente
	// (menor privilégio) computada pelo authn a partir do token e reconciliada com
	// o escopo selado.
	EffectiveAuthority []string
	// DelegationChain é a cadeia on-behalf-of verificada, projectada em forma
	// primitiva (sub/act_as) para o registo de atribuição e o audit WORM.
	DelegationChain []DelegationHop
	// PolicyVersion é a versão (tamper-evident) da policy-as-code de validação de
	// token aplicada pelo authn. É selada no audit WORM (liga a decisão à política).
	PolicyVersion string

	// KeyID é o identificador NÃO-SECRETO da chave de infra pooled seleccionada por
	// THROUGHPUT (routing/keypool), DESACOPLADA da identidade. Entra no registo de
	// atribuição para provar que a chave usada foi uma chave concreta — NUNCA "o
	// pool" — e nunca é o segredo (ADR-006). Vazio se não há keypool configurado.
	KeyID string

	// Decisions acumula o rasto determinista dos estágios.
	Decisions []Decision

	// clock é o relógio injectável (nunca usado em decisões).
	clock func() time.Time
}

// DelegationHop é um elo (sub → act_as) da cadeia de delegação on-behalf-of, em
// forma PRIMITIVA. O pacote pipeline não importa identity/delegation; o estágio
// de authn projecta a cadeia verificada (raiz humana → agente actual) para esta
// forma, que o registo de atribuição e o audit WORM consomem.
type DelegationHop struct {
	Sub   string
	ActAs string
}

// Now devolve o instante do relógio injectado (ou time.Now se nenhum).
func (e *Exchange) Now() time.Time {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now()
}

// record acrescenta uma decisão ao rasto.
func (e *Exchange) record(stage, result, reason string) {
	e.Decisions = append(e.Decisions, Decision{Stage: stage, Result: result, Reason: reason})
}

// Record acrescenta uma decisão ao rasto a partir de um estágio EXTERNO ao
// pacote (ex.: o estágio pipeline/authn de AOS-057, que vive noutro pacote e não
// consegue chamar o record não-exportado). Espelha o record interno.
func (e *Exchange) Record(stage, result, reason string) { e.record(stage, result, reason) }

// Stage é um estágio da pipeline. Process muta o [Exchange] ou devolve erro
// (fail-closed). Name identifica o estágio nos registos/decisões.
type Stage interface {
	Name() string
	Process(ctx context.Context, ex *Exchange) error
}

// Stages agrupa os cinco estágios da pipeline por PAPEL (ponto de extensão). Um
// ticket posterior substitui só o campo do seu papel, preservando a ordem fixa.
type Stages struct {
	Auth        Stage
	Allowlist   Stage
	Routing     Stage
	CacheLayout Stage
	Metering    Stage
}

// DefaultStages devolve a pipeline de referência: os cinco estágios PASS-THROUGH.
func DefaultStages() Stages {
	return Stages{
		Auth:        PassthroughAuth{},
		Allowlist:   PassthroughAllowlist{},
		Routing:     IdentityRouting{},
		CacheLayout: PassthroughCacheLayout{},
		Metering:    PassthroughMetering{},
	}
}

// ordered devolve os estágios PRÉ-invocação por ordem fixa (auth → allowlist →
// routing → cache-layout). O metering corre DEPOIS da invocação do provider.
func (s Stages) ordered() []Stage {
	return []Stage{s.Auth, s.Allowlist, s.Routing, s.CacheLayout}
}

// Pipeline é a cadeia determinística executável.
type Pipeline struct {
	stages Stages
}

// New constrói uma Pipeline com os estágios dados. Campos nil são substituídos
// pelos pass-through de referência (nunca há um estágio ausente).
func New(s Stages) *Pipeline {
	def := DefaultStages()
	if s.Auth == nil {
		s.Auth = def.Auth
	}
	if s.Allowlist == nil {
		s.Allowlist = def.Allowlist
	}
	if s.Routing == nil {
		s.Routing = def.Routing
	}
	if s.CacheLayout == nil {
		s.CacheLayout = def.CacheLayout
	}
	if s.Metering == nil {
		s.Metering = def.Metering
	}
	return &Pipeline{stages: s}
}

// NewDefault constrói a pipeline de referência (todos os estágios pass-through).
func NewDefault() *Pipeline { return New(DefaultStages()) }

// Stages devolve os estágios configurados (introspecção/testes).
func (p *Pipeline) Stages() Stages { return p.stages }

// Invoke é a função que faz a invocação do provider. Recebe o [Exchange] já
// resolvido (ResolvedModel/Provider/Region) e deve preencher [Exchange.Usage].
type Invoke func(ctx context.Context, ex *Exchange) error

// Execute corre a pipeline DETERMINÍSTICA completa de uma chamada:
//
//	auth → allowlist → routing → cache-layout → [invoke provider] → metering
//
// Fail-closed: se um estágio pré-invocação recusar, invoke NÃO corre e o erro
// propaga-se; se invoke falhar, o metering NÃO corre. Devolve o primeiro erro.
// É o caminho SÍNCRONO (Chat/Embeddings), onde ex.Usage já está preenchido pela
// invocação antes de o metering correr.
func (p *Pipeline) Execute(ctx context.Context, ex *Exchange, invoke Invoke) error {
	if err := p.ExecutePreInvoke(ctx, ex, invoke); err != nil {
		return err
	}
	return p.Meter(ctx, ex)
}

// ExecutePreInvoke corre os estágios PRÉ-invocação (auth → allowlist → routing →
// cache-layout) e a invocação do provider, SEM o estágio de metering. É o caminho
// do STREAMING: o usage só está disponível no fim do stream, pelo que o metering
// é adiado (ver [Pipeline.Meter]) e não pode correr aqui sobre usage vazio.
// Fail-closed igual ao [Pipeline.Execute].
func (p *Pipeline) ExecutePreInvoke(ctx context.Context, ex *Exchange, invoke Invoke) error {
	if ex.clock == nil {
		ex.clock = time.Now
	}
	for _, st := range p.stages.ordered() {
		if err := st.Process(ctx, ex); err != nil {
			return &StageError{Stage: st.Name(), Err: err}
		}
	}
	if invoke != nil {
		if err := invoke(ctx, ex); err != nil {
			return err
		}
	}
	return nil
}

// Meter corre o estágio de metering isoladamente, com [Exchange.Usage] já
// preenchido. O streaming invoca-o no fecho do stream (usage final), garantindo
// que o metering (custo USD de AOS-062) NUNCA corre sobre zero tokens. Fail-closed.
func (p *Pipeline) Meter(ctx context.Context, ex *Exchange) error {
	if err := p.stages.Metering.Process(ctx, ex); err != nil {
		return &StageError{Stage: p.stages.Metering.Name(), Err: err}
	}
	return nil
}

// WithClock injecta um relógio determinista no Exchange (para timestamps).
func WithClock(ex *Exchange, clock func() time.Time) {
	ex.clock = clock
}

// StageError envolve o erro de um estágio, identificando qual recusou
// (fail-closed atribuível).
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	return "pipeline: estagio " + e.Stage + " recusou: " + e.Err.Error()
}
func (e *StageError) Unwrap() error { return e.Err }

// ErrDenied é o erro-sentinela genérico de recusa fail-closed de um estágio (ex.:
// allowlist). Os tickets de governação envolvem-no com a razão concreta.
var ErrDenied = errors.New("pipeline: recusado fail-closed")
