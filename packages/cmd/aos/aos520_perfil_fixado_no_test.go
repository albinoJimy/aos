package main

// AOS-520 (PR 0) — O PERFIL DA ROTA FICA FIXADO POR RUN, medido pelo nó COMPOSTO.
//
// O molde é o do AOS-519 (o cliente de modelo sai de [parseModelFromEnv], com a pasta de perfis
// assinados, e fala com um provider em httptest) e, para a retoma, o do AOS-490: o nó é recriado
// sobre o MESMO Event Store e o run é re-hospedado pelas duas vias — `POST /runs/{id}/resume` e a
// varredura de crash-resume.
//
// O que se prova aqui:
//
//   - a INÉRCIA: sem perfis assinados o nó não fixa nada, e o registo de retoma tem os bytes da
//     base (os goldens do nó, que correm sem a pasta, são a outra metade);
//   - a FIXAÇÃO: com perfis assinados, o run novo fica fixado no hostRun, o digest vai para o
//     registo de retoma e para o manifesto de cada turno;
//   - as TRÊS SAÍDAS de um run fixado retomado com outro perfil: a API recusa antes de
//     re-hospedar, o crash-resume deixa-o órfão declarado, e reposto o perfil o run conclui;
//   - o run ANTIGO, sem fixação, não é fixado a meio.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/substrate/eventstore"
)

// Os dois perfis assinados destes testes, para o MESMO alias (o que o nó pede): o A é o do
// AOS-519; o B espera outro modelo.
const (
	aos520ModeloA = aos519Novo
	aos520ModeloB = "openai/modelo-b-1.0"
)

var (
	aos520PerfilA = rt.ProfileJSON
	aos520PerfilB = strings.Replace(rt.ProfileJSON, "modelo-novo-4.5", "modelo-b-1.0", 1)
)

func aos520Digest(t *testing.T, perfil string) string {
	t.Helper()
	p, err := modelgateway.ParseRouteProfile([]byte(perfil))
	if err != nil {
		t.Fatal(err)
	}
	return p.Digest()
}

// aos520Assinados carrega os perfis assinados de uma pasta com um registo válido para o perfil dado.
func aos520Assinados(t *testing.T, perfil string) *perfisAssinados {
	t.Helper()
	aos519AmbienteDoGateway(t)
	aos519Pasta(t, perfil)
	assinados, err := loadSignedRouteProfilesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	return assinados
}

// aos520RegistoDaBase são os bytes do registo de retoma de um run SEM perfil fixado, medidos no
// commit da base deste ticket (f2b5ea90, o AOS-519) para o Goal de [aos520GoalDaBase].
const aos520RegistoDaBase = `{"RunID":"run-520-inercia","Principal":{"NHIID":"nhi:agente","AgentID":"","AgentClass":"","Board":"","DelegationChain":null,"Authority":null,"SubjectAuthority":null,"UserID":"","MandateID":"","MandateSigner":"","RequestedBy":"","Subject":""},"Subject":"","Scope":["cap:fs.read"],"Model":{"ModelID":"gpt-4o","Params":null,"Seed":0},"System":"","Tools":null,"AllowedTools":null,"Inputs":null,"Skills":null,"Objective":"o trabalho de um run","MemoryContext":null,"MaxTurns":4,"ParentTraceParent":"","AssemblyVersion":"1.4.0","CompletionMode":"observe"}`

func aos520GoalDaBase() agentruntime.Goal {
	return agentruntime.Goal{RunID: "run-520-inercia", Principal: referencemonitor.Principal{NHIID: "nhi:agente"}, Scope: []string{"cap:fs.read"},
		Objective: "o trabalho de um run", MaxTurns: 4, CompletionMode: agentruntime.CompletionObserve,
		Model: agentruntime.ModelConfig{ModelID: aos486Modelo}, AssemblyVersion: agentruntime.AssemblyVersion140}
}

