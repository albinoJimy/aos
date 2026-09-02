package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// OS TESTES DOS PLANOS.
//
// A versão anterior deste ficheiro verificava TEXTO: procurava `admitControlMTLS(` no corpo de
// cada handler de uma lista escrita à mão. Falhou de duas maneiras ao mesmo tempo:
//
//  1. verificava METADE da afirmação (só o mTLS; as mesmas rotas estavam também fora do balde de
//     admissão, e o teste — verde — não disse nada);
//
//  2. a lista era um conjunto ABERTO. Escrevi-a a partir das rotas em que estava a trabalhar, e
//     NENHUMA rota de DSAR constava dela. O detector partilhava o modo de falha do defeito.
//
// Estes testes trocam as duas propriedades: percorrem a TABELA REAL (conjunto fechado — uma rota
// só é servida se lá estiver) e observam COMPORTAMENTO (pedido real, estado real, corpo alcançado
// ou não), em vez de procurarem uma cadeia de caracteres que pode estar depois de um `return`.
// ---------------------------------------------------------------------------

// handlerDeTeste constrói o mínimo para exercitar as barreiras: os corpos reais nunca correm
// (é isso que se quer medir), pelo que svc/node podem ficar a zero.
func handlerDeTeste(mtls bool) *apiHandler {
	return &apiHandler{
		controlMTLS: mtls,
		// Balde generoso: o que se mede aqui é o mTLS, e um 429 por exaustão de tokens seria
		// um falso verde — 429 != 200, e o teste concluiria "barreira aplicada" pela razão errada.
		ctrlBucket: newTokenBucket(10000, 10000, time.Now),
	}
}

// comCertificado devolve um pedido com cadeia de cliente VERIFICADA, como o `net/http` a entrega
// depois de a validar contra a `ClientCAs` montada.
func comCertificado(r *http.Request) *http.Request {
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{&x509.Certificate{}}}}
	return r
}

// TestPlanoDeControloExigeCertificadoDeCliente é o teste central, e é COMPORTAMENTAL.
//
// Para CADA rota da tabela real, com o mTLS composto, dispara um pedido SEM certificado e observa
// duas coisas: o estado devolvido e se o corpo do handler chegou a ser alcançado. As duas juntas —
// porque um 403 vindo de dentro do handler não seria a mesma coisa que a barreira ter agido.
//
// O CONTROLO que o torna não-vacuoso está nos outros dois ramos: as rotas ABERTAS têm de passar no
// mesmo pedido (senão o invólucro estaria a recusar tudo, e o verde do primeiro ramo não valeria
// nada), e as de CONTROLO têm de passar quando o certificado existe (senão o plano estaria a
// bloquear por si, e não pelo certificado).
func TestPlanoDeControloExigeCertificadoDeCliente(t *testing.T) {
	h := handlerDeTeste(true)
	tabela := h.tabelaDeRotas()
	if len(tabela) < 20 {
		t.Fatalf("a tabela so tem %d rotas — se encolheu, este teste deixa de cobrir o que afirma", len(tabela))
	}

	var controlo, abertas int
	for _, rt := range tabela {
		metodo, caminho, ok := strings.Cut(rt.padrao, " ")
		if !ok {
			t.Fatalf("padrao %q sem metodo — a tabela mudou de forma", rt.padrao)
		}
		caminho = strings.ReplaceAll(caminho, "{id}", "r1")

		// (a) SEM certificado.
		chegou := false
		w := httptest.NewRecorder()
		h.barreirasDe(rt.plano, func(http.ResponseWriter, *http.Request) { chegou = true })(
			w, httptest.NewRequest(metodo, caminho, nil))

		switch rt.plano {
		case planoControlo:
			controlo++
			if w.Code != http.StatusForbidden {
				t.Errorf("%s (%v): sem certificado devolveu %d, quero 403", rt.padrao, rt.plano, w.Code)
			}
			if chegou {
				t.Errorf("%s (%v): o CORPO do handler correu sem certificado de cliente", rt.padrao, rt.plano)
			}
			// (b) COM certificado — o corpo TEM de ser alcançado. Sem este ramo, um invólucro
			// que recusasse SEMPRE passaria no ramo (a) e o teste ficaria verde a medir nada.
			chegou = false
			w2 := httptest.NewRecorder()
			h.barreirasDe(rt.plano, func(http.ResponseWriter, *http.Request) { chegou = true })(
				w2, comCertificado(httptest.NewRequest(metodo, caminho, nil)))
			if !chegou {
				t.Errorf("%s (%v): COM certificado verificado o corpo NAO foi alcancado (%d) — a barreira recusa por si, nao pelo certificado",
					rt.padrao, rt.plano, w2.Code)
			}
		case planoAberto:
			abertas++
			// (c) CONTROLO de não-vacuidade: as rotas abertas passam no MESMO pedido que as de
			// controlo recusam. É o que distingue "a barreira decide" de "o invólucro nega tudo".
			if !chegou {
				t.Errorf("%s (%v): rota ABERTA foi recusada (%d) — sondas e scrape ficariam sem resposta",
					rt.padrao, rt.plano, w.Code)
			}
		}
	}
	if controlo == 0 || abertas == 0 {
		t.Fatalf("o teste nao exercitou os dois lados (controlo=%d, abertas=%d) — seria vacuoso", controlo, abertas)
	}
}

