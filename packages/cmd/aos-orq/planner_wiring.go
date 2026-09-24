package main

// planner_wiring.go — T2 do AOS-388: compõe o pipeline goal→DAG multi-nó no aos-orq.
//
// É a metade que faltava do fluxo F2E-02: em vez de receber os nós por `--nodes` ou um
// PlanDocument pronto por `--plan-doc`, o `--goal` corre o PLANEADOR GOVERNADO —
// `planner.Planner.Decompose` (mediação RM, reserva de orçamento CAS, emissão da NHI
// `agent:planner`, N tentativas) → `planvalidate.Validate` (AOS-231) → materialização
// com o DELEGATOR REAL (fim do `recusaSpawn`). O gate humano (AOS-236) fica FORA deste
// âmbito por decisão do dono — é o eixo do DEF-274, ticket próprio.
//
// # A costura do modelo
//
// O `decompose.LLMDecomposer` chama uma porta `decompose.Model`. O modelo REAL (sobre o
// Model Gateway) é a tarefa T2-B; até lá, o único `Model` disponível neste binário é o
// [fixtureModel] — DECLARADAMENTE NÃO-PRODUÇÃO — que devolve um PlanDocument de um
// ficheiro, para o pipeline (governação + validação + spawn real) ser exercido
// ponta-a-ponta sem um LLM vivo. Sem `--decompose-fixture` e sem o gateway, o `--goal`
// recusa fail-closed.
//
// # Identidade
//
// O backbone de identidade é REAL: um emissor ed25519 EFÉMERO (uma execução = um
// processo sob lease) sela uma raiz `human:<worker>` e emite o token NHI do run, cuja
// cadeia de delegação termina no humano (ADR-003). O RM é o mínimo (`rm.New()` +
// `Register`) — o "RM real" do próprio teste do planeador; a cadeia PDP completa
// (NewProductionSecure) é endurecimento posterior.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	orchestrator "github.com/aos-ref/control-plane/orchestrator"
	decompose "github.com/aos-ref/control-plane/orchestrator/decompose"
	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	planmaterialize "github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	planner "github.com/aos-ref/control-plane/orchestrator/planner"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	planvalidate "github.com/aos-ref/control-plane/orchestrator/planvalidate"
	runlifecycle "github.com/aos-ref/control-plane/runlifecycle"
	rm "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

const (
	// capPlan é a capability que flui do token do run para a NHI agent:planner (o
	// IssueChild exige Authority ⊆ folha-do-pai ∩ Scope-da-classe-do-filho).
	capPlan = "cap:plan"
	// classeCoordenador é a classe do agente-run (raiz da árvore de delegação do run).
	classeCoordenador = "coordinator"
	// classePlaneador é a classe da NHI agent:planner (o sub-agente que decompõe).
	classePlaneador = "planner"
	// classeWorker é a classe das NHIs filhas dos papéis-que-expandem, cunhadas pelo
	// Delegator no DESPACHO governado (AOS-390, ADR-024). Sem a registar no emissor, o
	// spawn de um papel falha com E_UNKNOWN_CLASS (correcção habilitadora do AOS-393,
	// preservada pelo ADR-024 — muda o momento do spawn, não a sua exigência de identidade).
	classeWorker = "worker"
	// tokenTTL é o tempo de vida dos tokens efémeros deste processo. Curto: o run vive
	// numa só execução sob lease.
	tokenTTL = 30 * time.Minute
)

// fixtureModel é um [decompose.Model] NÃO-PRODUÇÃO: devolve um PlanDocument lido de ficheiro.
// Existe para exercitar o pipeline do Planner ponta-a-ponta sem um LLM vivo. O binário só o usa
// quando `--decompose-fixture` é dado.
//
// AOS-415: aceita VÁRIOS documentos (o flag separa-os por vírgula) e devolve um por TENTATIVA,
// repetindo o último. É o que permite exercitar, pelo processo real, o caso que a produção mediu:
// a 1.ª decomposição recusada pela validação e a 2.ª admitida.
type fixtureModel struct {
	conteudos []string
	chamadas  int
}