// INÉRCIA: SEM PERFIS ASSINADOS NÃO SE FIXA NADA, E O REGISTO DE RETOMA TEM OS BYTES DA BASE. Sem
// a pasta (o ponteiro nil) e com a pasta vazia: o Goal de um run novo fica sem digest — para
// qualquer alias da tabela —, o registo de retoma é byte a byte o da base, e a re-escrita na retoma
// também. O nó sem perfis assinados não ganha um campo.
func TestAOS520_No_SemPerfisAssinados_NadaEFixadoEORegistoEODaBase(t *testing.T) {
	for nome, assinados := range map[string]*perfisAssinados{"sem pasta": nil, "pasta vazia": {}} {
		t.Run(nome, func(t *testing.T) {
			for _, p := range modelgateway.RouteProfiles() {
				goal := fixarPerfilCom(agentruntime.Goal{Model: agentruntime.ModelConfig{ModelID: p.Requested}}, assinados)
				if goal.RouteProfileDigest != "" {
					t.Fatalf("sem perfis assinados o run do alias %q ficou fixado em %q", p.Requested, goal.RouteProfileDigest)
				}
			}
			n := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: assinados}
			goal := n.fixarPerfilDoRun(fixarProjeccao(aos520GoalDaBase()))
			rec, err := resumeRecordFromGoal(goal)
			if err != nil {
				t.Fatal(err)
			}
			cru, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			if string(cru) != aos520RegistoDaBase {
				t.Fatalf("o registo de retoma de um run sem perfil fixado nao e o da base:\n veio:  %s\n quero: %s", cru, aos520RegistoDaBase)
			}
			// A retoma: o Goal volta com a marca, a hospedagem não o fixa, e a re-escrita tem os bytes do original.
			retomado := rec.GoalWith("cred")
			if retomado.RouteProfileDigest != agentruntime.RouteProfileDigestUnpinned {
				t.Fatalf("o run sem perfil fixado volta com a marca; veio %q", retomado.RouteProfileDigest)
			}
			reescrito, err := resumeRecordFromGoal(n.fixarPerfilDoRun(fixarProjeccao(retomado)))
			if err != nil {
				t.Fatal(err)
			}
			if outra, _ := json.Marshal(reescrito); string(outra) != aos520RegistoDaBase {
				t.Fatalf("a re-escrita do registo na retoma mudou os bytes:\n veio:  %s", outra)
			}
			// E a conferência da retoma não tem nada a dizer de um run sem perfil fixado.
			if err := n.conferirPerfilFixado(rec); err != nil {
				t.Fatalf("um run sem perfil fixado foi recusado na retoma: %v", err)
			}
		})
	}
	// Com o nó nil (o caminho defensivo das funções do Goal) também não se fixa nada.
	var semNo *Node
	if got := semNo.fixarPerfilDoRun(aos520GoalDaBase()).RouteProfileDigest; got != "" {
		t.Fatalf("no nil fixou %q", got)
	}
}

// A FIXAÇÃO: COM PERFIS ASSINADOS, o run novo fica fixado no perfil EM VIGOR para o seu alias — o
// assinado, quando há um; a entrada da tabela em código, quando nenhum assinado a substituiu; nada,
// quando o alias não tem perfil. Um Goal que já traz digest, ou a marca, não é tocado. O digest vai
// para o registo de retoma e volta dele.
func TestAOS520_No_ComPerfisAssinados_ORunNovoFicaFixado(t *testing.T) {
	assinados := aos520Assinados(t, aos520PerfilA)
	digestA := aos520Digest(t, aos520PerfilA)
	if digestA == aos519DigestsDaTabela[aos486Modelo] {
		t.Fatal("o perfil assinado tem o digest do da tabela: o teste nao distingue nada")
	}
	fixado := func(alias, jaTraz string) string {
		return fixarPerfilCom(agentruntime.Goal{Model: agentruntime.ModelConfig{ModelID: alias}, RouteProfileDigest: jaTraz}, assinados).RouteProfileDigest
	}
	if got := fixado(aos486Modelo, ""); got != digestA {
		t.Fatalf("run novo do alias assinado fixado em %q; quer o digest do perfil ASSINADO %s", got, digestA)
	}
	if got := fixado("k3", ""); got != aos519DigestsDaTabela["k3"] {
		t.Fatalf("run novo de um alias que so a tabela tem fixado em %q; quer o digest da tabela", got)
	}
	if got := fixado("alias-sem-perfil", ""); got != "" {
		t.Fatalf("um alias sem perfil fixou %q", got)
	}
	// Um run RETOMADO não é tocado: nem o que traz outro digest, nem o que traz a marca.
	outro := "sha256:" + strings.Repeat("7", 64)
	if got := fixado(aos486Modelo, outro); got != outro {
		t.Fatalf("o digest de um run retomado foi trocado por %q", got)
	}
	if got := fixado(aos486Modelo, agentruntime.RouteProfileDigestUnpinned); got != agentruntime.RouteProfileDigestUnpinned {
		t.Fatalf("um run antigo, sem fixacao, foi fixado a meio em %q", got)
	}

	// O REGISTO DE RETOMA: o digest vai, volta, e a re-escrita não o troca.
	n := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: assinados}
	goal := n.fixarPerfilDoRun(aos520GoalDaBase())
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RouteProfileDigest != digestA || rec.GoalWith("c").RouteProfileDigest != digestA || n.fixarPerfilDoRun(rec.GoalWith("c")).RouteProfileDigest != digestA {
		t.Fatalf("o digest fixado nao sobreviveu ao registo: registo %q", rec.RouteProfileDigest)
	}
	// O registo do run fixado difere do da base SÓ no campo novo.
	cru, _ := json.Marshal(rec)
	if quer := strings.TrimSuffix(aos520RegistoDaBase, "}") + `,"RouteProfileDigest":"` + digestA + `"}`; string(cru) != quer {
		t.Fatalf("o registo de um run fixado:\n veio:  %s\n quero: %s", cru, quer)
	}
	// Um registo ANTIGO (sem o campo), retomado neste nó que agora fixa: leva a marca, não é
	// fixado, grava os bytes de antes — e segue (o perfil assinado não substituiu uma rota com estado).
	var antigo integration.ResumeRecord
	if err := json.Unmarshal([]byte(aos520RegistoDaBase), &antigo); err != nil {
		t.Fatal(err)
	}
	hospedado := n.fixarPerfilDoRun(antigo.GoalWith("c"))
	if hospedado.RouteProfileDigest != agentruntime.RouteProfileDigestUnpinned {
		t.Fatalf("o run antigo foi fixado a meio: %q", hospedado.RouteProfileDigest)
	}
	reescrito, err := resumeRecordFromGoal(hospedado)
	if err != nil {
		t.Fatal(err)
	}
	if outra, _ := json.Marshal(reescrito); string(outra) != aos520RegistoDaBase {
		t.Fatalf("a re-escrita do registo antigo mudou os bytes: %s", outra)
	}
	if err := n.conferirPerfilFixado(antigo); err != nil {
		t.Fatalf("o run antigo foi recusado: %v", err)
	}
}

