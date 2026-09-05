package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// TestPayloadDeAutonomiaAmarraNivelEMotivo é o controlo central desta rota.
//
// A assinatura do emissor cobre o payload canónico. Se o NÍVEL não estivesse lá dentro, uma
// assinatura legítima de "L1" seria reapresentável como "L5" — o operador assinaria conceder
// pouca autonomia e alguém aplicaria muita, com o selo a nomeá-lo a ele.
//
// E o MOTIVO também entra. Sem isso, a justificação seria um campo que se troca depois de
// assinado: o registo ficaria com a assinatura de uma decisão e o texto de outra.
func TestPayloadDeAutonomiaAmarraNivelEMotivo(t *testing.T) {
	base := integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "leitura de rotina")

	variantes := map[string][]byte{
		"outro nivel":   integration.CanonicalAutonomyPayload("agt-1", "fs", "L5", "leitura de rotina"),
		"outro motivo":  integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "outra coisa"),
		"outro agente":  integration.CanonicalAutonomyPayload("agt-2", "fs", "L1", "leitura de rotina"),
		"outro dominio": integration.CanonicalAutonomyPayload("agt-1", "http", "L1", "leitura de rotina"),
	}
	for nome, v := range variantes {
		if string(v) == string(base) {
			t.Errorf("%s: payload IGUAL ao base — a assinatura serviria para os dois", nome)
		}
	}

	// Deslizamento de fronteira: sem length-prefix, ("agt", "1fs") e ("agt1", "fs") colidiriam,
	// e uma assinatura de um par valeria para outro.
	if string(integration.CanonicalAutonomyPayload("agt", "1fs", "L1", "x")) ==
		string(integration.CanonicalAutonomyPayload("agt1", "fs", "L1", "x")) {
		t.Error("colisao por deslizamento de fronteira entre agent e domain")
	}
}

// TestEmissorAssinadoEAceiteEOsOutrosNao percorre os controlos que separam "a rota existe" de "a
// rota autentica": emissor registado passa, desconhecido não, e o MESMO nonce não passa duas
// vezes.
//
// O último é o que impede capturar um pedido legítimo e reaplicá-lo — que numa API que muda o
// nível de supervisão seria uma escalada de privilégio silenciosa.
func TestEmissorAssinadoEAceiteEOsOutrosNao(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	outraPub, outraPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = outraPub

	auth, err := integration.NewEd25519Authenticator(memNonceStore{vistos: map[string]bool{}}, 5*time.Minute)
	if err != nil {
		t.Fatalf("autenticador: %v", err)
	}
	auth.Register("human:op", pub)

	payload := integration.CanonicalAutonomyPayload("agt-1", "fs", "L4", "porque sim")
	agora := time.Now().UTC()

	// (1) emissor REGISTADO, assinatura sobre este payload ⇒ aceite.
	em, err := integration.SignEmitter("human:op", priv, integration.AutonomyScope, control.SignalAutonomy, payload, agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em); err != nil {
		t.Fatalf("emissor registado devia passar: %v", err)
	}

	// (2) CONTROLO — o MESMO emitter outra vez. O nonce é de uso único.
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em); err == nil {
		t.Fatal("o MESMO nonce passou duas vezes — um pedido capturado seria reaplicavel")
	}

	// (3) CONTROLO — assinatura de uma chave NÃO registada.
	em2, err := integration.SignEmitter("human:intruso", outraPriv, integration.AutonomyScope, control.SignalAutonomy, payload, agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em2); err == nil {
		t.Fatal("um emissor NAO registado foi aceite")
	}

	// (4) CONTROLO — assinatura válida, mas para OUTRO nível. É o cenário que o payload existe
	// para fechar: a mesma pessoa, a mesma chave, um pedido diferente do que assinou.
	em3, err := integration.SignEmitter("human:op", priv, integration.AutonomyScope, control.SignalAutonomy,
		integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "porque sim"), agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em3); err == nil {
		t.Fatal("uma assinatura de L1 foi aceite para uma mudanca para L4")
	}
}

