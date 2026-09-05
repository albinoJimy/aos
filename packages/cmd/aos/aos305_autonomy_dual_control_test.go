package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-305 — a autoridade sobre o nível de autonomia.
//
// O que a auditoria mediu (`analises/10` §3.1): UMA assinatura de QUALQUER operador levava um
// par de L0 a L5 num salto, sem papel, sem tecto e sem segunda pessoa — e o `escalate`
// desaparecia. Estes testes fixam as três coisas que passam a ser verdade: só quem detém
// `autonomy:set` muda níveis; atravessar o limiar do gate humano (L4/L5) exige DUAS assinaturas
// de emissores DISTINTOS; e o selo nomeia os dois.

// operadoresParaTeste monta a rota com DOIS operadores registados (op:a, op:b), ambos com
// autonomy:set, e um TERCEIRO (op:c) registado mas SEM a capability.
func operadoresParaTeste(t *testing.T) (*apiHandler, map[string]ed25519.PrivateKey, audit.Store) {
	t.Helper()
	worm := audit.NewMemStore()
	sink := &autonomyWORMSink{}
	sink.bind(worm)
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(sink))

	auth, err := integration.NewEd25519Authenticator(memNonceStore{vistos: map[string]bool{}}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	privs := map[string]ed25519.PrivateKey{}
	for _, id := range []string{"op:a", "op:b", "op:c"} {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		auth.Register(id, pub)
		privs[id] = priv
	}
	h := &apiHandler{
		node: &Node{
			Autonomy:        &autonomyWiring{registry: reg, sink: sink},
			SteerAuth:       auth,
			AutonomySetters: map[string]bool{"op:a": true, "op:b": true}, // op:c assina mas NÃO pode
			WORM:            worm,
		},
		cfg: apiConfig{maxBodyBytes: 1 << 20, now: time.Now},
	}
	return h, privs, worm
}

// pedidoDual constrói o corpo com uma ou duas assinaturas sobre o MESMO payload.
func pedidoDual(t *testing.T, privs map[string]ed25519.PrivateKey, id, coID, agente, dominio, nivel, motivo string) *http.Request {
	t.Helper()
	payload := integration.CanonicalAutonomyPayload(agente, dominio, nivel, motivo)
	em, err := integration.SignEmitter(id, privs[id], integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	corpo := map[string]any{
		"emitter": emissorDeWire(em), "agent": agente, "domain": dominio, "level": nivel, "reason": motivo,
	}
	if coID != "" {
		co, err := integration.SignEmitter(coID, privs[coID], integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		corpo["co_emitter"] = emissorDeWire(co)
	}
	b, err := json.Marshal(corpo)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest("POST", "/autonomy", bytes.NewReader(b))
}

// TestAOS305_UmaAssinaturaNaoRemoveOGateHumano reproduz a medição da auditoria e prova que
// passou a ser recusada: L0 → L5 com um só emissor ⇒ 403, nível intacto, nada selado.
func TestAOS305_UmaAssinaturaNaoRemoveOGateHumano(t *testing.T) {
	h, privs, worm := operadoresParaTeste(t)

	for _, nivel := range []string{"L4", "L5"} {
		w := httptest.NewRecorder()
		h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "", "agt-1", "fs", nivel, "medicao da auditoria: uma so assinatura"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s com UMA assinatura devolveu %d, quero 403: %s", nivel, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "co_emitter") {
			t.Errorf("a recusa nao diz o que falta (co_emitter): %s", w.Body.String())
		}
	}
	if got, ok := h.node.Autonomy.registry.Get("agt-1", "fs"); ok {
		t.Fatalf("o nivel foi aplicado (%s) apesar do 403", got)
	}
	if head, _ := worm.Head(t.Context(), autonomy.DefaultAutonomyPartition); head != 0 {
		t.Fatalf("selou %d alteracao(oes) apesar de recusar", head)
	}
}

// TestAOS305_DuasPessoasRemovemOGateHumano — o caminho legítimo: dois emissores distintos com
// autonomy:set, o mesmo payload. Aplica, e o selo nomeia os DOIS.
func TestAOS305_DuasPessoasRemovemOGateHumano(t *testing.T) {
	h, privs, worm := operadoresParaTeste(t)

	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "op:b", "agt-1", "fs", "L5", "rotina madura, duas pessoas concordam"))
	if w.Code != http.StatusOK {
		t.Fatalf("duas assinaturas deviam aplicar: %d %s", w.Code, w.Body.String())
	}
	if got, _ := h.node.Autonomy.registry.Get("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("nivel aplicado = %s, quero L5", got)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["actor"] != "op:a,op:b" {
		t.Errorf("a resposta nomeia actor=%v, quero os dois (op:a,op:b)", resp["actor"])
	}
	// O selo de autonomia nomeia os dois — a pergunta «quem baixou a supervisao» tem duas respostas.
	head, err := worm.Head(t.Context(), autonomy.DefaultAutonomyPartition)
	if err != nil || head == 0 {
		t.Fatalf("nada selado (head=%d err=%v)", head, err)
	}
	recs, _ := worm.Read(t.Context(), autonomy.DefaultAutonomyPartition, head, head)
	if recs[0].Principal.NHIID != "op:a,op:b" || recs[0].Obligations[0].Params["actor"] != "op:a,op:b" {
		t.Errorf("o selo nomeia %q / %q, quero op:a,op:b nos dois", recs[0].Principal.NHIID, recs[0].Obligations[0].Params["actor"])
	}
	// E o selo de CONTROLO também.
	hc, _ := worm.Head(t.Context(), controlSealPartition)
	rc, _ := worm.Read(t.Context(), controlSealPartition, hc, hc)
	if len(rc) != 1 || rc[0].Principal.NHIID != "op:a,op:b" {
		t.Errorf("selo de controlo nao nomeia os dois: %+v", rc)
	}
}