// A CONFERÊNCIA DA RETOMA DECIDE PELO DIGEST SELADO NO REGISTO. O mesmo nó, três registos: o do
// run fixado no perfil em vigor segue; o do run fixado noutro perfil é recusado; e o nó sem a
// pasta recusa o run que começou com um perfil assinado. O alias conferido é o que o nó pede.
func TestAOS520_No_AConferenciaDaRetomaUsaODigestDoRegisto(t *testing.T) {
	digestA, digestB := aos520Digest(t, aos520PerfilA), aos520Digest(t, aos520PerfilB)
	noB := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: aos520Assinados(t, aos520PerfilB)}
	registo := func(digest string) integration.ResumeRecord {
		return integration.ResumeRecord{RunID: "r", Model: agentruntime.ModelConfig{ModelID: aos486Modelo}, RouteProfileDigest: digest}
	}
	if err := noB.conferirPerfilFixado(registo(digestB)); err != nil {
		t.Fatalf("run fixado no perfil em vigor: %v", err)
	}
	err := noB.conferirPerfilFixado(registo(digestA))
	if !errors.Is(err, modelgateway.ErrPinnedRouteProfile) || causaDoPerfilFixado(err) != modelgateway.PinnedCauseProfileDifferent {
		t.Fatalf("run fixado no perfil A, num no com o perfil B: %v", err)
	}
	// Sem a pasta (a tabela em código): o run que começou com o perfil assinado não segue.
	semPasta := &Node{modelID: aos486Modelo, modeloAutoritativo: true}
	if err := semPasta.conferirPerfilFixado(registo(digestA)); causaDoPerfilFixado(err) != modelgateway.PinnedCauseProfileDifferent {
		t.Fatalf("run fixado num perfil assinado, num no sem a pasta: %v", err)
	}
	// O nó passou a pedir um alias sem perfil: o modelo em que o run ficou fixado não é servido.
	outroAlias := &Node{modelID: "alias-sem-perfil", modeloAutoritativo: true, perfisDaRota: noB.perfisDaRota}
	if err := outroAlias.conferirPerfilFixado(registo(digestB)); causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("run fixado, num no que pede um alias sem perfil: %v", err)
	}
	if c := causaDoPerfilFixado(errors.New("outro erro")); c != "" {
		t.Fatalf("causa de um erro alheio: %q", c)
	}
}

