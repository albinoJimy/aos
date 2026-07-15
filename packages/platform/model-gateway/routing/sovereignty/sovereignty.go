// Package sovereignty é a GUARDA DE FRONTEIRA no caminho de FAILOVER do Model
// Gateway (AOS-058, tecnica/06 §5, EPIC-09/AOS-094, ADR-011). É o coração da
// soberania de dados imposta por desenho: quando o endpoint regional primário está
// saturado ou indisponível, o failover só pode escolher um endpoint/chave da MESMA
// fronteira de soberania — NUNCA cross-border. Um failover cross-border
// transferiria PII para fora da jurisdição (o risco "fuga de soberania por
// failover", violação potencial do GDPR).
//
// # Prova ESTRUTURAL (o coração do ticket)
//
// A guarda RECEBE os candidatos e a fronteira e FILTRA fail-closed os cross-border
// ANTES de qualquer escolha — nunca "tenta e depois volta atrás". A selecção
// ([Guard.Failover]) só percorre os SOBREVIVENTES intra-fronteira: um candidato
// cross-border é DESCARTADO ([Decision.Dropped]), não ordenado ao fundo. É, por
// construção, impossível o failover devolver um endpoint fora da fronteira — a
// disponibilidade nunca é comprada à custa da soberania.
//
// # Diagrama de decisão (tecnica/06 §5)
//
//	allowlist (policy/allowlist) → saúde do primário → failover INTRA-fronteira
//	(esta guarda) → REJEIÇÃO se nenhum intra-fronteira.
//
// [Guard.Route] implementa o ramo saúde→failover→rejeição da soberania; sem
// capacidade intra-fronteira REJEITA (backpressure graciosa a montante), nunca
// encaminha cross-border. AOS-059 (router cost/load-aware) sobrepõe a SUA escolha
// APENAS sobre os sobreviventes intra-fronteira que esta guarda autoriza — a
// decisão de soberania que o router consome.
//
// # Determinismo
//
// A saúde do endpoint é INJECTÁVEL ([HealthFunc]) — sem rede nem rand na decisão. A
// selecção entre sobreviventes intra-fronteira é determinista (KeyID ascendente);
// AOS-059 substitui-a pelo seu critério de custo/carga, sempre DENTRO da fronteira.
package sovereignty

// Boundary é a FRONTEIRA DE SOBERANIA (jurisdição legal) — ex.: "eu", "us". Duas
// regiões na mesma Boundary partilham jurisdição; o failover entre elas é
// intra-fronteira. Regiões em Boundaries distintas são cross-border.
type Boundary string

// Endpoint é um candidato de rota: uma chave de infra NÃO-SECRETA (KeyID, ex.:
// "acct-eu-1") e a sua região. NUNCA transporta segredos (ADR-006).
type Endpoint struct {
	KeyID  string
	Region string
}

// Outcome é o veredicto do ramo de soberania do diagrama de decisão.
type Outcome string

const (
	// OutcomePrimary — o endpoint primário está saudável e intra-fronteira: usa-se.
	OutcomePrimary Outcome = "primary"
	// OutcomeFailover — o primário caiu; escolheu-se um alternativo INTRA-fronteira.
	OutcomeFailover Outcome = "failover_intra"
	// OutcomeReject — sem capacidade intra-fronteira: REJEITA fail-closed (nunca
	// cross-border). Backpressure graciosa a montante.
	OutcomeReject Outcome = "reject_no_intra"
)

// Decision é o resultado da guarda de soberania. Chosen só é válido quando Outcome
// é Primary ou Failover. Dropped lista os candidatos cross-border que foram
// FILTRADOS ANTES da selecção — nunca elegíveis (a prova estrutural).
type Decision struct {
	Outcome         Outcome
	Chosen          Endpoint
	Home            Boundary
	RequestedRegion string
	// Dropped são os candidatos cross-border descartados pela guarda (nunca
	// escolhidos). Alimentam o DENY explícito e atribuível quando a rejeição se deve
	// a só existir capacidade fora da fronteira.
	Dropped []Endpoint
}

// CrossBorderBlocked indica que a rejeição se deveu a SÓ existirem candidatos
// cross-border (a guarda tinha para onde ir, mas era fora da fronteira, e recusou).
// É o caso de GOVERNAÇÃO: exige um DENY explícito, registado e atribuível a
// principal + board (ver o registo de governação em policy/allowlist).
func (d Decision) CrossBorderBlocked() bool {
	return d.Outcome == OutcomeReject && len(d.Dropped) > 0
}

// HealthFunc reporta se um endpoint está saudável. INJECTÁVEL: sem rede nem rand na
// decisão (determinismo em teste). Nil ⇒ o primário é tratado como indisponível
// (força a avaliação do failover).
type HealthFunc func(Endpoint) bool

