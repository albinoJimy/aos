package bancoensaio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A TOOL `aconselhar` E O CONSELHEIRO (AOS-528).
//
// O modelo executor — o barato — pode, a meio do run, pedir conselho a um modelo mais forte. O
// pedido é uma TOOL do banco, registada no Reference Monitor do nó de ensaio como as outras: o
// executor decide quando a chama, e o que manda (a pergunta e, se quiser, o contexto) é tudo o
// que o conselheiro vê. A resposta do conselheiro volta ao executor como resultado da tool — é
// texto de um modelo, e é tratado como os outros resultados de tool: dado, não instrução.
//
// # NÃO É A FERRAMENTA `advisor` DA ANTHROPIC
//
// A nativa só existe na API directa de um fornecedor e devolve o conselho cifrado. Esta é nossa:
// o conselheiro é qualquer modelo de qualquer fornecedor que o banco conheça (`chaves.go`), pelo
// MESMO proxy efémero, numa segunda rota com alias próprio ([AliasDoConselheiro]), com a sua
// chave, o seu contador e os seus tectos.
//
// # O QUE PASSA POR ONDE
//
// O pedido ao conselheiro sai daqui, pela API compatível OpenAI do proxy — como a sonda —, e
// não pelo Model Gateway do nó de ensaio: o gateway compõe o turno do EXECUTOR (a projecção do
// tail, a allowlist do alias do executor). O transporte é o contado ([transporteContado]): cada
// consulta é reservada no contador do fornecedor do conselheiro ANTES de sair, e o gasto
// estimado conta-se no preço do modelo dele.
//
// # O QUE NÃO SAI DAQUI
//
// A pergunta, o contexto e o conselho não vão para o relatório, o resumo, a saída normal nem a de
// erros: só contagens ([ConsultasDoRun]). Com `--guardar-respostas` ficam no ficheiro de
// respostas do run, ao lado do texto do executor (respostas.go). Os erros que a tool devolve ao
// executor são frases fixas, sem nada do corpo da resposta do fornecedor.

// ToolAconselhar é o nome da tool do conselheiro.
const ToolAconselhar = "aconselhar"

// AliasDoConselheiro é o `model_name` da segunda rota do proxy: o nome que o conselheiro pede.
const AliasDoConselheiro = "rota-conselheiro"

// SystemDoConselheiro é o prompt de sistema de cada consulta. Diz ao conselheiro que só vê o que
// o executor lhe manda.
const SystemDoConselheiro = "You are an expert adviser consulted by another assistant in the middle of a task. You see only the question and the context it sends you, nothing else. Answer the question directly and briefly, in plain text. If the context is not enough to answer, say what is missing."

// TokensDoConselho limita a resposta de cada consulta.
const TokensDoConselho = 1024

// O número de consultas por run.
const (
	// MaxConsultasPorOmissao é o máximo de consultas por run quando a linha de comandos não diz
	// outro (`--max-consultas`).
	MaxConsultasPorOmissao = 2
	// MaxConsultasAceites é o maior máximo de consultas por run que o banco aceita.
	MaxConsultasAceites = 5
)

// maxCorpoDoConselho limita o que se lê da resposta de uma consulta.
const maxCorpoDoConselho = 1 << 20

// ToolDoConselho devolve o schema da tool `aconselhar`, tal como é oferecida ao executor. Não está
// em [ToolsDoBanco]: só entra no nó de ensaio que tem conselheiro, e só nos runs do braço que o
// liga ([BracoComConselheiro]) — o digest da configuração das corridas sem conselheiro não muda.
func ToolDoConselho() ToolDoBanco {
	return ToolDoBanco{
		Nome:       ToolAconselhar,
		Descricao:  "Asks a stronger assistant for advice in the middle of the task, and returns its answer as text. The adviser sees only the question and the context you send it: not the objective, not the documents you have read. Use it when you are not sure of the answer. It can be called only a limited number of times per task.",
		Parametros: json.RawMessage(`{"type":"object","properties":{"pergunta":{"type":"string","description":"The question to ask the adviser"},"contexto":{"type":"string","description":"Optional: the facts or the document excerpts the adviser needs in order to answer"}},"required":["pergunta"],"additionalProperties":false}`),
	}
}