// QUEM SUBMETE NÃO FIXA NEM DESFIXA: o `POST /runs` recusa um corpo com um campo de perfil, e o
// pedido de run não tem campo por onde um digest — ou a marca de «não fixado» — entre.
func TestAOS520_No_OPedidoHTTPNaoTrazPerfilFixado(t *testing.T) {
	h := &apiHandler{cfg: apiConfig{maxBodyBytes: 1 << 16}}
	for _, corpo := range []string{
		`{"run_id":"r","objective":"o","principal_nhi":"p","route_profile_digest":"none"}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","RouteProfileDigest":"none"}`,
		`{"run_id":"r","objective":"o","principal_nhi":"p","perfil":"sha256:00"}`,
	} {
		var req submitRequest
		r := httptest.NewRequest(http.MethodPost, "/runs", strings.NewReader(corpo))
		if status, ok := h.decodeJSON(httptest.NewRecorder(), r, &req); ok || status != http.StatusBadRequest {
			t.Fatalf("o corpo %s tinha de ser recusado com 400; veio ok=%v status=%d", corpo, ok, status)
		}
	}
	campos := reflect.TypeOf(submitRequest{})
	for i := 0; i < campos.NumField(); i++ {
		nome := strings.ToLower(campos.Field(i).Name + " " + string(campos.Field(i).Tag))
		for _, proibido := range []string{"profile", "perfil", "digest_do_perfil", "route"} {
			if strings.Contains(nome, proibido) {
				t.Fatalf("o pedido de run ganhou um campo por onde o perfil fixado entra: %s", campos.Field(i).Name)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// O nó composto, recriado sobre o mesmo substrato
// ---------------------------------------------------------------------------

// aos520Substrato é o que sobrevive de uma incarnação do nó à seguinte: o Event Store, o cofre das
// chaves por-titular e os aprovadores (sem eles o nó não compõe o registo de retoma).
type aos520Substrato struct {
	// escuta, quando definida, é o Event Store que a incarnação recebe em vez de `store`: o mesmo
	// store, a registar cada Append que lhe pedem (incluindo os que a deduplicação absorve).
	escuta    *aos520StoreQueEscuta
	store     *eventstore.Store
	vault     audit.KeyVault
	approvers []ApproverConfig
}

func aos520NovoSubstrato(t *testing.T) *aos520Substrato {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	return &aos520Substrato{store: store, vault: audit.NewInMemoryKeyVault(nil), approvers: crashResumeApprovers(t)}
}

// aos520No é uma incarnação do nó, com o log do serviço à mão.
type aos520No struct {
	*aos486No
	log *bytes.Buffer
}

// aos520Compor levanta uma incarnação do nó sobre o substrato dado, com o perfil assinado dado
// (vazio ⇒ SEM a pasta de perfis) e a governação da rota dada (vazio ⇒ a variável não existe). O
// provider declara ter servido `servido`.
func aos520Compor(t *testing.T, sub *aos520Substrato, perfil, governacao, servido string) *aos520No {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")
	if perfil == "" {
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_DIR")
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR")
	} else {
		aos519Pasta(t, perfil)
	}
	if governacao == "" {
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
	} else {
		t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", governacao)
	}
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EventStore, cfg.EventStorePath = sub.store, ""
		if sub.escuta != nil {
			cfg.EventStore = sub.escuta
		}
		cfg.DSARVault = sub.vault
		cfg.Approvers = sub.approvers
	})
	n.upstream.pede = "arquivo"
	n.upstream.responde = aos490RespostaDoProvider
	n.upstream.cabecalhos = func(int) map[string]string { return aos505NoCabecalhos(servido, aos505NoAPIBaseB) }
	log := &bytes.Buffer{}
	n.svc.logMu.Lock()
	n.svc.logw = log
	n.svc.logMu.Unlock()
	return &aos520No{aos486No: n, log: log}
}

func (n *aos520No) lido() string {
	n.svc.logMu.Lock()
	defer n.svc.logMu.Unlock()
	return n.log.String()
}

// fechar pára o serviço e o nó desta incarnação: o «nó foi recriado».
func (n *aos520No) fechar(t *testing.T) {
	t.Helper()
	if err := n.svc.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_ = n.node.Close()
}

// aos520ComoOHostRun aplica ao Goal o que o [NodeService.hostRun] lhe fixa antes do registo de
// retoma e do primeiro turno, pela mesma ordem. Serve à incarnação que «crasha» depois do turno 1
// (molde do AOS-490: o turno corre pelo runtime, sem o hostRun). Que o hostRun fixa de facto o
// perfil prova-o [TestAOS520_No_OHostRunFixaEGravaOPerfil], por `POST /runs`.
func aos520ComoOHostRun(n *Node, goal agentruntime.Goal) agentruntime.Goal {
	goal = n.fixarModelo(goal)
	if goal.AssemblyVersion == "" {
		goal.AssemblyVersion = layoutDosRunsNovos(n.estadoDoProvider != nil)
	}
	goal = fixarLayout(goal)
	goal = n.fixarProjeccaoDoRun(goal)
	goal = n.fixarPerfilDoRun(goal)
	return n.fixarConclusao(goal)
}

// aos520TurnoUmECrash dá o turno 1 do run nesta incarnação e «crasha»: o run fica em `running`,
// sem desfecho, com o registo de retoma escrito pela via do nó. Devolve o Goal hospedado.
func aos520TurnoUmECrash(t *testing.T, n *aos520No, runID string) agentruntime.Goal {
	t.Helper()
	ctx := context.Background()
	goal := aos520ComoOHostRun(n.node, agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent}, Objective: aos490Objectivo, MaxTurns: 3,
	})
	prod := goal
	prod.Credential = n.tok
	prod.MaxTurns = 1
	if _, _, err := n.node.Runtime.Run(withModelCredential(ctx, n.tok), prod, nil); err != nil && !errors.Is(err, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("turno 1 antes do crash: %v", err)
	}
	mq, err := state.NewMachine(n.node.EventStore, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mq.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mq.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim do crash simulado: %v", err)
	}
	if err := n.svc.putResumeRecord(ctx, goal); err != nil {
		t.Fatalf("registo de retoma: %v", err)
	}
	return goal
}

// aos520RetomarPelaAPI retoma o run por `POST /runs/{id}/resume`, com a credencial fresca desta
// incarnação, e espera que conclua. O run entra no balde de suspensos à mão, como no AOS-490.
func aos520RetomarPelaAPI(t *testing.T, n *aos520No, runID string) {
	t.Helper()
	n.svc.mu.Lock()
	n.svc.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
	n.svc.mu.Unlock()
	if resp := postJSON(n.h, "POST", "/runs/"+runID+"/resume", map[string]any{"credential": n.tok}); resp.Code != http.StatusAccepted {
		t.Fatalf("a retoma tinha de ser aceite (202); veio %d %s\n%s", resp.Code, resp.Body.String(), n.lido())
	}
	aos486Esperar(t, n.svc, runID)
}

