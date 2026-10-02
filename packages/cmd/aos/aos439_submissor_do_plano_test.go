package main

// aos439_submissor_do_plano_test.go — O SUBMISSOR VIAJA ATÉ AO RUN FILHO, DERIVADO PELO NÓ
// (AOS-439), E OS DADOS DO RUN FILHO SÃO DELE (AOS-440).
//
// O caminho inteiro, num nó durável e soberano, com o emissor MANDATADO composto:
//
//	alice (submissora) ── POST /plans ──► fila
//	drenador (lista fechada) ── POST /plans/claim ──► reclamação viva, geração 1
//	drenador ── POST /runs {plan_request: {plano, 1}} com o NHI do mandato v2 ──► run filho
//
// e prova-se: (1) a decisão selada da tool call diz QUEM PEDIU (alice) e SOB QUE MANDATO; (2) o
// conteúdo do run filho — captura do turno e output da tool — é selado sob a KEK da alice, e o
// apagamento dela torna-o ilegível sem tocar no de outro titular; (3) cada falha do vínculo é
// recusada com a mesma 403; (4) um submissor fora dos `requesters` é recusado com o código que o
// `aos-orq` fecha como terminal.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
)

const (
	aos439Alice    = "sub-alice-439"   // a submissora nomeada no mandato
	aos439Mallory  = "sub-mallory-439" // uma submissora que o mandato NÃO nomeia
	aos439Mandato  = "m-439"
	aos439Drenador = govReader // o drenador da fila (tnBaseConfig)
	aos439Outro    = "nhi:drenador-2-439"
)

func aos439Headers(principal string) map[string]string {
	return map[string]string{HeaderReaderPrincipal: principal, HeaderReaderBoard: govBoard}
}

// aos439ToolModel alterna: chamada ímpar ⇒ UMA tool call; par ⇒ resposta final. Runs submetidos
// um de cada vez (espera-se cada um) têm assim exactamente uma tool call cada.
type aos439ToolModel struct {
	mu sync.Mutex
	n  int
}