// TestNivelDeWireEFailClosed — "L9", "l4 " ou vazio não podem resolver para o valor-zero, que é
// L0. Resolver silenciosamente daria um 200 a quem pediu outra coisa: o operador acreditaria ter
// mudado a postura e teria aplicado a mais restritiva.
func TestNivelDeWireEFailClosed(t *testing.T) {
	for _, mau := range []string{"", "L9", "9", "alto", "LX", "L-1"} {
		if _, ok := parseAutonomyLevelWire(mau); ok {
			t.Errorf("level %q foi aceite", mau)
		}
	}
	for entrada, quero := range map[string]string{
		"L0": "L0", "l4": "L4", " L5 ": "L5", "L3": "L3",
	} {
		got, ok := parseAutonomyLevelWire(entrada)
		if !ok || got.String() != quero {
			t.Errorf("level %q -> (%v,%v), quero %s", entrada, got, ok, quero)
		}
	}
}

// memNonceStore e um armazem de nonces EM MEMORIA para os testes. Consome de verdade: o par
// (scope, nonce) so passa a primeira vez. Um duplo que aceitasse sempre tornaria o controlo de
// replay verde sem exercitar nada.
type memNonceStore struct{ vistos map[string]bool }

func (m memNonceStore) ConsumeNonce(_ context.Context, scope string, nonce []byte) (bool, error) {
	k := fmt.Sprintf("%s|%x", scope, nonce)
	if m.vistos[k] {
		return false, nil
	}
	m.vistos[k] = true
	return true, nil
}

// TestPisoDeAmbienteEFailClosed — a fronteira de ambiente do piso.
//
// VAZIO ⇒ L0, exactamente como sem a variável: um nó que não a defina não muda de comportamento.
// Um valor FORA do vocabulário ABORTA em vez de cair no valor-zero — que é L0 e passaria por
// "aceite" enquanto ignorava em silêncio o que o operador escreveu. Um typo que produz a postura
// mais restritiva é o pior tipo de typo: nada parece errado, logo ninguém o procura.
func TestPisoDeAmbienteEFailClosed(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_DEFAULT", "")
	if lvl, err := parseAutonomyDefault(); err != nil || lvl.String() != "L0" {
		t.Fatalf("vazio -> (%v,%v), quero (L0,nil)", lvl, err)
	}
	t.Setenv("AOS_AUTONOMY_DEFAULT", "L3")
	if lvl, err := parseAutonomyDefault(); err != nil || lvl.String() != "L3" {
		t.Fatalf("L3 -> (%v,%v), quero (L3,nil)", lvl, err)
	}
	for _, mau := range []string{"L9", "alto", "3", "LX"} {
		t.Setenv("AOS_AUTONOMY_DEFAULT", mau)
		if _, err := parseAutonomyDefault(); !errors.Is(err, ErrBadAutonomyDefault) {
			t.Errorf("AOS_AUTONOMY_DEFAULT=%q devia abortar, veio %v", mau, err)
		}
	}
}

