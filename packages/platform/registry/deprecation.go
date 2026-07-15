package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/registry/domain"
)

// Erros do ciclo de vida de deprecação/rollback (AOS-052). Sentinelas comparáveis
// com errors.Is. Fail-closed em toda a superfície.
var (
	// ErrStillReferenced — tentou-se RETIRAR (remover do catálogo) uma versão que
	// ainda é REFERENCIADA por, pelo menos, uma trajectória. Uma versão nunca é
	// removida enquanto o replay fiel de alguma trajectória depender dela (ADR-012).
	ErrStillReferenced = &RegistryError{Code: "E_REG_STILL_REFERENCED", msg: "versao ainda referenciada por trajectorias; retirada recusada"}

	// ErrNotDeprecated — tentou-se RETIRAR uma versão que não passou por deprecated. A
	// deprecação é FORMAL: uma versão atravessa sempre deprecated ANTES de qualquer
	// retirada, nunca é removida abruptamente de active.
	ErrNotDeprecated = &RegistryError{Code: "E_REG_NOT_DEPRECATED", msg: "retirada exige deprecacao formal previa (deprecated antes de retirar)"}

	// ErrUnknownVersion — a versão referida não está no catálogo desta linha de
	// versões (default-deny: uma versão desconhecida não é operável).
	ErrUnknownVersion = &RegistryError{Code: "E_REG_UNKNOWN_VERSION", msg: "versao desconhecida na linha de versoes"}

	// ErrVersionRevoked — a operação incide sobre uma versão REVOGADA (estado
	// terminal): nem referenciável, nem reactivável, nem alvo de rollback.
	ErrVersionRevoked = &RegistryError{Code: "E_REG_VERSION_REVOKED", msg: "versao revogada (estado terminal)"}

	// ErrNoReference — tentou-se libertar uma referência de trajectória inexistente
	// (contador já a zero para essa trajectória). Fail-closed: evita um contador
	// negativo que mascararia uma versão realmente ainda referenciada.
	ErrNoReference = &RegistryError{Code: "E_REG_NO_REFERENCE", msg: "referencia de trajectoria inexistente"}

	// ErrNotRollbackable — o alvo de rollback não é uma versão anterior válida e
	// disponível (inexistente, revogada, ou igual à active corrente).
	ErrNotRollbackable = &RegistryError{Code: "E_REG_NOT_ROLLBACKABLE", msg: "alvo de rollback invalido (inexistente/revogado/ja active)"}
)

// Atributos e operações de span do ciclo de vida (públicos: id/versão/estado não
// são segredos). Reutilizam a porta Tracer zero-dep do Agent Runtime.
const (
	opDeprecate = "registry.lifecycle.deprecate"
	opRetire    = "registry.lifecycle.retire"
	opRollback  = "registry.lifecycle.rollback"
	opReference = "registry.lifecycle.reference"
)

// versionState é o estado de uma versão dentro de uma linha de versões: o seu
// estado de ciclo de vida e o contador de trajectórias que a referenciam, por
// identificador de trajectória (o replay fiel de cada uma depende desta versão).
type versionState struct {
	status domain.Status
	refs   map[string]int // trajectoryID -> contagem
}

func (vs *versionState) totalRefs() int {
	n := 0
	for _, c := range vs.refs {
		n += c
	}
	return n
}

// LifecycleAdmissionVerifier é o GATE de re-verificação da PROMOÇÃO a active pela
// via do rollback (deprecated→active). Espelha o AdmissionVerifier de Registry.
// SetStatus mas ao nível da versão: um Verify que devolva erro IMPEDE o rollback
// (fail-closed). É o PONTO DE EXTENSÃO onde a re-verificação de assinatura/revogação
// (AOS-048 Q1) é imposta — a chave do publicador pode ter sido revogada entre a
// primeira promoção e a re-activação, pelo que a reactivação NÃO é confiança
// herdada. Por omissão nil (sem gate injectado): a composition-root autoritativa
// DEVE ligar aqui o mesmo verificador criptográfico usado por Registry.SetStatus.
type LifecycleAdmissionVerifier interface {
	// Verify decide se a versão pode ser (re)promovida a active. nil = admitido.
	Verify(ctx context.Context, v domain.Version) error
}

