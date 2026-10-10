package bancoensaio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// AOS-512 — A CONTA SEM SALDO E O LIMITE DE RITMO. Defeito medido na primeira corrida real
// (2026-10-08): 429 a todos os 212 pedidos, por saldo insuficiente, e o banco percorreu a
// corrida inteira. Aqui: o tipo do erro em vocabulário fechado, o aborto cedo, a série a meio,
// a sonda antes da corrida e a prova de que a mensagem do fornecedor não chega a saída nenhuma.

// sentinelaDaConta faz de identificador da conta dentro da `message` do fornecedor. É falsa.
const sentinelaDaConta = "SENTINELA-CONTA-org-9f27c41b6e"

// respostaDeTeste é uma resposta de um fornecedor de teste: 200 (delegada no provider falso)
// ou um erro com o tipo dado.
type respostaDeTeste struct {
	codigo int
	tipo   string
}

var (
	semSaldo  = respostaDeTeste{http.StatusTooManyRequests, "exceeded_current_quota_error"}
	semRitmo  = respostaDeTeste{http.StatusTooManyRequests, "rate_limit_reached_error"}
	estranho  = respostaDeTeste{http.StatusTooManyRequests, "tipo_que_o_banco_nao_conhece"}
	duzentos  = respostaDeTeste{codigo: http.StatusOK}
	quinhento = respostaDeTeste{http.StatusInternalServerError, "server_error"}
)

func repetir(r respostaDeTeste, n int) []respostaDeTeste {
	out := make([]respostaDeTeste, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// fornecedorDeTeste responde, pedido a pedido, com a sequência dada (esgotada ⇒ 200). A
// mensagem de cada erro leva a sentinela da conta, como a do fornecedor real leva a conta.
type fornecedorDeTeste struct {
	mu        sync.Mutex
	respostas []respostaDeTeste
	pedidos   int
	falso     *ProviderFalso
}

func novoFornecedorDeTeste(respostas []respostaDeTeste) *fornecedorDeTeste {
	return &fornecedorDeTeste{respostas: respostas, falso: NovoProviderFalso([]Comportamento{ComportamentoCumpre})}
}

func (f *fornecedorDeTeste) recebidos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pedidos
}

func (f *fornecedorDeTeste) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	resposta := duzentos
	if f.pedidos < len(f.respostas) {
		resposta = f.respostas[f.pedidos]
	}
	f.pedidos++
	f.mu.Unlock()
	if resposta.codigo == http.StatusOK {
		f.falso.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resposta.codigo)
	motivo := "suspended, please recharge"
	if resposta == semSaldo {
		motivo = "suspended due to insufficient balance, please recharge"
	}
	fmt.Fprintf(w, `{"error":{"type":%q,"message":"Your account %s is %s"}}`, resposta.tipo, sentinelaDaConta, motivo)
}