func (m *fixtureModel) Complete(_ context.Context, _, _ string) (string, error) {
	i := m.chamadas
	m.chamadas++
	if i >= len(m.conteudos) {
		i = len(m.conteudos) - 1
	}
	return m.conteudos[i], nil
}

// modeloDeDecomposicao escolhe o [decompose.Model] do `--goal`. Enquanto o Model Gateway
// não é composto (T2-B), a única fonte é o fixture NÃO-PRODUÇÃO; sem ele, o `--goal`
// recusa fail-closed — não há decomposição real por LLM neste binário ainda, e admitir
// um modelo vazio seria a capacidade-fantasma que o banner de postura existe para evitar.
func modeloDeDecomposicao(fixturePath string) (decompose.Model, error) {
	if fixturePath == "" {
		// Sem fixture: a decomposição usará o Model Gateway (AOS-391), composto em
		// decomporEMaterializar sob a NHI do run. Se também não houver gateway
		// (AOS_MODEL_ENDPOINT ausente), o chamador recusa fail-closed. nil ⇒ "usar gateway".
		return nil, nil
	}
	return carregarFixtureModel(fixturePath)
}

// carregarFixtureModel lê os ficheiros-fixture do decompositor — um por TENTATIVA, separados
// por vírgula (AOS-415). Fail-closed: um ficheiro vazio não é um modelo.
func carregarFixtureModel(path string) (*fixtureModel, error) {
	var conteudos []string
	for _, p := range strings.Split(path, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("fixture do decompositor %q: %w", p, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return nil, fmt.Errorf("fixture do decompositor %q vazio", p)
		}
		conteudos = append(conteudos, string(raw))
	}
	if len(conteudos) == 0 {
		return nil, fmt.Errorf("fixture do decompositor %q vazio", path)
	}
	return &fixtureModel{conteudos: conteudos}, nil
}