// Lifecycle gere o CICLO DE VIDA de deprecação e o ROLLBACK ATÓMICO de UMA linha de
// versões (todas as versões de um mesmo artefacto id). Complementa a máquina de
// estados do domínio (domain.CanTransition) e o catálogo append-only do Registry
// com duas garantias que o ticket AOS-052 exige e que são de natureza transversal
// às versões:
//
//   - DEPRECAÇÃO FORMAL: uma versão passa por deprecated ANTES de qualquer retirada
//     e NUNCA é removida enquanto uma trajectória a referenciar (contador de
//     referências por trajectória) → ErrStillReferenced / ErrNotDeprecated.
//   - ROLLBACK ATÓMICO: repor uma versão anterior como active é um SWAP ÚNICO sob um
//     único lock — a active corrente passa a deprecated e a anterior a active na
//     MESMA secção crítica, sem estado híbrido observável (duas active ou nenhuma).
//
// O catálogo subjacente é APPEND-ONLY: uma versão "retirada" sai da linha de
// versões operável mas o seu registo histórico permanece na fonte de verdade
// (Event Store) — o Lifecycle projecta a operabilidade, não apaga a história.
//
// Seguro para concorrência (um único mutex serializa toda a mutação e o rollback).
// Construir com [NewLifecycle].
type Lifecycle struct {
	mu       sync.Mutex
	id       string
	versions map[domain.Version]*versionState
	active   domain.Version // 0.0.0 = nenhuma active
	tracer   agentruntime.Tracer
	admit    LifecycleAdmissionVerifier // nil = sem gate de re-promoção (default)
}

// LifecycleOption configura o Lifecycle.
type LifecycleOption func(*Lifecycle)

// WithLifecycleTracer injecta a porta de observabilidade. Por omissão NoopTracer.
func WithLifecycleTracer(t agentruntime.Tracer) LifecycleOption {
	return func(l *Lifecycle) {
		if t != nil {
			l.tracer = t
		}
	}
}

// WithLifecycleAdmissionVerifier injecta o gate de re-verificação da promoção a
// active pela via do rollback (deprecated→active). Por omissão nil: sem verificador
// injectado o rollback não re-verifica assinatura/revogação (a defesa incondicional
// é a exclusão de staging e o respeito pela máquina de estados). Um valor nil é
// ignorado (mantém o default).
func WithLifecycleAdmissionVerifier(v LifecycleAdmissionVerifier) LifecycleOption {
	return func(l *Lifecycle) {
		if v != nil {
			l.admit = v
		}
	}
}

// NewLifecycle constrói o gestor de ciclo de vida de uma linha de versões (id).
func NewLifecycle(id string, opts ...LifecycleOption) *Lifecycle {
	l := &Lifecycle{
		id:       id,
		versions: make(map[domain.Version]*versionState),
		tracer:   agentruntime.NoopTracer{},
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// ID devolve o identificador do artefacto desta linha de versões.
func (l *Lifecycle) ID() string { return l.id }

// Track regista uma versão do catálogo na linha de versões com o seu estado. É
// idempotente na presença (re-tracking com o mesmo estado é no-op); um estado
// diferente ACTUALIZA (a fonte de verdade é o Registry — o Lifecycle é a projecção
// operável). Se a versão passar a active, torna-se a active corrente. Fail-closed:
// versão zero ou estado inválido são recusados.
func (l *Lifecycle) Track(v domain.Version, status domain.Status) error {
	if v.IsZero() {
		return ErrUnpinnedResolution
	}
	if !status.Valid() {
		return ErrInvalidRequest
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		vs = &versionState{status: status, refs: map[string]int{}}
		l.versions[v] = vs
	} else {
		vs.status = status
	}
	if status == domain.StatusActive {
		l.active = v
	}
	return nil
}

// Reference incrementa o contador de referências de uma versão para a trajectória
// dada — a trajectória gravou (version, digest) desta versão no seu manifesto
// imutável e o seu replay fiel depende dela. Fail-closed: versão desconhecida →
// ErrUnknownVersion; versão revogada → ErrVersionRevoked; trajectória vazia →
// ErrInvalidRequest.
func (l *Lifecycle) Reference(ctx context.Context, v domain.Version, trajectoryID string) error {
	_, span := l.tracer.StartSpan(ctx, opReference)
	defer span.End()
	span.SetAttribute(AttrArtifactID, l.id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	if trajectoryID == "" {
		span.SetAttribute(AttrDecision, "denied")
		return ErrInvalidRequest
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return ErrUnknownVersion
	}
	if vs.status == domain.StatusRevoked {
		span.SetAttribute(AttrDecision, "denied")
		return ErrVersionRevoked
	}
	vs.refs[trajectoryID]++
	span.SetAttribute(AttrDecision, "referenced")
	return nil
}

// Release liberta uma referência de trajectória previamente registada. Fail-closed:
// libertar uma referência inexistente devolve ErrNoReference (nunca deixa o
// contador ir a negativo — o que faria uma versão referenciada parecer removível).
func (l *Lifecycle) Release(v domain.Version, trajectoryID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		return ErrUnknownVersion
	}
	if vs.refs[trajectoryID] <= 0 {
		return ErrNoReference
	}
	vs.refs[trajectoryID]--
	if vs.refs[trajectoryID] == 0 {
		delete(vs.refs, trajectoryID)
	}
	return nil
}

// RefCount devolve o número total de referências de trajectória de uma versão.
func (l *Lifecycle) RefCount(v domain.Version) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		return 0
	}
	return vs.totalRefs()
}