func aos520Estado(t *testing.T, n *aos520No, runID string) state.State {
	t.Helper()
	st, err := n.node.stateGates.currentState(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func aos520NumEventos(t *testing.T, n *aos520No, runID string) int {
	t.Helper()
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

// O HOSTRUN FIXA E GRAVA. Um run submetido por `POST /runs` num nó com um perfil assinado: o
// registo de retoma que o hostRun escreveu traz o digest do perfil assinado, e o manifesto de CADA
// turno também — com a governação da rota DESLIGADA, em que antes não levava digest nenhum. O
// replay reproduz o run a 100%. O controlo — o mesmo run num nó SEM a pasta — não tem o campo no
// registo nem o digest no manifesto.
func TestAOS520_No_OHostRunFixaEGravaOPerfil(t *testing.T) {
	ctx := context.Background()
	digestA := aos520Digest(t, aos520PerfilA)

	t.Run("controlo sem a pasta", func(t *testing.T) {
		n := aos520Compor(t, aos520NovoSubstrato(t), "", "", aos505NoEsperado)
		m := n.correr(t, "run-520-sem-pasta", nil)
		rec, ok, err := n.node.ResumeRecords.Get(ctx, "run-520-sem-pasta")
		if err != nil || !ok {
			t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
		}
		if cru, _ := json.Marshal(rec); rec.RouteProfileDigest != "" || strings.Contains(string(cru), "RouteProfileDigest") {
			t.Fatalf("sem a pasta o registo de retoma ganhou o perfil fixado: %s", cru)
		}
		for i, r := range aos505NoRotas(t, m.eventos) {
			if r.digest != "" || r.check != "" {
				t.Fatalf("turno %d sem a pasta e sem governacao: digest=%q check=%q; o manifesto e o de antes", i+1, r.digest, r.check)
			}
		}
	})

	n := aos520Compor(t, aos520NovoSubstrato(t), aos520PerfilA, "", aos520ModeloA)
	const runID = "run-520-fixado"
	m := n.correr(t, runID, nil)
	rec, ok, err := n.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
	}
	if rec.RouteProfileDigest != digestA {
		t.Fatalf("o registo de retoma escrito pelo hostRun traz o perfil %q; quer o do perfil assinado %s", rec.RouteProfileDigest, digestA)
	}
	rotas := aos505NoRotas(t, m.eventos)
	if len(rotas) != 2 {
		t.Fatalf("turnos = %d", len(rotas))
	}
	for i, r := range rotas {
		// Governação desligada: não há comparação (check vazio), e o digest é o FIXADO.
		if r.digest != digestA || r.check != "" {
			t.Fatalf("turno %d: digest=%q check=%q; num run fixado o manifesto traz o digest do perfil (%s) mesmo sem governacao, e nenhuma comparacao inventada", i+1, r.digest, r.check, digestA)
		}
	}
	// Os pedidos ao provider são os goldens de sempre: a fixação não toca no wire.
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Errorf("pedido %d de um run fixado nao e o golden do AOS-490:\n veio:  %s\n quero: %s", i+1, got, quer)
		}
	}
	aos505NoReplay(t, n.aos486No, runID, m)
}