// decomporEMaterializar corre o pipeline goal→DAG multi-nó sob a posse deste run: compõe
// o backbone (identidade real + RM mínimo + orçamento partilhado), decompõe o objectivo
// pelo PLANEADOR GOVERNADO, valida a estrutura (AOS-231), passa pelo GATE DE APROVAÇÃO
// (AOS-408) e materializa com o DELEGATOR REAL. Fail-closed em cada passo.
//
// Um plano de risco (`danger`, ou lacuna de capacidade) NÃO materializa aqui: fica PENDENTE de
// decisão humana como facto no log e a função devolve [errPlanoPendente], que o chamador traduz na
// saída própria. A decisão é dada por fora, pelo subcomando `decide` — é o que torna a aprovação
// ASSÍNCRONA em vez de prender o processo à espera de um humano.
//
// govAudit é o WORM durável de governação do gateway resolvido do ambiente (AOS-395), ou nil
// para o MemStore de referência. Só é usado quando a decomposição vai pelo gateway vivo.
// planOut, quando dado, é o ficheiro onde o documento pendente é escrito para o humano o rever e
// para o `decide` o reapresentar (o documento cru NÃO vive no log, ADR-005 — só o seu hash).
func decomporEMaterializar(ctx context.Context, ten *runlifecycle.Tenure, store runlifecycle.EventStore, rec *runlifecycle.PlanRecorder, snap planvalidate.Snapshot, goal string, model decompose.Model, gwCfg *gatewayConfig, worker string, govAudit audit.Store, planOut string, exe *configDoExecutor) error {
	runID := ten.RunID()

	// (1)–(3) BASE DE EXECUÇÃO — identidade real, RM mínimo e orçamento partilhado. É a MESMA que
	// o `--plan-doc` compõe (AOS-412): as duas vias só diferem na ORIGEM do documento.
	b, err := comporBaseDeExecucao(ctx, runID, worker, snap)
	if err != nil {
		return err
	}

	// (3-bis) MODEL GATEWAY (AOS-391) — quando NÃO há fixture, o `model` chega nil e a
	// decomposição usa o LLM vivo via Model Gateway, sob a identidade do issuer efémero e o
	// token do run (que sela `model:invoke`). O verifier trusta o issuer deste run. Sem
	// gateway configurado, é fail-closed (a montante, em main).
	if model == nil {
		verifier := identity.NewVerifier(identity.WithTrustedIssuer("iss:aos-orq", b.iss.PublicKey()))
		gwModel, mErr := construirModeloGateway(ctx, gwCfg, verifier, b.tokenDoRun, govAudit)
		if mErr != nil {
			return fmt.Errorf("model gateway: %w", mErr)
		}
		model = gwModel
	}

	// (4) DECOMPOSER + PLANEADOR GOVERNADO.
	dec, err := decompose.New(model, decompose.WithModelID("aos-orq/decompose"), decompose.WithCapabilities(renderCapabilities(snap)))
	if err != nil {
		return fmt.Errorf("decompositor: %w", err)
	}
	// AOS-415: a validação estrutural (AOS-231) entra NO LAÇO de tentativas do planeador. Antes
	// corria só aqui a jusante, e uma recusa era terminal — medido em produção nas validações do
	// AOS-412 e do AOS-414, as duas com a 1.ª decomposição recusada e o `serve` a acabar.
	pl, err := planner.NewPlanner(b.bud, b.mon, b.iss, dec, planner.WithValidator(validadorDoSnapshot{snap: snap}))
	if err != nil {
		return fmt.Errorf("planeador: %w", err)
	}

	// (5) DECOMPOSE(goal) — sob mediação, reserva e NHI filha reais.
	res, err := pl.Decompose(ctx, planner.DecomposeRequest{
		RunID:             runID,
		PlanID:            rec.PlanID(),
		ParentBudgetNode:  runID,
		PlannerBudgetNode: runID + "-planner",
		ParentToken:       b.tokenDoRun,
		Child: identity.ChildRequest{
			AgentID:    "agent:planner",
			AgentClass: classePlaneador,
			Authority:  []string{capPlan},
		},
		Context: planner.PlanningContext{
			Goal:             goal,
			ContextUnits:     int64(len(goal)/4) + 1,
			CapabilitiesHash: snap.Hash,
		},
	})
	if err != nil {
		return fmt.Errorf("decomposição do objectivo: %w", err)
	}
	fmt.Printf("decomposto: objectivo -> plano de %d nos (tentativas=%d, planner_nhi=%s)\n", len(res.Doc.Nodes), res.Attempts, res.PlannerNHI)

	// (6) VALIDAÇÃO ESTRUTURAL (AOS-231) — fail-closed. O documento é untrusted; a forma
	// já passou em plan.Decode, aqui valida-se aciclicidade/tools/tectos contra o snapshot
	// pinado. O tecto de cardinalidade é o DERIVADO da revisibilidade humana.
	if err := validarEstrutura(res.Doc, snap); err != nil {
		return err
	}

	// (6-bis) GATE DE APROVAÇÃO DE PLANO (AOS-408, fecha o residual do DEF-274). Interpõe-se
	// ANTES da materialização porque é essa a fronteira que dá a propriedade: nenhuma admissão
	// no DAG e nenhum spawn sem que o plano esteja decidido. Um plano de risco sai daqui como
	// PENDENTE — factos no log, nada materializado — e a decisão chega pelo subcomando `decide`.
	hashDoPlano, err := gatearPlano(ctx, pedidoDeGate{
		rec:       rec,
		store:     store,
		runID:     runID,
		doc:       res.Doc,
		snap:      snap,
		tentativa: res.Attempts,
		planOut:   planOut,
	})
	if err != nil {
		return err // errPlanoPendente ⇒ saída 6; qualquer outro ⇒ fail-closed
	}

	// (7)+(8) MATERIALIZAÇÃO ADMISSÃO-PURA e DESPACHO GOVERNADO — a mesma função que o
	// `--plan-doc` usa (AOS-412).
	b.exe = exe
	return materializarEDespachar(ctx, ten, store, rec, b, snap, res.Doc, hashDoPlano, worker)
}

