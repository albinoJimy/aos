package main

// AOS-502 — O QUE A REVISÃO ADVERSARIAL ENCONTROU SEM TESTE QUE MORDESSE (I2 e I3).
//
// O código da prova estava certo, e nada o prendia: cinco mutações dos ramos de FALHA DE LEITURA
// sobreviviam à suite. Cada teste daqui mata uma, e diz qual:
//
//   - RN25 — no handler, a recusa TRANSITÓRIA respondia 503 só por escrito: trocar o 503 por
//     «hospeda» não partia nada. [TestAOS502_RecusaTransitoria_Responde503ENaoHospeda];
//   - RN19 — o estado durável do run anterior que não se lê agora.
//     [TestAOS502_EstadoDuravelIlegivel_Responde503ENaoHospeda];
//   - RN20 e RN20b — a residência do run anterior que o WORM não devolve agora (403 em vez de
//     503, ou o erro ignorado). [TestAOS502_ResidenciaIlegivelOuNaoSelada];
//   - RN11 — a residência NÃO SELADA aceite. O mesmo teste.
//
// E a peça do nó para o I2: o `GET /runs/{id}` de uma tentativa diz de que pedido ela é
// ([TestAOS502_GetDaTentativa_DizDeQuePedidoE]).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos502RecusadasPor lê do `/metrics` o contador de recusas de uma causa.
func aos502RecusadasPor(t *testing.T, n *aos502No, causa string, quer int) {
	t.Helper()
	if m := n.metricas(t); !aos502TemSerie(m, `aos_runs_retry_refused_total{causa="`+causa+`"}`, quer) {
		t.Fatalf("queria %d recusa(s) com a causa %s:\n%s", quer, causa, aos502SoRetry(m))
	}
}

// TestAOS502_RecusaTransitoria_Responde503ENaoHospeda (RN25): com o stream do run anterior
// ilegível AGORA, o `POST /runs` da tentativa responde 503 — pelo handler real, e não só pela
// função da prova —, não hospeda nada, e a tool não corre. Com o log outra vez legível, o MESMO
// pedido é admitido: a tentativa não ficou dada por recusada.
func TestAOS502_RecusaTransitoria_Responde503ENaoHospeda(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-503"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	n.modeloChama(aos502Leitura)
	anterior, tentativa := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)

	original := n.node.EventStore
	n.node.EventStore = aos502StoreQueFalha{EventStorePort: original, falha: anterior}
	codigo, corpo := n.pedirTentativa(t, plano, ger, 2)
	n.node.EventStore = original
	if codigo != http.StatusServiceUnavailable || !strings.Contains(corpo, "indisponivel") {
		t.Fatalf("com o log do run anterior ilegivel agora a tentativa responde 503 indisponivel; veio %d %s", codigo, corpo)
	}
	if n.existe(t, tentativa) || *n.execs[aos502Leitura] != 0 {
		t.Fatalf("a tentativa que nao se provou nao e hospedada nem corre a tool (stream=%t, execs=%d)", n.existe(t, tentativa), *n.execs[aos502Leitura])
	}
	aos502RecusadasPor(t, n, causaRetryIndisponivel, 1)
	if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_admitted_total", 0) {
		t.Fatalf("nada foi admitido:\n%s", aos502SoRetry(m))
	}
	// O MESMO pedido, com o log legível: admitido.
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("controlo: o 503 nao deu a tentativa por recusada — o mesmo pedido e admitido depois; veio %d %s", codigo, corpo)
	}
	n.esperar(t, tentativa)
	if *n.execs[aos502Leitura] != 1 {
		t.Fatalf("controlo: a tentativa admitida corre a tool uma vez; correu %d", *n.execs[aos502Leitura])
	}
}

// aos502EstadoQueFalha é o store da máquina de estados com a leitura de UM stream a falhar.
type aos502EstadoQueFalha struct {
	state.EventStore
	falha string
}

func (s aos502EstadoQueFalha) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	if streamID == s.falha {
		return nil, fmt.Errorf("substrato indisponivel (teste)")
	}
	return s.EventStore.Read(ctx, streamID, fromSeq)
}

// TestAOS502_EstadoDuravelIlegivel_Responde503ENaoHospeda (RN19): o stream do run anterior lê-se,
// e o ESTADO DURÁVEL dele (a máquina de estados, que tem a sua própria leitura) não. É transitório:
// 503, sem hospedar — e nunca a 403 de uma recusa, que o `aos-orq` não volta a tentar.
func TestAOS502_EstadoDuravelIlegivel_Responde503ENaoHospeda(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-estado"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	n.modeloChama(aos502Leitura)
	anterior, tentativa := idDaTentativa(plano, aos502No_, 1), idDaTentativa(plano, aos502No_, 2)

	gates := n.svc.node.stateGates
	original := gates.store
	gates.store = aos502EstadoQueFalha{EventStore: original, falha: anterior}
	// A função da prova, primeiro: a causa e a natureza.
	api := aos502Interno(t, n, n.node.EventStore)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	_, causa, transitoria := api.provarTentativa(context.Background(), chamador, *aos502Vinculo(plano, aos502No_, ger, 2))
	// E o handler real.
	codigo, corpo := n.pedirTentativa(t, plano, ger, 2)
	gates.store = original
	if causa != causaRetryIndisponivel || !transitoria {
		t.Fatalf("um estado duravel que nao se le agora e indisponivel e TRANSITORIO; veio causa=%q transitoria=%t", causa, transitoria)
	}
	if codigo != http.StatusServiceUnavailable {
		t.Fatalf("o handler responde 503; veio %d %s", codigo, corpo)
	}
	if n.existe(t, tentativa) || *n.execs[aos502Leitura] != 0 {
		t.Fatal("a tentativa que nao se provou nao e hospedada nem corre a tool")
	}
	aos502RecusadasPor(t, n, causaRetryIndisponivel, 1)
}

