package bancoensaio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// O BRAÇO DO MODELO DE VISÃO (AOS-529). Um adaptador de [MotorOCR] que manda cada imagem a um
// modelo de visão e lhe pede que transcreva o texto. É a opção (d) da decisão D2 do desenho («o
// próprio LLM a ler a imagem»), medida ao lado do OCR local para a comparação — não é o caminho
// da tool, que corre sem rede no gVisor.
//
// # O QUE PASSA POR ONDE
//
// Como o conselheiro do AOS-528 (aconselhar.go): o pedido sai pela API compatível OpenAI do
// MESMO proxy efémero do banco (a imagem fixada do LiteLLM), numa rota com alias próprio
// ([AliasDaVisao]), com a chave e a base do fornecedor do modelo, e pelo transporte contado
// ([transporteContado]) — cada pedido reservado no contador do fornecedor ANTES de sair, e o gasto
// estimado contado no preço do modelo. O fornecedor e o modelo vêm do ficheiro de chaves do dono,
// pelas mesmas listas e tectos (`chaves.go`).
//
// A imagem vai como parte `image_url`, numa data URL em base64, ao lado de uma instrução FIXA:
// transcrever o texto tal como está, sem comentar.
//
// # O PDF FICA FORA DESTE BRAÇO
//
// O PDF da bateria é uma página com a MESMA imagem de `fatura-b-ruido`. Rasterizá-lo para o modelo
// de visão daria os mesmos píxeis que ele já recebe nessa imagem: um pedido pago a mais, sem
// informação nova, e mais uma dependência (o `pdftoppm` da imagem do Tesseract) num braço que não
// usa Docker para mais nada. A imagem do PDF sai com o estado `fora` e não entra nas contas.
//
// # O TEXTO DO MODELO NÃO SAI DAQUI
//
// Nem o texto da resposta nem o corpo de um erro do fornecedor vão para o relatório, o resumo ou
// as saídas; só contagens, códigos HTTP e tokens. Com `--guardar-respostas` o texto fica no
// ficheiro de cada imagem, ao lado do relatório.

// AliasDaVisao é o `model_name` da rota do modelo de visão no proxy.
const AliasDaVisao = "rota-visao"

// InstrucaoDaVisao é o prompt de sistema de cada pedido; PedidoDaVisao, o texto que acompanha a
// imagem. Fixos: mudá-los muda a medição.
const (
	InstrucaoDaVisao = "You are a text transcription engine. Transcribe all the text in the image exactly as it appears, line by line, keeping the original spelling, accents, letter case, numbers and punctuation. Do not translate, correct, summarise, explain or comment. Output only the transcribed text."
	PedidoDaVisao    = "Transcribe the text in this image."
)

// TokensDaVisao limita a resposta de cada imagem; TempoDaVisao, a espera por ela.
const (
	TokensDaVisao = 2048
	TempoDaVisao  = 120 * time.Second
)

// maxCorpoDaVisao limita o que se lê de uma resposta.
const maxCorpoDaVisao = 1 << 20

// CfgDaVisao configura o motor de visão.
type CfgDaVisao struct {
	// BaseURL é a raiz da API compatível OpenAI do proxy (`http://host:porta/v1`).
	BaseURL string
	// Credencial é o `Bearer` da rota (a chave mestra do proxy). É um segredo.
	Credencial string
	// Fornecedor e Modelo identificam o modelo no relatório.
	Fornecedor, Modelo string
	// Contador é o contador do fornecedor do modelo: cada pedido é reservado nele antes de sair.
	// Obrigatório (fail-closed).
	Contador *Contador
	// Preco, quando presente, dá o custo estimado por imagem.
	Preco *Preco
	// Timeout de cada imagem. Zero ⇒ [TempoDaVisao].
	Timeout time.Duration
	// Transporte HTTP de base. nil ⇒ [http.DefaultTransport].
	Transporte http.RoundTripper
}

// MotorOCRVisao é o motor de visão.
type MotorOCRVisao struct {
	base, credencial, fornecedor, modelo string
	cliente                              *http.Client
	contador                             *Contador
	preco                                *Preco
	timeout                              time.Duration

	mu       sync.Mutex
	custo    int64
	comCusto bool
}

// ErrVisao — o motor de visão não se pode compor.
var ErrVisao = errors.New("banco-ensaio: motor de visao invalido")

// NovoMotorOCRVisao monta o motor de visão. Sem rota ou sem contador, não há motor.
func NovoMotorOCRVisao(cfg CfgDaVisao) (*MotorOCRVisao, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" || cfg.Contador == nil || cfg.Modelo == "" {
		return nil, fmt.Errorf("%w: sem endereco da rota, sem contador ou sem modelo", ErrVisao)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = TempoDaVisao
	}
	transporte := cfg.Transporte
	if transporte == nil {
		transporte = http.DefaultTransport
	}
	return &MotorOCRVisao{
		base: base, credencial: cfg.Credencial, fornecedor: cfg.Fornecedor, modelo: cfg.Modelo,
		contador: cfg.Contador, preco: cfg.Preco, timeout: timeout,
		cliente: &http.Client{Timeout: timeout, Transport: &transporteContado{base: transporte, contador: cfg.Contador}},
	}, nil
}