// correrContra corre um plano de `runs` runs de um pedido contra o fornecedor de teste.
func correrContra(t *testing.T, f *fornecedorDeTeste, runs int, afinar func(*CfgDaCorrida)) *Relatorio {
	t.Helper()
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "c"})
	if err != nil {
		t.Fatal(err)
	}
	defer no.Fechar()
	cfg := CfgDaCorrida{Modo: ModoFalso, Plano: planoDeUmPedido(b, runs), Bateria: b, No: no, Relogio: relogioFixo}
	if afinar != nil {
		afinar(&cfg)
	}
	r, err := Correr(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// O TIPO DO ERRO sai de uma lista fechada: o que lá não está é `outro`.
func TestAOS512_Erros_VocabularioFechado(t *testing.T) {
	casos := []struct {
		nome   string
		codigo int
		corpo  string
		quer   string
	}{
		{"o corpo medido em 2026-10-08", 429, `{"error":{"type":"exceeded_current_quota_error","message":"x suspended due to insufficient balance x"}}`, TipoSaldoInsuficiente},
		{"quota insuficiente", 429, `{"error":{"type":"insufficient_quota"}}`, TipoSaldoInsuficiente},
		{"qualquer billing_*", 429, `{"error":{"type":"billing_qualquer_coisa"}}`, TipoSaldoInsuficiente},
		{"saldo com outro codigo HTTP", 402, `{"error":{"type":"insufficient_quota"}}`, TipoSaldoInsuficiente},
		{"limite de ritmo", 429, `{"error":{"type":"rate_limit_reached_error"}}`, TipoLimiteDeRitmo},
		{"limite de ritmo, outra grafia", 429, `{"error":{"type":"RATE_LIMIT_EXCEEDED"}}`, TipoLimiteDeRitmo},
		{"o tipo vem no code", 429, `{"error":{"type":"invalid_request_error","code":"rate_limit_exceeded"}}`, TipoLimiteDeRitmo},
		{"o code e um numero", 429, `{"error":{"type":"tipo_novo","code":429}}`, TipoOutro},
		{"o proxy reembrulhou: o tipo so esta na mensagem", 429, `{"error":{"type":"None","code":"429","message":"RateLimitError: {\"error\":{\"type\":\"exceeded_current_quota_error\"}}"}}`, TipoSaldoInsuficiente},
		{"o proxy reembrulhou: so a frase de saldo", 429, `{"error":{"type":null,"message":"account suspended due to Insufficient Balance"}}`, TipoSaldoInsuficiente},
		{"o saldo ganha ao ritmo na mensagem", 429, `{"error":{"message":"rate_limit_error wrapping insufficient_quota"}}`, TipoSaldoInsuficiente},
		{"modelo desconhecido", 404, `{"error":{"type":"model_not_found"}}`, TipoModeloDesconhecido},
		{"chave pelo tipo", 400, `{"error":{"type":"invalid_authentication_error"}}`, TipoChaveRecusada},
		{"chave pelo codigo HTTP", 401, `{"error":{"message":"recusado"}}`, TipoChaveRecusada},
		{"403 sem corpo", 403, ``, TipoChaveRecusada},
		{"tipo desconhecido", 429, `{"error":{"type":"tipo_que_o_banco_nao_conhece","message":"nada"}}`, TipoOutro},
		{"429 sem corpo", 429, ``, TipoOutro},
		{"corpo que nao e JSON", 500, `<html>` + sentinelaDaConta, TipoOutro},
		{"error e uma string", 500, `{"error":"` + sentinelaDaConta + `"}`, TipoOutro},
	}
	vocabulario := map[string]bool{}
	for _, v := range TiposDeErro() {
		vocabulario[v] = true
	}
	for _, c := range casos {
		tem := classificarErro(c.codigo, []byte(c.corpo))
		if tem != c.quer || !vocabulario[tem] {
			t.Errorf("%s: classificarErro = %q, quer %q", c.nome, tem, c.quer)
		}
	}
}

// (1) ABORTAR CEDO: três 429 seguidos desde o primeiro pedido param a corrida, com a causa lida
// do tipo do erro. Um 200 entre eles, ou a regra desligada, não param.
func TestAOS512_Corrida_429DesdeOInicioAbortam(t *testing.T) {
	ligar := func(c *CfgDaCorrida) { c.AbortarApos429Iniciais = Respostas429QueAbortam }
	casos := []struct {
		nome      string
		respostas []respostaDeTeste
		afinar    func(*CfgDaCorrida)
		terminou  string
		pedidos   int
	}{
		{"conta sem saldo", repetir(semSaldo, 6), ligar, TerminouSaldoInsuficiente, 3},
		{"limite de ritmo", repetir(semRitmo, 6), ligar, TerminouLimiteDeRitmo, 3},
		{"429 com tipo desconhecido", repetir(estranho, 6), ligar, TerminouSo429, 3},
		{"um 200 antes do terceiro", []respostaDeTeste{semSaldo, semSaldo, duzentos, semSaldo, semSaldo, semSaldo}, ligar, TerminouCompleta, 6},
		{"um 500 primeiro: ja nao e desde o inicio", append([]respostaDeTeste{quinhento}, repetir(semSaldo, 5)...), ligar, TerminouCompleta, 6},
		{"a regra desligada", repetir(semSaldo, 6), nil, TerminouCompleta, 6},
	}
	for _, c := range casos {
		f := novoFornecedorDeTeste(c.respostas)
		r := correrContra(t, f, 6, c.afinar)
		if r.Terminou != c.terminou || f.recebidos() != c.pedidos {
			t.Errorf("%s: terminou=%s com %d pedidos; quer %s e %d", c.nome, r.Terminou, f.recebidos(), c.terminou, c.pedidos)
		}
	}
}

// (2) A SÉRIE A MEIO: dez 429 seguidos depois de já ter havido um 200 param a corrida, com
// causa própria. Nove, ou uma série interrompida por um 200, não param.
func TestAOS512_Corrida_SerieDe429AMeioPara(t *testing.T) {
	ligar := func(c *CfgDaCorrida) {
		c.AbortarApos429Iniciais, c.AbortarAposSerieDe429 = Respostas429QueAbortam, SerieDe429QueAborta
	}
	// Dois 200, depois só 429: pára ao décimo 429 — doze pedidos, e não os vinte do plano.
	f := novoFornecedorDeTeste(append(repetir(duzentos, 2), repetir(semSaldo, 18)...))
	r := correrContra(t, f, 20, ligar)
	if r.Terminou != TerminouSerieDe429 || f.recebidos() != 2+SerieDe429QueAborta {
		t.Errorf("serie a meio: terminou=%s com %d pedidos; quer %s e %d", r.Terminou, f.recebidos(), TerminouSerieDe429, 2+SerieDe429QueAborta)
	}
	// (4) O relatório conta os erros por tipo, ao lado dos códigos HTTP.
	if r.Taxas.TiposDeErro[TipoSaldoInsuficiente] != SerieDe429QueAborta || len(r.Taxas.TiposDeErro) != 1 || r.Taxas.HTTP["429"] != SerieDe429QueAborta {
		t.Errorf("contagem por tipo de erro = %v (HTTP %v)", r.Taxas.TiposDeErro, r.Taxas.HTTP)
	}
	if resumo := ResumoEmTexto(r); !strings.Contains(resumo, "tipos de erro") || !strings.Contains(resumo, TipoSaldoInsuficiente) {
		t.Errorf("o resumo tinha de levar a contagem por tipo de erro")
	}
	if conselho := conselhoDaParagem(r); !strings.Contains(conselho, "saldo") {
		t.Errorf("serie de 429 por saldo: o conselho tinha de falar do saldo: %q", conselho)
	}

	// Nove seguidos e um 200 a seguir: a série recomeça do zero e a corrida completa.
	interrompida := append(append(append(repetir(duzentos, 1), repetir(semRitmo, 9)...), duzentos), repetir(semRitmo, 9)...)
	f = novoFornecedorDeTeste(interrompida)
	if r := correrContra(t, f, 20, ligar); r.Terminou != TerminouCompleta || f.recebidos() != 20 {
		t.Errorf("serie interrompida: terminou=%s com %d pedidos; quer completa e 20", r.Terminou, f.recebidos())
	}
	// Sem a regra, a série não pára nada.
	f = novoFornecedorDeTeste(append(repetir(duzentos, 2), repetir(semSaldo, 18)...))
	if r := correrContra(t, f, 20, nil); r.Terminou != TerminouCompleta || f.recebidos() != 20 {
		t.Errorf("sem a regra: terminou=%s com %d pedidos; quer completa e 20", r.Terminou, f.recebidos())
	}
}

// (3) A SONDA: um pedido antes do primeiro caso. Sem 200, a corrida não começa e a causa sai em
// vocabulário fechado; com 200, a corrida corre e a sonda conta como um pedido.
func TestAOS512_Sonda_SemDuzentosACorridaNaoComeca(t *testing.T) {
	sondar := func(c *CfgDaCorrida) { c.Sondar = true }
	casos := []struct {
		nome      string
		primeira  respostaDeTeste
		terminou  string
		resultado string
	}{
		{"conta sem saldo", semSaldo, TerminouSaldoInsuficiente, TipoSaldoInsuficiente},
		{"limite de ritmo", semRitmo, TerminouLimiteDeRitmo, TipoLimiteDeRitmo},
		{"chave recusada", respostaDeTeste{http.StatusUnauthorized, "authentication_error"}, TerminouChaveRecusada, TipoChaveRecusada},
		{"modelo desconhecido", respostaDeTeste{http.StatusNotFound, "model_not_found"}, TerminouModeloDesconhecido, TipoModeloDesconhecido},
		{"outro", quinhento, TerminouSondaFalhou, TipoOutro},
	}
	for _, c := range casos {
		f := novoFornecedorDeTeste([]respostaDeTeste{c.primeira})
		r := correrContra(t, f, 6, sondar)
		if r.Terminou != c.terminou || f.recebidos() != 1 || len(r.Observacoes) != 0 {
			t.Errorf("%s: terminou=%s, %d pedidos, %d observacoes; quer %s, 1 e 0", c.nome, r.Terminou, f.recebidos(), len(r.Observacoes), c.terminou)
		}
		if r.Sonda == nil || r.Sonda.HTTP != c.primeira.codigo || r.Sonda.Resultado != c.resultado || r.Pedidos.Enviados != 1 {
			t.Errorf("%s: sonda no relatorio = %+v, enviados %d", c.nome, r.Sonda, r.Pedidos.Enviados)
		}
	}

	// Com 200 a corrida corre inteira: a sonda e os seis pedidos.
	f := novoFornecedorDeTeste(nil)
	r := correrContra(t, f, 6, sondar)
	if r.Terminou != TerminouCompleta || f.recebidos() != 7 || r.Pedidos.Enviados != 7 || len(r.Observacoes) != 6 {
		t.Errorf("sonda com 200: terminou=%s, %d pedidos, enviados %d, %d observacoes", r.Terminou, f.recebidos(), r.Pedidos.Enviados, len(r.Observacoes))
	}
	if r.Sonda == nil || r.Sonda.Resultado != SondaOK || r.Sonda.HTTP != http.StatusOK {
		t.Errorf("sonda com 200 no relatorio = %+v", r.Sonda)
	}
	// Sem a sonda pedida, nenhum pedido a mais e nenhum registo dela.
	f = novoFornecedorDeTeste(nil)
	if r := correrContra(t, f, 6, nil); f.recebidos() != 6 || r.Sonda != nil {
		t.Errorf("sem sonda: %d pedidos, sonda %+v", f.recebidos(), r.Sonda)
	}
}

// A sonda conta no tecto do dia como UM pedido: é reservada no contador antes de sair, e uma
// corrida que só cabe sem ela não arranca.
func TestAOS512_Sonda_ContaNoTecto(t *testing.T) {
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	correr := func(tecto int64, respostas []respostaDeTeste) (*Relatorio, *fornecedorDeTeste, string, error) {
		contador, caminho := abrirContadorDeTeste(t, tecto)
		f := novoFornecedorDeTeste(respostas)
		srv := httptest.NewServer(f)
		t.Cleanup(srv.Close)
		no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: a.bateria, BaseURL: srv.URL + "/v1", Credencial: "c", Contador: contador})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(no.Fechar)
		r, err := Correr(context.Background(), CfgDaCorrida{Modo: ModoReal, Plano: planoDeUmPedido(a.bateria, 3), Bateria: a.bateria, No: no, Contador: contador, Relogio: relogioFixo, Sondar: true})
		return r, f, caminho, err
	}
	// Tecto de 3 para um plano de 3: com a sonda são 4, e não cabe. Nada sai.
	if _, f, _, err := correr(3, nil); !errors.Is(err, ErrNaoCabe) || f.recebidos() != 0 {
		t.Fatalf("a sonda tinha de contar para o que cabe: err=%v, %d pedidos", err, f.recebidos())
	}
	// Tecto de 4: cabe. A sonda falha por saldo — o contador fica em 1 e mais nada sai.
	r, f, caminho, err := correr(4, []respostaDeTeste{semSaldo})
	if err != nil {
		t.Fatal(err)
	}
	if f.recebidos() != 1 || r.Terminou != TerminouSaldoInsuficiente || r.Pedidos.Enviados != 1 || *r.Pedidos.GastosHoje != 1 {
		t.Errorf("sonda falhada com contador: %d pedidos, terminou=%s, pedidos=%+v", f.recebidos(), r.Terminou, r.Pedidos)
	}
	if dias := lerContador(t, caminho); dias[diaDosTestes]["falso"].Pedidos != 1 {
		t.Errorf("o contador tinha de ter 1 pedido (a sonda): %v", dias)
	}
	// Tecto de 4 e a sonda com 200: os quatro pedidos contam.
	r, f, _, err = correr(4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.recebidos() != 4 || r.Terminou != TerminouCompleta || *r.Pedidos.GastosHoje != 4 {
		t.Errorf("sonda com 200 e contador: %d pedidos, terminou=%s, pedidos=%+v", f.recebidos(), r.Terminou, r.Pedidos)
	}
}