// baseDeExecucao é o que um run precisa para materializar e despachar sob identidade real
// (AOS-412): o emissor efémero, o token do run (a raiz da cadeia de delegação), o RM mínimo
// com as tools do planeador e do spawn, e o orçamento partilhado da árvore.
//
// Existe como tipo porque passou a ter DOIS chamadores — o `--goal`, que ainda decompõe, e o
// `--plan-doc`, que recebe um documento já aprovado. Antes, só o `--goal` a compunha, e o
// `--plan-doc` materializava com um token de faz-de-conta (`"nhi:"+worker`) e sem despacho: um
// plano aprovado por essa via era admitido no DAG e ficava pendente para sempre.
type baseDeExecucao struct {
	iss        *identity.Issuer
	tokenDoRun string
	mon        *rm.Monitor
	bud        *budget.Budget
	// exe é o executor de nós (AOS-413); nil ⇒ o despacho não executa.
	exe *configDoExecutor
}

// comporBaseDeExecucao compõe a base de execução de um run: (1) backbone de identidade real —
// emissor efémero, raiz humana e token do run —, (2) RM mínimo com as tools do planeador e do
// spawn registadas, (3) orçamento partilhado por Planner + Delegator + admissão.
func comporBaseDeExecucao(ctx context.Context, runID, worker string, snap planvalidate.Snapshot) (*baseDeExecucao, error) {
	// (1) BACKBONE DE IDENTIDADE REAL — emissor efémero + raiz humana + token do run.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("chave do emissor: %w", err)
	}
	// AOS-393: a autoridade sobre TOOLS que a cadeia de delegação do run tem de carregar
	// para materializar papéis que as usam — a UNIÃO das capabilities coarse do snapshot
	// pinado (tecto). O clamp por-nó do Materializer (authorityForNode) restringe depois
	// cada filho às SUAS tools; sem estas caps no token do run e na classe do filho, o
	// IssueChild do spawn recusa (Authority ⊄ folha-do-pai ∩ Scope-da-classe). A classe do
	// planeador NÃO as inclui — o planeador decompõe, não invoca tools.
	toolCaps := toolCapabilities(snap)
	// AOS-391: o token do run (coordenador) SELA `model:invoke` — é o Principal que o
	// estágio authn REAL do Model Gateway verifica na decomposição por LLM (`--goal` sem
	// fixture). capPlan + toolCaps + model:invoke. (Fidelidade ADR-020 residual: o ideal
	// seria a NHI `agent:planner`, mas o planner não expõe o token filho ao decompositor.)
	coordCaps := append(append([]string{capPlan}, toolCaps...), modelInvokeCapability)
	classes := map[string]identity.ClassPolicy{
		classeCoordenador: {TTL: tokenTTL, Scope: coordCaps},
		classePlaneador:   {TTL: tokenTTL, Scope: []string{capPlan}},
		// classeWorker carrega capPlan + toolCaps (correcção habilitadora do AOS-393): o
		// spawn de um papel (agora disparado pelo DispatchSink, ADR-024) cunha uma NHI filha
		// cuja Authority tem de ser ⊆ folha-do-pai ∩ Scope-da-classe; sem toolCaps aqui e no
		// token do run, o IssueChild recusaria. O clamp por-nó restringe cada filho às SUAS
		// tools a jusante.
		classeWorker: {TTL: tokenTTL, Scope: append([]string{capPlan}, toolCaps...)},
	}
	iss, err := identity.NewIssuer("iss:aos-orq", priv, classes)
	if err != nil {
		return nil, fmt.Errorf("emissor de identidade: %w", err)
	}
	runTok, err := iss.Issue(ctx, identity.IssueRequest{
		UserID:        "human:" + worker,
		AgentID:       "agt-" + runID,
		AgentClass:    classeCoordenador,
		UserAuthority: coordCaps,
	})
	if err != nil {
		return nil, fmt.Errorf("token NHI do run: %w", err)
	}

	// (2) RM MÍNIMO — permite genuinamente e regista a tool do planeador (senão
	// default-deny). É o "RM real" do teste do planner; a cadeia PDP completa é posterior.
	mon := rm.New()
	if err := mon.Register("agent.plan", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		return nil, fmt.Errorf("registo da tool do planeador: %w", err)
	}
	// AOS-393: o Delegator medeia o spawn de papéis-que-expandem com a tool `agent.spawn`
	// (default do `NewDelegator`). Sem a registar, o RM nega-o por default-deny e a
	// materialização de um papel aborta. Registá-la mantém a mediação OBRIGATÓRIA (o RM
	// corre a cadeia neutra + este handler antes de permitir) — não a contorna.
	if err := mon.Register("agent.spawn", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		return nil, fmt.Errorf("registo da tool de spawn: %w", err)
	}

	// (3) ORÇAMENTO PARTILHADO por Planner + Delegator + admissão da materialização (uma
	// só árvore, raiz = runID).
	//
	// O TECTO É CONFIGURÁVEL DESDE AOS-434, e o que ele governa está escrito em
	// `budget_env.go` — em resumo: a soma das ESTIMATIVAS DECLARADAS, não o consumo real. A
	// leitura do ambiente já correu (e já falhou, se fosse para falhar) no arranque, antes de
	// se tomar posse do run; aqui não pode falhar por configuração, e a segunda leitura dá o
	// mesmo valor porque o ambiente do processo não muda.
	tecto, err := tectoDoPlanoDoAmbiente()
	if err != nil {
		return nil, fmt.Errorf("tecto de orçamento do plano: %w", err)
	}
	bud, err := budget.New(runID, tecto)
	if err != nil {
		return nil, fmt.Errorf("orçamento da árvore: %w", err)
	}
	return &baseDeExecucao{iss: iss, tokenDoRun: runTok.Compact, mon: mon, bud: bud}, nil
}

