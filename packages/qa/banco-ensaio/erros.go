package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// OS ERROS DO FORNECEDOR, em vocabulário fechado, e a SONDA antes da corrida.
//
// Porque existe (medido em 2026-10-08, na primeira corrida real): a conta do fornecedor estava
// sem saldo e respondeu 429 aos 212 pedidos. O banco só abortava com recusas de autenticação,
// percorreu a corrida inteira, gastou 212 unidades do tecto do dia e o relatório só dizia «429».
//
// O que se lê do corpo de um erro é o TIPO, e só para o traduzir num destes valores. A
// `message` do fornecedor traz identificadores da conta: nunca é guardada, impressa nem contada.

// Os tipos de erro de um pedido que não teve 200.
const (
	// TipoChaveRecusada — o fornecedor recusou a autenticação (401, 403, ou um tipo de chave).
	TipoChaveRecusada = "chave_recusada"
	// TipoSaldoInsuficiente — a conta não tem saldo ou quota. Repetir não adianta.
	TipoSaldoInsuficiente = "saldo_insuficiente"
	// TipoLimiteDeRitmo — pedidos a mais por unidade de tempo. Uma pausa entre pedidos resolve.
	TipoLimiteDeRitmo = "limite_de_ritmo"
	// TipoModeloDesconhecido — o fornecedor não conhece o modelo pedido.
	TipoModeloDesconhecido = "modelo_desconhecido"
	// TipoRotaIndisponivel — o fornecedor conhece o modelo mas não tem, para esta conta e este
	// pedido, quem o sirva: as definições da conta (política de dados, fornecedores permitidos)
	// ou o que o pedido exige (tools) excluem todos os endpoints. Repetir não adianta.
	TipoRotaIndisponivel = "rota_indisponivel"
	// TipoOutro — qualquer outro erro, incluindo um corpo que não se lê.
	TipoOutro = "outro"
)

// TiposDeErro devolve o vocabulário fechado dos tipos de erro.
func TiposDeErro() []string {
	return []string{TipoChaveRecusada, TipoSaldoInsuficiente, TipoLimiteDeRitmo, TipoModeloDesconhecido, TipoRotaIndisponivel, TipoOutro}
}

// tiposConhecidos é a lista FECHADA dos valores de `error.type` (ou `error.code`) que o banco
// reconhece. Um valor que cá não esteja dá [TipoOutro]. A ordem conta na procura dentro da
// mensagem: o saldo vem antes do ritmo, porque uma conta sem saldo responde com o código HTTP
// dos limites de ritmo e a confusão a evitar é precisamente essa.
var tiposConhecidos = []struct{ token, tipo string }{
	{"exceeded_current_quota_error", TipoSaldoInsuficiente},
	{"insufficient_quota", TipoSaldoInsuficiente},
	{"insufficient_balance", TipoSaldoInsuficiente},
	{"insufficient_user_quota", TipoSaldoInsuficiente},
	{"billing_error", TipoSaldoInsuficiente},
	{"billing_not_active", TipoSaldoInsuficiente},
	{"billing_hard_limit_reached", TipoSaldoInsuficiente},
	{"rate_limit_reached_error", TipoLimiteDeRitmo},
	{"rate_limit_exceeded", TipoLimiteDeRitmo},
	{"rate_limit_error", TipoLimiteDeRitmo},
	{"rate_limit_reached", TipoLimiteDeRitmo},
	{"requests_limit_reached", TipoLimiteDeRitmo},
	{"tokens_limit_reached", TipoLimiteDeRitmo},
	{"too_many_requests", TipoLimiteDeRitmo},
	{"throttling_error", TipoLimiteDeRitmo},
	{"model_not_found", TipoModeloDesconhecido},
	{"model_not_found_error", TipoModeloDesconhecido},
	{"resource_not_found_error", TipoModeloDesconhecido},
	{"not_found_error", TipoModeloDesconhecido},
	{"invalid_model", TipoModeloDesconhecido},
	{"authentication_error", TipoChaveRecusada},
	{"invalid_authentication_error", TipoChaveRecusada},
	{"incorrect_api_key_error", TipoChaveRecusada},
	{"invalid_api_key", TipoChaveRecusada},
	{"permission_error", TipoChaveRecusada},
	{"permission_denied_error", TipoChaveRecusada},
}