// AS TRÊS SAÍDAS DA RETOMA DE UM RUN FIXADO. Perfil A assinado, um turno, e o nó é recriado com o
// perfil B assinado para o MESMO alias:
//
//  1. a retoma pela API é RECUSADA (409, com a causa), antes de re-hospedar — o run fica suspenso,
//     sem um evento novo e sem um pedido ao provider;
//  2. a varredura de crash-resume deixa-o ÓRFÃO, declarado e contado — não é selado `failed`;
//  3. reposto o perfil A, a retoma é aceite e o run CONCLUI, com o digest de A nos dois turnos
//     e fidelidade de replay de 100%.
//
// O mesmo se passa num nó a que tiraram a pasta de perfis (a tabela em código tem outro perfil
// para o alias).
func TestAOS520_No_RunFixadoRetomadoComOutroPerfil_AsTresSaidas(t *testing.T) {
	ctx := context.Background()
	digestA := aos520Digest(t, aos520PerfilA)
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-retomado"

	// ===== INCARNAÇÃO A: o perfil A; turno 1; crash.
	noA := aos520Compor(t, sub, aos520PerfilA, "", aos520ModeloA)
	goal := aos520TurnoUmECrash(t, noA, runID)
	if goal.RouteProfileDigest != digestA {
		t.Fatalf("o run novo ficou fixado em %q; quer o perfil A (%s)", goal.RouteProfileDigest, digestA)
	}
	rec, ok, err := noA.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok || rec.RouteProfileDigest != digestA {
		t.Fatalf("registo de retoma: ok=%v err=%v perfil=%q", ok, err, rec.RouteProfileDigest)
	}
	if n := len(noA.upstream.pedidos()); n != 1 {
		t.Fatalf("pedidos da incarnacao A = %d", n)
	}
	eventosAntes := aos520NumEventos(t, noA, runID)
	noA.fechar(t)

	// recusado exercita as saídas 1 e 2 numa incarnação que NÃO tem o perfil A em vigor.
	recusado := func(t *testing.T, no *aos520No) {
		t.Helper()
		// (1) A API.
		no.svc.mu.Lock()
		no.svc.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
		no.svc.mu.Unlock()
		resp := postJSON(no.h, "POST", "/runs/"+runID+"/resume", map[string]any{"credential": no.tok})
		if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), modelgateway.PinnedCauseProfileDifferent) {
			t.Fatalf("a retoma pela API tinha de dar 409 com a causa %s; veio %d %s", modelgateway.PinnedCauseProfileDifferent, resp.Code, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), "sha256:") {
			t.Fatalf("a resposta ao cliente leva digests: %s", resp.Body.String())
		}
		if err := no.svc.Resume(ctx, runID, no.tok); !errors.Is(err, ErrResumePerfilFixado) || !errors.Is(err, modelgateway.ErrPinnedRouteProfile) {
			t.Fatalf("Resume: %v; quer ErrResumePerfilFixado", err)
		}
		no.svc.mu.Lock()
		_, aindaSuspenso := no.svc.suspended[runID]
		_, aCorrer := no.svc.runs[runID]
		delete(no.svc.suspended, runID) // para a varredura o ver como órfão, e não como suspenso
		no.svc.mu.Unlock()
		if !aindaSuspenso || aCorrer {
			t.Fatalf("depois da recusa o run tinha de continuar suspenso e nao hospedado: suspenso=%v a_correr=%v", aindaSuspenso, aCorrer)
		}
		if !strings.Contains(no.lido(), "RECUSADA") || !strings.Contains(no.lido(), "causa="+modelgateway.PinnedCauseProfileDifferent) {
			t.Fatalf("a recusa nao ficou no log do operador:\n%s", no.lido())
		}
		// (2) O crash-resume: um órfão visto, nenhum retomado, declarado com a causa.
		scanned, resumed, err := no.svc.ResumeInterruptedRuns(ctx)
		if err != nil || scanned != 1 || resumed != 0 {
			t.Fatalf("a varredura devia ver 1 orfao e nao retomar nenhum: scanned=%d resumed=%d err=%v", scanned, resumed, err)
		}
		log := no.lido()
		for _, quer := range []string{"crash-resume: run \"" + runID + "\" fixado num perfil de rota", "deixado como orfao", "AOS-520", "causa=" + modelgateway.PinnedCauseProfileDifferent,
			"1 run(s) orfaos", "0 RETOMADO(s)", "1 nao retomado(s) FAIL-CLOSED"} {
			if !strings.Contains(log, quer) {
				t.Fatalf("o orfao nao ficou declarado e contado — falta %q:\n%s", quer, log)
			}
		}
		// Nas duas saídas: o run continua `running` (recuperável), sem um evento novo, e o provider não viu nada.
		if st := aos520Estado(t, no, runID); st != state.Running {
			t.Fatalf("o run ficou em %q; tinha de continuar `running` — nao e selado `failed`", st)
		}
		if got := aos520NumEventos(t, no, runID); got != eventosAntes {
			t.Fatalf("a recusa escreveu no stream do run: %d eventos, eram %d", got, eventosAntes)
		}
		if n := len(no.upstream.pedidos()); n != 0 {
			t.Fatalf("a recusa fez %d pedido(s) ao provider", n)
		}
	}

	// ===== INCARNAÇÃO B: o perfil B, assinado, para o mesmo alias.
	noB := aos520Compor(t, sub, aos520PerfilB, "", aos520ModeloB)
	if got := aos520Digest(t, aos520PerfilB); got == digestA {
		t.Fatal("os dois perfis tem o mesmo digest")
	}
	recusado(t, noB)
	noB.fechar(t)

	// ===== INCARNAÇÃO SEM A PASTA: a tabela em código tem OUTRO perfil para o alias.
	semPasta := aos520Compor(t, sub, "", "", aos505NoEsperado)
	recusado(t, semPasta)
	semPasta.fechar(t)

	// ===== (3) REPOSTO O PERFIL A: o run retoma e conclui. Pela API, com credencial fresca: a
	// varredura de crash-resume não a tem, e com o gateway real o turno vivo exige-a (AOS-278).
	noA2 := aos520Compor(t, sub, aos520PerfilA, "", aos520ModeloA)
	aos520RetomarPelaAPI(t, noA2, runID)
	if st := aos520Estado(t, noA2, runID); st != state.Complete {
		t.Fatalf("o run retomado ficou em %q; quer complete", st)
	}
	if n := len(noA2.upstream.pedidos()); n != 1 {
		t.Fatalf("a retoma devia interrogar o modelo 1 vez (o turno 2; o 1 volta da captura), interrogou %d", n)
	}
	m := noA2.medir(t, runID, 0)
	rotas := aos505NoRotas(t, m.eventos)
	if len(rotas) != 2 {
		t.Fatalf("turnos = %d", len(rotas))
	}
	for i, r := range rotas {
		if r.digest != digestA {
			t.Fatalf("turno %d do run retomado: digest %q; quer o do perfil em que o run esta fixado (%s)", i+1, r.digest, digestA)
		}
	}
	// O registo de retoma continua a ser o do arranque: a re-hospedagem não o trocou.
	if rec, ok, err := noA2.node.ResumeRecords.Get(ctx, runID); err != nil || !ok || rec.RouteProfileDigest != digestA {
		t.Fatalf("registo de retoma depois da retoma: ok=%v err=%v perfil=%q", ok, err, rec.RouteProfileDigest)
	}
	// REPLAY A 100% do run fixado e retomado: reconstrói-se dos artefactos selados.
	aos505NoReplay(t, noA2.aos486No, runID, m)
}