// aos502WormDaResidencia é o WORM do nó com a leitura do selo de residência de UM run a falhar
// (`falha`) ou a responder que não há selo (`semSelo`).
type aos502WormDaResidencia struct {
	audit.Store
	particao string
	falha    bool
	semSelo  bool
}

func (w aos502WormDaResidencia) At(ctx context.Context, partition string, seq uint64) (audit.AuditRecord, bool, error) {
	if partition == w.particao {
		switch {
		case w.falha:
			return audit.AuditRecord{}, false, fmt.Errorf("worm indisponivel (teste)")
		case w.semSelo:
			return audit.AuditRecord{}, false, nil
		}
	}
	return w.Store.At(ctx, partition, seq)
}

// TestAOS502_ResidenciaIlegivelOuNaoSelada (RN20, RN20b, RN11): a residência do run anterior é a
// última condição da prova, e tem dois modos de não estar lá que NÃO se confundem:
//
//   - o WORM não a devolve AGORA — transitório: 503, sem hospedar. Ignorar o erro (RN20b)
//     deixava a prova seguir sem a região; responder 403 (RN20) dava por recusada uma tentativa
//     que o nó não chegou a julgar;
//   - o run anterior NÃO TEM residência selada — recusa definitiva (RN11): um run sem selo de
//     criação não prova em que região correu, e a tentativa herdava a de quem a pede.
func TestAOS502_ResidenciaIlegivelOuNaoSelada(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-residencia"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	anterior := idDaTentativa(plano, aos502No_, 1)
	q := *aos502Vinculo(plano, aos502No_, ger, 2)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	particao := readResidencyPartition(anterior)
	if _, selada, err := aos502Interno(t, n, n.node.EventStore).readGov.runResidency(context.Background(), anterior); err != nil || !selada {
		t.Fatalf("pre-condicao: o run anterior tem a residencia selada; selada=%t err=%v", selada, err)
	}

	api := aos502Interno(t, n, n.node.EventStore)
	api.readGov.worm = aos502WormDaResidencia{Store: n.node.WORM, particao: particao, falha: true}
	if _, causa, transitoria := api.provarTentativa(context.Background(), chamador, q); causa != causaRetryIndisponivel || !transitoria {
		t.Fatalf("uma residencia que o WORM nao devolve agora e indisponivel e TRANSITORIA (503); veio causa=%q transitoria=%t", causa, transitoria)
	}

	api = aos502Interno(t, n, n.node.EventStore)
	api.readGov.worm = aos502WormDaResidencia{Store: n.node.WORM, particao: particao, semSelo: true}
	if _, causa, transitoria := api.provarTentativa(context.Background(), chamador, q); causa != causaRetryResidencia || transitoria {
		t.Fatalf("um run anterior sem residencia selada e uma RECUSA (403), e nao admite; veio causa=%q transitoria=%t", causa, transitoria)
	}

	// Controlo: o mesmo handler, com o WORM do nó, admite.
	api = aos502Interno(t, n, n.node.EventStore)
	if prova, causa, _ := api.provarTentativa(context.Background(), chamador, q); causa != "" || prova.anterior != anterior {
		t.Fatalf("controlo: com a residencia selada e legivel a prova passa; veio causa=%q prova=%+v", causa, prova)
	}
}

// aos502Tentativa lê o `plan_attempt` da resposta crua de um `GET /runs/{id}`.
func aos502Tentativa(t *testing.T, cru []byte) *tentativaNaAPI {
	t.Helper()
	var r struct {
		PlanAttempt *tentativaNaAPI `json:"plan_attempt"`
	}
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	return r.PlanAttempt
}