func (m *aos439ToolModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.mu.Lock()
	m.n++
	n := m.n
	m.mu.Unlock()
	if n%2 == 1 {
		return agentruntime.ModelResponse{
			// O recurso na região do board: o PDP soberano impõe a obrigação `region`.
			ToolCalls: []agentruntime.ToolInvocation{{ToolID: "counter", Capability: durCap, Input: []byte("pedido"),
				ResourceType: "file", ResourceValue: "/docs/a", ResourceRegion: govRegion}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{Text: "feito", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

type aos439Fixture struct {
	node   *Node
	svc    *NodeService
	h      http.Handler
	auto   ed25519.PrivateKey
	humano ed25519.PrivateKey
	// humanoB é um SEGUNDO humano pinado (revisão do AOS-440): o mesmo agente, outro humano.
	humanoB ed25519.PrivateKey
	// sk é o autenticador FIDO2 do `tnHuman`, quando a fixture foi montada com um (AOS-446 fase 1).
	sk *aos446Autenticador
}

// noAOS439 compõe o nó: durável (WAL + WORM em disco), soberano (um board), a cadeia real de
// mediação de uma tool (o molde do AOS-245), o emissor mandatado com um humano pinado e dois
// drenadores.
func noAOS439(t *testing.T) *aos439Fixture {
	t.Helper()
	return noAOS439ComWORM(t, true)
}

// noAOS439ComWORM é o mesmo nó, com a escrita do WORM v4 ligada ou não (AOS_AUDIT_WRITE_V4).
func noAOS439ComWORM(t *testing.T, v4 bool) *aos439Fixture {
	t.Helper()
	epoca := audit.SchemaV3
	if v4 {
		epoca = audit.SchemaV4
	}
	return noAOS439Com(t, epoca, nil)
}

// noAOS439Com é a fixture parametrizada (AOS-446 fase 1): a ÉPOCA que o WORM escreve, e um
// autenticador FIDO2 opcional — quando != nil, o humano `tnHuman` é pinado com a chave de
// HARDWARE dele em vez da seed de software, e os mandatos dele assinam-se em SSHSIG.
func noAOS439Com(t *testing.T, epoca uint8, sk *aos446Autenticador) *aos439Fixture {
	t.Helper()
	return noAOS439Em(t, t.TempDir(), epoca, sk, nil, nil)
}

// noAOS439Em é a mesma fixture sobre um DIRECTÓRIO dado e com o pino do `tnHuman` substituível —
// as duas coisas de que um teste de REINÍCIO precisa: o WAL, o WORM e o registo de retoma
// sobrevivem, e o `.env` é outro (AOS-446 fase 1).
//
// apiOpts (AOS-477) vão ao handler — o E2E do sentido inverso compõe o catálogo de tools.
func noAOS439Em(t *testing.T, dir string, epoca uint8, sk *aos446Autenticador, pinoDoHumano *identity.MandateSigner, kek audit.KeyVault, apiOpts ...APIOption) *aos439Fixture {
	t.Helper()
	signer := durSigner(t)
	entry := counterEntry(t, signer)
	auto, humano := chaveAos427(t, 21), chaveAos427(t, 22)

	cfg := tnBaseConfig()
	cfg.PlanDrainers = []string{aos439Drenador, aos439Outro}
	cfg.AuditWriteSchema = epoca
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.BoardRegions = map[string]string{govBoard: govRegion}
	cfg.Model = &aos439ToolModel{}
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil, entry)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	var err error
	cfg.PDP, err = pdp.Open("../../control-plane/pdp/policies")
	if err != nil {
		t.Fatalf("abrir bundle de politica: %v", err)
	}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, durCap).Set(durAgent, durCap).Set("agent:"+durClass, durCap)
	cfg.MandatedIssuerID = issAutoDeTeste
	cfg.MandatedIssuerPubKey = auto.Public().(ed25519.PublicKey)
	humanoB := chaveAos427(t, 24)
	cfg.MandateSigners = pinosDeSoftwareNo(map[string]ed25519.PublicKey{tnHuman: humano.Public().(ed25519.PublicKey), aos440HumanoB: humanoB.Public().(ed25519.PublicKey)})
	if sk != nil {
		cfg.MandateSigners[tnHuman] = []identity.MandateSigner{sk.pino(t)}
	}
	if pinoDoHumano != nil {
		cfg.MandateSigners[tnHuman] = []identity.MandateSigner{*pinoDoHumano}
	}
	// A CUSTÓDIA DA KEK, partilhada entre dois arranques. Sem ela o vault de referência é
	// in-memory e as KEK morrem com o processo — o registo de retoma, que é cifrado sob a KEK do
	// titular, deixaria de se conseguir ler. Injectá-la é o que a produção faz (Config.DSARVault),
	// e é o que torna o teste de REINÍCIO possível.
	if kek != nil {
		cfg.DSARVault = kek
	}
	// O four-eyes composto é o que compõe o registo de retoma (AOS-021) — é por ele que se mede a
	// retoma do run filho (AOS-440).
	aprovador := chaveAos427(t, 23)
	cfg.Approvers = []ApproverConfig{{Principal: "human:aprovador-439", PubKey: aprovador.Public().(ed25519.PublicKey), Authority: []string{"approve:danger"}}}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (AOS-439): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if err := node.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		return []byte("OUTPUT-SINTETICO-439"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	if node.ResumeRecords == nil {
		t.Fatal("fixture: o registo de retoma tem de estar composto")
	}
	svc, h := newAPI(t, node, apiOpts...)
	return &aos439Fixture{node: node, svc: svc, h: h, auto: auto, humano: humano, humanoB: humanoB, sk: sk}
}

// tokenDoMandato cunha o NHI do run como o timer do servidor: sob um mandato v2 assinado pelo
// humano pinado, que nomeia os `requesters` dados.
func (f *aos439Fixture) tokenDoMandato(t *testing.T, requesters ...string) string {
	t.Helper()
	return f.tokenDoMandatoDe(t, f.humano, tnHuman, aos439Mandato, requesters...)
}

// tokenDoMandatoDe cunha o MESMO agente sob o mandato `mandato` do humano `humanoID` — para os
// testes da retoma distinguirem humano e mandato com o agente igual.
func (f *aos439Fixture) tokenDoMandatoDe(t *testing.T, chave ed25519.PrivateKey, humanoID, mandato string, requesters ...string) string {
	t.Helper()
	agora := tnClock()()
	sm, err := identity.SignMandate(chave, identity.Mandate{
		ID: mandato, Human: humanoID, Board: govBoard, AgentID: durAgent, AgentClass: durClass,
		PolicyRef: "policy://" + durClass, Scope: []string{durCap}, Issuer: issAutoDeTeste, MaxTTLSeconds: 2700,
		NotBefore: agora.Add(-time.Hour).Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
		Requesters: requesters,
	})
	if err != nil {
		t.Fatalf("assinar o mandato: %v", err)
	}
	iss, err := identity.NewIssuer(issAutoDeTeste, f.auto, map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}, identity.WithIssuerClock(tnClock()))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: humanoID, AgentID: durAgent, AgentClass: durClass, PolicyRef: "policy://" + durClass,
		Board: govBoard, UserAuthority: []string{durCap}, Mandate: &sm,
	})
	if err != nil {
		t.Fatalf("cunhar sob o mandato: %v", err)
	}
	return tok.Compact
}

// pedirEReclamar submete um plano como `submissor` e reclama-o como o drenador.
func (f *aos439Fixture) pedirEReclamar(t *testing.T, plano, submissor string) int {
	t.Helper()
	if r := postReq(f.h, "/plans", map[string]any{"run_id": plano, "objective": "objectivo de " + submissor}, aos439Headers(submissor)); r.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d %s", r.Code, r.Body.String())
	}
	r := postReq(f.h, "/plans/claim", nil, aos439Headers(aos439Drenador))
	if r.Code != http.StatusOK {
		t.Fatalf("POST /plans/claim: %d %s", r.Code, r.Body.String())
	}
	var p respostaDeReclamo
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil || p.RunID != plano {
		t.Fatalf("reclamado %+v (%v), esperava %s", p, err, plano)
	}
	return p.Geracao
}

