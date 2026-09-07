package main

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/revalidation"
	"github.com/aos-ref/platform/registry/signing"
)

// AOS-369 — OS CONTADORES DE MEDIAÇÃO DO REFERENCE MONITOR CHEGAM AO /metrics, E A PERDA DE UM
// REGISTO DE MEDIAÇÃO TIRA O NÓ DE ROTAÇÃO.
//
// Antes disto, os contadores permits/denials/escalations do PEP mandatório do nó moviam-se no RM
// e ninguém os via de fora, e a falha em GRAVAR duravelmente a PROVA de um deny/escalate era
// descartada em silêncio — um WORM em baixo negava 100% das tool calls sem deixar rasto,
// indistinguível de um nó ocioso. Estes testes provam a correcção FIM-A-FIM pelo nó real.

// aos369ToolPingModel emite uma tool call (`counter`/durCap) no PRIMEIRO turno de CADA run e
// conclui no segundo — o contador é partilhado entre runs, pelo que a alternância par/ímpar faz
// cada `Runtime.Run` mediar EXACTAMENTE uma tool call e terminar (turnos ímpares emitem, pares
// concluem). É o mínimo para atravessar a mediação real do nó em vários runs sobre o MESMO RM.
type aos369ToolPingModel struct{ calls int32 }

func (m *aos369ToolPingModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if atomic.AddInt32(&m.calls, 1)%2 == 1 {
		return agentruntime.ModelResponse{
			ToolCalls: []agentruntime.ToolInvocation{{
				ToolID:     "counter",
				Capability: durCap,
				Input:      []byte("tick"),
			}},
			Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		}, nil
	}
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// aos369PermitNodeComWorm compõe o NÓ pela MESMA via de produção de aos220PermitNode (Bootstrap
// sobre a cadeia real identity→revalidation→policy→taint→scope→egress, com o bundle assinado
// committado carregado do ambiente), mas INJECTA `w` como o WORM do nó — o `audit.Store` único de
// que o sink de mediação do RM depende. Assim, alternar `w.recusa` parte/repara o
// RecordMediation. Conduz-se a mediação por `node.Runtime.Run` DIRECTO (nunca POST /runs), pelo
// que o selo de governação (`seloWORM`) NÃO é tocado — o único eixo que pode avermelhar o
// /readyz aqui é o registo de mediação do RM, que é exactamente o que se está a provar.
func aos369PermitNodeComWorm(t *testing.T, w *wormSoLeitura, model agentruntime.ModelClient) (*Node, string) {
	t.Helper()
	ctx := context.Background()

	t.Setenv("AOS_POLICY_BUNDLE_DIR", pdpPoliciesDir)
	t.Setenv("AOS_POLICY_TRUST_ANCHOR", aos220CommittedAnchorHex(t))

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if cfg.PDP == nil {
		t.Fatal("o bundle assinado devia ter sido carregado do ambiente (cfg.PDP != nil)")
	}

	// A INJECÇÃO sob teste: o WORM do nó é `w`. O sink de mediação do RM é composto sobre ele.
	cfg.WORM = w

	// Restante cadeia de PERMIT (idêntica a aos220PermitNode): supply-chain assinada, catálogo com
	// a tool, autoridade user∩classe, classe de identidade.
	signer := durSigner(t)
	entry := counterEntry(t, signer)
	auditStore := audit.NewMemStore()
	trust, err := signing.NewTrustStore(auditStore)
	if err != nil {
		t.Fatalf("trust store: %v", err)
	}
	if err := trust.Add(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatalf("trust add: %v", err)
	}
	revalidator, err := revalidation.New(trust, auditStore)
	if err != nil {
		t.Fatalf("revalidator: %v", err)
	}

	cfg.Model = model
	cfg.Catalog = catalogStub{entries: []domain.Entry{entry}}
	cfg.Revalidator = revalidator
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+aos220Human, durCap).
		Set(durAgent, durCap).
		Set("agent:"+durClass, durCap)

	node, err := Bootstrap(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("Bootstrap (no de permit com WORM injectado): %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	tok, err := node.Authority.MintForHuman(ctx, aos220Human, durAgent, durClass, []string{durCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	return node, tok.Compact
}

// aos369MedeiUmaCall conduz UM run pela cadeia real do nó, mediando exactamente uma tool call.
func aos369MedeiUmaCall(t *testing.T, node *Node, credential, runID string) {
	t.Helper()
	if _, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: credential,
		Objective:  "AOS-369 mediacao com registo observavel",
		MaxTurns:   4,
	}, nil); err != nil {
		t.Fatalf("Runtime.Run(%s): %v", runID, err)
	}
}

