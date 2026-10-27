package bancoensaio

// AOS-565 — PRIMEIRO CRITÉRIO DO TICKET, E É UMA MEDIÇÃO: O PROXY DEIXA PASSAR O FORNECEDOR
// EFECTIVO DO CORPO DA RESPOSTA?
//
// O ADR-043 §2.4 regra 3 exige que «o fornecedor é fixado por pedido … e o fornecedor efectivo
// fica selado». O spike AOS-558 mediu a IDA (`provider`, `modalities`, `plugins` e `transforms`
// chegam ao fornecedor nas duas formas de rota). A VOLTA parte-se em duas, e só uma se mede
// aqui:
//
//   - (i) o fornecedor a montante EMITE a identidade efectiva? Exige a OpenRouter real e custo —
//     é o AOS-570a, e NÃO é esta medição;
//   - (ii) se ela vier no corpo, o proxy DEIXA-A PASSAR até ao cliente? É esta.
//
// A (ii) é decisiva por si: se o proxy a remover, então mesmo que a OpenRouter a emita o nó
// nunca a veria, e a metade «selar o fornecedor efectivo» do AOS-565 tem de ir buscar o valor a
// outro sítio (um cabeçalho) ou cair.
//
// O QUE SE MEDE, por cada uma das duas formas de rota (`openrouter/<autor>/<modelo>` e
// `openai/<autor>/<modelo>` com a base do fornecedor, que é a de produção), em TRÊS corridas:
//
//  1. a chave de topo `provider` que o falso emitiu — chegou ao cliente, chegou com outro nome,
//     ou foi removida;
//  2. a chave de topo INVENTADA `aos_sonda` — o CONTROLO, sem o qual a medida não distingue «o
//     proxy preserva chaves de topo desconhecidas em geral» de «o proxy trata a `provider` de
//     forma especial»;
//  3. os cabeçalhos da resposta ao cliente: os nomes todos, quais levam a sentinela do falso, e
//     quais nomeiam um fornecedor.
//
// TRÊS CORRIDAS POR CASO, e não uma: uma corrida só apresenta não-determinismo como
// determinismo, e o próprio spike AOS-558 apanhou uma resposta instável assim (§4.3).
//
// MEDE, NÃO JULGA: que o proxy remova as chaves é um resultado e vai no relatório
// (`AOS_BANCO_FORNECEDOR_REPORT`). Fica vermelho só se o ensaio não se montar, se o pedido de
// controlo não chegar ao falso, ou se uma chave aparecer no relatório.
//
// PRECISA DE DOCKER, da imagem já descarregada e de um binário LINUX do banco. Só a pedido:
//
//	AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=<binário linux> go test -run '^TestAOS565_ProxyReal_FornecedorDeVolta$' -v -count=1 .
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real — se ela emite o fornecedor efectivo, com que
// nome e com que valor. O que o falso responde é escolha de quem o escreveu, pelo que um
// resultado NEGATIVO aqui é um controlo negativo sobre um recurso fabricado: diria que o proxy
// não deixa passar ESTE corpo, nunca que o fornecedor não se consegue reportar por via nenhuma.
// E não mede o `drop_params: false` que produção corre: a configuração do banco é fixa em
// `true` — o que, no sentido da VOLTA, não é um parâmetro de pedido (ver §4 do relatório).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// aos565Modelo é o modelo da rota do ensaio, na forma `<autor>/<modelo>` da OpenRouter. O falso
// responde a qualquer um; o nome só importa ao proxy.
const aos565Modelo = "anthropic/claude-sonnet-4.5"

// aos565Corridas é o número de corridas por caso. Três, pela regra acima.
const aos565Corridas = 3

