package main

// CREDENCIAL RECUSADA — dar nome, no log do operador, a uma recusa que a resposta HTTP não
// pode explicar.
//
// [readGovernance.autorizarSemMemo] descartava o `err` de [readCredentialVerifier.verify]:
// replay de `jti`, assinatura inválida, janela fora de prazo e claim `board` em falta
// colapsavam todos num `false` mudo. A resposta é uniforme por desenho — não se revela ao
// chamador qual invariante falhou — pelo que a causa não existia em lado NENHUM.
//
// Estes testes trancam as três propriedades que tornam a declaração útil e segura:
//
//   - o REPLAY é nomeado como replay (o caso cuja causa não se deduz: credencial válida,
//     dentro do prazo, e recusada);
//   - uma credencial APRESENTADA e inválida por outra razão também é nomeada;
//   - a AUSÊNCIA de credencial NÃO é nomeada — senão qualquer sonda anónima escreveria no
//     log do nó à vontade, e a observabilidade que se acrescenta passaria a ser um vector.
//
// E a invariante que não pode mudar: a DECISÃO continua a ser negar, com ou sem log.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aos-ref/integration/oidc"
)

// credQueFalha é um [readCredentialVerifier] que recusa SEMPRE com o erro que lhe for dado —
// o mínimo para exercitar o ramo de erro sem montar um IdP.
type credQueFalha struct{ err error }

func (c credQueFalha) verify(_ context.Context, _ *http.Request) (string, string, error) {
	return "", "", c.err
}

// govComCredencialQueFalha compõe o mínimo: a credencial forte COMPOSTA (é `cred != nil` que
// leva o código ao ramo sob teste) e um colector do log do operador.
func govComCredencialQueFalha(err error, colector *[]string) *readGovernance {
	return &readGovernance{
		cred: credQueFalha{err: err},
		declararRecusa: func(formato string, args ...any) {
			// Formata-se como o [NodeService.log] real (printf): a asserção observa o texto
			// que o operador veria, não o formato cru.
			*colector = append(*colector, fmt.Sprintf(formato, args...))
		},
	}
}

// TestCredencialRecusada_ReplayENomeado: o caso que motivou a mudança. Um token reutilizado é
// recusado com a credencial certa e dentro do prazo — sem isto, o operador não tem por onde
// começar.
func TestCredencialRecusada_ReplayENomeado(t *testing.T) {
	var log []string
	g := govComCredencialQueFalha(oidc.ErrTokenReplayed, &log)

	_, ok := g.autorizarSemMemo(httptest.NewRequest(http.MethodPost, "/runs", nil))

	if ok {
		t.Fatal("uma credencial recusada NAO pode autorizar — a decisao mudou")
	}
	if len(log) != 1 {
		t.Fatalf("esperava UMA declaracao, obtive %d: %v", len(log), log)
	}
	if !strings.Contains(log[0], "REPLAY") {
		t.Fatalf("o replay tem de ser nomeado COMO replay, senao nao se distingue das outras recusas.\nlog: %q", log[0])
	}
}

// TestCredencialRecusada_OutraInvalidezENomeada: qualquer credencial apresentada e recusada
// deixa rasto, não só o replay. Sem este caso, o filtro podia ser escrito como "só declara
// replay" e todas as outras causas voltariam ao silêncio.
func TestCredencialRecusada_OutraInvalidezENomeada(t *testing.T) {
	var log []string
	g := govComCredencialQueFalha(ErrNoBoardClaim, &log)

	if _, ok := g.autorizarSemMemo(httptest.NewRequest(http.MethodPost, "/runs", nil)); ok {
		t.Fatal("uma credencial sem claim board NAO pode autorizar")
	}
	if len(log) != 1 {
		t.Fatalf("uma credencial APRESENTADA e invalida tem de ser nomeada; obtive %d declaracoes: %v", len(log), log)
	}
}

// TestCredencialRecusada_AusenciaNaoENomeada é a metade que protege o nó: sem credencial
// nenhuma NÃO se escreve no log.
//
// É o que uma sonda anónima produz. Declará-lo daria a quem não está autenticado a capacidade
// de encher o log do nó — trocar um ponto cego por um vector seria uma correcção pior do que o
// defeito.
func TestCredencialRecusada_AusenciaNaoENomeada(t *testing.T) {
	var log []string
	g := govComCredencialQueFalha(ErrNoReadCredential, &log)

	if _, ok := g.autorizarSemMemo(httptest.NewRequest(http.MethodGet, "/runs/x", nil)); ok {
		t.Fatal("sem credencial NAO pode autorizar")
	}
	if len(log) != 0 {
		t.Fatalf("VECTOR: a ausencia de credencial escreveu no log — uma sonda anonima podia enche-lo.\nlog: %v", log)
	}
}

// TestCredencialRecusada_SemColectorNaoRebenta: `declararRecusa` nil é a composição legada e a
// dos testes que não observam o log. Tem de continuar a negar, em silêncio, sem panicar.
func TestCredencialRecusada_SemColectorNaoRebenta(t *testing.T) {
	g := &readGovernance{cred: credQueFalha{err: oidc.ErrTokenReplayed}}

	if _, ok := g.autorizarSemMemo(httptest.NewRequest(http.MethodPost, "/runs", nil)); ok {
		t.Fatal("sem colector a decisao tem de continuar a ser NEGAR")
	}
}

// TestCredencialRecusada_SentinelaEmbrulhadaAindaEReconhecida guarda a premissa de que o
// filtro anti-vector depende: a ausência de credencial tem de continuar reconhecível DEPOIS de
// atravessar a fronteira do verificador.
//
// É o caso realista, e não a comparação de um sentinela consigo próprio (que seria trivialmente
// verdadeira e não observaria nada): [oidcReadCredential.verify] propaga erros tipados, e um
// refactor que passasse a embrulhá-los com `fmt.Errorf` SEM `%w` faria o `errors.Is` deixar de
// casar. O filtro passaria então a declarar no log toda a sonda anónima — o vector que
// [readGovernance.nomearCredencialRecusada] existe para não abrir — e nenhum dos outros testes
// ficaria vermelho, porque todos injectam o sentinela cru.
func TestCredencialRecusada_SentinelaEmbrulhadaAindaEReconhecida(t *testing.T) {
	embrulhada := fmt.Errorf("verify: %w", ErrNoReadCredential)

	var log []string
	g := govComCredencialQueFalha(embrulhada, &log)

	if _, ok := g.autorizarSemMemo(httptest.NewRequest(http.MethodGet, "/runs/x", nil)); ok {
		t.Fatal("uma credencial ausente, ainda que embrulhada, NAO pode autorizar")
	}
	if len(log) != 0 {
		t.Fatalf("VECTOR: a ausencia deixou de ser reconhecida depois de embrulhada e foi para o log.\nlog: %v", log)
	}
	// A metade que prova que o teste acima não passa por vacuidade: se o `errors.Is` deixasse
	// de casar, ISTO é o que mudaria — e o caso do replay continua a ser nomeado.
	if !errors.Is(embrulhada, ErrNoReadCredential) {
		t.Fatal("o embrulho perdeu o sentinela — %w em falta na cadeia de verify")
	}
}