// validadorDoSnapshot adapta a validação estrutural (AOS-231) à porta [planner.Validator]: é o
// MESMO `planvalidate.Validate` que corre a jusante, com o MESMO snapshot pinado — não uma
// segunda opinião sobre o que é admissível.
//
// O que atravessa a fronteira é só o que o veredicto traz em CÓDIGOS: a regra, o sub-código e o
// node_id do locator. O enum foi escrito para isto («sinal accionável sem vazar conteúdo
// untrusted», `planvalidate/verdict.go`), e o node_id tem grammar fechada.
type validadorDoSnapshot struct{ snap planvalidate.Snapshot }

func (v validadorDoSnapshot) Validate(doc plan.PlanDocument) *planner.Rejection {
	ver := planvalidate.Validate(doc, v.snap, planvalidate.Ceilings{MaxNodes: planvalidate.DefaultMaxNodes})
	if !ver.Rejected() {
		return nil
	}
	return &planner.Rejection{Rule: string(ver.Rule), Reason: string(ver.Reason), NodeID: ver.Locator.NodeID}
}

// validarEstrutura corre a validação estrutural (AOS-231) — aciclicidade, resolução das tools no
// snapshot pinado, tectos. Fail-closed: o documento é untrusted venha de onde vier — do modelo, ou
// de um ficheiro que um operador passou em `--plan-doc` (que, até ao AOS-412, não era validado).
func validarEstrutura(doc plan.PlanDocument, snap planvalidate.Snapshot) error {
	if v := planvalidate.Validate(doc, snap, planvalidate.Ceilings{MaxNodes: planvalidate.DefaultMaxNodes}); v.Rejected() {
		return fmt.Errorf("plano rejeitado na validação estrutural (AOS-231): %s", v.Reason)
	}
	return nil
}