// TestAOS305_DuasAssinaturasDaMesmaPessoaNaoChegam — «duas assinaturas» tem de ser «duas pessoas».
// O mesmo emissor a assinar duas vezes (com nonces distintos) é recusado.
func TestAOS305_DuasAssinaturasDaMesmaPessoaNaoChegam(t *testing.T) {
	h, privs, _ := operadoresParaTeste(t)
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "op:a", "agt-1", "fs", "L5", "sozinho a fingir que sao dois"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("o mesmo emissor duas vezes devolveu %d, quero 403", w.Code)
	}
	if _, ok := h.node.Autonomy.registry.Get("agt-1", "fs"); ok {
		t.Fatal("aplicou com duas assinaturas da MESMA pessoa")
	}
}

// TestAOS305_SemCapabilityNaoMudaNadaNemParaL1 — assinar bem não chega. op:c está em
// AOS_OPERATORS (assina steer/pause) mas não em AOS_AUTONOMY_SETTERS: é recusado ATÉ para L1, e
// também como co-emissor.
func TestAOS305_SemCapabilityNaoMudaNadaNemParaL1(t *testing.T) {
	h, privs, _ := operadoresParaTeste(t)

	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDual(t, privs, "op:c", "", "agt-1", "fs", "L1", "baixar e seguro, mas nao e ele quem decide"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("operador SEM autonomy:set devolveu %d para L1, quero 403", w.Code)
	}
	// Como segunda perna também não conta: op:a + op:c NÃO são «duas pessoas com o direito».
	w2 := httptest.NewRecorder()
	h.handleAutonomySet(w2, pedidoDual(t, privs, "op:a", "op:c", "agt-1", "fs", "L5", "um com direito, outro sem"))
	if w2.Code != http.StatusForbidden {
		t.Fatalf("co_emitter SEM autonomy:set devolveu %d, quero 403", w2.Code)
	}
	if _, ok := h.node.Autonomy.registry.Get("agt-1", "fs"); ok {
		t.Fatal("aplicou com um co-emissor sem a capability")
	}
}

// TestAOS305_TransicoesAbaixoDoLimiarExigemUmaAssinatura — L0..L3 e qualquer DESCIDA continuam
// a exigir uma só assinatura (de quem tem o direito). Reduzir privilégio não pode ser mais caro
// do que concedê-lo.
func TestAOS305_TransicoesAbaixoDoLimiarExigemUmaAssinatura(t *testing.T) {
	h, privs, _ := operadoresParaTeste(t)
	for _, nivel := range []string{"L0", "L1", "L2", "L3"} {
		w := httptest.NewRecorder()
		h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "", "agt-1", "fs", nivel, "abaixo do limiar"))
		if w.Code != http.StatusOK {
			t.Fatalf("%s com uma assinatura devolveu %d, quero 200: %s", nivel, w.Code, w.Body.String())
		}
	}
	// Sobe a L5 com dois; desce a L1 com um.
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "op:b", "agt-1", "fs", "L5", "duas pessoas"))
	if w.Code != http.StatusOK {
		t.Fatalf("subida L5: %d %s", w.Code, w.Body.String())
	}
	w2 := httptest.NewRecorder()
	h.handleAutonomySet(w2, pedidoDual(t, privs, "op:b", "", "agt-1", "fs", "L1", "descer e a direccao segura"))
	if w2.Code != http.StatusOK {
		t.Fatalf("descida L5->L1 com UMA assinatura devolveu %d, quero 200: %s", w2.Code, w2.Body.String())
	}
	if got, _ := h.node.Autonomy.registry.Get("agt-1", "fs"); got != autonomy.L1 {
		t.Fatalf("nivel = %s, quero L1", got)
	}
}