// (5) SENTINELAS, pela linha de comandos do modo real: a `message` do erro do fornecedor — com
// um identificador de conta — não aparece em stdout, em stderr, no relatório, no resumo nem no
// contador. E a mensagem final diz a causa e o que fazer.
func TestAOS512_Real_SaldoERitmo_AMensagemDoFornecedorNaoSai(t *testing.T) {
	proibidos := []string{sentinelaDaConta, "suspended", "recharge", "Your account", sentinelaChaveKimi, sentinelaBaseKimi, chaveMestraDeTeste}
	casos := []struct {
		nome      string
		respostas []respostaDeTeste
		pedidos   int
		terminou  string
		conselho  string
	}{
		// A corrida de 2026-10-08, repetida: agora a sonda pára-a ao primeiro pedido.
		{"a sonda encontra a conta sem saldo", repetir(semSaldo, 300), 1, TerminouSaldoInsuficiente, "nao tem saldo"},
		{"a sonda encontra o limite de ritmo", repetir(semRitmo, 300), 1, TerminouLimiteDeRitmo, "--pausa"},
		// A sonda passa e o saldo acaba logo a seguir: três 429 e a corrida pára.
		{"sem saldo logo depois da sonda", append([]respostaDeTeste{duzentos}, repetir(semSaldo, 300)...), 1 + Respostas429QueAbortam, TerminouSaldoInsuficiente, "nao tem saldo"},
		{"limite de ritmo logo depois da sonda", append([]respostaDeTeste{duzentos}, repetir(semRitmo, 300)...), 1 + Respostas429QueAbortam, TerminouLimiteDeRitmo, "--pausa"},
		// O saldo acaba a meio: dez 429 seguidos e a corrida pára.
		{"sem saldo a meio", append(repetir(duzentos, 3), repetir(semSaldo, 300)...), 3 + SerieDe429QueAborta, TerminouSerieDe429, "nao tem saldo"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			chaves := escreverChaves(t, chavesDeTeste())
			pasta := filepath.Dir(chaves)
			saida := filepath.Join(pasta, "relatorios")
			f := novoFornecedorDeTeste(c.respostas)
			e := executar(t, Ambiente{Lancador: &lancadorDeTeste{t: t, devolver: f}}, "real", "--chaves", chaves, "--fornecedor", "kimi",
				"--experiencia", "separadores", "--saida", saida)
			if e.codigo != SaidaParouAMeio || f.recebidos() != c.pedidos {
				t.Fatalf("codigo %d (quer %d), pedidos %d (quer %d)\n%s", e.codigo, SaidaParouAMeio, f.recebidos(), c.pedidos, e.stderr)
			}
			if !strings.Contains(e.stderr, "("+c.terminou+")") || !strings.Contains(e.stderr, c.conselho) {
				t.Errorf("a mensagem final tinha de dizer a causa (%s) e o que fazer (%s): %q", c.terminou, c.conselho, e.stderr)
			}
			tudo := tudoOQueFoiEscrito(t, e, pasta)
			verSemFugas(t, c.nome, tudo, proibidos)
			if !strings.Contains(tudo, `"terminou": "`+c.terminou+`"`) {
				t.Errorf("o relatorio parcial tinha de dizer a causa %s", c.terminou)
			}
			// O contador gastou só o que saiu — e não os 212 da corrida inteira.
			if dias := lerContador(t, filepath.Join(pasta, "contador.json")); dias[diaDosTestes]["kimi"].Pedidos != int64(c.pedidos) {
				t.Errorf("contador: %v, quer %d pedidos", dias, c.pedidos)
			}
			achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
			if len(achados) != 1 {
				t.Fatalf("relatorios escritos: %v", achados)
			}
			cru, _ := os.ReadFile(achados[0])
			if c.pedidos > 1 && !strings.Contains(string(cru), `"`+TipoSaldoInsuficiente+`": `) && !strings.Contains(string(cru), `"`+TipoLimiteDeRitmo+`": `) {
				t.Errorf("o relatorio tinha de contar os erros por tipo")
			}
		})
	}
}