// O RUN ANTIGO NÃO É FIXADO A MEIO. Um run começado num nó SEM a pasta (sem perfil fixado) e
// retomado num nó que agora tem um perfil assinado para o alias: leva a marca de «não fixado»,
// continua — com o perfil novo, como hoje — e conclui; o seu registo de retoma não ganha o campo.
func TestAOS520_No_RunAntigoRetomadoNumNoQueFixa_ContinuaComoHoje(t *testing.T) {
	ctx := context.Background()
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-antigo"

	antes := aos520Compor(t, sub, "", "", aos505NoEsperado)
	goal := aos520TurnoUmECrash(t, antes, runID)
	if goal.RouteProfileDigest != "" {
		t.Fatalf("sem a pasta o run ficou fixado em %q", goal.RouteProfileDigest)
	}
	antes.fechar(t)

	depois := aos520Compor(t, sub, aos520PerfilA, "", aos520ModeloA)
	aos520RetomarPelaAPI(t, depois, runID)
	if st := aos520Estado(t, depois, runID); st != state.Complete {
		t.Fatalf("o run antigo retomado ficou em %q", st)
	}
	rec, ok, err := depois.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("registo: ok=%v err=%v", ok, err)
	}
	if cru, _ := json.Marshal(rec); rec.RouteProfileDigest != "" || strings.Contains(string(cru), "RouteProfileDigest") {
		t.Fatalf("o run antigo foi fixado a meio — o registo ganhou o campo: %s", cru)
	}
	// Não fixado e com a governação desligada: nenhum turno declara digest.
	for i, r := range aos505NoRotas(t, depois.medir(t, runID, 0).eventos) {
		if r.digest != "" {
			t.Fatalf("turno %d do run antigo declara o digest %q: foi tratado como fixado", i+1, r.digest)
		}
	}
}

// A CONFERÊNCIA DO ADAPTADOR, PELO RUNTIME DO NÓ (revisão, A4). No nó, um run fixado noutro perfil
// é recusado antes de ser re-hospedado, pelo que a conferência do adaptador nunca é a primeira a
// falar — é defesa em profundidade. Aqui chama-se o runtime do nó composto DIRECTAMENTE, como um
// embedder, sem passar pela retoma nem pelo hostRun: um Goal fixado num digest que não é o do
// perfil em vigor falha no primeiro turno, com a causa, e NENHUM pedido chega ao provider. O
// controlo — o mesmo Goal fixado no perfil em vigor — dá o turno.
func TestAOS520_No_RuntimeDirecto_OAdaptadorRecusaOGoalFixadoNoutroPerfil(t *testing.T) {
	n := aos520Compor(t, aos520NovoSubstrato(t), aos520PerfilA, "", aos520ModeloA)
	ctx := withModelCredential(context.Background(), n.tok)
	goal := func(runID, fixado string) agentruntime.Goal {
		g := aos520ComoOHostRun(n.node, agentruntime.Goal{
			RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent}, Objective: aos490Objectivo, MaxTurns: 1,
			RouteProfileDigest: fixado, // já vem fixado: a hospedagem não lhe toca
		})
		g.Credential = n.tok
		return g
	}
	outro := goal("run-520-embedder", aos520Digest(t, aos520PerfilB))
	if outro.RouteProfileDigest != aos520Digest(t, aos520PerfilB) {
		t.Fatalf("o Goal fixado foi tocado pela hospedagem: %q", outro.RouteProfileDigest)
	}
	_, _, err := n.node.Runtime.Run(ctx, outro, nil)
	if !errors.Is(err, modelgateway.ErrPinnedRouteProfile) || causaDoPerfilFixado(err) != modelgateway.PinnedCauseProfileDifferent {
		t.Fatalf("o runtime tinha de falhar o turno com a causa %s; veio %v", modelgateway.PinnedCauseProfileDifferent, err)
	}
	if pedidos := len(n.upstream.pedidos()); pedidos != 0 {
		t.Fatalf("o turno recusado fez %d pedido(s) ao provider", pedidos)
	}
	if turnos := aos486Contar(aos490Eventos(t, n.node.EventStore.(*eventstore.Store), "run-520-embedder"), agentruntime.EventTypeTurnRecorded); turnos != 0 {
		t.Fatalf("o turno recusado ficou gravado: %d turn.recorded", turnos)
	}
	// Controlo: fixado no perfil em vigor, o turno é dado (e o run pára no tecto de 1 turno).
	if _, _, err := n.node.Runtime.Run(ctx, goal("run-520-embedder-ok", aos520Digest(t, aos520PerfilA)), nil); err != nil && !errors.Is(err, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("fixado no perfil em vigor: %v", err)
	}
	if pedidos := len(n.upstream.pedidos()); pedidos != 1 {
		t.Fatalf("o controlo devia ter feito 1 pedido; fez %d", pedidos)
	}
}

// aos520StoreQueEscuta é o Event Store do teste com um ouvido no Append: guarda o que lhe PEDEM
// para escrever, antes de a deduplicação por (run_id, step_id) decidir se fica.
type aos520StoreQueEscuta struct {
	*eventstore.Store
	mu      sync.Mutex
	pedidos []eventstore.EventInput
}

func (s *aos520StoreQueEscuta) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	s.mu.Lock()
	s.pedidos = append(s.pedidos, in)
	s.mu.Unlock()
	return s.Store.Append(ctx, streamID, in, opts...)
}

func (s *aos520StoreQueEscuta) turnosPedidos(runID string) []eventstore.EventInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []eventstore.EventInput
	for _, in := range s.pedidos {
		if in.Type == agentruntime.EventTypeTurnRecorded && in.RunID == runID {
			out = append(out, in)
		}
	}
	return out
}

