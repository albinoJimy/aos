// AOS-172 (E7) — SOBERANIA/CONFORMIDADE DE LEITURA do nó `aos`. Fecha os três
// entregáveis do ticket REUTILIZANDO a governança que já existe (nada de regra nova):
//
//   - (D7) READ-PATH SOBERANO FAIL-CLOSED. Um gate de authz POR-CHAMADOR sobre os
//     handlers de leitura (GET /runs/{id} e o SSE de trajectória) — a authz que AOS-167
//     DEFERIU para aqui. O leitor declara um principal COM BOARD (headers de leitura
//     demo-grade); o nó resolve board→região pelo MESMO [govsov.Registry] que o PDP usa
//     (AOS-094). Board vazio/desconhecido, região não resolvível, ou credencial de leitura
//     ausente ⇒ NEGA fail-closed com o MESMO 404 uniforme e não-enumerável do resto da API
//     (a razão nomeia SÓ o board — identificador de governação, nunca PII). NÃO se duplica a
//     regra: RegionFor é a autoridade board→região de AOS-094/ADR-011.
//
//   - (D6) SELO WORM DE LEITURA SENSÍVEL. Cada leitura sensível (abertura do stream SSE; o
//     GET de desfecho) sela UM registo na hash-chain WORM tamper-evident (audit.Store.Append)
//     que regista QUEM leu (principal/board), QUE run, a REGIÃO resolvida e o instante —
//     POR-PARTIÇÃO, SEM PII/segredos (só ids/metadados/hashes/board/região). Hoje esta
//     leitura é SILENCIOSA; passa a ser auditada. O selo é uma PRÉ-CONDIÇÃO de conformidade,
//     NÃO telemetria best-effort: se o WORM não selar, a leitura é NEGADA fail-closed —
//     contraste deliberado com o fail-open da observabilidade OTLP de AOS-173.
//
// DEFERIDO (declarado, análogo ao tratamento de D4 para identidade): o PROVISIONAMENTO real
// de regiões/boards (IdP de soberania da organização) fica para EPIC-09/10. Aqui a REGRA
// fail-closed é FIXA (Carta §4) e o registo board→região é DEMO-GRADE, self-hosted por
// config e declarado no arranque — NÃO um IdP de regiões real, NÃO multi-nó/DR.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// Headers de LEITURA demo-grade (D7). O leitor declara a sua identidade de governação —
// principal (NHI de leitura) + BOARD — nestes headers. É o análogo de leitura ao emitter
// assinado do canal de controlo (AOS-160): DEMO-GRADE (a credencial forte OIDC/mTLS do
// leitor é a porta a preencher no IdP de soberania, EPIC-09/10). Fail-closed: ausência de
// qualquer um ⇒ NEGA (nunca uma leitura anónima resolve uma região).
const (
	// HeaderReaderPrincipal declara o principal (NHI) do leitor — identificador de
	// governação, não PII. Selado no WORM como QUEM leu.
	HeaderReaderPrincipal = "X-Aos-Reader"
	// HeaderReaderBoard declara o BOARD do leitor — a CHAVE da soberania por board
	// (AOS-094). O nó resolve-o para a região autorizada; vazio/desconhecido ⇒ NEGA.
	HeaderReaderBoard = "X-Aos-Board"
)

// Capabilities de leitura sensível seladas no WORM (D6) — vocabulário estável, sem PII.
const (
	capReadOutcome    = "read:outcome"
	capReadTrajectory = "read:trajectory"

	// readAuditToolID identifica o produtor do selo de leitura (ToolID), sem PII.
	readAuditToolID = "gov.read"
	// readBoardObligation carrega o board do leitor no selo (identificador de governação).
	readBoardObligation = "gov.read.board"
	// readResourceType rotula o Resource cujo Value é o run lido (id, não PII).
	readResourceType = "run"
)

// readerIdentity é a identidade de governação RESOLVIDA de um leitor autorizado: o principal
// e o board declarados, mais a REGIÃO autorizada que o board resolveu (D7). Só identificadores
// de governação — nunca PII.
type readerIdentity struct {
	principal string
	board     string
	region    string
}

// boardRegionResolver é a regra board→região que o read-path consulta. Ambos o
// [govsov.Registry] cru (via legado) e a [SovereignRegionAuthority] (AOS-205, fonte com
// rotação+auditoria) a satisfazem — a REGRA (RegionFor fail-closed) NÃO se duplica; o que varia
// é a FONTE. Board vazio/desconhecido ⇒ ("", false) ⇒ NEGA.
type boardRegionResolver interface {
	RegionFor(board string) (string, bool)
}

