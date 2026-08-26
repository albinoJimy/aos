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
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aos-ref/integration/oidc"
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
	// readResidencyObligation carrega a REGIÃO DE RESIDÊNCIA DO RUN no selo de leitura D6
	// (AOS-182, DEF-202) — para o selo provar a COINCIDÊNCIA leitor.região == run.região que a
	// authz já impôs. É um identificador de GOVERNAÇÃO (não PII), a par do board do leitor.
	readResidencyObligation = "gov.read.residency"
	// readResourceType rotula o Resource cujo Value é o run lido (id, não PII).
	readResourceType = "run"

	// AOS-182 (DEF-202) — RESIDÊNCIA DO RUN. capRunResidency rotula o selo que ESTABELECE a
	// região de residência do run na CRIAÇÃO (submit); residencyToolID identifica o seu produtor
	// (sem PII). O selo é encadeado numa partição POR-RUN dedicada ([readResidencyPartition]),
	// tamper-evidente, e é a fonte que a leitura futura consulta para exigir a coincidência de
	// região — NÃO a região auto-declarada pelo leitor.
	capRunResidency = "residency:run"
	residencyToolID = "gov.residency"
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
	// saude observa os `Append` desta via — ver [saudeDeSelagem]. Pode ser nil (composicao legada).
	saude *saudeDeSelagem
	// regions é a fonte board→região (AOS-094/ADR-011): [govsov.Registry] no caminho legado ou a
	// [SovereignRegionAuthority] com rotação+auditoria (AOS-205). RegionFor resolve fail-closed.
	regions boardRegionResolver
	// cred é a CREDENCIAL FORTE do leitor (AOS-205). Quando composta (OIDC/mTLS verificado contra
	// o IdP de soberania), o principal e o board são DERIVADOS das CLAIMS VERIFICADAS — o header
	// X-Aos-Board auto-declarado deixa de autorizar. nil ⇒ via LEGADA (headers demo-grade),
	// aceite só FORA de produção (a produção exige a credencial — ver main.go).
	cred readCredentialVerifier
	// declararRecusa nomeia, no LOG DO OPERADOR, uma credencial que foi APRESENTADA e
	// RECUSADA. nil ⇒ silencioso (composição legada e testes que não observam o log). Ligado
	// depois da construção, como o `saude` — ver [NewAPIHandler].
	//
	// NÃO altera a resposta: a recusa continua uniforme, pela mesma razão que o 403 do
	// four-eyes o é (não se revela ao chamador qual invariante falhou). O que muda é do lado
	// de DENTRO — ver [readGovernance.autorizarSemMemo] para o porquê e para o filtro que
	// impede isto de ser um vector de enchimento do log.
	declararRecusa func(format string, args ...any)
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
func (g *readGovernance) autorizarSemMemo(r *http.Request) (readerIdentity, bool) {
	var principal, board string
	if g.cred != nil {
		// CREDENCIAL FORTE (AOS-205): o principal e o board vêm das CLAIMS VERIFICADAS do
		// ID-token (OIDC/mTLS contra o IdP de soberania), NÃO dos headers. Um X-Aos-Board
		// forjado (board válido sem credencial, ou credencial de outro titular com outro board)
		// é IGNORADO — a decisão depende só da credencial verificada. Falha de verificação
		// (sem Bearer, assinatura/janela/replay inválidos, sem claim board) ⇒ NEGA fail-closed.
		p, b, err := g.cred.verify(r.Context(), r)
		if err != nil {
			g.nomearCredencialRecusada(err)
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

// nomearCredencialRecusada regista, no log do operador, uma credencial APRESENTADA e recusada.
//
// PORQUÊ. O `err` de [readCredentialVerifier.verify] era descartado: replay do `jti`, assinatura
// inválida, janela fora de prazo e claim `board` em falta colapsavam todos num `false` mudo, e a
// resposta HTTP é — correctamente — uniforme. O resultado era que a causa NÃO EXISTIA em lado
// nenhum: nem na resposta (por desenho), nem no log (por omissão).
//
// O caso que mais custa é o REPLAY, e custa por ser contra-intuitivo: o verificador OIDC marca o
// `jti` como usado ([oidc.Verifier.checkReplay]), pelo que um cliente que reutilize o MESMO token
// é recusado — com a credencial certa, dentro do prazo, e sem nada que o explique.
//
// O ALCANCE É TODO O PEDIDO AUTENTICADO, leituras incluídas, e é isso que o torna caro. Medido
// em produção a 2026-08-26:
//
//	um token,  o MESMO run tres vezes  ->  200, 404, 404
//	token fresco por leitura, 4 runs   ->  200, 200, 200, 200
//
// A segunda leitura em diante é recusada, e a recusa sai como o 404 uniforme e não-enumerável do
// read-path — indistinguível de «esse run não existe». Custou-me várias horas e uma conclusão
// ERRADA (dei quatro runs por perdidos e fui procurar a causa no selo do estado terminal, que
// estava bom). O log não ajudou em nenhum momento, porque não dizia nada. É exactamente esta a
// hora que a declaração abaixo poupa a quem vier a seguir.
//
// O FILTRO NÃO É COSMÉTICO. `ErrNoReadCredential` — não veio Bearer nenhum — é EXCLUÍDO de
// propósito: é o que qualquer sonda anónima produz, e registá-lo daria a quem não está
// autenticado a capacidade de escrever no log do nó à vontade. Só se nomeia o que exigiu uma
// credencial para acontecer.
//
// NUNCA O TOKEN. Os erros de [oidcReadCredential.verify] são tipados e propagados SEM o material
// sensível (o próprio `bearerToken` documenta que o valor não é logado em lado nenhum); o que
// aqui se escreve é o erro, não a credencial.
func (g *readGovernance) nomearCredencialRecusada(err error) {
	if g.declararRecusa == nil || errors.Is(err, ErrNoReadCredential) {
		return
	}
	if errors.Is(err, oidc.ErrTokenReplayed) {
		// Nomeado à parte porque é o único cujo diagnóstico não se deduz: a credencial é
		// válida e o cliente está a fazer algo razoável (reutilizar um token dentro do prazo).
		g.declararRecusa("credencial de leitura RECUSADA por REPLAY de jti: o token ja foi usado noutro pedido. " +
			"A resposta e uniforme por desenho; a causa fica aqui. Quem escreve tem de obter um token NOVO por pedido")
		return
	}
	g.declararRecusa("credencial de leitura RECUSADA (apresentada e invalida): %v", err)
}

// authorizeRead é a authz de LEITURA POR-RUN (AOS-182, DEF-202): resolve o leitor (via
// [readGovernance.authorize]) E impõe a fronteira de SOBERANIA POR-RUN — leitor.região tem de
// COINCIDIR com a REGIÃO DE RESIDÊNCIA do run (a que o submissor selou na criação). Devolve
// (identidade, residência-do-run, true) só quando o leitor está autorizado E a residência
// coincide; caso contrário (_, _, false) — NEGA fail-closed (o chamador devolve o 404 uniforme e
// não-enumerável). É a comparação que faltava: [authorize] resolvia a região do LEITOR mas não
// tinha a região do RUN com que a confrontar, pelo que um leitor de OUTRA região (com credencial
// válida do SEU board) podia ler um run alheio.
//
// RETRO-COMPAT (fixado): um run SEM residência selada (criado no modo LEGADO — sem soberania
// composta no submit — ou por uma via in-process que não passou pela criação soberana) resolve
// para residência AUSENTE e é lido SEM check cross-region, exactamente como o resto do read-path
// D6/D7 se comporta sem topologia soberana. Em modo SOBERANO todo o run criado via ingresso tem
// residência selada (fail-closed no submit), pelo que NENHUM run soberano é servido cross-region.
func (g *readGovernance) authorizeRead(ctx context.Context, r *http.Request, runID string) (readerIdentity, string, bool) {
	id, ok := g.authorize(r)
	if !ok {
		return readerIdentity{}, "", false
	}
	region, ok := g.podeLerRun(ctx, id, runID)
	if !ok {
		return readerIdentity{}, "", false
	}
	return id, region, true
}

// podeLerRun aplica a REGRA DE RESIDÊNCIA a uma identidade JÁ VERIFICADA.
//
// Está separada de [readGovernance.authorizeRead] por uma razão concreta, e não por arrumação: há
// caminhos que precisam de decidir sobre MUITOS runs a partir de UMA credencial, e re-verificar a
// credencial por cada run é impossível — o verificador OIDC tem anti-replay por `jti`
// ([oidc.Verifier.checkReplay] marca o token como usado), pelo que a SEGUNDA verificação do mesmo
// pedido devolve `ErrTokenReplayed`.
//
// Foi exactamente o que aconteceu ao `POST /autonomy/simular` quando o escrevi: autenticava uma
// vez, e depois chamava `authorizeRead` por cada run distinto. Em produção — onde a credencial é
// OIDC — a primeira chamada consumia o `jti` e TODAS as seguintes falhavam como replay, pelo que a
// resposta seria `avaliados: 0` sempre, e em silêncio. O teste não o apanhou porque compunha o
// gate pela via LEGADA de headers, que nunca chama o verificador.
//
// A regra fica AQUI e não duplicada: quem tem a identidade verificada pergunta por run, e a
// resposta é a mesma que o `GET /runs/{id}` daria.
func (g *readGovernance) podeLerRun(ctx context.Context, id readerIdentity, runID string) (string, bool) {
	region, sealed, err := g.runResidency(ctx, runID)
	if err != nil {
		// Falha ao resolver a residência do run ⇒ NEGA fail-closed (nunca se serve na dúvida).
		return "", false
	}
	if !sealed {
		// Run sem residência selada (legado) ⇒ sem check cross-region (retro-compat).
		return "", true
	}
	if !regionsCoincide(id.region, region) {
		// CROSS-REGION: leitor de região != residência do run ⇒ NEGA (nunca servido).
		return "", false
	}
	return region, true
}

// regionsCoincide compara duas regiões de governação. Ambas são saídas de [govsov.Registry.RegionFor]
// (já normalizadas), pelo que a comparação é directa; o EqualFold+TrimSpace é defesa em profundidade.
func regionsCoincide(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// readResidencyPartition é a partição WORM POR-RUN que guarda a RESIDÊNCIA do run (AOS-182,
// DEF-202) — namespaced com "gov.residency/" para não colidir com a cadeia de mediação do RM
// (por RunID cru) nem com os selos de leitura ("gov.read/"). A residência de cada run forma a sua
// própria cadeia tamper-evidente, verificável isoladamente.
func readResidencyPartition(runID string) string { return "gov.residency/" + runID }

// sealResidency ESTABELECE a residência de um run na CRIAÇÃO (submit): sela, de forma durável e
// tamper-evidente na partição por-run, a REGIÃO resolvida do SUBMISSOR (a mesma resolução
// board→região dos leitores, via [readGovernance.authorize] — não uma auto-declaração sem
// verificação). SEM PII: só o principal pseudónimo, o board (identificador de governação) e a
// região. É PRÉ-CONDIÇÃO da hospedagem do run em modo soberano (o chamador recusa o submit se
// isto falhar), para que nenhum run soberano fique legível ANTES de a sua residência estar durável.
func (g *readGovernance) sealResidency(ctx context.Context, submitter readerIdentity, runID string) error {
	// IDEMPOTENTE POR-RunID: a residência é fixada na PRIMEIRA criação e não é re-negociável. Uma
	// re-submissão do MESMO RunID — incluindo uma vinda de credencial de OUTRA região — NÃO
	// acrescenta um segundo registo à partição: [runResidency] já toma o primeiro registo como
	// autoritativo (a fronteira em vigor nunca muda), e saltar o Append aqui evita poluir o trilho
	// tamper-evidente com "submits" de regiões que não residem o run e evita o seu crescimento a cada
	// retry idempotente de svc.Submit. Se já há residência selada, é no-op.
	if _, sealed, err := g.runResidency(ctx, runID); err != nil {
		return err
	} else if sealed {
		return nil
	}
	rec := audit.AuditRecord{
		Partition:  readResidencyPartition(runID),
		Timestamp:  g.now().UTC(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: submitter.principal},
		Capability: capRunResidency,
		RunID:      runID,
		ToolID:     residencyToolID,
		// Resource liga o selo ao run e grava a REGIÃO DE RESIDÊNCIA (soberania), tamper-evidente.
		Resource: audit.Resource{Type: readResourceType, Value: runID, Region: submitter.region},
		// O board do submissor entra como obrigação (identificador de governação, não PII).
		Obligations: []audit.Obligation{{Type: readBoardObligation, Fields: []string{submitter.board}}},
	}
	_, err := g.worm.Append(ctx, rec)
	if g.saude != nil {
		err = g.saude.registar(err)
	}
	return err
}

// runResidency resolve a REGIÃO DE RESIDÊNCIA de um run a partir do selo de criação. Usa o PRIMEIRO
// registo da partição (audit_seq 1) como AUTORITATIVO — a residência é fixada no primeiro submit e
// não é re-negociável por uma re-submissão de outra região (que apenas acrescentaria um registo ao
// trilho, sem mudar a fronteira em vigor). Devolve (região, true, nil) quando há residência selada;
// ("", false, nil) quando a partição está vazia (run legado, sem residência).
func (g *readGovernance) runResidency(ctx context.Context, runID string) (string, bool, error) {
	rec, ok, err := g.worm.At(ctx, readResidencyPartition(runID), 1)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	return rec.Resource.Region, true, nil
}

// seal emite o SELO WORM de leitura sensível (D6): um registo tamper-evident, POR-PARTIÇÃO e
// SEM PII, que regista QUEM leu (principal), o BOARD, o run, a REGIÃO do leitor, a REGIÃO DE
// RESIDÊNCIA do run e o instante. É uma PRÉ-CONDIÇÃO de conformidade — o chamador NEGA a leitura
// fail-closed se esta devolver erro (o WORM não selou). Só metadados/ids entram no selo; o board
// é identificador de governação (não PII) e as regiões são fronteiras de soberania.
//
// SEMÂNTICA (AOS-182, DEF-202 — resolvido): Resource.Region é a região do LEITOR (govsov.RegionFor
// sobre o board do leitor); a obrigação [readResidencyObligation] carrega a REGIÃO DE RESIDÊNCIA
// DO RUN (a que o submissor selou na criação). Como a authz por-run já impôs leitor.região ==
// run.região ANTES de chegar aqui, o selo GRAVA AS DUAS e prova a sua COINCIDÊNCIA — fechando o
// residuo que a versão anterior deixava declarado (o selo afirmava só a região do leitor porque o
// read-path ainda não rastreava board→região por-run). Para um run LEGADO (sem residência selada)
// residency vem vazio e a obrigação é omitida — o selo mantém a forma anterior (retro-compat).
func (g *readGovernance) seal(ctx context.Context, id readerIdentity, residency, runID, capability string) error {
	obligations := []audit.Obligation{{Type: readBoardObligation, Fields: []string{id.board}}}
	if residency != "" {
		// Grava a residência do run (identificador de governação, não PII) — a prova da coincidência.
		obligations = append(obligations, audit.Obligation{Type: readResidencyObligation, Fields: []string{residency}})
	}
	rec := audit.AuditRecord{
		Partition:  readAuditPartition(runID),
		Timestamp:  g.now().UTC(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: id.principal},
		Capability: capability,
		RunID:      runID,
		ToolID:     readAuditToolID,
		// Resource liga o selo ao run lido e à região do LEITOR (soberania), tamper-evidente.
		Resource:    audit.Resource{Type: readResourceType, Value: runID, Region: id.region},
		Obligations: obligations,
	}
	_, err := g.worm.Append(ctx, rec)
	if g.saude != nil {
		err = g.saude.registar(err)
	}
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

// admitSovereignRead aplica a REGRA D7 a um handler de leitura POR-RUN. Quando o gate soberano
// NÃO está composto (readGov nil) devolve a identidade-zero, residência vazia e proceed=true — o
// comportamento LEGADO (sem authz por-chamador), preservado para nós sem soberania configurada: a
// REGRA é FIXA (Carta §4) mas a TOPOLOGIA (regiões/boards reais) é CONDICIONAL ao provisioning.
// Quando composto, resolve o leitor E impõe a fronteira de SOBERANIA POR-RUN (leitor.região ==
// residência do run, AOS-182/DEF-202): um leitor não autorizado OU de outra região que a
// residência do run é NEGADO fail-closed com o 404 UNIFORME e não-enumerável (a MESMA resposta de
// um run inexistente) — sem revelar PII nem a existência de qualquer run. Devolve também a
// residência resolvida do run, para o selo D6 a gravar (prova da coincidência).
func (h *apiHandler) admitSovereignRead(w http.ResponseWriter, r *http.Request, runID string) (readerIdentity, string, bool) {
	if h.readGov == nil {
		return readerIdentity{}, "", true
	}
	id, residency, ok := h.readGov.authorizeRead(r.Context(), r, runID)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return readerIdentity{}, "", false
	}
	return id, residency, true
}

// sealSensitiveRead emite o SELO WORM de leitura sensível (D6) como PRÉ-CONDIÇÃO da leitura.
// Gate não composto (readGov nil) ⇒ no-op legado (true). Composto ⇒ sela; se o WORM NÃO selar,
// NEGA a leitura fail-closed (503) — o selo é obrigação de conformidade, NÃO telemetria
// best-effort (contraste deliberado com o fail-open de AOS-173). Deve ser chamado ANTES de
// comprometer a resposta (headers de 200 / corpo), para que a negação ainda possa devolver um
// status.
func (h *apiHandler) sealSensitiveRead(w http.ResponseWriter, r *http.Request, id readerIdentity, residency, runID, capability string) bool {
	if h.readGov == nil {
		return true
	}
	if err := h.readGov.seal(r.Context(), id, residency, runID, capability); err != nil {
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return false
	}
	return true
}

// authorize resolve a identidade do leitor, VERIFICANDO A CREDENCIAL UMA VEZ POR PEDIDO.
//
// O corpo real está em [readGovernance.autorizarSemMemo]; esta camada só decide se é preciso
// correr. Ver [memoLeitor] para o porquê — e para a razão pela qual isto NÃO enfraquece o
// anti-replay entre pedidos.
//
// Sem memo no contexto (pedido construído à mão, contexto derivado), verifica como sempre
// verificou: degrada para o comportamento anterior, nunca para «aceita sem verificar».
func (g *readGovernance) authorize(r *http.Request) (readerIdentity, bool) {
	m := memoDe(r)
	if m == nil {
		return g.autorizarSemMemo(r)
	}
	if m.feito {
		// SEGUNDA chamada no MESMO pedido. Contá-la é deliberado: a repetição continua a ser um
		// erro de desenho, e o memo só impede que se pague em PRODUÇÃO. Sem este contador, um
		// teste que afirme «a credencial foi verificada uma vez» passaria a passar mesmo com a
		// travessia repetida de volta — o memo absorveria a prova junto com o defeito.
		m.repetidas++
		return m.id, m.ok
	}
	id, ok := g.autorizarSemMemo(r)
	m.feito, m.id, m.ok = true, id, ok
	return id, ok
}