// ReferenceManifest é a LIGAÇÃO EM CÓDIGO entre o DependencyManifest IMUTÁVEL
// (trajectory_manifest.go) e o contador de referências que Retire consulta
// (AOS-052-Q3). Para cada dependência pinada do manifesto cujo NOME corresponda ao
// id DESTA linha de versões, incrementa a referência da trajectória do manifesto —
// materializando a invariante "uma versão gravada por uma trajectória não é
// removível enquanto essa trajectória a referenciar", sem MUTAR o manifesto (que
// permanece um value type imutável).
//
// Um Lifecycle é por-id (uma linha de versões); um manifesto abrange várias
// dependências de vários ids — por isso só as entradas cujo Name == l.id são
// contadas aqui (as restantes pertencem a outras linhas). O congelamento AUTOMÁTICO
// e transversal de TODO o tool set de um run (varrendo todas as linhas) é o
// congelamento por run (AOS-050); ReferenceManifest é a ponte por-linha que garante
// que a referência contada NÃO diverge do manifesto persistido.
//
// Fail-closed: manifesto sem trajectória → ErrInvalidManifest; versão de uma
// dependência não-parseável → ErrInvalidRequest; versão desconhecida/revogada nesta
// linha propaga o erro de [Reference]. As referências já aplicadas antes de um erro
// mantêm-se (a operação não é transaccional entre dependências distintas; cada
// dependência é uma referência independente, como chamadas separadas a Reference).
func (l *Lifecycle) ReferenceManifest(ctx context.Context, m DependencyManifest) error {
	tid := m.TrajectoryID()
	if tid == "" {
		return ErrInvalidManifest
	}
	for _, d := range m.Deps() {
		if d.Name != l.id {
			continue
		}
		v, err := domain.ParseVersion(d.Version)
		if err != nil {
			return fmt.Errorf("%w: dependencia %q com versao invalida %q", ErrInvalidRequest, d.Name, d.Version)
		}
		if err := l.Reference(ctx, v, tid); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseManifest é o inverso de [ReferenceManifest]: liberta uma referência por
// cada dependência do manifesto que corresponda ao id desta linha (ex.: quando uma
// trajectória é descartada e deixa de exigir replay fiel). Fail-closed idêntico;
// libertar uma referência inexistente propaga ErrNoReference de [Release].
func (l *Lifecycle) ReleaseManifest(m DependencyManifest) error {
	tid := m.TrajectoryID()
	if tid == "" {
		return ErrInvalidManifest
	}
	for _, d := range m.Deps() {
		if d.Name != l.id {
			continue
		}
		v, err := domain.ParseVersion(d.Version)
		if err != nil {
			return fmt.Errorf("%w: dependencia %q com versao invalida %q", ErrInvalidRequest, d.Name, d.Version)
		}
		if err := l.Release(v, tid); err != nil {
			return err
		}
	}
	return nil
}

// Deprecate marca uma versão como deprecated — o passo FORMAL que precede qualquer
// retirada. Uma versão deprecated continua REFERENCIÁVEL (não é removida) mas é
// desencorajada. Fail-closed: versão desconhecida → ErrUnknownVersion; versão
// revogada → ErrVersionRevoked; transição não permitida pela máquina de estados →
// ErrInvalidTransition. Deprecar a active corrente limpa a active (deixa a linha
// sem active até um rollback/promoção repor uma).
func (l *Lifecycle) Deprecate(ctx context.Context, v domain.Version) error {
	_, span := l.tracer.StartSpan(ctx, opDeprecate)
	defer span.End()
	span.SetAttribute(AttrArtifactID, l.id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return ErrUnknownVersion
	}
	if vs.status == domain.StatusDeprecated {
		span.SetAttribute(AttrDecision, "deprecated") // idempotente
		return nil
	}
	if vs.status == domain.StatusRevoked {
		span.SetAttribute(AttrDecision, "denied")
		return ErrVersionRevoked
	}
	if !domain.CanTransition(vs.status, domain.StatusDeprecated) {
		span.SetAttribute(AttrDecision, "denied")
		return domain.ErrInvalidTransition
	}
	vs.status = domain.StatusDeprecated
	if l.active == v {
		l.active = domain.Version{}
	}
	span.SetAttribute(AttrDecision, "deprecated")
	return nil
}

// Retire remove uma versão da linha de versões OPERÁVEL. Impõe as duas invariantes
// da retirada segura (fail-closed):
//
//   - a versão TEM de estar deprecated (deprecação formal prévia) → ErrNotDeprecated;
//   - a versão NÃO PODE ter referências vivas → ErrStillReferenced.
//
// O registo histórico permanece na fonte de verdade append-only; Retire projecta
// apenas a indisponibilidade operacional. Uma versão revogada não é retirável por
// esta via (a revogação é o seu próprio estado terminal) → ErrVersionRevoked.
func (l *Lifecycle) Retire(ctx context.Context, v domain.Version) error {
	_, span := l.tracer.StartSpan(ctx, opRetire)
	defer span.End()
	span.SetAttribute(AttrArtifactID, l.id)
	span.SetAttribute(AttrArtifactVersion, v.String())

	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return ErrUnknownVersion
	}
	if vs.status == domain.StatusRevoked {
		span.SetAttribute(AttrDecision, "denied")
		return ErrVersionRevoked
	}
	if vs.status != domain.StatusDeprecated {
		span.SetAttribute(AttrDecision, "denied")
		return ErrNotDeprecated
	}
	if vs.totalRefs() > 0 {
		span.SetAttribute(AttrDecision, "still_referenced")
		return ErrStillReferenced
	}
	delete(l.versions, v)
	span.SetAttribute(AttrDecision, "retired")
	return nil
}

// Rollback repõe uma versão ANTERIOR como active de forma ATÓMICA: num único
// SWAP sob o lock, a active corrente (se houver) passa a deprecated e o alvo passa
// a active. Não há estado intermédio observável com duas versões active nem com
// nenhuma — [Snapshot], [Active] e qualquer outra operação vêem sempre a linha ou
// no estado ANTERIOR (alvo ainda não-active) ou no POSTERIOR (swap concluído).
//
// Fail-closed: o alvo tem de existir, não estar revogado, não ser já a active
// corrente e estar FORMALMENTE deprecated → ErrNotRollbackable / ErrUnknownVersion /
// ErrVersionRevoked. O rollback só re-promove uma versão ANTERIOR (previamente
// promovida e depois deprecated) — o catálogo append-only garante que ela ainda
// existe.
//
// GATES DE PROMOÇÃO (AOS-052-Q2). A promoção deprecated→active respeita as MESMAS
// invariantes de Registry.SetStatus, que este SWAP não pode contornar:
//
//   - domain.CanTransition: a transição para active tem de ser permitida pela
//     máquina de estados (default-deny);
//   - EXCLUSÃO DE STAGING: uma versão em staging NUNCA foi verificada/promovida e não
//     é alvo de rollback — promovê-la por esta via saltaria a primeira verificação de
//     admissão (AOS-047/048). Só uma versão deprecated é re-activável;
//   - RE-VERIFICAÇÃO (AOS-048 Q1): quando um LifecycleAdmissionVerifier está
//     injectado, a re-activação re-verifica assinatura/revogação (a confiança na
//     origem não é imutável) — um Verify que falhe ABORTA o rollback sem qualquer
//     mutação (ErrAdmissionDenied).
func (l *Lifecycle) Rollback(ctx context.Context, target domain.Version) error {
	_, span := l.tracer.StartSpan(ctx, opRollback)
	defer span.End()
	span.SetAttribute(AttrArtifactID, l.id)
	span.SetAttribute(AttrArtifactVersion, target.String())

	l.mu.Lock()
	defer l.mu.Unlock()

	tvs, ok := l.versions[target]
	if !ok {
		span.SetAttribute(AttrDecision, "not_found")
		return ErrUnknownVersion
	}
	if tvs.status == domain.StatusRevoked {
		span.SetAttribute(AttrDecision, "denied")
		return ErrVersionRevoked
	}
	if l.active == target {
		span.SetAttribute(AttrDecision, "denied")
		return ErrNotRollbackable
	}
	// A transição para active tem de ser permitida pela máquina de estados (o mesmo
	// default-deny de Registry.SetStatus; o rollback não é um atalho para active).
	if !domain.CanTransition(tvs.status, domain.StatusActive) {
		span.SetAttribute(AttrDecision, "denied")
		return domain.ErrInvalidTransition
	}
	// EXCLUSÃO DE STAGING: rollback re-promove uma versão FORMALMENTE deprecated, nunca
	// uma versão staging não-verificada (fecha o bypass staging→active de AOS-047/048).
	if tvs.status != domain.StatusDeprecated {
		span.SetAttribute(AttrDecision, "denied")
		return ErrNotRollbackable
	}
	// RE-VERIFICAÇÃO da re-promoção (AOS-048 Q1) quando um gate está injectado: a
	// reactivação não herda a confiança da primeira promoção. Falha → aborta o SWAP.
	if l.admit != nil && domain.RequiresAdmissionGate(tvs.status, domain.StatusActive) {
		if verr := l.admit.Verify(ctx, target); verr != nil {
			span.SetAttribute(AttrDecision, "admission_denied")
			return fmt.Errorf("%w: %v", ErrAdmissionDenied, verr)
		}
	}
	// SWAP ATÓMICO (secção crítica única): despromove a active corrente e promove o
	// alvo. A ordem das mutações é irrelevante para um observador externo — nenhum
	// consegue intercalar leituras entre elas (o mesmo lock serializa tudo).
	if !l.active.IsZero() {
		if cur, ok := l.versions[l.active]; ok && cur.status == domain.StatusActive {
			cur.status = domain.StatusDeprecated
		}
	}
	tvs.status = domain.StatusActive
	l.active = target
	span.SetAttribute(AttrDecision, "rolled_back")
	return nil
}

// Active devolve a versão active corrente e se existe alguma.
func (l *Lifecycle) Active() (domain.Version, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active.IsZero() {
		return domain.Version{}, false
	}
	return l.active, true
}

// Status devolve o estado de ciclo de vida de uma versão e se ela é conhecida.
func (l *Lifecycle) Status(v domain.Version) (domain.Status, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	vs, ok := l.versions[v]
	if !ok {
		return "", false
	}
	return vs.status, true
}

// LifecycleSnapshot é uma fotografia CONSISTENTE da linha de versões: o estado de
// cada versão conhecida e a active corrente, capturados sob o lock numa única
// passagem. É a base das asserções de atomicidade (nenhuma fotografia mostra duas
// versões active).
type LifecycleSnapshot struct {
	Active    domain.Version
	HasActive bool
	Statuses  map[domain.Version]domain.Status
}

// Snapshot devolve uma fotografia consistente (sob o lock) da linha de versões. A
// contagem de versões em estado active nunca é > 1 (invariante do swap atómico).
func (l *Lifecycle) Snapshot() LifecycleSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	statuses := make(map[domain.Version]domain.Status, len(l.versions))
	for v, vs := range l.versions {
		statuses[v] = vs.status
	}
	return LifecycleSnapshot{
		Active:    l.active,
		HasActive: !l.active.IsZero(),
		Statuses:  statuses,
	}
}

// Versions devolve as versões conhecidas em ordem SemVer crescente (determinismo de
// iteração; nunca a ordem de mapa).
func (l *Lifecycle) Versions() []domain.Version {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]domain.Version, 0, len(l.versions))
	for v := range l.versions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}