// TestAOS305_LimiarEFuncaoDoDestino — o predicado que decide a cerimónia.
func TestAOS305_LimiarEFuncaoDoDestino(t *testing.T) {
	for _, l := range []autonomy.Level{autonomy.L0, autonomy.L1, autonomy.L2, autonomy.L3} {
		if autonomyDualControlRequired(l) {
			t.Errorf("%s nao devia exigir duas assinaturas", l)
		}
	}
	for _, l := range []autonomy.Level{autonomy.L4, autonomy.L5} {
		if !autonomyDualControlRequired(l) {
			t.Errorf("%s devia exigir duas assinaturas — e o limiar em que danger deixa de esperar por um humano", l)
		}
	}
}

// TestAOS305_BootstrapRecusaSetterForaDeOperators — um direito atribuído a quem não tem pubkey é
// uma capacidade anunciada e não cumprida; o arranque aborta.
func TestAOS305_BootstrapRecusaSetterForaDeOperators(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.Operators = map[string]ed25519.PublicKey{"op:a": pub}
	cfg.AutonomySetters = []string{"op:a", "op:fantasma"}
	if _, err := Bootstrap(context.Background(), cfg, io.Discard); !errors.Is(err, ErrBadAutonomySetters) {
		t.Fatalf("setter fora de AOS_OPERATORS devia abortar com ErrBadAutonomySetters, veio: %v", err)
	}
	// CONTROLO — com o setter a constar, arranca e o conjunto fica no nó.
	cfg.AutonomySetters = []string{"op:a"}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if !node.AutonomySetters["op:a"] || len(node.AutonomySetters) != 1 {
		t.Fatalf("AutonomySetters no no = %v, quero {op:a}", node.AutonomySetters)
	}
}

// TestAOS305_ParseSetters — a gramática: vírgulas, espaços, duplicados.
func TestAOS305_ParseSetters(t *testing.T) {
	got, err := parseAutonomySetters(" op:b , op:a ,, ")
	if err != nil || len(got) != 2 || got[0] != "op:a" || got[1] != "op:b" {
		t.Fatalf("parse = %v, %v; quero [op:a op:b] ordenado", got, err)
	}
	if got, err := parseAutonomySetters(""); got != nil || err != nil {
		t.Fatalf("vazio devia dar (nil, nil), veio %v %v", got, err)
	}
	if _, err := parseAutonomySetters("op:a,op:a"); !errors.Is(err, ErrBadAutonomySetters) {
		t.Fatalf("duplicado devia falhar com ErrBadAutonomySetters, veio %v", err)
	}
	if _, err := parseAutonomySetters(",,"); !errors.Is(err, ErrBadAutonomySetters) {
		t.Fatalf("so virgulas devia falhar, veio %v", err)
	}
}

// TestAOS305_BannerNosDoisSentidos — o banner declara a postura real: sem setters diz que a rota
// recusa tudo; com setters nomeia-os e declara o limiar de duas assinaturas.
func TestAOS305_BannerNosDoisSentidos(t *testing.T) {
	sem := strings.Join(autonomySettersBanner(nil), "\n")
	if !strings.Contains(sem, "NENHUM operador") || !strings.Contains(sem, "RECUSA") {
		t.Errorf("sem setters o banner devia declarar a recusa total:\n%s", sem)
	}
	com := strings.Join(autonomySettersBanner(map[string]bool{"op:b": true, "op:a": true}), "\n")
	for _, exigido := range []string{"2 operador(es)", "op:a,op:b", "L4 ou L5", "DUAS assinaturas", "co_emitter"} {
		if !strings.Contains(com, exigido) {
			t.Errorf("banner com setters nao contem %q:\n%s", exigido, com)
		}
	}
}