func (f *aos439Fixture) submeterFilho(t *testing.T, runID, cred string, vinculo *vinculoAoPedido, quem string) *httpResult {
	t.Helper()
	r := postReq(f.h, "/runs", submitRequest{RunID: runID, Objective: "trabalho do no", Credential: cred, PlanRequest: vinculo}, aos439Headers(quem))
	return &httpResult{code: r.Code, body: r.Body.String()}
}

type httpResult struct {
	code int
	body string
}

func (f *aos439Fixture) esperar(t *testing.T, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, ok, err := f.svc.Wait(ctx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", runID, ok, err)
	}
}

// decisaoDaTool devolve o registo WORM da tool call do run.
func decisaoDaTool(t *testing.T, node *Node, runID string) audit.AuditRecord {
	t.Helper()
	recs, err := node.WORM.Read(context.Background(), runID, 1, 100)
	if err != nil {
		t.Fatalf("ler o WORM do run %q: %v", runID, err)
	}
	for _, r := range recs {
		if r.ToolID == "counter" {
			return r
		}
	}
	t.Fatalf("nenhuma decisao da tool counter selada na particao %q (%d registos)", runID, len(recs))
	return audit.AuditRecord{}
}

// O CAMINHO INTEIRO — critérios do AOS-439 e do AOS-440 no mesmo run.
func TestAOS439SubmissorViajaAteAoSeloEOsDadosSaoDele(t *testing.T) {
	ctx := context.Background()
	f := noAOS439(t)
	const plano = "plano-439"
	const filho = plano + separadorDoRunFilho + "n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	tok := f.tokenDoMandato(t, aos439Alice)

	if r := f.submeterFilho(t, filho, tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("o run filho com o vinculo valido tinha de ser aceite (201), veio %d %s", r.code, r.body)
	}
	f.esperar(t, filho)

	// (AOS-439) A DECISÃO SELADA DIZ QUEM PEDIU E SOB QUE MANDATO — e a raiz da cadeia continua a
	// ser o humano do mandato (decisão B+: o humano responde, o submissor é selado ao lado).
	dec := decisaoDaTool(t, f.node, filho)
	if dec.Decision != audit.DecisionAllow {
		t.Fatalf("a tool call tinha de ser permitida para o teste medir o selo: %+v", dec)
	}
	if dec.SchemaVersion != audit.SchemaV4 {
		t.Errorf("a decisao nova tem de ser selada em v4, veio %d", dec.SchemaVersion)
	}
	if dec.Principal.RequestedBy != aos439Alice {
		t.Errorf("o selo da decisao tem de nomear quem PEDIU o plano (%q), veio %q", aos439Alice, dec.Principal.RequestedBy)
	}
	if dec.Principal.MandateID != aos439Mandato {
		t.Errorf("o selo da decisao tem de nomear o mandato (%q), veio %q", aos439Mandato, dec.Principal.MandateID)
	}
	if len(dec.Principal.DelegationChain) == 0 || dec.Principal.DelegationChain[0].Sub != "human:"+tnHuman {
		t.Errorf("a raiz da cadeia continua a ser o humano do mandato: %+v", dec.Principal.DelegationChain)
	}
	if err := audit.Verify(ctx, f.node.WORM, filho, 1, dec.AuditSeq); err != nil {
		t.Errorf("a cadeia do run filho tem de verificar: %v", err)
	}

	// E o evento de mediação no Event Store leva os mesmos dois campos.
	eventos, err := f.node.EventStore.Read(ctx, filho, 1)
	if err != nil {
		t.Fatal(err)
	}
	visto := false
	for _, e := range eventos {
		if e.Type != referencemonitor.EventTypeMediated {
			continue
		}
		var p struct {
			Principal struct {
				RequestedBy string `json:"requested_by"`
				MandateID   string `json:"mandate_id"`
			} `json:"principal"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		visto = true
		if p.Principal.RequestedBy != aos439Alice || p.Principal.MandateID != aos439Mandato {
			t.Errorf("o evento de mediacao tem de levar requested_by e mandate_id: %+v", p.Principal)
		}
	}
	if !visto {
		t.Error("nenhum tool.call.mediated no stream do run filho")
	}

	// (AOS-440) O CONTEÚDO É DA ALICE: captura e output da tool selados sob a KEK dela, e NÃO sob
	// a do drenador que chamou o nó.
	capturado, sujeitoDaCaptura := sealedContentOf(t, f.node, filho)
	if sujeitoDaCaptura != aos439Alice {
		t.Fatalf("a captura do run filho tem de ser selada sob o submissor %q, veio %q", aos439Alice, sujeitoDaCaptura)
	}
	saida, selada, sujeitoDoLedger := aos245LedgerRecord(t, f.node.EventStore, filho)
	if !selada || sujeitoDoLedger != aos439Alice {
		t.Fatalf("o output da tool tem de ser selado sob o submissor %q, veio selado=%t sujeito=%q", aos439Alice, selada, sujeitoDoLedger)
	}
	if _, err := audit.OpenContent(f.node.DSARVault, aos439Alice, capturado); err != nil {
		t.Fatalf("antes do apagamento a captura abre sob a alice: %v", err)
	}

	// Um run de OUTRO titular (submetido directamente pelo drenador, sem plano) — o controlo de que
	// o apagamento não é por atacado.
	const outro = "run-439-outro-titular"
	manualTok, err := f.node.Authority.MintForHuman(ctx, tnHuman, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("mint manual: %v", err)
	}
	manual := manualTok.Compact
	if r := f.submeterFilho(t, outro, manual, nil, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("run directo: %d %s", r.code, r.body)
	}
	f.esperar(t, outro)
	capOutro, sujeitoOutro := sealedContentOf(t, f.node, outro)
	if sujeitoOutro != aos439Drenador {
		t.Fatalf("um run sem plano continua sob quem o chamou (%q), veio %q", aos439Drenador, sujeitoOutro)
	}

	// O APAGAMENTO DA ALICE torna o run filho ilegível — captura e output — e não toca no outro.
	if _, err := f.node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-439", SubjectID: aos439Alice}); err != nil {
		t.Fatalf("DSAR erase da alice: %v", err)
	}
	if _, err := audit.OpenContent(f.node.DSARVault, aos439Alice, capturado); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("depois do apagamento da alice a captura tinha de ficar ilegivel (ErrDecrypt), veio %v", err)
	}
	if _, err := audit.OpenContent(f.node.DSARVault, aos439Alice, saida); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("depois do apagamento da alice o output da tool tinha de ficar ilegivel (ErrDecrypt), veio %v", err)
	}
	if _, err := audit.OpenContent(f.node.DSARVault, aos439Drenador, capOutro); err != nil {
		t.Fatalf("o apagamento da alice nao pode tocar no conteudo de outro titular: %v", err)
	}
}

// CADA FALHA DO VÍNCULO É RECUSADA — e com a MESMA 403, e SEM criar o run.
func TestAOS439VinculoRecusaCadaFalha(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-439-v"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	tok := f.tokenDoMandato(t, aos439Alice)
	uniforme := `{"error":"nao autorizado"}`

	casos := []struct {
		nome    string
		runID   string
		vinculo *vinculoAoPedido
		quem    string
	}{
		{"geracao que nao e a da reclamacao", plano + "~n1", &vinculoAoPedido{RunID: plano, Geracao: ger + 1}, aos439Drenador},
		{"plano que nunca foi pedido", "fantasma~n1", &vinculoAoPedido{RunID: "fantasma", Geracao: 1}, aos439Drenador},
		{"drenador que NAO reclamou", plano + "~n1", &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Outro},
		{"quem nao e drenador", plano + "~n1", &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Alice},
		{"run_id que nao e do plano", "outro-plano~n1", &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador},
		{"run_id sem no", plano + "~", &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador},
		{"run_id igual ao plano", plano, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador},
		{"geracao zero", plano + "~n1", &vinculoAoPedido{RunID: plano}, aos439Drenador},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			r := f.submeterFilho(t, c.runID, tok, c.vinculo, c.quem)
			if r.code != http.StatusForbidden || strings.TrimSpace(r.body) != uniforme {
				t.Fatalf("%s: tinha de dar a 403 uniforme, veio %d %s", c.nome, r.code, r.body)
			}
		})
	}
	// NENHUM destes criou o run: o vínculo certo, a seguir, é aceite como run NOVO.
	if r := f.submeterFilho(t, plano+"~n1", tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("o vinculo valido depois das recusas tinha de dar 201 (um run novo), veio %d %s", r.code, r.body)
	}
	f.esperar(t, plano+"~n1")

	// E depois do desfecho da geração, o vínculo dela deixa de valer.
	if r := postReq(f.h, "/plans/outcome", map[string]any{"run_id": plano, "generation": ger, "classe": DesfechoTransitorio}, aos439Headers(aos439Drenador)); r.Code != http.StatusNoContent {
		t.Fatalf("desfecho: %d", r.Code)
	}
	if r := f.submeterFilho(t, plano+"~n2", tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusForbidden {
		t.Fatalf("uma geracao com desfecho ja nao vincula, veio %d %s", r.code, r.body)
	}
}

// O MANDATO v2 RECUSA quem ele não nomeia — com o código que o `aos-orq` fecha como terminal — e
// recusa o run SEM submissor, com a 403 uniforme.
func TestAOS439MandatoRecusaSubmissorForaDosRequesters(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-439-m"
	ger := f.pedirEReclamar(t, plano, aos439Mallory)
	tok := f.tokenDoMandato(t, aos439Alice) // nomeia só a alice

	r := f.submeterFilho(t, plano+"~n1", tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador)
	if r.code != http.StatusForbidden || !strings.Contains(r.body, codigoRequerenteForaDoMandato) {
		t.Fatalf("um submissor fora dos requesters tinha de dar 403 com %s, veio %d %s", codigoRequerenteForaDoMandato, r.code, r.body)
	}
	// Sem vínculo, o mesmo token não corre nada: um v2 exige um submissor derivado.
	r = f.submeterFilho(t, "run-439-sem-vinculo", tok, nil, aos439Drenador)
	if r.code != http.StatusForbidden || strings.Contains(r.body, codigoRequerenteForaDoMandato) {
		t.Fatalf("um run sem submissor sob um mandato v2 tinha de dar a 403 uniforme, veio %d %s", r.code, r.body)
	}
	// A recusa deixa prova no WORM (AOS-435), atribuída a quem chamou.
	recs, err := f.node.WORM.Read(context.Background(), credencialRecusadaPartition, 1, 100)
	if err != nil || len(recs) < 2 {
		t.Fatalf("as recusas por mandato tinham de ser seladas: %d registos, %v", len(recs), err)
	}
}

// SEM GATE SOBERANO não há vínculo: o campo é recusado, não ignorado.
func TestAOS439VinculoSemGateSoberanoERecusado(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	r := postReq(h, "/runs", submitRequest{RunID: "p~n", PlanRequest: &vinculoAoPedido{RunID: "p", Geracao: 1}}, nil)
	if r.Code != http.StatusForbidden {
		t.Fatalf("sem gate soberano o vinculo tinha de ser recusado (403), veio %d %s", r.Code, r.Body.String())
	}
}

// A RECLAMAÇÃO EXPIRADA e a REGIÃO ERRADA — medidas na função, com o relógio e o chamador
// escolhidos pelo teste.
func TestAOS439VinculoExpiradoOuDeOutraRegiao(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-439-t"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	h := &apiHandler{node: f.node}
	drenador := readerIdentity{principal: aos439Drenador, board: govBoard, region: govRegion}
	v := vinculoAoPedido{RunID: plano, Geracao: ger}
	agora := time.Now().UTC()

	if rb, err := h.submissorDoPedido(context.Background(), drenador, plano+"~n1", v, agora); err != nil || rb != aos439Alice {
		t.Fatalf("controlo: o vinculo agora tinha de dar a alice, veio %q %v", rb, err)
	}
	if _, err := h.submissorDoPedido(context.Background(), drenador, plano+"~n1", v, agora.Add(ttlDaReclamacao)); !errors.Is(err, errVinculoRecusado) {
		t.Fatalf("uma reclamacao expirada nao vincula, veio %v", err)
	}
	outraRegiao := drenador
	outraRegiao.region = govRegionUS
	if _, err := h.submissorDoPedido(context.Background(), outraRegiao, plano+"~n1", v, agora); !errors.Is(err, errVinculoRecusado) {
		t.Fatalf("um drenador de outra regiao nao vincula, veio %v", err)
	}
}

// A HIPÓTESE DO DESENHO, medida (AOS-440): em modo soberano o NHIID do run é o principal OIDC de
// quem chama, e a retoma comparava-o com o AgentID da credencial — logo um run escalado era
// irretomável com a SUA própria credencial. Vermelho com a comparação antiga (mutação provada).
func TestAOS440RetomaSoberanaComACredencialDoProprioAgente(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-440-r"
	const filho = plano + "~n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	tok := f.tokenDoMandato(t, aos439Alice)
	if r := f.submeterFilho(t, filho, tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)

	rec, ok, err := f.node.ResumeRecords.Get(context.Background(), filho)
	if err != nil || !ok {
		t.Fatalf("o registo de retoma do run tinha de existir: ok=%t %v", ok, err)
	}
	if rec.Principal.NHIID != aos439Drenador || rec.Principal.AgentID != durAgent || rec.Subject != aos439Alice || rec.Principal.RequestedBy != aos439Alice {
		t.Fatalf("registo de retoma: NHIID=%q AgentID=%q Subject=%q RequestedBy=%q", rec.Principal.NHIID, rec.Principal.AgentID, rec.Subject, rec.Principal.RequestedBy)
	}
	// O run finge-se suspenso (molde do AOS-263) e retoma-se com a credencial do PRÓPRIO agente.
	f.svc.mu.Lock()
	f.svc.suspended[filho] = &runState{runID: filho, suspended: true, done: make(chan struct{})}
	f.svc.mu.Unlock()
	err = f.svc.Resume(context.Background(), filho, tok)
	if errors.Is(err, ErrResumePrincipalMismatch) {
		t.Fatalf("a retoma com a credencial do PROPRIO agente foi recusada como divergente: %v", err)
	}
	if errors.Is(err, ErrResumeCredencialNaoVerifica) {
		t.Fatalf("a credencial do proprio agente, sob um mandato que nomeia a submissora, tinha de passar: %v", err)
	}
	// E um mandato que NÃO nomeia a submissora não retoma o run dela.
	f.svc.mu.Lock()
	f.svc.suspended[filho] = &runState{runID: filho, suspended: true, done: make(chan struct{})}
	f.svc.mu.Unlock()
	if err := f.svc.Resume(context.Background(), filho, f.tokenDoMandato(t, aos439Mallory)); !errors.Is(err, ErrResumeCredencialNaoVerifica) {
		t.Fatalf("a retoma sob um mandato que nao nomeia a submissora tinha de ser recusada, veio %v", err)
	}
}

// aos440HumanoB é o segundo humano pinado da fixture.
const aos440HumanoB = "operator-bob"

// A RETOMA COMPARA AGENTE, HUMANO E MANDATO (revisão do AOS-439/440). O mesmo `agent_id` cunhado
// para OUTRO humano, ou sob OUTRO mandato do mesmo humano, não continua o run. Vermelho com a
// comparação só do agente (mutação provada).
func TestAOS440RetomaComparaHumanoEMandato(t *testing.T) {
	f := noAOS439(t)
	const plano = "plano-440-h"
	const filho = plano + "~n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	if r := f.submeterFilho(t, filho, f.tokenDoMandato(t, aos439Alice), &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)
	rec, ok, err := f.node.ResumeRecords.Get(context.Background(), filho)
	if err != nil || !ok || rec.Principal.UserID != tnHuman || rec.Principal.MandateID != aos439Mandato {
		t.Fatalf("o registo de retoma tem de guardar o humano e o mandato da credencial: UserID=%q MandateID=%q ok=%t %v",
			rec.Principal.UserID, rec.Principal.MandateID, ok, err)
	}
	suspender := func() {
		f.svc.mu.Lock()
		f.svc.suspended[filho] = &runState{runID: filho, suspended: true, done: make(chan struct{})}
		f.svc.mu.Unlock()
	}
	for _, c := range []struct {
		nome string
		tok  string
	}{
		// O MESMO id de mandato, para isolar a guarda do humano (os ids são escolhidos por quem assina).
		{"mesmo agente e id de mandato, OUTRO humano", f.tokenDoMandatoDe(t, f.humanoB, aos440HumanoB, aos439Mandato, aos439Alice)},
		{"mesmo agente e humano, OUTRO mandato", f.tokenDoMandatoDe(t, f.humano, tnHuman, aos439Mandato+"-outro", aos439Alice)},
	} {
		suspender()
		if err := f.svc.Resume(context.Background(), filho, c.tok); !errors.Is(err, ErrResumePrincipalMismatch) {
			t.Errorf("%s: a retoma tinha de dar ErrResumePrincipalMismatch, veio %v", c.nome, err)
		}
	}
	// Controlo: a credencial do mesmo agente, humano e mandato passa a guarda.
	suspender()
	if err := f.svc.Resume(context.Background(), filho, f.tokenDoMandato(t, aos439Alice)); errors.Is(err, ErrResumePrincipalMismatch) {
		t.Fatalf("a credencial do proprio run foi recusada: %v", err)
	}
}

// O SEPARADOR É CONTRATO DOS DOIS LADOS: o nó verifica `<plano>~<nó>` e o `aos-orq` compõe-no.
// Os dois binários não se importam, pelo que o teste lê a constante do orquestrador da fonte.
func TestAOS439SeparadorDoRunFilhoCasaComOOrquestrador(t *testing.T) {
	fonte := lerFonteDeTeste(t, "../aos-orq/node_executor.go")
	if !strings.Contains(fonte, "separadorDoRunFilho = \""+separadorDoRunFilho+"\"") {
		t.Fatalf("o aos-orq nao declara separadorDoRunFilho = %q — os dois lados do contrato <plano>~<no> divergiram", separadorDoRunFilho)
	}
}

// A RECLAMAÇÃO VIVE MAIS DO QUE O PLANO (revisão do AOS-439). O vínculo exige a reclamação viva;
// com o TTL abaixo do prazo do `serve`, um nó do plano que ficasse pronto depois do TTL era recusado
// e a geração inteira perdia-se. O prazo vive no `aos-orq`, que este módulo não importa: lê-se a
// constante da fonte, como o separador.
//
// O PIOR CASO (2.ª ronda da revisão): uma geração não é só o prazo do plano. Antes dele o `serve`
// decompõe — até N tentativas do planeador, cada uma um pedido ao modelo com o seu egress — e
// re-hidrata os payloads do log. Soma-se tudo, das fontes:
//
//	prazoDoPlanoPorOmissao (aos-orq/node_executor.go)
//	+ maxAttempts do planeador (control-plane/orchestrator/planner/planner.go) × egress do modelo
//	+ prazoDeRehidratacao (aos-orq/node_executor.go)
//
// O egress NÃO é constante do código: é `AOS_MODEL_EGRESS_TIMEOUT`, e o valor de produção está no
// `.env.example` do servidor (`#AOS_MODEL_EGRESS_TIMEOUT=120s`). Lê-se de lá; quem subir o egress em
// produção tem de subir o TTL, e é esse `.env.example` que o documenta.
func TestAOS439TTLDaReclamacaoExcedeOPrazoDoPlano(t *testing.T) {
	minutos := func(fonte, rx, nome string) time.Duration {
		t.Helper()
		m := regexp.MustCompile(rx).FindStringSubmatch(fonte)
		if m == nil {
			t.Fatalf("nao encontrei %s na fonte — a forma mudou e o teste tem de a acompanhar", nome)
		}
		n, _ := strconv.Atoi(m[1])
		return time.Duration(n) * time.Minute
	}
	executor := lerFonteDeTeste(t, "../aos-orq/node_executor.go")
	prazo := minutos(executor, `prazoDoPlanoPorOmissao\s*=\s*(\d+)\s*\*\s*time\.Minute`, "prazoDoPlanoPorOmissao")
	rehidratacao := minutos(executor, `prazoDeRehidratacao\s*=\s*(\d+)\s*\*\s*time\.Minute`, "prazoDeRehidratacao")

	m := regexp.MustCompile(`maxAttempts:\s*(\d+),`).FindStringSubmatch(lerFonteDeTeste(t, "../../control-plane/orchestrator/planner/planner.go"))
	if m == nil {
		t.Fatal("nao encontrei o maxAttempts por omissao do planeador")
	}
	tentativas, _ := strconv.Atoi(m[1])

	m = regexp.MustCompile(`(?m)^#?AOS_MODEL_EGRESS_TIMEOUT=(\d+)s\s*$`).FindStringSubmatch(lerFonteDeTeste(t, "../../../deploy/server/.env.example"))
	if m == nil {
		t.Fatal("nao encontrei o AOS_MODEL_EGRESS_TIMEOUT de producao no .env.example do servidor")
	}
	segundos, _ := strconv.Atoi(m[1])
	egress := time.Duration(segundos) * time.Second

	pior := prazo + time.Duration(tentativas)*egress + rehidratacao
	if ttlDaReclamacao <= pior {
		t.Fatalf("ttlDaReclamacao (%s) tem de exceder o pior caso de uma geracao (%s = prazo %s + %d tentativas x egress %s + rehidratacao %s): "+
			"um no pronto depois do TTL e recusado pelo vinculo", ttlDaReclamacao, pior, prazo, tentativas, egress, rehidratacao)
	}
}

// POR OMISSÃO (sem AOS_AUDIT_WRITE_V4) o nó escreve v3: a decisão da tool fica selada como sempre,
// SEM requested_by nem mandate_id no WORM, e os dois continuam no evento de mediação.
func TestAOS439PorOmissaoOWORMEscreveV3EOEventoLevaOSubmissor(t *testing.T) {
	ctx := context.Background()
	f := noAOS439ComWORM(t, false)
	const plano = "plano-439-v3"
	const filho = plano + "~n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	if r := f.submeterFilho(t, filho, f.tokenDoMandato(t, aos439Alice), &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)
	dec := decisaoDaTool(t, f.node, filho)
	if dec.SchemaVersion != audit.SchemaV3 || dec.Principal.RequestedBy != "" || dec.Principal.MandateID != "" {
		t.Fatalf("por omissao o WORM escreve v3 sem os campos novos: v%d %+v", dec.SchemaVersion, dec.Principal)
	}
	eventos, err := f.node.EventStore.Read(ctx, filho, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eventos {
		if e.Type == referencemonitor.EventTypeMediated {
			if !strings.Contains(string(e.Payload), `"requested_by":"`+aos439Alice+`"`) {
				t.Fatalf("com v3 o evento de mediacao continua a levar o submissor: %s", e.Payload)
			}
			return
		}
	}
	t.Fatal("nenhum tool.call.mediated no stream do run filho")
}