// ConsultasDoRun é o que o banco conta das consultas de UM run do braço com conselheiro. Só
// contagens: nem a pergunta nem o conselho.
type ConsultasDoRun struct {
	// Pedidas são as chamadas da tool pelo executor — a decisão de consultar —, contando as que
	// não chegaram a sair.
	Pedidas int `json:"pedidas"`
	// ComResposta são as consultas a que o conselheiro respondeu com texto.
	ComResposta int `json:"com_resposta"`
	// Vazias são as respondidas com 200 e sem texto (ou ilegíveis): falha do conselheiro.
	Vazias int `json:"respostas_vazias"`
	// RecusadasPeloLimite são as chamadas acima do máximo de consultas do run: não saíram.
	RecusadasPeloLimite int `json:"recusadas_pelo_limite_do_run"`
	// FalhadasPorCausaPassageira são as que não tiveram resposta por uma causa que não é do
	// modelo: 429, 5xx, erro de transporte, conta ou rota (4xx que não é de parâmetro), tecto ou
	// contador. Um run com uma delas, e que não fechou cumprido com os factos, sai da amostra.
	FalhadasPorCausaPassageira int `json:"falhadas_por_causa_passageira"`
	// FalhadasPorParametro são as recusadas com um 4xx de parâmetro: o pedido não serve.
	FalhadasPorParametro int `json:"falhadas_por_parametro"`
	// Pedidos são os pedidos HTTP que saíram para o conselheiro; HTTP, os códigos (0 = sem resposta).
	Pedidos         int   `json:"pedidos"`
	HTTP            []int `json:"http,omitempty"`
	TokensDeEntrada int64 `json:"tokens_de_entrada"`
	TokensDeSaida   int64 `json:"tokens_de_saida"`
}

// ConsultaGuardada é uma consulta, por extenso: só vai para o ficheiro de respostas, e só com
// `--guardar-respostas`. O Conselho fica vazio quando a consulta não teve resposta.
type ConsultaGuardada struct {
	Pergunta string `json:"pergunta"`
	Contexto string `json:"contexto"`
	Conselho string `json:"conselho"`
}

// CfgDoConselheiro configura o conselheiro de um nó de ensaio.
type CfgDoConselheiro struct {
	// BaseURL é a raiz da API compatível OpenAI da rota do conselheiro (`http://host:porta/v1`):
	// o mesmo proxy efémero do executor, ou o conselheiro falso no modo `falso`.
	BaseURL string
	// Credencial é o `Bearer` que se apresenta à rota (a chave mestra do proxy). É um segredo.
	Credencial string
	// Alias é o nome de modelo que se pede. Vazio ⇒ [AliasDoConselheiro].
	Alias string
	// Contador é o contador do fornecedor do conselheiro ([Contador.Irmao]): cada consulta é
	// reservada nele antes de sair. Obrigatório no modo com modelo real (quem o exige é [Correr]).
	Contador *Contador
	// MaxConsultas é o máximo de consultas por run, de 1 a [MaxConsultasAceites].
	MaxConsultas int
	// Timeout de cada consulta. Zero ⇒ 120 s.
	Timeout time.Duration
	// Transporte é o transporte HTTP de base. nil ⇒ [http.DefaultTransport].
	Transporte http.RoundTripper
}

// Conselheiro é o conselheiro de um nó de ensaio: o que a tool `aconselhar` chama.
type Conselheiro struct {
	base, credencial, alias string
	cliente                 *http.Client
	contador                *Contador
	maxConsultas            int

	mu sync.Mutex
	// ligado diz que o run aberto é do braço com conselheiro; doRun e guardadas são as consultas
	// dele. O banco corre um run de cada vez.
	ligado    bool
	doRun     *ConsultasDoRun
	guardadas []ConsultaGuardada
	// paragem é a causa por que a corrida tem de parar: o tecto ou o contador do conselheiro.
	paragem string
}

// ErrConselheiro — o conselheiro não se pode compor.
var ErrConselheiro = errors.New("banco-ensaio: conselheiro invalido")