// readGovernance é a costura de soberania/conformidade do read-path do nó (D7+D6). Compõe a
// autoridade board→região (a MESMA regra que o PDP usa, AOS-094) e o WORM durável já composto no
// nó (AOS-170). É imutável após construção e seguro para uso concorrente (a fonte board→região é
// concorrente-segura e o Store também).
type readGovernance struct {
	// regions é a fonte board→região (AOS-094/ADR-011): [govsov.Registry] no caminho legado ou a
	// [SovereignRegionAuthority] com rotação+auditoria (AOS-205). RegionFor resolve fail-closed.
	regions boardRegionResolver
	// cred é a CREDENCIAL FORTE do leitor (AOS-205). Quando composta (OIDC/mTLS verificado contra
	// o IdP de soberania), o principal e o board são DERIVADOS das CLAIMS VERIFICADAS — o header
	// X-Aos-Board auto-declarado deixa de autorizar. nil ⇒ via LEGADA (headers demo-grade),
	// aceite só FORA de produção (a produção exige a credencial — ver main.go).
	cred readCredentialVerifier
	// worm é o audit.Store tamper-evident (o MESMO do nó) onde o selo de leitura é encadeado.
	worm audit.Store
	// now carimba o selo (injectável para testes determinísticos; default time.Now).
	now func() time.Time
}

// newReadGovernance compõe a costura de leitura soberana. regions e worm são obrigatórios (o
// chamador só a compõe quando ambos existem — ver [WithReadSovereignty]/[WithSovereignAuthority]
// e o auto-wiring de [NewAPIHandler]). cred nil ⇒ via LEGADA por headers (demo-grade); composta
// ⇒ credencial forte verificada. now nil cai em time.Now.
func newReadGovernance(regions boardRegionResolver, cred readCredentialVerifier, worm audit.Store, now func() time.Time) *readGovernance {
	if now == nil {
		now = time.Now
	}
	return &readGovernance{regions: regions, cred: cred, worm: worm, now: now}
}

// authorize aplica a REGRA D7 fail-closed a um pedido de leitura: extrai o principal+board dos
// headers de leitura e resolve o board para a sua região autorizada pelo [govsov.Registry].
// Devolve (identidade resolvida, true) SÓ quando o principal e o board estão presentes E o
// board resolve para uma região autorizada; caso contrário (_, false) — NEGA fail-closed. NÃO
// revela PII nem a existência de qualquer run (a decisão depende só dos headers do leitor e do
// registo GOV, nunca do run pedido).
func (g *readGovernance) authorize(r *http.Request) (readerIdentity, bool) {
	var principal, board string
	if g.cred != nil {
		// CREDENCIAL FORTE (AOS-205): o principal e o board vêm das CLAIMS VERIFICADAS do
		// ID-token (OIDC/mTLS contra o IdP de soberania), NÃO dos headers. Um X-Aos-Board
		// forjado (board válido sem credencial, ou credencial de outro titular com outro board)
		// é IGNORADO — a decisão depende só da credencial verificada. Falha de verificação
		// (sem Bearer, assinatura/janela/replay inválidos, sem claim board) ⇒ NEGA fail-closed.
		p, b, err := g.cred.verify(r.Context(), r)
		if err != nil {
			return readerIdentity{}, false
		}
		principal, board = p, b
	} else {
		// VIA LEGADA demo-grade (aceite só FORA de produção): o leitor declara principal+board
		// nos headers. Fail-closed na mesma: ausência de qualquer um ⇒ NEGA (nunca anónimo).
		principal = strings.TrimSpace(r.Header.Get(HeaderReaderPrincipal))
		board = strings.TrimSpace(r.Header.Get(HeaderReaderBoard))
	}
	// Credencial de leitura ausente ⇒ NEGA (nunca uma leitura anónima é autorizada).
	if principal == "" || board == "" {
		return readerIdentity{}, false
	}
	// Board→região é a autoridade de AOS-094: board vazio/desconhecido, ou região não
	// resolvível ⇒ (_, false) ⇒ NEGA (nunca cross-border/leitura por omissão — ADR-011). Com a
	// credencial forte, `board` é o claim VERIFICADO — resolver aqui é resolver a fronteira que
	// o IdP asseriu, não a que o cliente afirmou.
	region, ok := g.regions.RegionFor(board)
	if !ok {
		return readerIdentity{}, false
	}
	return readerIdentity{principal: principal, board: board, region: region}, true
}