// materializarEDespachar admite os nós do plano DECIDIDO no DAG e despacha-os pela cadeia
// governada (AOS-412: um só caminho para o `--goal` e o `--plan-doc`).
//
// (7) MATERIALIZAÇÃO ADMISSÃO-PURA (AOS-390, ADR-024). A materialização admite os nós no DAG —
// folhas com a sua tool call, papéis-que-expandem como nós PENDENTES sem tool — e NÃO produz
// efeito. (8) DESPACHO GOVERNADO: o efeito por-nó nasce aqui — o plandispatch.Dispatcher decide a
// elegibilidade (gate + estado + depends_on + condicionais com poda branch_not_taken + cartão +
// headroom) e entrega os nós elegíveis ao DispatchSink — papel→Delegator.Spawn, folha→arranque.
// Sob a MESMA posse; o SCH continua derivador.
func materializarEDespachar(ctx context.Context, ten *runlifecycle.Tenure, store runlifecycle.EventStore, rec *runlifecycle.PlanRecorder, b *baseDeExecucao, snap planvalidate.Snapshot, doc plan.PlanDocument, hashDoPlano, worker string) error {
	runID := ten.RunID()

	// RETOMA (AOS-413): um plano JÁ materializado não se materializa de novo — admitir os nós
	// outra vez é recusado («nó já existe no grafo»). Com o executor, a retoma é o caso normal: o
	// `serve` anterior saiu com 8 (prazo esgotado com nós em voo) e este segue para o despacho
	// com o facto `plan.materialized` que está no log.
	if ja, err := materializadoNoLog(ctx, store, rec.PlanID()); err != nil {
		return err
	} else if ja != nil {
		if ja.PlanHash != hashDoPlano {
			return fmt.Errorf("o plano %s já foi materializado com o organigrama %s, e este é %s", rec.PlanID(), ja.PlanHash, hashDoPlano)
		}
		fmt.Printf("materializado (retoma, do log): plano=%s nos=%d\n", ja.PlanID, len(ja.Nodes))
		return despachar(ctx, ten, store, rec, b, snap, doc, *ja, worker)
	}

	adm, err := runlifecycle.NewBudgetAdmission(b.bud, runID)
	if err != nil {
		return err
	}
	m, err := ten.Materializer(ctx, snap, rec, adm)
	if err != nil {
		return fmt.Errorf("materializador: %w", err)
	}
	payload, err := m.Materialize(ctx, planmaterialize.Request{
		RunID:          runID,
		PlanID:         rec.PlanID(),
		ParentToken:    b.tokenDoRun,
		RootBudgetNode: runID,
		Doc:            doc,
		// AOS-408: o hash que o gate DECIDIU. Hoje é a MESMA derivação que o materializador faria
		// sozinho (sha256 sobre `plan.Encode` do mesmo documento), pelo que passá-lo não prova
		// nada por si — o que amarra a materialização à decisão é o gate ter confrontado este
		// hash com o `decision_ref`/`plan_hash` da decisão no log. Passa-se explicitamente para o
		// facto `plan.materialized` citar o hash que foi decidido, e não um recalculado.
		PlanHash: hashDoPlano,
	})
	if err != nil {
		// FAIL-CLOSED SEM VAZAR: a materialização é em duas fases e aborta antes de qualquer
		// efeito, mas os nós JÁ admitidos deixaram reservas pendentes. Sem esta devolução,
		// cada tentativa falhada encolhia a árvore até negar tudo.
		if rerr := adm.Release(ctx); rerr != nil {
			return fmt.Errorf("materialização falhou (%w) e a devolução das reservas também: %v", err, rerr)
		}
		return fmt.Errorf("materialização: %w", err)
	}
	if err := adm.Commit(ctx); err != nil {
		return fmt.Errorf("confirmação das reservas de admissão: %w", err)
	}

	fmt.Printf("materializado: plano=%s nos=%d oraculo=snapshot(%s)\n", payload.PlanID, len(payload.Nodes), snap.Hash)
	for _, n := range payload.Nodes {
		fmt.Printf("  no=%s kind=%s tools=%s\n", n.NodeID, n.Kind, strings.Join(n.Tools, "|"))
	}

	return despachar(ctx, ten, store, rec, b, snap, doc, payload, worker)
}