// NovoConselheiro monta o conselheiro. Fail-closed: sem rota, com um alias que não serve ou um
// máximo de consultas fora dos limites, não há conselheiro.
func NovoConselheiro(cfg CfgDoConselheiro) (*Conselheiro, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("%w: sem endereco da rota", ErrConselheiro)
	}
	alias := cfg.Alias
	if alias == "" {
		alias = AliasDoConselheiro
	}
	if !NomeDaRotaAceite(alias) {
		return nil, fmt.Errorf("%w: alias com caracteres que o banco nao aceita", ErrConselheiro)
	}
	if cfg.MaxConsultas < 1 || cfg.MaxConsultas > MaxConsultasAceites {
		return nil, fmt.Errorf("%w: o maximo de consultas por run vai de 1 a %d", ErrConselheiro, MaxConsultasAceites)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	transporte := cfg.Transporte
	if transporte == nil {
		transporte = http.DefaultTransport
	}
	return &Conselheiro{
		base: base, credencial: cfg.Credencial, alias: alias, contador: cfg.Contador, maxConsultas: cfg.MaxConsultas,
		cliente: &http.Client{Timeout: timeout, Transport: &transporteContado{base: transporte, contador: cfg.Contador}},
	}, nil
}

// abrir começa um run: `ligado` diz se é do braço com conselheiro.
func (c *Conselheiro) abrir(ligado bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ligado, c.doRun, c.guardadas = ligado, &ConsultasDoRun{}, nil
}

// fechar acaba o run e devolve as consultas dele (nil fora do braço com conselheiro).
func (c *Conselheiro) fechar() (*ConsultasDoRun, []ConsultaGuardada) {
	c.mu.Lock()
	defer c.mu.Unlock()
	doRun, guardadas := c.doRun, c.guardadas
	ligado := c.ligado
	c.ligado, c.doRun, c.guardadas = false, nil, nil
	if !ligado {
		return nil, nil
	}
	return doRun, guardadas
}

// paragemDaCorrida devolve a causa por que a corrida tem de parar por causa do conselheiro — o
// tecto do dia do fornecedor dele, ou o contador inutilizável —, ou vazio.
func (c *Conselheiro) paragemDaCorrida() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paragem
}

// Os erros que a tool devolve ao executor. Frases fixas: nada do corpo da resposta do fornecedor.
var (
	errConselheiroIndisponivel = errors.New("o conselheiro nao esta disponivel neste run")
	errLimiteDeConsultas       = errors.New("o limite de consultas deste run foi atingido: responda com o que ja tem")
	errConselheiroSemResposta  = errors.New("o conselheiro nao respondeu")
	errConselhoVazio           = errors.New("o conselheiro devolveu uma resposta vazia")
	errTectoDoConselheiro      = errors.New("o tecto do dia do fornecedor do conselheiro foi atingido: a consulta nao foi enviada")
	errContadorDoConselheiro   = errors.New("o contador do conselheiro esta inutilizavel: a consulta nao foi enviada")
)

// aconselhar é o corpo da tool `aconselhar`, registado no Reference Monitor do nó de ensaio.
func (c *Conselheiro) aconselhar(ctx context.Context, input []byte) ([]byte, error) {
	var args struct {
		Pergunta string `json:"pergunta"`
		Contexto string `json:"contexto"`
	}
	if json.Unmarshal(input, &args) != nil || strings.TrimSpace(args.Pergunta) == "" {
		return nil, errors.New(`argumentos ilegiveis: esperava {"pergunta": "...", "contexto": "..."}`)
	}
	c.mu.Lock()
	if !c.ligado || c.doRun == nil {
		c.mu.Unlock()
		return nil, errConselheiroIndisponivel
	}
	c.doRun.Pedidas++
	if c.doRun.Pedidas > c.maxConsultas {
		c.doRun.RecusadasPeloLimite++
		c.mu.Unlock()
		return nil, errLimiteDeConsultas
	}
	doRun := c.doRun
	c.mu.Unlock()

	guardada := ConsultaGuardada{Pergunta: args.Pergunta, Contexto: args.Contexto}
	conselho, ok, falha := c.consultar(ctx, args.Pergunta, args.Contexto, doRun)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doRun == doRun {
		guardada.Conselho = conselho
		c.guardadas = append(c.guardadas, guardada)
	}
	if !ok {
		return nil, falha
	}
	return []byte(conselho), nil
}