// Guard é a guarda de fronteira. Mapeia regiões → fronteiras de soberania; por
// omissão cada região é a sua própria fronteira (failover só "na mesma região",
// como o diagrama). Agrupar regiões numa jurisdição comum faz-se com [WithBoundary].
type Guard struct {
	boundaries map[string]Boundary
}

// Option configura o [Guard].
type Option func(*Guard)

// WithBoundary agrupa uma região numa fronteira de soberania (jurisdição) explícita
// — ex.: WithBoundary("eu-west", "eu") e WithBoundary("eu-central", "eu") tornam as
// duas regiões intra-fronteira entre si. Sem mapeamento, a fronteira de uma região é
// a própria região.
func WithBoundary(region string, b Boundary) Option {
	return func(g *Guard) {
		if region != "" && b != "" {
			g.boundaries[region] = b
		}
	}
}

// NewGuard constrói a guarda com o mapeamento região→fronteira dado.
func NewGuard(opts ...Option) *Guard {
	g := &Guard{boundaries: make(map[string]Boundary)}
	for _, o := range opts {
		o(g)
	}
	return g
}

// BoundaryOf devolve a fronteira de soberania de uma região (a própria região se
// não houver mapeamento explícito).
func (g *Guard) BoundaryOf(region string) Boundary {
	if b, ok := g.boundaries[region]; ok {
		return b
	}
	return Boundary(region)
}

// SameBoundary indica se duas regiões partilham fronteira de soberania.
func (g *Guard) SameBoundary(a, b string) bool { return g.BoundaryOf(a) == g.BoundaryOf(b) }

// partition separa os candidatos em intra-fronteira e cross-border FACE a home. É a
// filtragem que precede QUALQUER selecção (a prova estrutural). Candidatos com
// KeyID vazio OU região vazia são ignorados (fail-closed): uma região vazia é
// jurisdição INDEFINIDA — nunca "coincide" com uma fronteira e nunca é elegível
// (não é tratada como intra só porque home também seria vazio).
func (g *Guard) partition(home Boundary, candidates []Endpoint) (intra, cross []Endpoint) {
	for _, c := range candidates {
		if c.KeyID == "" || c.Region == "" {
			continue
		}
		if g.BoundaryOf(c.Region) == home {
			intra = append(intra, c)
		} else {
			cross = append(cross, c)
		}
	}
	return intra, cross
}

// Failover decide o failover a partir da região pedida e dos candidatos, quando o
// primário está indisponível. FILTRA os cross-border ANTES de escolher (estrutural):
// a selecção só percorre os sobreviventes intra-fronteira. Sem sobreviventes,
// REJEITA (nunca devolve um cross-border). A escolha determinista é por KeyID
// ascendente — AOS-059 substitui-a pelo seu critério, sempre sobre esta mesma lista
// intra-fronteira.
func (g *Guard) Failover(requestedRegion string, candidates []Endpoint) Decision {
	// Região pedida vazia = jurisdição INDEFINIDA na origem: fail-closed. Nunca
	// resolvemos para a fronteira vazia (que igualaria candidatos também sem região);
	// rejeita-se sem escolher (rejeição simples, não é o caso cross-border).
	if requestedRegion == "" {
		return Decision{Outcome: OutcomeReject, RequestedRegion: requestedRegion}
	}
	home := g.BoundaryOf(requestedRegion)
	intra, cross := g.partition(home, candidates)
	d := Decision{Home: home, RequestedRegion: requestedRegion, Dropped: cross}
	if len(intra) == 0 {
		d.Outcome = OutcomeReject
		return d
	}
	best := intra[0]
	for _, c := range intra[1:] {
		if c.KeyID < best.KeyID {
			best = c
		}
	}
	d.Outcome = OutcomeFailover
	d.Chosen = best
	return d
}

// Route implementa o ramo de soberania do diagrama de decisão (tecnica/06 §5):
//
//	primário saudável E intra-fronteira → PRIMARY
//	senão → failover INTRA-fronteira → REJECT se nenhum intra-fronteira
//
// NUNCA devolve um endpoint fora da fronteira da região pedida. É a decisão de
// soberania que o router (AOS-059) consome antes de aplicar custo/carga.
func (g *Guard) Route(requestedRegion string, primary Endpoint, candidates []Endpoint, healthy HealthFunc) Decision {
	home := g.BoundaryOf(requestedRegion)
	// O primário só é elegível se saudável E intra-fronteira (um primário mal
	// configurado fora da fronteira NUNCA é usado — defesa em profundidade). Uma
	// região pedida vazia ou um primário sem região são fail-closed: jurisdição
	// indefinida nunca coincide (delega ao Failover, que rejeita).
	if healthy != nil && primary.KeyID != "" && requestedRegion != "" && primary.Region != "" && g.BoundaryOf(primary.Region) == home && healthy(primary) {
		return Decision{
			Outcome:         OutcomePrimary,
			Chosen:          primary,
			Home:            home,
			RequestedRegion: requestedRegion,
		}
	}
	return g.Failover(requestedRegion, candidates)
}