// O `--so-plano` continua a não enviar nada: nem a sonda.
func TestAOS512_Real_SoPlanoNaoSonda(t *testing.T) {
	f := novoFornecedorDeTeste(nil)
	lanc := &lancadorDeTeste{t: t, devolver: f}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", escreverChaves(t, chavesDeTeste()), "--fornecedor", "kimi", "--experiencia", "separadores", "--so-plano")
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || f.recebidos() != 0 {
		t.Fatalf("--so-plano: codigo %d, %d lancamentos, %d pedidos\n%s", e.codigo, lanc.lancamentos, f.recebidos(), e.stderr)
	}
	if !strings.Contains(e.stderr, "212 pedidos, mais 1 de sonda") {
		t.Errorf("o plano tinha de anunciar a sonda: %q", e.stderr)
	}
}

// A Anthropic responde 400 `invalid_request_error` a uma conta sem créditos; o proxy
// reembrulha-o. Só a frase fixa o distingue de um pedido mal formado.
func TestAOS516_ClassificarErro_SaldoDaAnthropic(t *testing.T) {
	for corpo, quer := range map[string]string{
		`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the API."}}`:                                                                                           TipoSaldoInsuficiente,
		`{"error":{"message":"litellm.BadRequestError: AnthropicException - {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Your credit balance is too low\"}}","type":null,"code":"400"}}`: TipoSaldoInsuficiente,
		`{"error":{"type":"invalid_request_error","message":"max_tokens: must be greater than thinking.budget_tokens"}}`:                                                                                                    TipoOutro,
	} {
		if tem := classificarErro(400, []byte(corpo)); tem != quer {
			t.Errorf("classificarErro = %s, quer %s", tem, quer)
		}
	}
}