// TestConfigAceitaClasseERecusaAmbiguidade — a fronteira de configuração da cascata.
func TestConfigAceitaClasseERecusaAmbiguidade(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_LEVELS", "agt-1:fs=L4,class:agent-worker:http=L3")
	specs, err := parseAutonomyLevels()
	if err != nil {
		t.Fatalf("config valida recusada: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("li %d entradas, quero 2", len(specs))
	}
	if specs[0].agent != "agt-1" || specs[0].domain != "fs" {
		t.Errorf("instancia mal lida: %+v", specs[0])
	}
	// A entrada de classe fica com o PREFIXO no alvo — é o que a distingue de uma instância no
	// registo, e o que faz a regra passar pelo mesmo selo e histórico das outras.
	if specs[1].agent != autonomy.ClassPrefix+"agent-worker" || specs[1].domain != "http" {
		t.Errorf("classe mal lida: %+v", specs[1])
	}

	// CONTROLO — um agente NÃO pode invadir o namespace das classes. Sem esta recusa,
	// `class:x:fs` seria ambíguo entre "a classe x" e "o agente literalmente chamado class:x", e
	// a ambiguidade decidir-se-ia por ordem de código em vez de por intenção.
	t.Setenv("AOS_AUTONOMY_LEVELS", "class:x:fs=L4")
	if s, err := parseAutonomyLevels(); err != nil {
		t.Fatalf("class:x:fs devia ser lido como CLASSE x: %v", err)
	} else if s[0].agent != autonomy.ClassPrefix+"x" {
		t.Errorf("class:x:fs foi lido como %q", s[0].agent)
	}

	// E as recusas de sempre continuam.
	for _, mau := range []string{"agt-1=L4", "agt-1:fs", "agt-1:fs=L9", ":fs=L4", "agt-1:=L4"} {
		t.Setenv("AOS_AUTONOMY_LEVELS", mau)
		if _, err := parseAutonomyLevels(); err == nil {
			t.Errorf("config %q foi aceite", mau)
		}
	}
}

// TestBannerNaoAfirmaOPresente fecha duas mentiras que ESTE trabalho introduziu no banner.
//
// A primeira: enquanto o piso era sempre L0, dizer "par nao registado ⇒ L0" era verdade. Com
// AOS_AUTONOMY_DEFAULT deixou de ser, e anunciar L0 quando alguem declarou L3 diz ao operador que
// o sistema e MAIS supervisionado do que e — o sentido errado para uma imprecisao de seguranca.
//
// A segunda: a contagem e a do ARRANQUE. Com POST /autonomy os niveis mudam em runtime, e uma
// linha que afirme o presente descrevendo o passado e pior do que nao afirmar nada. O banner tem
// de dizer que e uma fotografia, e apontar onde esta o filme.
func TestBannerNaoAfirmaOPresente(t *testing.T) {
	w := buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "fs", level: autonomy.L4},
	}, autonomy.L3)
	if err := w.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	linha := strings.Join(autonomyPostureBanner(w), "\n")

	// O piso ANUNCIADO tem de ser o piso REAL.
	if !strings.Contains(linha, "L3") {
		t.Errorf("o banner nao anuncia o piso declarado L3:\n%s", linha)
	}
	if strings.Contains(linha, "⇒ L0") {
		t.Errorf("o banner continua a afirmar L0 com um piso L3 declarado:\n%s", linha)
	}
	// E tem de declarar que e uma fotografia do arranque, com o GET como fonte de verdade.
	for _, exigido := range []string{"NO ARRANQUE", "GET /autonomy", "DECLARADO em AOS_AUTONOMY_DEFAULT"} {
		if !strings.Contains(linha, exigido) {
			t.Errorf("o banner nao contem %q:\n%s", exigido, linha)
		}
	}

	// CONTROLO — sem piso declarado, o banner tem de dizer L0 E que e por OMISSAO. Sao posturas
	// identicas com responsaveis diferentes, e confundi-las apaga a informacao que o piso
	// declaravel introduziu.
	w2 := buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "fs", level: autonomy.L4},
	}, autonomy.L0)
	if err := w2.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatal(err)
	}
	l2 := strings.Join(autonomyPostureBanner(w2), "\n")
	if !strings.Contains(l2, "por omissao") {
		t.Errorf("sem piso declarado o banner devia dizer que L0 e por OMISSAO:\n%s", l2)
	}
}

// ---------------------------------------------------------------------------
// OS DOIS CONTROLOS QUE O PLANO EXIGIU À FASE 1 E QUE NÃO ESTAVAM COBERTOS.
//
// O plano (docs/plano-autonomia-operavel.md) escreveu quatro controlos para a rota. Três estavam
// exercidos — emissor não registado recusado, nonce reutilizado recusado, assinatura amarrada ao
// nível. Ao ir marcar a fase como feita, dei por mim a escrevê-lo de memória: faltavam DOIS.
//
// É o mesmo padrão do resto do dia — a verificação cobre o que ocorreu a quem a escreveu — e a
// única defesa que funcionou até agora foi ir CONFERIR em vez de recordar.
// ---------------------------------------------------------------------------

// noParaTeste monta o mínimo da rota: registo com sink ligado a um WORM em memória, autenticador
// com um emissor registado, e o par de chaves para assinar.
func noParaTeste(t *testing.T) (*apiHandler, ed25519.PrivateKey, audit.Store) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	worm := audit.NewMemStore()
	sink := &autonomyWORMSink{}
	sink.bind(worm)
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(sink))

	auth, err := integration.NewEd25519Authenticator(memNonceStore{vistos: map[string]bool{}}, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	auth.Register("human:op", pub)

	h := &apiHandler{
		node: &Node{
			Autonomy:  &autonomyWiring{registry: reg, sink: sink},
			SteerAuth: auth,
			// AOS-305: assinar não chega — o emissor tem de deter autonomy:set.
			AutonomySetters: map[string]bool{"human:op": true},
			WORM:            worm,
		},
		cfg: apiConfig{maxBodyBytes: 1 << 20, now: time.Now},
	}
	return h, priv, worm
}