// frasesDeSaldo são as frases fixas que, na mensagem de um erro, dizem que a conta não tem
// saldo. Só se procuram quando nem o tipo nem o código são conhecidos (ver [classificarErro]).
// A Anthropic responde 400 `invalid_request_error` a uma conta sem créditos: só a frase o diz
// (medido a 2026-10-10: a sonda da corrida do AOS-516 deu 400 e o banco só dizia «outro»).
//
// A OpenRouter não manda `error.type`: manda `error.code` com o código HTTP como NÚMERO e uma
// mensagem. Uma conta sem créditos responde 402 — «insufficient credits» ou «requires more
// credits» —, e é o código que decide ([codigoDePagamento]); as frases servem quando um proxy no
// meio muda o código.
var frasesDeSaldo = []string{"insufficient balance", "insufficient quota", "exceeded your current quota", "credit balance is too low", "insufficient credits", "requires more credits"}

// frasesDeRitmo são as frases fixas da OpenRouter para o limite de ritmo. Procuram-se DEPOIS das
// de saldo, pela razão de sempre — a confusão a evitar é tomar uma conta sem saldo por um limite
// de ritmo —, e SÓ contam numa resposta 429 (no código HTTP ou em `error.code`): a mesma frase
// dentro de outro erro é texto, não um limite.
var frasesDeRitmo = []string{"rate limit exceeded", "rate-limited"}

// fraseDeModeloInvalido e fraseSemEndpoints são as duas frases da OpenRouter para um pedido que
// ela não tem quem sirva. A segunda só é «modelo desconhecido» na forma que NOMEIA o modelo e
// mais nada («no endpoints found for <modelo>.»): com um qualificador («matching your data
// policy», «that support tool use») o modelo existe, e o que o exclui são as definições da conta
// ou o que o pedido exige — [TipoRotaIndisponivel].
const (
	fraseDeModeloInvalido = "is not a valid model id"
	fraseSemEndpoints     = "no endpoints found"
)

// frasesDeModeracao: um 403 com uma destas frases é a moderação do fornecedor a recusar o
// conteúdo, e não a chave.
var frasesDeModeracao = []string{"requires moderation", "input was flagged", "flagged for"}

// semEndpoints classifica uma mensagem com a [fraseSemEndpoints]. tem é false sem a frase.
func semEndpoints(mensagem string) (tipo string, tem bool) {
	i := strings.Index(mensagem, fraseSemEndpoints)
	if i < 0 {
		return "", false
	}
	resto := strings.TrimSpace(mensagem[i+len(fraseSemEndpoints):])
	if modelo, nomeia := strings.CutPrefix(resto, "for "); nomeia {
		// Só o nome do modelo, com ou sem ponto final, e mais nada.
		if modelo = strings.TrimSuffix(strings.TrimSpace(modelo), "."); modelo != "" && !strings.ContainsAny(modelo, " \t\n") {
			return TipoModeloDesconhecido, true
		}
	}
	return TipoRotaIndisponivel, true
}

// codigoDePagamento é o 402 (Payment Required): em qualquer fornecedor quer dizer que a conta
// não paga o pedido.
const codigoDePagamento = http.StatusPaymentRequired

// codigoNumerico lê um `error.code` que seja um código HTTP, escrito como número ou como texto
// (a OpenRouter manda `402`; o proxy, ao reembrulhar, manda `"402"`). Zero se não for.
func codigoNumerico(cru json.RawMessage) int {
	var n int
	if json.Unmarshal(cru, &n) == nil && n >= 100 && n <= 599 {
		return n
	}
	var s string
	if json.Unmarshal(cru, &s) == nil && len(s) == 3 && soAlgarismos(s) {
		return int(s[0]-'0')*100 + int(s[1]-'0')*10 + int(s[2]-'0')
	}
	return 0
}

// prefixoDeFacturacao: qualquer tipo `billing_*` é falta de saldo ou de facturação activa.
const prefixoDeFacturacao = "billing_"

// maxCorpoDeErro limita o que se lê do corpo de um erro para o classificar.
const maxCorpoDeErro = 64 << 10

func tipoDoToken(token string) (string, bool) {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return "", false
	}
	for _, c := range tiposConhecidos {
		if token == c.token {
			return c.tipo, true
		}
	}
	if strings.HasPrefix(token, prefixoDeFacturacao) {
		return TipoSaldoInsuficiente, true
	}
	return "", false
}