// TestSemMTLSCompostoNadaERecusado fixa a propriedade que tornava o defeito INVISÍVEL, para que
// ela fique escrita em vez de ser folclore.
//
// Sem `AOS_CONTROL_MTLS_CA_PATH`, `admitControlMTLS` devolve `true` sempre. Era por isso que um
// handler que a esquecesse se comportava de forma IDÊNTICA a um que a chamasse — em dev, em CI e
// na produção actual. O sinal só apareceria no dia em que alguém ligasse o mTLS, e apareceria como
// AUSÊNCIA de uma recusa, que ninguém observa.
func TestSemMTLSCompostoNadaERecusado(t *testing.T) {
	h := handlerDeTeste(false) // mTLS NÃO composto
	for _, rt := range h.tabelaDeRotas() {
		if rt.plano != planoControlo {
			continue
		}
		metodo, caminho, _ := strings.Cut(rt.padrao, " ")
		chegou := false
		w := httptest.NewRecorder()
		h.barreirasDe(rt.plano, func(http.ResponseWriter, *http.Request) { chegou = true })(
			w, httptest.NewRequest(metodo, strings.ReplaceAll(caminho, "{id}", "r1"), nil))
		if !chegou {
			t.Errorf("%s: com o mTLS DESLIGADO a rota recusou (%d) — mudou a postura de quem nao o compos", rt.padrao, w.Code)
		}
	}
}

// TestRotaSemPlanoAbortaOArranque — o valor-zero de [plano] é inválido, e o nó recusa servir.
//
// É esta a diferença entre a correcção e mais um teste: uma rota nova sem plano não falha um teste
// que alguém tem de se lembrar de correr — não arranca.
func TestRotaSemPlanoAbortaOArranque(t *testing.T) {
	h := handlerDeTeste(false)
	nada := func(http.ResponseWriter, *http.Request) {}
	err := registar(http.NewServeMux(), h, []rota{{"GET /x", nada, planoPorClassificar}})
	if !errors.Is(err, ErrRotaPorClassificar) {
		t.Fatalf("rota sem plano registou-se (err=%v) — o valor-zero deixou de ser fail-closed", err)
	}
	// CONTROLO: uma rota classificada regista-se sem erro. Sem isto, `registar` podia estar a
	// recusar tudo e o teste acima ficaria verde a medir nada.
	if err := registar(http.NewServeMux(), h, []rota{{"GET /x", nada, planoAberto}}); err != nil {
		t.Fatalf("rota classificada recusada: %v", err)
	}
	// E um padrão duplicado é erro tratável, não PANIC no arranque.
	if err := registar(http.NewServeMux(), h, []rota{
		{"GET /x", nada, planoAberto}, {"GET /x", nada, planoAberto},
	}); err == nil {
		t.Error("padrao duplicado passou — `mux.HandleFunc` entraria em panic no arranque")
	}
}