// consultar faz o pedido ao conselheiro e conta-o em `doRun`. Devolve o conselho, se houve, ou
// o erro que a tool devolve ao executor.
func (c *Conselheiro) consultar(ctx context.Context, pergunta, contexto string, doRun *ConsultasDoRun) (string, bool, error) {
	conteudo := pergunta
	if strings.TrimSpace(contexto) != "" {
		conteudo += "\n\nContext:\n" + contexto
	}
	corpo, _ := json.Marshal(map[string]any{
		"model": c.alias,
		"messages": []map[string]string{
			{"role": "system", "content": SystemDoConselheiro},
			{"role": "user", "content": conteudo},
		},
		"max_tokens": TokensDoConselho,
	})
	pedido := &pedidoHTTP{}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, chaveDaChamada, pedido), http.MethodPost, c.base+"/chat/completions", bytes.NewReader(corpo))
	if err != nil {
		c.contar(doRun, pedido, func(r *ConsultasDoRun) { r.FalhadasPorParametro++ })
		return "", false, errConselheiroSemResposta
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.credencial)
	resp, err := c.cliente.Do(req)
	switch {
	case errors.Is(err, ErrTectoAtingido):
		c.pararCom(TerminouTecto)
		c.contar(doRun, pedido, func(r *ConsultasDoRun) { r.FalhadasPorCausaPassageira++ })
		return "", false, errTectoDoConselheiro
	case errors.Is(err, ErrContador):
		c.pararCom(TerminouContador)
		c.contar(doRun, pedido, func(r *ConsultasDoRun) { r.FalhadasPorCausaPassageira++ })
		return "", false, errContadorDoConselheiro
	case err != nil:
		c.contar(doRun, pedido, func(r *ConsultasDoRun) { r.FalhadasPorCausaPassageira++ })
		return "", false, errConselheiroSemResposta
	}
	defer func() { _ = resp.Body.Close() }()
	lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDoConselho))
	if resp.StatusCode != http.StatusOK {
		parametro := quatroXXDeParametro(resp.StatusCode, pedido.lerTipo())
		c.contar(doRun, pedido, func(r *ConsultasDoRun) {
			if parametro {
				r.FalhadasPorParametro++
			} else {
				r.FalhadasPorCausaPassageira++
			}
		})
		return "", false, fmt.Errorf("o conselheiro respondeu com erro (HTTP %d)", resp.StatusCode)
	}
	var doc struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	legivel := json.Unmarshal(lido, &doc) == nil
	if c.contador != nil {
		// O gasto estimado do conselheiro conta no preço do modelo DELE, no contador do fornecedor
		// dele. Se não se gravar, o pedido seguinte já não sairia: a corrida pára.
		if uerr := c.contador.RegistarUso(doc.Usage.PromptTokens, doc.Usage.CompletionTokens); uerr != nil {
			c.pararCom(TerminouContador)
		}
	}
	conselho := ""
	if legivel && len(doc.Choices) > 0 {
		conselho = doc.Choices[0].Message.Content
	}
	vazio := strings.TrimSpace(conselho) == ""
	c.contar(doRun, pedido, func(r *ConsultasDoRun) {
		r.TokensDeEntrada += doc.Usage.PromptTokens
		r.TokensDeSaida += doc.Usage.CompletionTokens
		if vazio {
			r.Vazias++
		} else {
			r.ComResposta++
		}
	})
	if vazio {
		return "", false, errConselhoVazio
	}
	return conselho, true, nil
}

// contar regista, nas consultas do run, os pedidos que o transporte deixou sair e o que mais `f`
// disser.
func (c *Conselheiro) contar(doRun *ConsultasDoRun, pedido *pedidoHTTP, f func(*ConsultasDoRun)) {
	tentativas, _ := pedido.lerTentativas()
	c.mu.Lock()
	defer c.mu.Unlock()
	doRun.Pedidos += len(tentativas)
	doRun.HTTP = append(doRun.HTTP, tentativas...)
	f(doRun)
}

// pararCom regista a causa por que a corrida tem de parar.
func (c *Conselheiro) pararCom(causa string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paragem == "" {
		c.paragem = causa
	}
}

// Sondar faz a sonda da rota do conselheiro: UM pedido mínimo, pelo mesmo transporte contado das
// consultas. Conta no tecto do fornecedor dele.
func (c *Conselheiro) Sondar(ctx context.Context) Sonda {
	return sondarRota(ctx, c.cliente, c.base, c.credencial, c.alias, c.contador)
}