// classificarErro traduz a resposta de um pedido que não teve 200 num tipo do vocabulário
// fechado. Lê, por esta ordem: `error.type`; `error.code`; e — porque um proxy no meio pode
// reembrulhar o erro do fornecedor e deixar o tipo original só dentro da mensagem — a presença,
// na mensagem, de um dos tipos conhecidos ou de uma das frases fixas de saldo. Da mensagem só
// sai um valor desta lista; o texto dela não é devolvido nem guardado. Depois, por esta ordem:
// o 402 é [TipoSaldoInsuficiente]; uma frase fixa de ritmo NUM 429 é [TipoLimiteDeRitmo]; as
// frases de modelo e de falta de endpoints ([semEndpoints]); um 403 com uma frase de moderação é
// [TipoOutro]; um 401 ou um 403 são [TipoChaveRecusada]; e o resto é [TipoOutro]. O código conta
// igual venha no estado HTTP ou em `error.code` — um proxy no meio pode mudar o primeiro.
func classificarErro(status int, corpo []byte) string {
	var doc struct {
		Error json.RawMessage `json:"error"`
	}
	var erro struct {
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(corpo, &doc) == nil && len(doc.Error) > 0 {
		_ = json.Unmarshal(doc.Error, &erro)
	}
	texto := func(cru json.RawMessage) string {
		var s string
		if json.Unmarshal(cru, &s) == nil {
			return s
		}
		return ""
	}
	for _, token := range []string{texto(erro.Type), texto(erro.Code)} {
		if tipo, ok := tipoDoToken(token); ok {
			return tipo
		}
	}
	mensagem := strings.ToLower(texto(erro.Message))
	contem := func(frases []string) bool {
		for _, frase := range frases {
			if mensagem != "" && strings.Contains(mensagem, frase) {
				return true
			}
		}
		return false
	}
	for _, c := range tiposConhecidos {
		if mensagem != "" && strings.Contains(mensagem, c.token) {
			return c.tipo
		}
	}
	codigo := codigoNumerico(erro.Code)
	e := func(c int) bool { return status == c || codigo == c }
	doEndpoint, semEndpoint := semEndpoints(mensagem)
	switch {
	case contem(frasesDeSaldo), e(codigoDePagamento):
		return TipoSaldoInsuficiente
	case e(http.StatusTooManyRequests) && contem(frasesDeRitmo):
		return TipoLimiteDeRitmo
	case mensagem != "" && strings.Contains(mensagem, fraseDeModeloInvalido):
		return TipoModeloDesconhecido
	case semEndpoint:
		return doEndpoint
	case e(http.StatusForbidden) && contem(frasesDeModeracao):
		return TipoOutro
	case e(http.StatusUnauthorized), e(http.StatusForbidden):
		return TipoChaveRecusada
	}
	return TipoOutro
}

// tipoDaResposta classifica uma resposta sem 200 e devolve-a com o corpo reposto: quem vem a
// seguir (o adaptador do gateway) lê-o como se ninguém lhe tivesse tocado.
func tipoDaResposta(resp *http.Response) string {
	lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDeErro))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(lido), resp.Body), resp.Body}
	return classificarErro(resp.StatusCode, lido)
}

// vigiaDeErros segue os códigos HTTP dos pedidos de uma corrida, pela ordem, e diz quando ela
// deve parar para não gastar o tecto do dia com pedidos que não vão ter resposta.
type vigiaDeErros struct {
	pedidos int
	// soRecusasDeChave: todos os pedidos vistos foram 401 ou 403. so429: todos foram 429.
	soRecusasDeChave, so429 bool
	// seguidos429 é o comprimento da série de 429 em curso; tipoDoUltimo429, o tipo do último.
	seguidos429     int
	tipoDoUltimo429 string
}

func novoVigiaDeErros() *vigiaDeErros { return &vigiaDeErros{soRecusasDeChave: true, so429: true} }

func (v *vigiaDeErros) ver(status int, tipo string) {
	v.pedidos++
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		v.soRecusasDeChave = false
	}
	if status != http.StatusTooManyRequests {
		v.so429, v.seguidos429 = false, 0
		return
	}
	v.seguidos429++
	v.tipoDoUltimo429 = tipo
}