// Nome implementa [MotorOCR].
func (v *MotorOCRVisao) Nome() string { return NomeMotorVisao }

// Descricao implementa [MotorOCR].
func (v *MotorOCRVisao) Descricao() string {
	return fmt.Sprintf("modelo de visao %s/%s pelo proxy efemero do banco (imagem %s)", v.fornecedor, v.modelo, ImagemDoProxy)
}

// Condicoes implementa [MotorOCR].
func (v *MotorOCRVisao) Condicoes() CondicoesDoMotor {
	return CondicoesDoMotor{
		Limites: LimitesDoOCR{TempoPorChamadaS: int(v.timeout / time.Second), MemoriaPorChamadaB: -1, Rede: "o fornecedor, pelo proxy do banco"},
		Notas: []string{
			"modelo de visao: e a opcao (d) da decisao D2, com rede e custo por chamada; nao corre no gVisor, e a memoria nao se mede",
			"o tempo inclui a rede e a fila do fornecedor; o custo e uma estimativa pelos tokens do usage e pela tabela de precos, nao a factura",
			"o PDF fica fora deste braco: e a mesma pagina de fatura-b-ruido, e rasteriza-lo daria os mesmos pixeis",
			"um modelo pode corrigir o texto em vez de o transcrever; a comparacao com acentos exactos mostra-o, a regra dos factos nao",
		},
	}
}

// CustoMicroUSD devolve o custo estimado da corrida, e se havia preço para o calcular.
func (v *MotorOCRVisao) CustoMicroUSD() (int64, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.custo, v.comCusto
}

// Sondar faz a sonda da rota: UM pedido mínimo, pelo transporte contado. Conta como um pedido.
func (v *MotorOCRVisao) Sondar(ctx context.Context) Sonda {
	return sondarRota(ctx, v.cliente, v.base, v.credencial, AliasDaVisao, v.contador)
}

// tipoDeMedia devolve o tipo MIME de uma imagem que o modelo de visão recebe.
func tipoDeMedia(formato string) (string, bool) {
	switch formato {
	case FormatoPNG:
		return "image/png", true
	case FormatoJPEG:
		return "image/jpeg", true
	}
	return "", false
}

// corpoDaVisao é o pedido de uma imagem.
func corpoDaVisao(m MediaOCR, tipo string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model": AliasDaVisao,
		"messages": []any{
			map[string]any{"role": "system", "content": InstrucaoDaVisao},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": PedidoDaVisao},
				map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + tipo + ";base64," + base64.StdEncoding.EncodeToString(m.Bytes)}},
			}},
		},
		"max_tokens": TokensDaVisao,
	})
}

// Ler implementa [MotorOCR].
func (v *MotorOCRVisao) Ler(ctx context.Context, m MediaOCR) (LeituraOCR, error) {
	tipo, ok := tipoDeMedia(m.Formato)
	if !ok {
		return LeituraOCR{}, ErrOCRFora
	}
	corpo, err := corpoDaVisao(m, tipo)
	if err != nil {
		return LeituraOCR{}, err
	}
	pedido := &pedidoHTTP{}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, chaveDaChamada, pedido), http.MethodPost, v.base+"/chat/completions", bytes.NewReader(corpo))
	if err != nil {
		return LeituraOCR{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.credencial)
	resp, err := v.cliente.Do(req)
	switch {
	case errors.Is(err, ErrTectoAtingido):
		return LeituraOCR{}, &ParagemDoOCR{Causa: TerminouTecto}
	case errors.Is(err, ErrContador):
		return LeituraOCR{}, &ParagemDoOCR{Causa: TerminouContador}
	case err != nil:
		var ue interface{ Timeout() bool }
		if errors.As(err, &ue) && ue.Timeout() && ctx.Err() == nil {
			return LeituraOCR{}, ErrOCRTempo
		}
		return LeituraOCR{}, errors.New("o fornecedor nao respondeu")
	}
	defer func() { _ = resp.Body.Close() }()
	lido, _ := io.ReadAll(io.LimitReader(resp.Body, maxCorpoDaVisao))
	if resp.StatusCode != http.StatusOK {
		// Uma recusa da chave, da conta ou do ritmo repetia-se em todas as imagens e gastava o
		// tecto: a corrida pára. Os outros erros ficam na imagem. Nada do corpo do erro sai daqui.
		switch tipo := pedido.lerTipo(); {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusTooManyRequests:
			return LeituraOCR{}, &ParagemDoOCR{Causa: fmt.Sprintf("o fornecedor recusou (HTTP %d, %s)", resp.StatusCode, tipo)}
		default:
			return LeituraOCR{}, fmt.Errorf("o fornecedor respondeu com erro (HTTP %d, %s)", resp.StatusCode, tipo)
		}
	}
	var doc struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	legivel := json.Unmarshal(lido, &doc) == nil
	if uerr := v.contador.RegistarUso(doc.Usage.PromptTokens, doc.Usage.CompletionTokens); uerr != nil {
		return LeituraOCR{}, &ParagemDoOCR{Causa: TerminouContador}
	}
	l := LeituraOCR{MemoriaPico: -1, ComCusto: true, TokensDeEntrada: doc.Usage.PromptTokens, TokensDeSaida: doc.Usage.CompletionTokens, CustoMicroUSD: -1}
	if v.preco != nil {
		l.CustoMicroUSD = custoMicroUSD(*v.preco, doc.Usage.PromptTokens, doc.Usage.CompletionTokens)
		v.mu.Lock()
		v.custo += l.CustoMicroUSD
		v.comCusto = true
		v.mu.Unlock()
	}
	if legivel && len(doc.Choices) > 0 {
		// UMA RESPOSTA CORTADA NÃO É UMA TRANSCRIÇÃO (AOS-530, ADR-042 §2.8): o motivo de paragem
		// `length` diz que o modelo parou pelo limite de tokens. A imagem falha com a causa
		// `cortada`; os tokens e o custo contam na mesma, porque o pedido foi pago.
		if doc.Choices[0].FinishReason == "length" {
			return l, ErrOCRCortada
		}
		l.Texto = textoDoConteudo(doc.Choices[0].Message.Content)
	}
	if strings.TrimSpace(l.Texto) == "" {
		return l, errors.New("o modelo devolveu uma resposta vazia")
	}
	return l, nil
}