// aos520Folhas achata um documento JSON em caminho -> valor (os valores que não são objectos).
func aos520Folhas(t *testing.T, cru []byte) map[string]string {
	t.Helper()
	var doc any
	if err := json.Unmarshal(cru, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var andar func(prefixo string, v any)
	andar = func(prefixo string, v any) {
		if obj, ok := v.(map[string]any); ok {
			for k, filho := range obj {
				andar(strings.TrimPrefix(prefixo+"."+k, "."), filho)
			}
			return
		}
		b, _ := json.Marshal(v)
		out[prefixo] = string(b)
	}
	andar("", doc)
	return out
}

// O TURNO REPRODUZIDO DE UM RUN FIXADO, NA RETOMA (revisão, A7) — o que acontece, medido.
//
// Run fixado, governação da rota DESLIGADA. O `turn.recorded` do turno 1 traz o digest fixado; a
// captura de não-determinismo desse turno NÃO o guarda (só guarda a rota de um turno comparado).
// Na retoma, o turno 1 é reproduzido da captura e o runtime volta a PEDIR a gravação do seu
// `turn.recorded` — sem o digest. O que fica no log é o ORIGINAL, com digest, porque o Event Store
// deduplica por (run_id, step_id) e o primeiro fica; e o replay dá 100%.
//
// ISTO NÃO É NOVO, NEM É SÓ DO DIGEST. O pedido de re-gravação de um turno reproduzido já diferia
// do original em quatro campos que a captura não guarda ou que só o adaptador declara
// (`served_model_id`, `projection`, `projection_version`, `tools_offered`), em QUALQUER run com o
// gateway, fixado ou não: a retoma assenta na deduplicação, que é contrato de todas as
// implementações do Event Store (conformance `dedup-entre-handles`). O digest fixado junta-se a
// esse conjunto. O teste prende o conjunto EXACTO: quem lhe acrescentar um campo — ou quem passar
// a re-gravar por cima — fá-lo de propósito.
func TestAOS520_No_TurnoReproduzidoNaRetoma_FicaOOriginalNoLog(t *testing.T) {
	ctx := context.Background()
	digestA := aos520Digest(t, aos520PerfilA)
	sub := aos520NovoSubstrato(t)
	const runID = "run-520-reproduzido"

	noA := aos520Compor(t, sub, aos520PerfilA, "", aos520ModeloA)
	aos520TurnoUmECrash(t, noA, runID)
	original := aos505NoTurnos(aos490Eventos(t, sub.store, runID))
	if len(original) != 1 || !bytes.Contains(original[0], []byte(`"route_profile_digest":"`+digestA+`"`)) {
		t.Fatalf("o turno 1 original tinha de trazer o digest fixado: %s", original)
	}
	// O QUE A CAPTURA GUARDA: o plano de replay do turno 1 volta SEM digest (não foi comparado).
	rec, _, err := noA.node.ResumeRecords.Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	plano, err := noA.svc.replayPlanFor(ctx, runID, rec.Titular())
	if err != nil {
		t.Fatal(err)
	}
	if len(plano) != 1 || plano[1].RouteProfileDigest != "" || plano[1].RouteCheck != "" {
		t.Fatalf("a captura de um turno nao comparado mudou: digest=%q check=%q", plano[1].RouteProfileDigest, plano[1].RouteCheck)
	}
	noA.fechar(t)

	// A retoma, com o ouvido no Append.
	sub.escuta = &aos520StoreQueEscuta{Store: sub.store}
	noA2 := aos520Compor(t, sub, aos520PerfilA, "", aos520ModeloA)
	aos520RetomarPelaAPI(t, noA2, runID)
	pedidos := sub.escuta.turnosPedidos(runID)
	if len(pedidos) != 2 {
		t.Fatalf("a retoma devia pedir a gravacao de 2 turn.recorded (o reproduzido e o vivo); pediu %d", len(pedidos))
	}
	// O pedido de re-gravação do turno reproduzido: os campos que tem são os do original, e
	// faltam-lhe EXACTAMENTE estes.
	doOriginal, doPedido := aos520Folhas(t, original[0]), aos520Folhas(t, pedidos[0].Payload)
	var emFalta []string
	for caminho, valor := range doOriginal {
		noPedido, tem := doPedido[caminho]
		if !tem {
			emFalta = append(emFalta, caminho)
		} else if noPedido != valor {
			t.Errorf("o turno reproduzido pede %s=%s; o original tem %s", caminho, noPedido, valor)
		}
	}
	for caminho := range doPedido {
		if _, tem := doOriginal[caminho]; !tem {
			t.Errorf("o turno reproduzido pede um campo que o original nao tem: %s", caminho)
		}
	}
	sort.Strings(emFalta)
	quer := []string{"manifest.model.route_profile_digest", "manifest.model.served_model_id", "manifest.projection", "manifest.projection_version", "tools_offered"}
	if !reflect.DeepEqual(emFalta, quer) {
		t.Fatalf("campos do original que a re-gravacao do turno reproduzido nao leva:\n veio:  %v\n quero: %v", emFalta, quer)
	}
	// NO LOG fica o ORIGINAL, byte a byte, e o turno vivo traz o digest fixado.
	noLog := aos505NoTurnos(aos490Eventos(t, sub.store, runID))
	if len(noLog) != 2 || !bytes.Equal(noLog[0], original[0]) || !bytes.Contains(noLog[1], []byte(`"route_profile_digest":"`+digestA+`"`)) {
		t.Fatalf("os turn.recorded no log depois da retoma: %s", noLog)
	}
	aos505NoReplay(t, noA2.aos486No, runID, noA2.medir(t, runID, 0))
}