func pedidoDeAutonomia(t *testing.T, priv ed25519.PrivateKey, id, agente, dominio, nivel, motivo string) *http.Request {
	t.Helper()
	payload := integration.CanonicalAutonomyPayload(agente, dominio, nivel, motivo)
	em, err := integration.SignEmitter(id, priv, integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	corpo, err := json.Marshal(map[string]any{
		"emitter": emissorDeWire(em), "agent": agente, "domain": dominio, "level": nivel, "reason": motivo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest("POST", "/autonomy", bytes.NewReader(corpo))
}

// TestSeloNomeiaQuemAssinouENaoOQueOCorpoDiz — o `actor` selado vem do EMISSOR VERIFICADO.
//
// Se viesse do corpo, o chamador escolheria em nome de quem a mudança aparece no registo — e a
// pergunta a que uma auditoria destas tem de responder ("quem baixou a supervisão, e porquê")
// passaria a ser respondida por quem tem interesse na resposta.
func TestSeloNomeiaQuemAssinouENaoOQueOCorpoDiz(t *testing.T) {
	h, priv, worm := noParaTeste(t)

	// L3 e não L4: desde AOS-305 mudar PARA L4/L5 exige duas assinaturas, e o que este teste
	// mede é a ATRIBUIÇÃO do actor, não o limiar — que tem o seu próprio teste.
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDeAutonomia(t, priv, "human:op", "agt-1", "fs", "L3", "leitura de rotina corre sozinha"))
	if w.Code != http.StatusOK {
		t.Fatalf("POST devia aplicar: %d %s", w.Code, w.Body.String())
	}

	// (1) O selo da partição de AUTONOMIA nomeia o emissor e guarda o motivo.
	head, err := worm.Head(t.Context(), autonomy.DefaultAutonomyPartition)
	if err != nil || head == 0 {
		t.Fatalf("nada selado na particao de autonomia (head=%d, err=%v)", head, err)
	}
	recs, err := worm.Read(t.Context(), autonomy.DefaultAutonomyPartition, 1, head)
	if err != nil || len(recs) == 0 {
		t.Fatalf("ler particao de autonomia: %v", err)
	}
	selo := recs[len(recs)-1]
	if selo.Principal.NHIID != "human:op" {
		t.Errorf("o selo nomeia %q, tinha de nomear o EMISSOR VERIFICADO human:op", selo.Principal.NHIID)
	}
	// O motivo e o actor ficam nos PARAMS da obrigacao `autonomy.level_changed` — ligados ao
	// EntryHash pelo conteudo canonico, como o resto do registo. Procurei-os primeiro no campo
	// `Reason` do registo e nao estavam la: o teste acusava o sistema de nao selar o motivo
	// quando o defeito era meu. Vale a pena o comentario porque a proxima pessoa procura no
	// mesmo sitio errado.
	if len(selo.Obligations) == 0 {
		t.Fatalf("o selo nao tem obrigacao autonomy.level_changed: %+v", selo)
	}
	params := selo.Obligations[0].Params
	if !strings.Contains(params["reason"], "leitura de rotina") {
		t.Errorf("o motivo nao ficou no selo: %q — uma mudanca de nivel sem justificacao nao e auditavel", params["reason"])
	}
	if params["actor"] != "human:op" {
		t.Errorf("o actor do selo e %q, tinha de ser o emissor verificado", params["actor"])
	}
	if params["new_level"] != "L3" {
		t.Errorf("o selo diz new_level=%q, o POST aplicou L3", params["new_level"])
	}

	// (2) O selo de ACÇÃO DE CONTROLO nomeia o mesmo emissor.
	hc, err := worm.Head(t.Context(), controlSealPartition)
	if err != nil || hc == 0 {
		t.Fatalf("nada selado em %s (head=%d)", controlSealPartition, hc)
	}
	rc, err := worm.Read(t.Context(), controlSealPartition, 1, hc)
	if err != nil || len(rc) == 0 {
		t.Fatalf("ler %s: %v", controlSealPartition, err)
	}
	if got := rc[len(rc)-1].Principal.NHIID; got != "human:op" {
		t.Errorf("o selo de controlo nomeia %q, quero human:op", got)
	}

	// (3) CONTROLO — o corpo NÃO PODE transportar um actor. O decodificador recusa campos
	// desconhecidos, pelo que a tentativa nem chega à autenticação. É a forma mais forte da
	// afirmação: não é que o actor do corpo seja ignorado — é que não existe campo onde o pôr.
	payload := integration.CanonicalAutonomyPayload("agt-1", "fs", "L5", "outra")
	em, err := integration.SignEmitter("human:op", priv, integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	corpo, _ := json.Marshal(map[string]any{
		"emitter": emissorDeWire(em), "agent": "agt-1", "domain": "fs", "level": "L5", "reason": "outra",
		"actor": "human:outro-qualquer",
	})
	w2 := httptest.NewRecorder()
	h.handleAutonomySet(w2, httptest.NewRequest("POST", "/autonomy", bytes.NewReader(corpo)))
	if w2.Code != http.StatusBadRequest {
		t.Errorf("um corpo com `actor` devolveu %d, quero 400 — se passar, ha um campo onde escolher o autor", w2.Code)
	}
}

// TestGetReflecteOPost — o quarto controlo da Fase 1.
//
// Sem isto, `POST` podia devolver `200` e o `GET` continuar a mostrar a postura antiga: o operador
// acreditaria ter mudado o sistema. É o mesmo raciocínio pelo qual a rota devolve `501` quando o
// oráculo não está composto, em vez de um `200` que não faz nada.
func TestGetReflecteOPost(t *testing.T) {
	h, priv, worm := noParaTeste(t)
	// O GET passou a exigir a credencial forte do gate soberano, como o /dsar/erase.
	comLeitura(h, worm, regioesFixas{"board:prod": "eu"})

	// CONTROLO ANTES: o par ainda não existe, e o GET não o inventa.
	w0 := httptest.NewRecorder()
	h.handleAutonomyGet(w0, pedidoComLeitor("GET", "/autonomy", "board:prod", ""))
	if strings.Contains(w0.Body.String(), "agt-7") {
		t.Fatalf("o GET ja mostra um par que nunca foi registado: %s", w0.Body.String())
	}

	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDeAutonomia(t, priv, "human:op", "agt-7", "http", "L3", "tiering para egress"))
	if w.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.handleAutonomyGet(w2, pedidoComLeitor("GET", "/autonomy", "board:prod", ""))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET: %d", w2.Code)
	}
	var resp struct {
		Pairs []autonomyPairWire `json:"pairs"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta do GET ilegivel: %v (%s)", err, w2.Body.String())
	}
	achou := false
	for _, p := range resp.Pairs {
		if p.Agent == "agt-7" && p.Domain == "http" {
			achou = true
			if p.Level != "L3" {
				t.Errorf("o GET mostra %s, o POST aplicou L3 — a leitura nao reflecte a escrita", p.Level)
			}
		}
	}
	if !achou {
		t.Errorf("o par aplicado pelo POST nao aparece no GET: %s", w2.Body.String())
	}
}

// emissorDeWire converte o [control.Emitter] na forma de WIRE que a rota aceita.
//
// `control.Emitter` não tem tags JSON e o `Signature`/`Nonce` são `[]byte`; serializá-lo directo
// produz `ID`/`Signature` em maiúsculas, que o `decodeJSON` — com `DisallowUnknownFields` —
// recusa com 400. Foi o que aconteceu à primeira versão deste teste, e é a razão de esta
// conversão existir explicitamente em vez de se confiar no marshal por omissão.
func emissorDeWire(em control.Emitter) emitterWire {
	return emitterWire{
		ID:        em.ID,
		Signature: base64.StdEncoding.EncodeToString(em.Signature),
		Nonce:     base64.StdEncoding.EncodeToString(em.Nonce),
		IssuedAt:  em.IssuedAt,
	}
}

// ---------------------------------------------------------------------------
// AUTENTICACAO DAS ROTAS DE LEITURA DE AUTONOMIA.
//
// `GET /autonomy` e `POST /autonomy/simular` nasceram SEM verificacao de credencial. A
// classificacao `planoControlo` da-lhes o balde de admissao e o mTLS — e eu tratei isso como se
// fosse a barreira toda. Nao e: o mTLS NAO esta composto em producao e o edge encaminha tudo, pelo
// que a barreira de transporte e, hoje, um no-op.
//
// A de identidade faltava, e nenhum teste perguntou por ela porque eu so tinha pensado na
// primeira. E a mesma falha do dia numa forma nova: verificar o eixo em que se esta a pensar.
// ---------------------------------------------------------------------------

// regioesFixas é um resolvedor board→região de teste.
type regioesFixas map[string]string

func (m regioesFixas) RegionFor(board string) (string, bool) { r, ok := m[board]; return r, ok }

// comLeitura compõe o gate soberano por HEADERS (cred nil = via legada), que é o suficiente para
// exercitar "há credencial" vs "não há".
func comLeitura(h *apiHandler, worm audit.Store, boards regioesFixas) {
	h.readGov = newReadGovernance(boards, nil, worm, time.Now)
}

func pedidoComLeitor(metodo, caminho, board, corpo string) *http.Request {
	r := httptest.NewRequest(metodo, caminho, strings.NewReader(corpo))
	r.Header.Set("X-Aos-Reader", "human:auditor")
	r.Header.Set("X-Aos-Board", board)
	return r
}

// TestLeiturasDeAutonomiaExigemCredencial — as duas rotas de leitura recusam sem credencial, e
// respondem com ela. Os dois ramos, porque só o par distingue "a barreira decide" de "recusa tudo".
func TestLeiturasDeAutonomiaExigemCredencial(t *testing.T) {
	h, priv, worm := noParaTeste(t)
	comLeitura(h, worm, regioesFixas{"board:prod": "eu"})

	// Um par registado, para o GET ter o que devolver (L3: uma assinatura chega — AOS-305).
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDeAutonomia(t, priv, "human:op", "agt-9", "fs", "L3", "rotina"))
	if w.Code != http.StatusOK {
		t.Fatalf("preparacao falhou: %d %s", w.Code, w.Body.String())
	}

	casos := []struct {
		nome    string
		handler func(http.ResponseWriter, *http.Request)
		metodo  string
		caminho string
		corpo   string
	}{
		{"GET /autonomy", h.handleAutonomyGet, "GET", "/autonomy", ""},
		{"POST /autonomy/simular", h.handleAutonomySimular, "POST", "/autonomy/simular", `{"levels":"","default":"L4","max":5}`},
	}
	for _, k := range casos {
		// (a) SEM credencial ⇒ 403, e nada do corpo é revelado.
		w1 := httptest.NewRecorder()
		k.handler(w1, httptest.NewRequest(k.metodo, k.caminho, strings.NewReader(k.corpo)))
		if w1.Code != http.StatusForbidden {
			t.Errorf("%s sem credencial devolveu %d, quero 403 — corpo: %s", k.nome, w1.Code, w1.Body.String())
		}
		if strings.Contains(w1.Body.String(), "agt-9") {
			t.Errorf("%s sem credencial REVELOU um par: %s", k.nome, w1.Body.String())
		}

		// (b) CONTROLO — com credencial válida responde. Sem este ramo, um handler que recusasse
		// sempre passaria em (a) e o teste ficaria verde a medir nada.
		w2 := httptest.NewRecorder()
		k.handler(w2, pedidoComLeitor(k.metodo, k.caminho, "board:prod", k.corpo))
		if w2.Code != http.StatusOK {
			t.Errorf("%s COM credencial devolveu %d — %s", k.nome, w2.Code, w2.Body.String())
		}

		// (c) CONTROLO — um board DESCONHECIDO é recusado. É o que distingue "verifica a
		// credencial" de "aceita qualquer header".
		w3 := httptest.NewRecorder()
		k.handler(w3, pedidoComLeitor(k.metodo, k.caminho, "board:inventado", k.corpo))
		if w3.Code != http.StatusForbidden {
			t.Errorf("%s com board desconhecido devolveu %d, quero 403", k.nome, w3.Code)
		}
	}
}

// TestSimulacaoDesligadaSemGateComposto — fail-closed: sem governança soberana composta a rota
// não devolve histórico, devolve 501.
//
// A alternativa seria devolver tudo "porque não há gate", que é como uma barreira opcional se
// transforma numa fuga: o ambiente onde ela não está composta é precisamente o menos vigiado.
func TestSimulacaoDesligadaSemGateComposto(t *testing.T) {
	h, _, _ := noParaTeste(t) // readGov fica nil
	for nome, hf := range map[string]func(http.ResponseWriter, *http.Request){
		"GET /autonomy":          h.handleAutonomyGet,
		"POST /autonomy/simular": h.handleAutonomySimular,
	} {
		w := httptest.NewRecorder()
		hf(w, httptest.NewRequest("POST", "/x", strings.NewReader(`{"max":5}`)))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s sem gate composto devolveu %d, quero 501", nome, w.Code)
		}
	}
}

// TestSimulacaoNaoAtravessaRegioes é o controlo da propriedade que a autenticação sozinha NÃO dá.
//
// `authorize` diz QUEM é o leitor. Não diz que ele pode ler um run concreto. Sem o filtro por
// residência, um leitor de um board veria run ids, NHIs e recursos de TODAS as regiões — a recusa
// cross-region (AOS-172/205) contornada por uma rota de conforto, que é a maneira mais barata de
// perder a propriedade central deste sistema.
//
// O filtro não reimplementa a regra: pergunta a `authorizeRead`, a MESMA que o `GET /runs/{id}`
// usa. Este teste prova a consequência.
func TestSimulacaoNaoAtravessaRegioes(t *testing.T) {
	h, _, worm := noParaTeste(t)
	comLeitura(h, worm, regioesFixas{"board:eu": "eu", "board:us": "us"})
	ctx := t.Context()

	// Dois runs em regiões diferentes: residência selada + uma mediação de tool call em cada.
	for _, k := range []struct{ run, regiao, recurso string }{
		{"run-eu", "eu", "doc://europa"},
		{"run-us", "us", "doc://america"},
	} {
		if _, err := worm.Append(ctx, audit.AuditRecord{
			Partition: "gov.residency/" + k.run,
			Resource:  audit.Resource{Type: "run", Value: k.run, Region: k.regiao},
		}); err != nil {
			t.Fatalf("selar residencia de %s: %v", k.run, err)
		}
		if _, err := worm.Append(ctx, audit.AuditRecord{
			Partition: k.run, RunID: k.run, StepID: "s1", ToolID: "doc_read",
			Capability: "cap:fs.read",
			Principal:  audit.Principal{NHIID: "agt-" + k.regiao},
			Resource:   audit.Resource{Type: "file", Value: k.recurso, Region: k.regiao},
		}); err != nil {
			t.Fatalf("selar mediacao de %s: %v", k.run, err)
		}
	}

	corpo := `{"levels":"","default":"L4","max":50}`
	w := httptest.NewRecorder()
	h.handleAutonomySimular(w, pedidoComLeitor("POST", "/autonomy/simular", "board:eu", corpo))
	if w.Code != http.StatusOK {
		t.Fatalf("simular como leitor eu: %d %s", w.Code, w.Body.String())
	}
	resp := w.Body.String()

	// O leitor da UE vê o seu run...
	if !strings.Contains(resp, "run-eu") {
		t.Errorf("o leitor da UE nao viu o SEU proprio run — o filtro esta a recusar tudo: %s", resp)
	}
	// ...e NÃO vê o da outra região, nem o NHI, nem o recurso.
	for _, proibido := range []string{"run-us", "agt-us", "doc://america"} {
		if strings.Contains(resp, proibido) {
			t.Errorf("a simulacao revelou %q a um leitor de outra regiao: %s", proibido, resp)
		}
	}

	// CONTROLO SIMÉTRICO: o leitor dos EUA vê o seu e não vê o europeu. Sem esta metade, um filtro
	// que devolvesse sempre APENAS "run-eu" passaria no bloco acima.
	w2 := httptest.NewRecorder()
	h.handleAutonomySimular(w2, pedidoComLeitor("POST", "/autonomy/simular", "board:us", corpo))
	if w2.Code != http.StatusOK {
		t.Fatalf("simular como leitor us: %d", w2.Code)
	}
	if r2 := w2.Body.String(); !strings.Contains(r2, "run-us") || strings.Contains(r2, "run-eu") {
		t.Errorf("o leitor dos EUA devia ver run-us e nao run-eu: %s", r2)
	}
}

// credencialDeUmaVez imita o ANTI-REPLAY por `jti` do verificador OIDC: a primeira verificação
// passa, as seguintes devolvem replay — que é o que [oidc.Verifier.checkReplay] faz ao marcar o
// token como usado.
type credencialDeUmaVez struct {
	board      string
	verificoes int
}

func (c *credencialDeUmaVez) verify(context.Context, *http.Request) (string, string, error) {
	c.verificoes++
	if c.verificoes > 1 {
		return "", "", errors.New("oidc: token reutilizado (replay de jti)")
	}
	return "human:auditor", c.board, nil
}

// TestSimulacaoVerificaACredencialUMAVez é o teste que faltava — e a sua ausência escondeu um
// defeito que teria tornado a rota INÚTIL em produção.
//
// A primeira versão do filtro de região chamava `authorizeRead` por CADA run distinto, e isso
// re-verifica a credencial de cada vez. Com a credencial OIDC — a de produção — o verificador tem
// anti-replay por `jti`: a verificação do handler consome-o e todas as seguintes falham como
// replay. A rota teria devolvido `avaliados: 0` sempre, e em silêncio.
//
// Os testes existentes não o viam porque compunham o gate com `cred = nil` (a via LEGADA de
// headers), que nunca chama o verificador. Testavam o caminho que produção não usa.
func TestSimulacaoVerificaACredencialUMAVez(t *testing.T) {
	h, _, worm := noParaTeste(t)
	cred := &credencialDeUmaVez{board: "board:eu"}
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, cred, worm, time.Now)
	ctx := t.Context()

	// DOIS runs distintos, ambos da região do leitor: o filtro tem de os ver aos dois.
	for _, run := range []string{"run-a", "run-b"} {
		if _, err := worm.Append(ctx, audit.AuditRecord{
			Partition: "gov.residency/" + run,
			Resource:  audit.Resource{Type: "run", Value: run, Region: "eu"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := worm.Append(ctx, audit.AuditRecord{
			Partition: run, RunID: run, StepID: "s1", ToolID: "doc_read",
			Capability: "cap:fs.read",
			Principal:  audit.Principal{NHIID: "agt-" + run},
			Resource:   audit.Resource{Type: "file", Value: "doc://" + run, Region: "eu"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// PELO INVÓLUCRO REAL, e não chamando o handler à mão.
	//
	// A versão anterior chamava `handleAutonomySimular` directamente. Passava — mas passava por
	// acidente: sem invólucro não há memo do leitor, logo cada autorização ia mesmo ao
	// verificador e `verificoes` contava a verdade. No dia em que alguém encaminhasse este teste
	// pelo mux, o memo absorveria as repetições e a prova ficava muda SEM UMA LINHA MUDAR.
	//
	// Agora vai pelo caminho de produção e mede as DUAS coisas: quantas vezes o verificador foi
	// chamado, e quantas vezes ALGUÉM PEDIU para verificar outra vez. A segunda é a que continua
	// a morder quando o memo está lá.
	h.ctrlBucket = &tokenBucket{}
	var repetidas int
	envolvido := h.barreirasDe(planoControlo, func(w http.ResponseWriter, r *http.Request) {
		h.handleAutonomySimular(w, r)
		if m := memoDe(r); m != nil {
			repetidas = m.repetidas
		}
	})

	w := httptest.NewRecorder()
	envolvido(w, httptest.NewRequest("POST", "/autonomy/simular",
		strings.NewReader(`{"levels":"","default":"L4","max":50}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("simular: %d %s", w.Code, w.Body.String())
	}

	if repetidas != 0 {
		t.Errorf("a rota PEDIU %d re-verificacao(oes) da credencial no mesmo pedido — o memo "+
			"impede que isso custe em producao, mas a travessia por run continua errada e em "+
			"qualquer caminho sem memo volta a devolver `avaliados: 0` em silencio", repetidas)
	}
	if cred.verificoes != 1 {
		t.Errorf("a credencial foi verificada %d vezes — com anti-replay por jti, a segunda falha "+
			"e o historico desaparece em silencio", cred.verificoes)
	}
	corpo := w.Body.String()
	for _, run := range []string{"run-a", "run-b"} {
		if !strings.Contains(corpo, run) {
			t.Errorf("o run %q da REGIAO DO LEITOR desapareceu da simulacao: %s", run, corpo)
		}
	}
}