// TestAOS369_ContadoresDeMediacaoSaemNoMetrics prova AC-1/AC-2 (as séries emitem, com HELP) e o
// piso `grep -c '^aos_mediation_' >= 3`. Uma tool call PERMITIDA move o permits, que aparece no
// corpo do /metrics — a via que faltava.
func TestAOS369_ContadoresDeMediacaoSaemNoMetrics(t *testing.T) {
	w := &wormSoLeitura{dentro: audit.NewMemStore()}
	node, credential := aos369PermitNodeComWorm(t, w, &aos369ToolPingModel{})
	if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	svc, _ := newAPI(t, node)

	aos369MedeiUmaCall(t, node, credential, "run-aos369-metrics")

	corpo := metricasDe(t, &apiHandler{node: node, svc: svc})

	// AC: as TRÊS séries de contagem emitem sempre (Runtime sempre composto).
	var contagem int
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "aos_mediation_") && !strings.HasPrefix(l, "# ") {
			contagem++
		}
	}
	if contagem < 3 {
		t.Fatalf("grep -c '^aos_mediation_' = %d, quero >= 3 — os contadores do RM nao chegaram ao /metrics", contagem)
	}

	// AC: a série da PROVA PERDIDA existe COM HELP, e o HELP diz que a negacao aconteceu a mesma.
	if !strings.Contains(corpo, "# HELP aos_mediation_record_failures_total ") {
		t.Fatal("aos_mediation_record_failures_total sem linha de HELP")
	}
	linha := ""
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "# HELP aos_mediation_record_failures_total ") {
			linha = l
			break
		}
	}
	if !strings.Contains(linha, "PROVA") || !strings.Contains(strings.ToLower(linha), "negacao aconteceu") {
		t.Fatalf("o HELP de record_failures nao explica que a PROVA se perdeu mas a negacao aconteceu: %q", linha)
	}

	// NÃO-VACUIDADE: a call foi PERMITIDA ⇒ o permits saiu > 0. Sem mediacao, ficaria a 0.
	if v, ok := valorDe(t, corpo, "aos_mediation_permits_total"); !ok || v < 1 {
		t.Fatalf("aos_mediation_permits_total=%v (presente=%v) — a tool call legitima devia ter sido PERMITIDA e contada", v, ok)
	}
	// Com o WORM saudável, nenhuma prova se perdeu.
	if v, _ := valorDe(t, corpo, "aos_mediation_record_failures_total"); v != 0 {
		t.Fatalf("aos_mediation_record_failures_total=%v com o WORM saudavel — nada devia ter falhado", v)
	}
}