// TestAOS502_GetDaTentativa_DizDeQuePedidoE (I2): o `GET /runs/{id}` de um run que o nó hospedou
// como nova tentativa leva `plan_attempt` — o pedido, o plano, o nó e a tentativa da origem que o
// NÓ escreveu depois da prova. É por ele que o `aos-orq` que retoma um plano não segue um run que
// outro criou com o id da tentativa. Um run com esse id que NÃO foi admitido como tentativa — um
// `POST /runs` directo, ou a primeira tentativa de um nó `2` de um pedido chamado `<plano>~<nó>`
// — não o leva; e a primeira tentativa responde os bytes de sempre.
func TestAOS502_GetDaTentativa_DizDeQuePedidoE(t *testing.T) {
	t.Run("ATentativaAdmitida", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
		const plano = "plano-502-get"
		ger := n.pedir(t, plano)
		n.falharSemChamar(t, plano, ger)
		cru1, _ := n.ler(t, idDaTentativa(plano, aos502No_, 1))
		if strings.Contains(string(cru1), "plan_attempt") {
			t.Fatalf("a primeira tentativa responde os bytes de sempre, sem plan_attempt: %s", cru1)
		}
		n.modeloChama(aos502Leitura)
		if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
			t.Fatalf("tentativa 2: %d %s", codigo, corpo)
		}
		id2 := idDaTentativa(plano, aos502No_, 2)
		n.esperar(t, id2)
		cru2, r2 := n.ler(t, id2)
		quer := tentativaNaAPI{PlanRequest: plano, Generation: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}
		if tem := aos502Tentativa(t, cru2); r2.Status != "completed" || tem == nil || *tem != quer {
			t.Fatalf("o GET da tentativa admitida leva plan_attempt com o pedido, o plano, o no e a tentativa:\n  quero: %+v\n  veio:  %+v\n  %s", quer, tem, cru2)
		}
		if !strings.HasSuffix(strings.TrimSpace(string(cru2)), fmt.Sprintf(`,"plan_attempt":{"plan_request":%q,"generation":%d,"plan_id":%q,"node_id":%q,"attempt":2}}`, plano, ger, aos502PlanID, aos502No_)) {
			t.Fatalf("plan_attempt e o ultimo campo, e so ids e inteiros: %s", cru2)
		}
		// Com o stream da tentativa ilegível AGORA: 503 — e nunca a resposta sem o campo, que
		// quem retoma leria como «este run não é uma tentativa».
		// (Pela função, com um handler próprio: trocar o store do nó a meio corria contra a
		// medição do prompt da tentativa, que o lê noutra goroutine.)
		var resp runStateResponse
		api := aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: n.node.EventStore, falha: id2})
		if api.tentativaNaResposta(context.Background(), id2, &resp) || resp.PlanAttempt != nil {
			t.Fatalf("com o stream da tentativa ilegivel agora a resposta NAO sai (503); saiu com plan_attempt=%+v", resp.PlanAttempt)
		}
		api = aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: n.node.EventStore, ausente: id2})
		if !api.tentativaNaResposta(context.Background(), id2, &resp) || resp.PlanAttempt != nil {
			t.Fatalf("um stream que nao existe nao e uma avaria: a resposta sai, sem o campo; plan_attempt=%+v", resp.PlanAttempt)
		}
	})

	t.Run("UmRunDirectoComOIdDaTentativa_NaoOLeva", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
		const plano = "plano-502-get-directo"
		ger := n.pedir(t, plano)
		n.falharSemChamar(t, plano, ger)
		id2 := idDaTentativa(plano, aos502No_, 2)
		n.modeloChama(aos502Leitura)
		if codigo, corpo := n.submeter(t, id2, nil, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
			t.Fatalf("pre-condicao: o POST /runs directo com o id da tentativa e aceite (a forma do id nao e reservada); veio %d %s", codigo, corpo)
		}
		n.esperar(t, id2)
		cru, r := n.ler(t, id2)
		if r.Status != "completed" || strings.Contains(string(cru), "plan_attempt") {
			t.Fatalf("um run que o no nao admitiu como tentativa nao leva plan_attempt: %s", cru)
		}
	})

	t.Run("APrimeiraTentativaDeOutroPedidoComTil_NaoOLeva", func(t *testing.T) {
		n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
		const plano = "plano-502-get-til"
		ger := n.pedir(t, plano)
		n.falharSemChamar(t, plano, ger)
		// Um pedido chamado, literalmente, `<plano>~read_notes`: a primeira tentativa do nó `2`
		// dele tem o id da tentativa 2 do nó `read_notes` do pedido da vítima.
		outro := plano + separadorDoRunFilho + aos502No_
		if r := postReq(n.h, "/plans", map[string]any{"run_id": outro, "objective": "outro objectivo"}, aos439Headers("sub-mallory-502")); r.Code != http.StatusCreated {
			t.Skipf("o POST /plans ja nao aceita um id com o separador (%d): o caso deixou de existir", r.Code)
		}
		ger2 := n.reclamar(t, outro)
		id := idDaTentativa(plano, aos502No_, 2)
		n.modeloChama(aos502Leitura)
		if codigo, corpo := n.submeter(t, id, aos502Vinculo(outro, "2", ger2, 0), []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
			t.Fatalf("pre-condicao: a primeira tentativa do no \"2\" do pedido %q e aceite; veio %d %s", outro, codigo, corpo)
		}
		n.esperar(t, id)
		cru, _ := n.ler(t, id)
		if strings.Contains(string(cru), "plan_attempt") {
			t.Fatalf("a origem desse run nao tem attempt: o GET nao leva plan_attempt: %s", cru)
		}
	})
}