// causa devolve o estado final com que a corrida deve parar, ou "" se deve continuar.
func (v *vigiaDeErros) causa(cfg CfgDaCorrida) string {
	switch {
	case cfg.AbortarAposRecusasDeChave > 0 && v.soRecusasDeChave && v.pedidos >= cfg.AbortarAposRecusasDeChave:
		return TerminouChaveRecusada
	case cfg.AbortarApos429Iniciais > 0 && v.so429 && v.pedidos >= cfg.AbortarApos429Iniciais:
		switch v.tipoDoUltimo429 {
		case TipoSaldoInsuficiente:
			return TerminouSaldoInsuficiente
		case TipoLimiteDeRitmo:
			return TerminouLimiteDeRitmo
		}
		return TerminouSo429
	case cfg.AbortarAposSerieDe429 > 0 && v.seguidos429 >= cfg.AbortarAposSerieDe429:
		return TerminouSerieDe429
	}
	return ""
}

// TextoDaSonda é o conteúdo do único pedido da sonda. Não é um segredo nem um caso da bateria.
const TextoDaSonda = "ping"

// tokensDaSonda limita a resposta da sonda: o que interessa é o código HTTP.
const tokensDaSonda = 16

// Os resultados da sonda que não são um tipo de erro.
const (
	// SondaOK — a rota respondeu 200: a corrida pode começar.
	SondaOK = "ok"
	// SondaTecto e SondaContador — a sonda não saiu: o tecto do dia ou o contador não deixaram.
	SondaTecto    = "tecto_atingido"
	SondaContador = "contador_inutilizavel"
)

// Sonda é o registo, no relatório, do pedido de sonda feito antes da corrida.
type Sonda struct {
	// HTTP é o código da resposta (0 = sem resposta HTTP).
	HTTP int `json:"http"`
	// Resultado é [SondaOK], um tipo de erro do vocabulário fechado, ou a razão de não ter saído.
	Resultado string `json:"resultado"`

	enviada                        bool
	tokensDeEntrada, tokensDeSaida int64
}

// Sondar faz UM pedido mínimo à rota, pelo mesmo transporte dos pedidos da corrida — conta no
// tecto do dia como um pedido — e diz, em vocabulário fechado, se a rota responde. Não passa
// pelo runtime nem pelo gateway: não é uma observação e não entra em taxa nenhuma.
func (n *NoDeEnsaio) Sondar(ctx context.Context) Sonda {
	corpo, _ := json.Marshal(map[string]any{
		"model":      AliasDaRota,
		"messages":   []map[string]string{{"role": "user", "content": TextoDaSonda}},
		"max_tokens": tokensDaSonda,
	})
	pedido := &pedidoHTTP{}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, chaveDaChamada, pedido), http.MethodPost, n.base+"/chat/completions", bytes.NewReader(corpo))
	if err != nil {
		return Sonda{Resultado: TipoOutro}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.credencial)
	resp, err := n.cliente.Do(req)
	switch {
	case errors.Is(err, ErrTectoAtingido):
		return Sonda{Resultado: SondaTecto}
	case errors.Is(err, ErrContador):
		return Sonda{Resultado: SondaContador}
	case err != nil:
		return Sonda{Resultado: TipoOutro, enviada: true}
	}
	defer func() { _ = resp.Body.Close() }()
	s := Sonda{HTTP: resp.StatusCode, Resultado: pedido.lerTipo(), enviada: true}
	if resp.StatusCode != http.StatusOK {
		return s
	}
	s.Resultado = SondaOK
	var doc struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDeErro))
	_ = json.Unmarshal(lido, &doc)
	s.tokensDeEntrada, s.tokensDeSaida = doc.Usage.PromptTokens, doc.Usage.CompletionTokens
	if n.contador != nil {
		if uerr := n.contador.RegistarUso(s.tokensDeEntrada, s.tokensDeSaida); uerr != nil {
			s.Resultado = SondaContador
		}
	}
	return s
}

// terminouDaSonda traduz o resultado de uma sonda que não deu 200 no estado final da corrida.
func terminouDaSonda(s Sonda) string {
	switch s.Resultado {
	case TipoChaveRecusada:
		return TerminouChaveRecusada
	case TipoSaldoInsuficiente:
		return TerminouSaldoInsuficiente
	case TipoLimiteDeRitmo:
		return TerminouLimiteDeRitmo
	case TipoModeloDesconhecido:
		return TerminouModeloDesconhecido
	case TipoRotaIndisponivel:
		return TerminouRotaIndisponivel
	case SondaTecto:
		return TerminouTecto
	case SondaContador:
		return TerminouContador
	}
	return TerminouSondaFalhou
}