// textoDoConteudo lê o `content` de uma mensagem: uma string, ou uma lista de partes de texto.
func textoDoConteudo(cru json.RawMessage) string {
	var s string
	if json.Unmarshal(cru, &s) == nil {
		return s
	}
	var partes []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(cru, &partes) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range partes {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// VisaoFalsa é um modelo de visão falso, em HTTP compatível OpenAI: confere que o pedido traz a
// instrução fixa e UMA imagem em data URL, e responde com o texto de origem da imagem (pelo digest
// dos bytes), tal e qual ou sem acentos, e com tokens fixos. Serve os testes; não diz nada de um
// modelo real.
type VisaoFalsa struct {
	// Textos é o texto de origem de cada imagem, pelo digest dos seus bytes.
	Textos map[string]string
	// SemAcentos faz o falso devolver o texto sem acentos.
	SemAcentos bool
	// Estado, quando não zero, é o código HTTP de todas as respostas.
	Estado int
	// Cortar faz o falso responder com o motivo de paragem `length` (AOS-530): uma resposta
	// cortada pelo limite de tokens.
	Cortar bool

	mu        sync.Mutex
	Recebidos int
}

// NovaVisaoFalsa monta o falso com os textos da bateria de OCR.
func NovaVisaoFalsa(b *BateriaOCR) *VisaoFalsa {
	v := &VisaoFalsa{Textos: map[string]string{}}
	for _, m := range b.Media() {
		v.Textos[m.Digest] = m.textoDeOrigem
	}
	return v
}

// TokensDaVisaoFalsa são os tokens que o falso declara por resposta.
const (
	TokensDeEntradaDaVisaoFalsa = 1500
	TokensDeSaidaDaVisaoFalsa   = 80
)

func (v *VisaoFalsa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	v.Recebidos++
	v.mu.Unlock()
	if v.Estado != 0 {
		http.Error(w, `{"error":{"message":"recusado pelo falso","type":"invalid_request_error"}}`, v.Estado)
		return
	}
	var pedido struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.NewDecoder(r.Body).Decode(&pedido) != nil {
		http.Error(w, "pedido ilegivel", http.StatusBadRequest)
		return
	}
	texto := "ok"
	if len(pedido.Messages) == 2 {
		var partes []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if textoDoConteudo(pedido.Messages[0].Content) != InstrucaoDaVisao || json.Unmarshal(pedido.Messages[1].Content, &partes) != nil || len(partes) != 2 || partes[1].Type != "image_url" {
			http.Error(w, "pedido sem a instrucao fixa ou sem a imagem", http.StatusBadRequest)
			return
		}
		_, dados, ok := strings.Cut(partes[1].ImageURL.URL, ";base64,")
		cru, err := base64.StdEncoding.DecodeString(dados)
		if !ok || err != nil {
			http.Error(w, "imagem ilegivel", http.StatusBadRequest)
			return
		}
		digest := digestDe(cru)
		if texto, ok = v.Textos[digest]; !ok {
			http.Error(w, "imagem desconhecida", http.StatusBadRequest)
			return
		}
		if v.SemAcentos {
			texto = tirarAcentos(texto)
		}
	}
	paragem := "stop"
	if v.Cortar {
		paragem = "length"
	}
	resposta, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": texto}, "finish_reason": paragem}},
		"usage":   map[string]int64{"prompt_tokens": TokensDeEntradaDaVisaoFalsa, "completion_tokens": TokensDeSaidaDaVisaoFalsa},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resposta)
}