// TestClassificacaoDasRotasEDeliberada é a única lista escrita à mão que sobra — e é um conjunto
// FECHADO, verificado NOS DOIS SENTIDOS.
//
// A classificação "isto é plano de controlo?" é de JUÍZO, não de sintaxe: nada no código diz que
// mudar níveis de autonomia é mais grave do que ler /healthz. Esse juízo fica aqui, e a diferença
// para o allowlist anterior é decisiva: uma rota NOVA que não conste desta tabela faz o teste
// FALHAR, em vez de passar em silêncio. E BAIXAR uma rota de plano — de controlo para dados, por
// exemplo — obriga a vir aqui escrevê-lo.
func TestClassificacaoDasRotasEDeliberada(t *testing.T) {
	esperado := map[string]plano{
		"GET /healthz": planoAberto,
		"GET /readyz":  planoAberto,
		"GET /metrics": planoAberto,

		"POST /runs":                 planoDados,
		"GET /runs/{id}":             planoDados,
		"GET /runs/{id}/trajectory":  planoDados,
		"GET /runs/{id}/reconstruct": planoDados,

		"POST /runs/{id}/steer":      planoControlo,
		"POST /runs/{id}/pause":      planoControlo,
		"POST /runs/{id}/approve":    planoControlo,
		"POST /runs/{id}/challenge":  planoControlo,
		"POST /runs/{id}/resume":     planoControlo,
		"POST /runs/{id}/exhaustion": planoControlo,
		"POST /autonomy":             planoControlo,
		"GET /autonomy":              planoControlo,
		"POST /autonomy/simular":     planoControlo,
		"POST /promote":              planoControlo,
		// REVOGAÇÃO DE NHI (AOS-288). Plano de CONTROLO, e a decisão é deliberada: revogar um
		// token é uma acção de governação irreversível dentro da janela de vida dele, com a
		// mesma forma das outras — alvo de NÓ (um `jti`, não um run), assinatura ed25519 do
		// operador no corpo produzida fora do processo, nonce durável de uso único, e selo na
		// hash-chain. Não é planoGovernacao porque não se autentica pela credencial forte do
		// gate soberano: autentica-se pelos operadores pinados em AOS_OPERATORS, como o
		// /autonomy e o /promote. Não é planoAberto por razões que não precisam de escrita.
		"POST /nhi/revoke": planoControlo,

		// DECISÃO EM ABERTO, escrita em vez de omitida: o DSAR autentica-se pela credencial forte
		// do gate soberano (readGov) mas NÃO exige certificado de cliente. É a única superfície de
		// governação sem a barreira de transporte, e inclui o crypto-shred — a operação menos
		// reversível do nó. Ver o comentário de `barreirasDe` em planos.go.
		"POST /dsar/erase":   planoGovernacao,
		"POST /dsar/hold":    planoGovernacao,
		"POST /dsar/release": planoGovernacao,
		"POST /dsar/expire":  planoGovernacao,
	}

	real := map[string]plano{}
	for _, rt := range (&apiHandler{}).tabelaDeRotas() {
		real[rt.padrao] = rt.plano
	}

	var problemas []string
	for padrao, quero := range esperado {
		tem, ok := real[padrao]
		if !ok {
			problemas = append(problemas, padrao+": esta nesta lista mas NAO na tabela — foi removida? actualize a lista")
			continue
		}
		if tem != quero {
			problemas = append(problemas, padrao+": a tabela diz "+tem.String()+", esta lista diz "+quero.String()+
				" — se a mudanca foi deliberada, escreva-a aqui e diga PORQUE")
		}
	}
	for padrao := range real {
		if _, ok := esperado[padrao]; !ok {
			problemas = append(problemas, padrao+": rota NOVA sem classificacao deliberada — decida a que plano pertence e escreva-o aqui")
		}
	}
	sort.Strings(problemas)
	if len(problemas) > 0 {
		t.Errorf("a classificacao das rotas divergiu do juizo escrito:\n  %s", strings.Join(problemas, "\n  "))
	}
}

var reRegistoDeRota = regexp.MustCompile(`mux\.HandleFunc\(|mux\.Handle\(`)

// TestRegistoEOUnicoSitioQueRegistaRotas fecha o último buraco do desenho.
//
// Sem isto, alguém acrescentaria `mux.HandleFunc(...)` directamente e a rota seria servida SEM
// classificação e SEM barreiras — contornando toda a tabela sem que nada avisasse.
//
// Ao contrário do allowlist anterior, este teste é COMPLETO: as formas de registar uma rota num
// `http.ServeMux` são poucas e sintacticamente visíveis, pelo que varrer as fontes responde à
// pergunta TODA, em vez de responder à parte de que alguém se lembrou.
func TestRegistoEOUnicoSitioQueRegistaRotas(t *testing.T) {
	fontes, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var fora []string
	achouNoRegistar := false
	for _, f := range fontes {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ler %s: %v", f, err)
		}
		for i, linha := range strings.Split(string(b), "\n") {
			if !reRegistoDeRota.MatchString(linha) || strings.HasPrefix(strings.TrimSpace(linha), "//") {
				continue
			}
			if f == "planos.go" {
				achouNoRegistar = true
				continue
			}
			fora = append(fora, f+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(linha))
		}
	}
	// CONTROLO: se o varredor não encontrar a chamada legítima dentro de `registar`, a expressão
	// partiu-se e o teste estaria a dar verde por não ver NADA.
	if !achouNoRegistar {
		t.Fatal("o varredor nao encontrou `mux.HandleFunc` em planos.go — a expressao partiu-se e este teste seria vacuoso")
	}
	if len(fora) > 0 {
		t.Errorf("rotas registadas FORA de `registar` — ficam sem plano e sem barreiras:\n  %s\n\n"+
			"Acrescente-as a `tabelaDeRotas` com o plano que lhes corresponde (planos.go).",
			strings.Join(fora, "\n  "))
	}
}