// seal emite o SELO WORM de leitura sensível (D6): um registo tamper-evident, POR-PARTIÇÃO e
// SEM PII, que regista QUEM leu (principal), o BOARD, o run, a REGIÃO resolvida e o instante.
// É uma PRÉ-CONDIÇÃO de conformidade — o chamador NEGA a leitura fail-closed se esta devolver
// erro (o WORM não selou). Só metadados/ids entram no selo; o board é identificador de
// governação (não PII) e a região é a fronteira resolvida.
//
// SEMÂNTICA do Resource.Region (declarada — para NÃO ler enganosamente em compliance):
// a região selada é a que o BOARD DO LEITOR resolve (govsov.RegionFor), NÃO a residência
// real dos dados do run. Ao contrário de [pdp.PDP.applySovereignty] (que vincula o board do
// RECURSO), o read-path do nó ainda não rastreia board→região POR-RUN, pelo que o selo afirma
// a região DECLARADA pelo leitor. Quando o board→região por-run existir (provisioning real,
// EPIC-09/10) acrescenta-se a verificação de coincidência (leitor.região == run.região)
// fail-closed, alinhando com [govsov.Registry] — DEFERIDO, análogo ao tratamento de D4.
func (g *readGovernance) seal(ctx context.Context, id readerIdentity, runID, capability string) error {
	rec := audit.AuditRecord{
		Partition:  readAuditPartition(runID),
		Timestamp:  g.now().UTC(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: id.principal},
		Capability: capability,
		RunID:      runID,
		ToolID:     readAuditToolID,
		// Resource liga o selo ao run lido e à região resolvida (soberania), tamper-evidente.
		Resource: audit.Resource{Type: readResourceType, Value: runID, Region: id.region},
		// O board do leitor entra como obrigação (identificador de governação, não PII).
		Obligations: []audit.Obligation{{Type: readBoardObligation, Fields: []string{id.board}}},
	}
	_, err := g.worm.Append(ctx, rec)
	return err
}

// readAuditPartition é a partição WORM dedicada às leituras sensíveis de um run — namespaced
// com o prefixo "gov.read/" para NÃO colidir com a cadeia de mediação do RM (que particiona
// por RunID cru). Assim as leituras de um run formam a sua PRÓPRIA cadeia tamper-evidente,
// verificável isoladamente (Verify por-partição).
func readAuditPartition(runID string) string { return "gov.read/" + runID }

// ---------------------------------------------------------------------------
// Integração nos handlers de leitura (D7 authz + D6 selo)
// ---------------------------------------------------------------------------

// admitSovereignRead aplica a REGRA D7 a um handler de leitura. Quando o gate soberano NÃO
// está composto (readGov nil) devolve a identidade-zero e proceed=true — o comportamento
// LEGADO (sem authz por-chamador), preservado para nós sem soberania configurada: a REGRA é
// FIXA (Carta §4) mas a TOPOLOGIA (regiões/boards reais) é CONDICIONAL ao provisioning, que
// fica DEFERIDO. Quando composto, NEGA fail-closed com o 404 UNIFORME e não-enumerável (a
// MESMA resposta de um run inexistente) se o leitor não estiver autorizado — sem revelar PII
// nem a existência de qualquer run.
func (h *apiHandler) admitSovereignRead(w http.ResponseWriter, r *http.Request) (readerIdentity, bool) {
	if h.readGov == nil {
		return readerIdentity{}, true
	}
	id, ok := h.readGov.authorize(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return readerIdentity{}, false
	}
	return id, true
}

// sealSensitiveRead emite o SELO WORM de leitura sensível (D6) como PRÉ-CONDIÇÃO da leitura.
// Gate não composto (readGov nil) ⇒ no-op legado (true). Composto ⇒ sela; se o WORM NÃO selar,
// NEGA a leitura fail-closed (503) — o selo é obrigação de conformidade, NÃO telemetria
// best-effort (contraste deliberado com o fail-open de AOS-173). Deve ser chamado ANTES de
// comprometer a resposta (headers de 200 / corpo), para que a negação ainda possa devolver um
// status.
func (h *apiHandler) sealSensitiveRead(w http.ResponseWriter, r *http.Request, id readerIdentity, runID, capability string) bool {
	if h.readGov == nil {
		return true
	}
	if err := h.readGov.seal(r.Context(), id, runID, capability); err != nil {
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return false
	}
	return true
}