func TestAOS565_ProxyReal_FornecedorDeVolta(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 ou AOS_BANCO_FALSO_BIN vazio — medir o sentido de VOLTA do fornecedor atras do proxy real precisa de Docker, " +
			"da imagem de producao do proxy e de um binario linux do banco. POR VERIFICAR nesta execucao: se o proxy deixa passar as chaves de topo da resposta. " +
			"O instrumento (o falso emite as sentinelas, o classificador separa as saidas) tem testes que correm sem Docker (TestAOS565_Falso_*, TestAOS565_Classificar).")
	}
	const chave = "sk-falso-aos565-fornecedor"
	medidas := map[string]any{
		"imagem": ImagemDoProxy, "modelo": aos565Modelo, "corridas_por_caso": aos565Corridas,
		"chave_do_fornecedor": ChaveDoFornecedorNaResposta, "chave_de_controlo": ChaveDaSondaDeTopo,
		"cabecalho_do_falso": CabecalhoDaSondaDoFornecedor,
	}
	mestras := []string{}

	for _, prefixo := range []string{PrefixoDaOpenRouter, "openai"} {
		ctx, cancelar := context.WithTimeout(context.Background(), 12*time.Minute)
		vivo, err := (&LancadorDocker{Redactor: &Redactor{}}).Lancar(ctx, PedidoDeProxy{
			Prefixo: prefixo, Modelo: aos565Modelo, BinarioDoFalso: binario, Estado: EstadoExige,
			TurnosDoFalso: 1, FormaDoFalso: FormaOpenRouter, FornecedorNaResposta: true, Vida: 14 * time.Minute,
		}.ComSegredos(chave, ""))
		if err != nil {
			cancelar()
			t.Fatalf("%s: o proxy nao arrancou: %v", prefixo, err)
		}
		mestras = append(mestras, vivo.ChaveMestra())
		sonda := &aos565Sonda{t: t, ctx: ctx, vivo: vivo, antes: &FormaNoFornecedor{}}
		doPrefixo := map[string]any{"rota": prefixo + "/" + aos565Modelo}

		// O CONTROLO DA MONTAGEM: um pedido de chat só com texto tem de chegar ao falso. Se não
		// chegar, o ensaio não mediu nada e o teste fica vermelho.
		controlo := sonda.enviar(aos565CorpoDoPedido())
		if controlo.Pedidos != 1 || controlo.HTTP != http.StatusOK {
			vivo.Fechar()
			cancelar()
			t.Fatalf("%s: o controlo so com texto nao chegou ao falso: http=%d pedidos=%d encaminhamento=%s",
				prefixo, controlo.HTTP, controlo.Pedidos, controlo.Encaminhamento)
		}

		corridas := make([]*aos565Medida, 0, aos565Corridas)
		for n := 1; n <= aos565Corridas; n++ {
			m := sonda.enviar(aos565CorpoDoPedido())
			m.Corrida = n
			corridas = append(corridas, m)
			t.Logf("%s/corrida-%d: encaminhamento=%s http=%d provider=%s%s sonda=%s%s cabecalho_do_falso=%t",
				prefixo, n, m.Encaminhamento, m.HTTP, m.Provider.Classe, aos565Onde(m.Provider.Caminhos),
				m.Sonda.Classe, aos565Onde(m.Sonda.Caminhos), m.CabecalhoDoFalsoChegou)
		}
		doPrefixo["corridas"] = corridas
		doPrefixo["veredicto"] = aos565Veredicto(corridas)
		vivo.Fechar()
		cancelar()
		medidas[prefixo] = doPrefixo
	}

	resumo, err := json.Marshal(medidas)
	if err != nil {
		t.Fatalf("o relatorio nao serializa: %v", err)
	}
	for _, segredo := range append(mestras, chave, "Bearer ") {
		if segredo != "" && bytes.Contains(resumo, []byte(segredo)) {
			t.Fatalf("o relatorio deixa sair um segredo")
		}
	}
	t.Logf("AOS_BANCO_FORNECEDOR_REPORT %s", resumo)
}

// aos565CorpoDoPedido é o pedido de chat da medida: sem tools (o falso responde o texto final) e
// com a `provider` da IDA declarada, que é a forma em que a R3 vai fixar o fornecedor. A ida já
// está medida pelo AOS-558; vai aqui só para o pedido ser o do caso real.
func aos565CorpoDoPedido() []byte {
	return []byte(`{"model":"` + AliasDaRota + `","max_tokens":64,` +
		`"provider":{"order":["Anthropic"],"allow_fallbacks":false,"data_collection":"deny"},` +
		`"messages":[{"role":"user","content":"` + TextoDaSonda + `"}]}`)
}