// despachar compõe o Delegator e o despacho governado sobre o plano materializado.
func despachar(ctx context.Context, ten *runlifecycle.Tenure, store runlifecycle.EventStore, rec *runlifecycle.PlanRecorder, b *baseDeExecucao, snap planvalidate.Snapshot, doc plan.PlanDocument, payload plannerevents.MaterializedPayload, worker string) error {
	del, err := orchestrator.NewDelegator(b.bud, b.mon, b.iss)
	if err != nil {
		return fmt.Errorf("delegator: %w", err)
	}
	if err := composeEDespachar(ctx, ten, store, rec, del, b.bud, b.tokenDoRun, doc, payload, worker, snap, b.exe); err != nil {
		return fmt.Errorf("despacho governado: %w", err)
	}
	return nil
}

// materializadoNoLog devolve o facto `plan.materialized` do plano, se já existir (nil se não).
func materializadoNoLog(ctx context.Context, store runlifecycle.EventStore, planID string) (*plannerevents.MaterializedPayload, error) {
	eventos, err := store.Read(ctx, planID, 0)
	if err != nil {
		return nil, fmt.Errorf("ler o stream do plano %q: %w", planID, err)
	}
	for _, ev := range eventos {
		if ev.Type != plannerevents.EventMaterialized {
			continue
		}
		var p plannerevents.MaterializedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return nil, fmt.Errorf("plan.materialized ilegível no plano %q: %w", planID, err)
		}
		return &p, nil
	}
	return nil, nil
}

// toolCapabilities deriva as capabilities coarse das tools do snapshot pinado, pelo
// MESMO mapeamento que o Materializer usa para clampar a autoridade dos nós
// ([planmaterialize.DefaultCapabilityMapper] → "cap:tool:"+Name), deduplicadas e
// ordenadas (determinístico). É a UNIÃO/tecto que a cadeia de delegação do run carrega;
// cada filho é clampado às suas próprias tools a jusante. Usar o mapper canónico (e não
// um literal) impede a divergência silenciosa se a convenção coarse mudar.
func toolCapabilities(snap planvalidate.Snapshot) []string {
	seen := make(map[string]struct{}, len(snap.Tools))
	caps := make([]string, 0, len(snap.Tools))
	for _, t := range snap.Tools {
		c := planmaterialize.DefaultCapabilityMapper(plan.ToolRef{Name: t.Name})
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		caps = append(caps, c)
	}
	sort.Strings(caps)
	return caps
}

// renderCapabilities serializa o catálogo pinado do snapshot como texto para a mensagem
// user do decompositor (as tools que o modelo pode referenciar por {name,version,digest}).
// É apresentação, não política — a resolução real das tools é de AOS-231.
func renderCapabilities(snap planvalidate.Snapshot) string {
	if len(snap.Tools) == 0 {
		return "(catálogo de capabilities vazio)"
	}
	var b strings.Builder
	for _, t := range snap.Tools {
		fmt.Fprintf(&b, "- %s@%s (%s)\n", t.Name, t.Version, t.Digest)
	}
	return b.String()
}
