package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// O MEMO DO LEITOR — e o controlo que tem de vir PRIMEIRO.
//
// O memo existe para que uma credencial verificada NESTE pedido não seja re-verificada NESTE
// pedido (ver [memoLeitor]). A maneira de o escrever mal não é subtil: basta criá-lo FORA do
// closure por pedido, e uma verificação passa a servir TODOS os pedidos — um bypass de
// autenticação completo, que passaria em qualquer teste de caminho feliz e em qualquer teste de
// «o memo funciona».
//
// Por isso o primeiro teste deste ficheiro não prova que o memo serve; prova que ele NÃO
// atravessa a fronteira do pedido.
// ---------------------------------------------------------------------------------------------

// TestMemoNAOAtravessaAFronteiraDoPedido é o controlo que impede o bypass.
//
// A credencial aceita a PRIMEIRA verificação e recusa todas as seguintes (é o duplo do
// anti-replay por `jti`). Dois pedidos SEPARADOS: o primeiro tem de passar, o segundo tem de ser
// RECUSADO. Se o memo fosse partilhado, o segundo passava — e o anti-replay estaria desligado
// sem que nada o dissesse.
func TestMemoNAOAtravessaAFronteiraDoPedido(t *testing.T) {
	h, _, worm := noParaTeste(t)
	cred := &credencialDeUmaVez{board: "board:eu"}
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, cred, worm, time.Now)

	// Pelo invólucro REAL, que é onde o memo nasce.
	envolvido := h.barreirasDe(planoDados, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.readGov.authorize(r); !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	w1 := httptest.NewRecorder()
	envolvido(w1, httptest.NewRequest("GET", "/qualquer", nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("o PRIMEIRO pedido devia passar: %d", w1.Code)
	}

	w2 := httptest.NewRecorder()
	envolvido(w2, httptest.NewRequest("GET", "/qualquer", nil))
	if w2.Code == http.StatusOK {
		t.Fatal("o SEGUNDO pedido passou com uma credencial que so vale uma vez — o memo " +
			"atravessou a fronteira do pedido e o anti-replay esta DESLIGADO. E um bypass de " +
			"autenticacao, nao uma optimizacao com um efeito lateral")
	}
	if cred.verificoes != 2 {
		t.Errorf("o verificador foi chamado %d vez(es) — cada PEDIDO tem de ir la uma vez", cred.verificoes)
	}
}

// TestMemoServeODuploPedidoNoMESMOPedido é a propriedade que o memo existe para dar.
func TestMemoServeODuploPedidoNoMESMOPedido(t *testing.T) {
	h, _, worm := noParaTeste(t)
	cred := &credencialDeUmaVez{board: "board:eu"}
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, cred, worm, time.Now)

	var segundaOK bool
	var repetidas int
	envolvido := h.barreirasDe(planoDados, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.readGov.authorize(r); !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// SEGUNDA autorização no MESMO pedido — o padrão que causou o defeito do PR #96.
		_, segundaOK = h.readGov.authorize(r)
		repetidas = memoDe(r).repetidas
		w.WriteHeader(http.StatusOK)
	})

	envolvido(httptest.NewRecorder(), httptest.NewRequest("GET", "/qualquer", nil))

	if !segundaOK {
		t.Error("a SEGUNDA autorizacao do mesmo pedido falhou — e o defeito do #96 a acontecer " +
			"outra vez: o token e o mesmo, foi apresentado uma vez, e o replay e nosso")
	}
	if cred.verificoes != 1 {
		t.Errorf("o verificador foi chamado %d vezes no MESMO pedido, queria 1", cred.verificoes)
	}
	// E a repetição fica CONTADA. Sem isto o memo tornaria muda a própria prova: um teste que só
	// meça `verificoes` deixaria de conseguir distinguir «não repetiu» de «repetiu e o memo
	// absorveu», e a bateria que guarda o #96 ficaria vacuosa sem uma linha mudar.
	if repetidas != 1 {
		t.Errorf("repetidas=%d, queria 1 — sem este contador a repeticao fica invisivel", repetidas)
	}
}

// TestSemMemoVERIFICAComoSempre é o ramo de degradação, e tem de degradar para o lado seguro.
//
// Um pedido construído à mão (metade dos testes deste pacote) ou um handler que derive um
// contexto novo não trazem memo. Sem memo, verifica-se — nunca se aceita sem verificar.
func TestSemMemoVERIFICAComoSempre(t *testing.T) {
	h, _, worm := noParaTeste(t)
	cred := &credencialDeUmaVez{board: "board:eu"}
	h.readGov = newReadGovernance(regioesFixas{"board:eu": "eu"}, cred, worm, time.Now)

	r := httptest.NewRequest("GET", "/qualquer", nil) // SEM invólucro: sem memo.
	if memoDe(r) != nil {
		t.Fatal("este pedido nao devia trazer memo — o cenario nao esta montado")
	}
	if _, ok := h.readGov.authorize(r); !ok {
		t.Fatal("a primeira devia passar")
	}
	if _, ok := h.readGov.authorize(r); ok {
		t.Error("sem memo, a segunda verificacao TEM de ir ao verificador e ser recusada — se " +
			"passou, alguem trocou o fail-closed por um default permissivo")
	}
	if cred.verificoes != 2 {
		t.Errorf("verificoes=%d, queria 2 (sem memo nao ha cache)", cred.verificoes)
	}
}

// TestTODOSOsPlanosInjectamOMemo é o teste de CABLAGEM, e é a quinta vez que o escrevo hoje.
//
// `barreirasDe` tinha três ramos e um deles — o dos planos ABERTO e DADOS — devolvia o handler
// CRU, sem invólucro nenhum. Uma correcção que injectasse o memo só nos ramos com barreira de
// admissão passaria em todos os testes acima (que usam `planoDados`… se eu tivesse escolhido
// `planoControlo`) e deixaria de fora as rotas mais chamadas do nó, incluindo `POST /runs`.
func TestTODOSOsPlanosInjectamOMemo(t *testing.T) {
	h, _, _ := noParaTeste(t)
	// O balde de admissão do plano de controlo não vem composto pelo `noParaTeste`, e sem ele os
	// planos de governação e controlo desreferenciam nil ANTES de chegarem ao handler. Capacidade
	// <= 0 significa SEM limite, que é o que se quer aqui: mede-se a injecção do memo, não a
	// admissão.
	h.ctrlBucket = &tokenBucket{}
	for _, p := range []struct {
		nome  string
		plano plano
	}{
		{"aberto", planoAberto},
		{"dados", planoDados},
		{"governacao", planoGovernacao},
		{"controlo", planoControlo},
	} {
		var visto bool
		envolvido := h.barreirasDe(p.plano, func(w http.ResponseWriter, r *http.Request) {
			visto = memoDe(r) != nil
		})
		envolvido(httptest.NewRecorder(), httptest.NewRequest("GET", "/qualquer", nil))
		if !visto {
			t.Errorf("plano %s: o handler nao recebeu memo — as rotas deste plano ficam com o "+
				"defeito do #96 por fechar", p.nome)
		}
	}
}