// aos565Medida é o que se mediu numa corrida. O corpo da resposta NÃO vai no relatório: só o que
// dele se concluiu.
type aos565Medida struct {
	Corrida         int            `json:"corrida"`
	HTTP            int            `json:"http"`
	TipoDoErro      string         `json:"tipo_do_erro,omitempty"`
	Ms              int64          `json:"ms"`
	Pedidos         int            `json:"pedidos_no_fornecedor"`
	Caminhos        map[string]int `json:"caminhos_no_fornecedor,omitempty"`
	Recusas         map[string]int `json:"recusas_do_falso,omitempty"`
	Autenticacao    map[string]int `json:"respostas_401_do_falso,omitempty"`
	Encaminhamento  string         `json:"encaminhamento"`
	ErroDeTransport bool           `json:"erro_de_transporte,omitempty"`
	RastoDoProxy    string         `json:"rasto_do_proxy,omitempty"`

	// Provider é o que aconteceu à chave de topo `provider` do fornecedor.
	Provider aos565Chegada `json:"provider"`
	// Sonda é o que aconteceu à chave de topo INVENTADA — o controlo.
	Sonda aos565Chegada `json:"aos_sonda"`
	// ChavesDoTopoDaResposta são os nomes das chaves de topo do corpo que chegou ao cliente.
	ChavesDoTopoDaResposta []string `json:"chaves_do_topo_da_resposta,omitempty"`
	// Cabecalhos são os nomes dos cabeçalhos da resposta ao cliente, em minúsculas e ordenados.
	Cabecalhos []string `json:"cabecalhos_da_resposta,omitempty"`
	// CabecalhoDoFalsoChegou diz se o cabeçalho que o FALSO emitiu
	// ([CabecalhoDaSondaDoFornecedor]) sobreviveu à passagem pelo proxy.
	CabecalhoDoFalsoChegou bool `json:"cabecalho_do_falso_chegou"`
	// CabecalhosQueNomeiamFornecedor são os cabeçalhos cujo NOME fala de fornecedor
	// (`provider`), com o valor — são os candidatos à via alternativa do AOS-565.
	CabecalhosQueNomeiamFornecedor map[string]string `json:"cabecalhos_que_nomeiam_fornecedor,omitempty"`

	corpo []byte
}

// aos565Sonda manda pedidos de chat ao proxy e lê, a cada um, o que mudou no fornecedor falso.
type aos565Sonda struct {
	t     *testing.T
	ctx   context.Context
	vivo  *ProxyVivo
	antes *FormaNoFornecedor
}

// enviar manda um pedido de chat ao proxy e devolve a medida.
func (s *aos565Sonda) enviar(corpo []byte) *aos565Medida {
	m := &aos565Medida{}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.vivo.BaseURL+"/chat/completions", bytes.NewReader(corpo))
	if err != nil {
		s.t.Fatalf("pedido mal formado: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.vivo.ChaveMestra())
	inicio := time.Now()
	if resp, rerr := http.DefaultClient.Do(req); rerr != nil {
		m.ErroDeTransport = true
	} else {
		m.corpo, _ = io.ReadAll(io.LimitReader(resp.Body, maxCorpoDoFalsoDoEstado))
		_ = resp.Body.Close()
		m.HTTP = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			m.TipoDoErro = classificarErro(resp.StatusCode, m.corpo[:min(len(m.corpo), maxCorpoDeErro)])
		}
		m.lerCabecalhos(resp.Header)
	}
	m.Ms = time.Since(inicio).Milliseconds()
	if m.HTTP != http.StatusOK && s.vivo.Rasto != nil {
		m.RastoDoProxy = s.vivo.Rasto()
	}
	depois, ferr := LerFormaDoFalso(s.ctx, s.vivo.EnderecoDoFalso)
	if ferr != nil {
		s.vivo.Fechar()
		s.t.Fatalf("o falso nao deu a forma: %v", ferr)
	}
	m.Pedidos = depois.Pedidos - s.antes.Pedidos
	m.Caminhos = aos516Diferenca(s.antes.Caminhos, depois.Caminhos)
	m.Recusas = aos516Diferenca(s.antes.Recusas, depois.Recusas)
	m.Autenticacao = aos516Diferenca(s.antes.Autenticacao, depois.Autenticacao)
	s.antes = depois
	m.Encaminhamento = aos558Encaminhamento(&aos558Medida{
		Pedidos: m.Pedidos, Caminhos: m.Caminhos, Recusas: m.Recusas, Autenticacao: m.Autenticacao,
	})
	m.classificar()
	return m
}