// TestPlanosConsomemOBaldeDeAdmissao existe porque a PRIMEIRA versão destes testes voltou a
// verificar METADE da afirmação — pela QUARTA vez no mesmo dia.
//
// Os testes acima davam verde com o balde de admissão REMOVIDO do invólucro: usavam um balde
// generoso, e sem 429 possível a ausência de `admitControl` era indistinguível da sua presença.
// Exactamente o defeito que este ficheiro foi escrito para fechar, reproduzido dentro dele.
//
// Não foi relendo o teste que se descobriu — foi INJECTANDO a divergência e exigindo que ele
// caísse. Um teste que nunca se viu falhar por uma razão concreta não é evidência de nada.
//
// Aqui o balde tem UM token e não repõe: o segundo pedido TEM de ser 429 e não pode alcançar o
// corpo. O plano de GOVERNAÇÃO entra no mesmo teste porque o balde é a sua ÚNICA barreira de
// invólucro — se lá faltasse, nada no resto da bateria daria sinal.
func TestPlanosConsomemOBaldeDeAdmissao(t *testing.T) {
	comBaldeDeUmToken := func() *apiHandler {
		return &apiHandler{controlMTLS: false, ctrlBucket: newTokenBucket(1, 0, time.Now)}
	}

	var controlo, governacao int
	for _, rt := range (&apiHandler{}).tabelaDeRotas() {
		if rt.plano != planoControlo && rt.plano != planoGovernacao {
			continue
		}
		if rt.plano == planoControlo {
			controlo++
		} else {
			governacao++
		}
		metodo, caminho, _ := strings.Cut(rt.padrao, " ")
		caminho = strings.ReplaceAll(caminho, "{id}", "r1")
		h := comBaldeDeUmToken()
		corpo := func(http.ResponseWriter, *http.Request) {}

		// 1.º pedido — gasta o único token e passa.
		w1 := httptest.NewRecorder()
		h.barreirasDe(rt.plano, corpo)(w1, httptest.NewRequest(metodo, caminho, nil))
		if w1.Code == http.StatusTooManyRequests {
			t.Fatalf("%s: o PRIMEIRO pedido ja foi recusado — o balde do teste esta mal montado e o resto nao mede nada", rt.padrao)
		}

		// 2.º pedido — sem tokens.
		chegou := false
		w2 := httptest.NewRecorder()
		h.barreirasDe(rt.plano, func(http.ResponseWriter, *http.Request) { chegou = true })(
			w2, httptest.NewRequest(metodo, caminho, nil))
		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("%s (%v): com o balde vazio devolveu %d, quero 429 — a rota NAO passa pela admissao",
				rt.padrao, rt.plano, w2.Code)
		}
		if chegou {
			t.Errorf("%s (%v): o CORPO correu com o balde de admissao vazio", rt.padrao, rt.plano)
		}
	}
	if controlo == 0 || governacao == 0 {
		t.Fatalf("nao exercitou os dois planos (controlo=%d, governacao=%d)", controlo, governacao)
	}

	// CONTROLO: as rotas ABERTAS não podem consumir o balde. Sondas de orquestrador são
	// frequentes; passá-las pela admissão causaria falso-unready — é a decisão que a tabela
	// declara, e sem este ramo o teste acima ficaria verde com o invólucro a limitar tudo.
	h := comBaldeDeUmToken()
	for _, rt := range h.tabelaDeRotas() {
		if rt.plano != planoAberto {
			continue
		}
		metodo, caminho, _ := strings.Cut(rt.padrao, " ")
		for i := 0; i < 5; i++ {
			chegou := false
			w := httptest.NewRecorder()
			h.barreirasDe(rt.plano, func(http.ResponseWriter, *http.Request) { chegou = true })(
				w, httptest.NewRequest(metodo, caminho, nil))
			if !chegou {
				t.Fatalf("%s: a sonda foi recusada a %da tentativa (%d) — as rotas abertas estao a consumir o balde", rt.padrao, i+1, w.Code)
			}
		}
	}
}