// TestAOS369_WormEmBaixo_ContaAPerdaEReadyz503_ERecupera é o negative-control e2e e a metade que
// interessa ao orquestrador. Cobre, na MESMA vida do nó (last-outcome):
//
//	FASE saudavel  ⇒ record_failures==0, permits sobe, /readyz==200;
//	FASE em baixo  ⇒ record_failures sobe E /readyz==503 (a PROVA do deny perdeu-se);
//	FASE recuperada⇒ /readyz volta a 200 (last-outcome, nao «alguma vez falhou») e o
//	                 contador NAO recua.
//
// Mutação (i): remover a cláusula de mediação do /readyz ⇒ a asserção de 503 avermelha (nada mais
// causa 503 aqui — o selo de governação nunca é tocado). Mutação (ii): reverter :478 ⇒ a asserção
// de record_failures avermelha.
func TestAOS369_WormEmBaixo_ContaAPerdaEReadyz503_ERecupera(t *testing.T) {
	w := &wormSoLeitura{dentro: audit.NewMemStore()} // recusa=false
	node, credential := aos369PermitNodeComWorm(t, w, &aos369ToolPingModel{})
	if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	svc, h := newAPI(t, node)
	hMetrics := &apiHandler{node: node, svc: svc}

	// ---- FASE SAUDÁVEL: a mediação permite e o registo grava. ----
	aos369MedeiUmaCall(t, node, credential, "run-aos369-ok")

	corpo := metricasDe(t, hMetrics)
	if v, ok := valorDe(t, corpo, "aos_mediation_permits_total"); !ok || v < 1 {
		t.Fatalf("FASE SAUDAVEL: permits=%v (presente=%v), devia ter subido", v, ok)
	}
	if v, _ := valorDe(t, corpo, "aos_mediation_record_failures_total"); v != 0 {
		t.Fatalf("FASE SAUDAVEL: record_failures=%v, devia ser 0", v)
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("FASE SAUDAVEL: /readyz devia estar 200, veio %d", code)
	}
	// EMPARELHAMENTO (achado F, eixo de mediação — AOS-369): o aos_ready do /metrics tem de
	// CONCORDAR com o /readyz, senão os dois sinais separam-se sem ninguém dar por isso.
	if v, _ := valorDe(t, corpo, "aos_ready"); v != 1 {
		t.Fatalf("FASE SAUDAVEL: aos_ready devia concordar com /readyz (1), veio %v", v)
	}

	// ---- FASE EM BAIXO: o disco enche. A call é mediada; o registo pós-decisão perde-se. ----
	w.recusa = true
	aos369MedeiUmaCall(t, node, credential, "run-aos369-broken")

	corpo = metricasDe(t, hMetrics)
	if v, _ := valorDe(t, corpo, "aos_mediation_record_failures_total"); v < 1 {
		t.Fatalf("FASE EM BAIXO: a perda do registo de mediacao NAO foi contada (%v) — o no nega "+
			"100%% das tool calls sem deixar prova", v)
	}
	if code, _ := getProbe(h, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("FASE EM BAIXO: o /readyz continua verde com o registo de mediacao a falhar — o "+
			"orquestrador encaminha para um no que nao deixa prova das negacoes; veio %d", code)
	}
	// O /healthz NÃO muda — o processo está vivo. Distinguir vivo de pronto é o proposito das sondas.
	if code, _ := getProbe(h, "/healthz"); code != http.StatusOK {
		t.Fatalf("FASE EM BAIXO: o /healthz nao devia mudar (o processo esta vivo), veio %d", code)
	}
	// EMPARELHAMENTO: com o registo de mediação a falhar, o aos_ready tem de ler 0 a par do /readyz
	// 503 — a divergência que o achado F apanhou para o WORM, agora fixada também para a mediação.
	if v, _ := valorDe(t, corpo, "aos_ready"); v != 0 {
		t.Fatalf("FASE EM BAIXO: aos_ready devia concordar com /readyz (0) no eixo de mediacao, veio %v", v)
	}

	// Guarda o contador do pico para provar que NÃO recua na recuperação.
	corpoPico := metricasDe(t, hMetrics)
	pico, _ := valorDe(t, corpoPico, "aos_mediation_record_failures_total")

	// ---- FASE RECUPERADA: o disco esvazia. Um registo bem-sucedido limpa a prontidão. ----
	w.recusa = false
	aos369MedeiUmaCall(t, node, credential, "run-aos369-recover")

	if code, _ := getProbe(h, "/readyz"); code != http.StatusOK {
		t.Fatalf("FASE RECUPERADA: uma mediacao BEM-SUCEDIDA nao limpou a prontidao (last-outcome) — "+
			"um solucco de disco deixaria o no fora de rotacao ate reinicio; veio %d", code)
	}
	corpo = metricasDe(t, hMetrics)
	if v, _ := valorDe(t, corpo, "aos_mediation_record_failures_total"); v < pico {
		t.Fatalf("FASE RECUPERADA: o contador recuou de %v para %v — um contador cumulativo que "+
			"esquece esconde o incidente", pico, v)
	}
	// EMPARELHAMENTO: recuperada a prontidão, o aos_ready volta a 1 a par do /readyz 200.
	if v, _ := valorDe(t, corpo, "aos_ready"); v != 1 {
		t.Fatalf("FASE RECUPERADA: aos_ready devia voltar a concordar com /readyz (1), veio %v", v)
	}
}