// lerCabecalhos regista os NOMES dos cabeçalhos da resposta (nunca os valores, tirando os dos
// que nomeiam um fornecedor, que são o que o critério do ticket pede) e se o cabeçalho que o
// falso emitiu sobreviveu.
func (m *aos565Medida) lerCabecalhos(h http.Header) {
	for nome, valores := range h {
		minusculo := strings.ToLower(nome)
		m.Cabecalhos = append(m.Cabecalhos, minusculo)
		junto := strings.Join(valores, ",")
		if strings.Contains(junto, SentinelaDoCabecalho) {
			m.CabecalhoDoFalsoChegou = true
		}
		// Um cabeçalho cujo NOME fala de fornecedor é um candidato à via alternativa do ticket:
		// o valor vai, porque é precisamente o que ele teria de levar. O do falso fica de fora:
		// o valor dele já se sabe, e é a sentinela.
		if strings.Contains(minusculo, "provider") && minusculo != strings.ToLower(CabecalhoDaSondaDoFornecedor) {
			if m.CabecalhosQueNomeiamFornecedor == nil {
				m.CabecalhosQueNomeiamFornecedor = map[string]string{}
			}
			m.CabecalhosQueNomeiamFornecedor[minusculo] = junto
		}
	}
	sort.Strings(m.Cabecalhos)
}

// classificar lê o corpo que chegou ao cliente e diz o que aconteceu às duas chaves. Um pedido
// que NÃO chegou ao fornecedor não tem sentinela nenhuma para perder: dizer «removida» desse
// seria uma medida falsa, e por isso fica [aos565NaoMedida].
func (m *aos565Medida) classificar() {
	if m.Encaminhamento != CaminhoDeChat {
		m.Provider = aos565Chegada{Classe: aos565NaoMedida}
		m.Sonda = aos565Chegada{Classe: aos565NaoMedida}
		return
	}
	m.Provider = aos565Classificar(m.corpo, ChaveDoFornecedorNaResposta, SentinelaDoFornecedor)
	m.Sonda = aos565Classificar(m.corpo, ChaveDaSondaDeTopo, SentinelaDaSonda)
	var topo map[string]json.RawMessage
	if json.Unmarshal(m.corpo, &topo) == nil {
		for k := range topo {
			m.ChavesDoTopoDaResposta = append(m.ChavesDoTopoDaResposta, k)
		}
		sort.Strings(m.ChavesDoTopoDaResposta)
	}
}

// aos565Onde põe os caminhos entre parênteses, ou nada quando não há.
func aos565Onde(caminhos []string) string {
	if len(caminhos) == 0 {
		return ""
	}
	return "(" + strings.Join(caminhos, " ") + ")"
}

// aos565Veredicto resume as três corridas de uma rota: a classe de cada chave quando as três
// concordam, ou a lista delas quando não concordam — porque três corridas que divergem são o
// resultado, e escondê-las numa só apresentaria não-determinismo como determinismo.
func aos565Veredicto(corridas []*aos565Medida) map[string]string {
	resumir := func(valores []string) string {
		iguais := true
		for _, v := range valores {
			iguais = iguais && v == valores[0]
		}
		if iguais && len(valores) > 0 {
			return valores[0]
		}
		return "INSTAVEL: " + strings.Join(valores, " | ")
	}
	var provider, sonda, cabecalho []string
	for _, c := range corridas {
		provider = append(provider, c.Provider.Classe+aos565Onde(c.Provider.Caminhos))
		sonda = append(sonda, c.Sonda.Classe+aos565Onde(c.Sonda.Caminhos))
		cabecalho = append(cabecalho, map[bool]string{true: "chegou", false: "nao_chegou"}[c.CabecalhoDoFalsoChegou])
	}
	return map[string]string{
		ChaveDoFornecedorNaResposta: resumir(provider),
		ChaveDaSondaDeTopo:          resumir(sonda),
		"cabecalho_do_falso":        resumir(cabecalho),
	}
}
