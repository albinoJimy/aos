package bancoensaio

// AOS-558 — SPIKE DA PRÉ-CONDIÇÃO DA FASE R3 DO ADR-043: O PROXY FIXADO ENCAMINHA AS APIS DE
// IMAGEM E DE VÍDEO, OU SÓ O CHAT?
//
// A §3 E do ADR-043 deixa em aberto um adaptador directo à OpenRouter — que é um destino de
// egress novo, recusado hoje pelo guard-test AOS-069 — e manda decidir «pelo resultado de um
// spike que meça se o LiteLLM fixado encaminha as APIs de imagem e de vídeo». O AOS-534 mediu
// uma parte de ENTRADA dentro de `chat/completions`; este cenário mede ENDEREÇOS QUE NÃO SÃO O
// DO CHAT, e o sentido da SAÍDA, que é o que a R3 acrescenta:
//
//  1. `POST …/images/generations` — a geração de imagem pelo endpoint dedicado;
//  2. `POST …/chat/completions` com `modalities` e com as chaves de roteamento da OpenRouter
//     (`provider`, `plugins`, `transforms`) — se sobrevivem ao `drop_params: true` que a
//     configuração de produção tem;
//  3. `POST …/videos`, `GET …/videos/<id>` e `GET …/videos/<id>/content` — a API ASSÍNCRONA de
//     vídeo.
//
// Em cada caso mede-se o ENCAMINHAMENTO (a que caminho o pedido chegou ao fornecedor falso, ou
// se não chegou) e a SAÍDA (se os bytes que o falso emitiu voltaram iguais ao cliente). O falso
// regista a forma, nunca o conteúdo ([FormaNoFornecedor]).
//
// MEDE, NÃO JULGA: uma recusa do proxy é o resultado do spike, e vai no relatório
// (`AOS_BANCO_ENDPOINTS_REPORT`), por rota e por caso. Fica vermelho só se o ensaio não se
// montar, se o proxy não chegar ao falso num pedido de controlo só com texto, ou se uma chave
// aparecer no relatório.
//
// PRECISA DE DOCKER, da imagem já descarregada e de um binário LINUX do banco. Só a pedido:
//
//	AOS_BANCO_PROXY=1 AOS_BANCO_FALSO_BIN=<binário linux> go test -run '^TestAOS558_ProxyReal_ImagemEVideoPeloProxy$' -v -count=1 .
//
// O QUE NÃO PROVA: nada sobre a OpenRouter real — que modelos geram imagem ou vídeo, com que
// preço e com que limites, nem se a forma que o proxy monta é a que a OpenRouter aceita. Isso é
// outro elo, com custo.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// aos558ModeloDeImagem é o nome, na OpenRouter, do modelo da rota do ensaio: um que a OpenRouter
// documenta como capaz de gerar imagem. O falso responde a qualquer um; o nome só importa ao
// proxy, se ele decidir pela ficha do modelo o que fazer com o pedido.
const aos558ModeloDeImagem = "google/gemini-2.5-flash-image"

// Os encaminhamentos possíveis de um caso ([aos558Medida.Encaminhamento]). Ou é o nome do
// caminho em que o pedido chegou ao fornecedor (o vocabulário de [ClassificarCaminho]), ou:
const (
	// aos558NaoChegouAoFornecedor — nenhum pedido chegou ao fornecedor. NÃO se chama «recusado
	// pelo proxy» de propósito: há casos em que o proxy responde 200 e nada chega lá: quem
	// lesse o relatório em bruto leria o contrário do que aconteceu.
	aos558NaoChegouAoFornecedor = "nao_chegou_ao_fornecedor"
	// aos558RecusadoPeloFalso — chegou ao fornecedor e foi ELE que o recusou (a chave, o tecto
	// do corpo, um corpo ilegível). Separa-se de «não chegou»: num caso destes o proxy
	// encaminhou, e sem este nome o instrumento dizia que não.
	aos558RecusadoPeloFalso = "recusado_pelo_falso"
	// aos558MaisDeUmCaminho — chegou mais do que um pedido, a caminhos diferentes.
	aos558MaisDeUmCaminho = "mais_de_um_caminho"
)

// Os resultados do sentido de volta ([aos558Medida.Saida]).
const (
	// aos558SaidaIntacta — os bytes que o falso emitiu voltaram iguais ao cliente.
	aos558SaidaIntacta = "intacta"
	// aos558SaidaPerdida — chegaram ao fornecedor, mas o que voltou não traz os bytes.
	aos558SaidaPerdida = "alterada_ou_perdida"
	// aos558SaidaNaoAplicavel — o caso não tem bytes de saída para comparar.
	aos558SaidaNaoAplicavel = "nao_aplicavel"
)

func TestAOS558_ProxyReal_ImagemEVideoPeloProxy(t *testing.T) {
	binario := os.Getenv("AOS_BANCO_FALSO_BIN")
	if os.Getenv("AOS_BANCO_PROXY") != "1" || binario == "" {
		t.Skip("SALTADO: AOS_BANCO_PROXY != 1 ou AOS_BANCO_FALSO_BIN vazio — medir as APIs de imagem e de video atras do proxy real precisa de Docker, " +
			"da imagem de producao do proxy e de um binario linux do banco. POR VERIFICAR nesta execucao: o que o proxy faz a um pedido de imagem ou de video. " +
			"O registo do caminho e das chaves no falso, sem proxy, correu (TestAOS558_Falso_*).")
	}
	const chave = "sk-falso-aos558-endpoints"
	shaDoPNG := sha256.Sum256(PNGDoFalso())
	esperado := hex.EncodeToString(shaDoPNG[:])
	medidas := map[string]any{"imagem": ImagemDoProxy, "modelo": aos558ModeloDeImagem, "sha256_do_png_do_falso": esperado}
	mestras := []string{}

	for _, prefixo := range []string{PrefixoDaOpenRouter, "openai"} {
		ctx, cancelar := context.WithTimeout(context.Background(), 12*time.Minute)
		vivo, err := (&LancadorDocker{Redactor: &Redactor{}}).Lancar(ctx, PedidoDeProxy{
			Prefixo: prefixo, Modelo: aos558ModeloDeImagem, BinarioDoFalso: binario, Estado: EstadoExige,
			TurnosDoFalso: 1, FormaDoFalso: FormaOpenRouter, ImagemDeSaida: true, Vida: 14 * time.Minute,
		}.ComSegredos(chave, ""))
		if err != nil {
			cancelar()
			t.Fatalf("%s: o proxy nao arrancou: %v", prefixo, err)
		}
		mestras = append(mestras, vivo.ChaveMestra())
		sonda := &aos558Sonda{t: t, ctx: ctx, vivo: vivo, antes: &FormaNoFornecedor{}}
		doPrefixo := map[string]any{"rota": prefixo + "/" + aos558ModeloDeImagem}

		// O CONTROLO: um pedido de chat só com texto tem de chegar ao falso. Se não chegar, o
		// ensaio não mediu nada e o teste fica vermelho.
		controlo := sonda.enviar(http.MethodPost, "/chat/completions",
			[]byte(`{"model":"`+AliasDaRota+`","max_tokens":64,"messages":[{"role":"user","content":"`+TextoDaSonda+`"}]}`))
		if controlo.Pedidos != 1 || controlo.HTTP != http.StatusOK {
			vivo.Fechar()
			cancelar()
			t.Fatalf("%s: o controlo so com texto nao chegou ao falso: %+v", prefixo, controlo)
		}
		// A resposta do controlo já traz o `images` do falso (é um pedido sem tools): mede-se de
		// graça se um `images` de uma resposta de chat em passagem sobrevive ao proxy. NÃO é
		// gate: se o proxy o filtrar, isso é um resultado do spike e não um ensaio mal montado.
		controlo.medirSaidaDaImagem(esperado)
		doPrefixo["controlo_texto"] = controlo

		// A SENTINELA É PRODUZIDA NOUTRO BINÁRIO. O PNG que viaja é o do binário LINUX do falso,
		// dentro do contentor; `esperado` é o do processo do teste. Se não forem da mesma árvore,
		// todos os casos de imagem davam `alterada_ou_perdida` — um falso negativo calado que
		// inverteria a conclusão. O falso publica o seu sha na forma: confronta-se aqui, na
		// montagem do ensaio, onde um vermelho não se confunde com um resultado do spike.
		if forma, ferr := LerFormaDoFalso(ctx, vivo.EnderecoDoFalso); ferr != nil || forma.ShaDoPNG != esperado {
			vivo.Fechar()
			cancelar()
			t.Fatalf("%s: o PNG do falso nao e o deste teste (falso=%q, teste=%q, err=%v): o binario de AOS_BANCO_FALSO_BIN nao e desta arvore",
				prefixo, shaDoFalso(forma), esperado, ferr)
		}

		// 1. A geração de imagem pelo endpoint dedicado.
		imagem := sonda.enviar(http.MethodPost, "/images/generations",
			[]byte(`{"model":"`+AliasDaRota+`","prompt":"um plano de um comodo vazio","n":1,"size":"1024x1024"}`))
		imagem.medirSaidaDaImagem(esperado)
		doPrefixo["imagem_endpoint_dedicado"] = imagem

		// 2. A geração de imagem pelo chat, com as chaves de roteamento da OpenRouter.
		chat := sonda.enviar(http.MethodPost, "/chat/completions", []byte(`{"model":"`+AliasDaRota+`","max_tokens":64,`+
			`"modalities":["image","text"],"provider":{"order":["Google"],"data_collection":"deny"},`+
			`"plugins":[{"id":"web"}],"transforms":["middle-out"],`+
			`"messages":[{"role":"user","content":"um plano de um comodo vazio"}]}`))
		chat.medirSaidaDaImagem(esperado)
		doPrefixo["imagem_pelo_chat_com_modalities"] = chat

		// 3. A API assíncrona de vídeo: criar, perguntar o estado, pedir o conteúdo.
		criar := sonda.enviar(http.MethodPost, "/videos",
			[]byte(`{"model":"`+AliasDaRota+`","prompt":"um comodo visto de dentro","seconds":"4"}`))
		doPrefixo["video_criar"] = criar
		// O estado pede-se com o id QUE O PROXY DEVOLVEU — é esse o fluxo da API assíncrona, e um
		// id inventado mediria outra coisa (o proxy deriva do id a rota por onde vai perguntar).
		// Sem id na resposta, fica o do falso, e o relatório diz qual foi.
		idDoJob, deQuem := IDDoJobDeVideo, "do_falso (a resposta da criacao nao trouxe id utilizavel)"
		if id, forma := aos558IDDoJob(criar.corpo); id != "" {
			idDoJob, deQuem = id, forma
		}
		doPrefixo["id_do_job"] = deQuem
		estado := sonda.enviar(http.MethodGet, "/videos/"+idDoJob, nil)
		doPrefixo["video_estado"] = estado
		conteudo := sonda.enviar(http.MethodGet, "/videos/"+idDoJob+"/content", nil)
		if bytes.Equal(conteudo.corpo, conteudoDoVideoDoFalso) {
			conteudo.Saida = aos558SaidaIntacta
		} else if conteudo.Encaminhamento == CaminhoDoConteudoDoVideo {
			conteudo.Saida = aos558SaidaPerdida
		}
		doPrefixo["video_conteudo"] = conteudo

		for nome, m := range doPrefixo {
			if medida, ok := m.(*aos558Medida); ok {
				t.Logf("%s/%s: encaminhamento=%s saida=%s%s http=%d", prefixo, nome,
					medida.Encaminhamento, medida.Saida, aos558Sufixo(medida.FormaDaSaida), medida.HTTP)
			}
		}
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
	t.Logf("AOS_BANCO_ENDPOINTS_REPORT %s", resumo)
}

// aos558Medida é o que se mediu num caso. O corpo da resposta NÃO vai no relatório: só o que
// dele se concluiu ([aos558Medida.Saida]).
type aos558Medida struct {
	HTTP            int            `json:"http"`
	TipoDoErro      string         `json:"tipo_do_erro,omitempty"`
	Ms              int64          `json:"ms"`
	Pedidos         int            `json:"pedidos_no_fornecedor"`
	Caminhos        map[string]int `json:"caminhos_no_fornecedor,omitempty"`
	ChavesDoTopo    map[string]int `json:"chaves_do_topo_no_fornecedor,omitempty"`
	Recusas         map[string]int `json:"recusas_do_falso,omitempty"`
	Autenticacao    map[string]int `json:"respostas_401_do_falso,omitempty"`
	Encaminhamento  string         `json:"encaminhamento"`
	Saida           string         `json:"saida"`
	FormaDaSaida    string         `json:"forma_da_saida,omitempty"`
	RastoDoProxy    string         `json:"rasto_do_proxy,omitempty"`
	ErroDeTransport bool           `json:"erro_de_transporte,omitempty"`

	corpo []byte
}

// aos558Sonda manda pedidos ao proxy e lê, a cada um, o que mudou no fornecedor falso.
type aos558Sonda struct {
	t     *testing.T
	ctx   context.Context
	vivo  *ProxyVivo
	antes *FormaNoFornecedor
}

// enviar manda um pedido ao proxy e devolve a medida. O corpo da resposta fica na medida para
// quem o quiser comparar, e não entra no relatório.
func (s *aos558Sonda) enviar(metodo, caminho string, corpo []byte) *aos558Medida {
	m := &aos558Medida{Saida: aos558SaidaNaoAplicavel}
	var leitor io.Reader
	if corpo != nil {
		leitor = bytes.NewReader(corpo)
	}
	req, err := http.NewRequestWithContext(s.ctx, metodo, s.vivo.BaseURL+caminho, leitor)
	if err != nil {
		s.t.Fatalf("pedido mal formado para %s: %v", caminho, err)
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
	}
	m.Ms = time.Since(inicio).Milliseconds()
	// Numa recusa, a causa está no registo do proxy: só nomes de excepção e de função.
	if m.HTTP != http.StatusOK && s.vivo.Rasto != nil {
		m.RastoDoProxy = s.vivo.Rasto()
	}
	depois, ferr := LerFormaDoFalso(s.ctx, s.vivo.EnderecoDoFalso)
	if ferr != nil {
		s.vivo.Fechar()
		s.t.Fatalf("%s: o falso nao deu a forma: %v", caminho, ferr)
	}
	m.Pedidos = depois.Pedidos - s.antes.Pedidos
	m.Caminhos = aos516Diferenca(s.antes.Caminhos, depois.Caminhos)
	m.ChavesDoTopo = aos516Diferenca(s.antes.ChavesDoTopo, depois.ChavesDoTopo)
	m.Recusas = aos516Diferenca(s.antes.Recusas, depois.Recusas)
	m.Autenticacao = aos516Diferenca(s.antes.Autenticacao, depois.Autenticacao)
	s.antes = depois
	m.Encaminhamento = aos558Encaminhamento(m)
	return m
}

// aos558Encaminhamento diz a que caminho o pedido chegou ao fornecedor — ou que não chegou, ou
// que chegou e foi o fornecedor a recusá-lo. A ordem importa: uma recusa do falso (401, 400) só
// se distingue de «não chegou» pelos contadores dele, e sem isso um pedido que o proxy
// ENCAMINHOU era reportado como não encaminhado.
func aos558Encaminhamento(m *aos558Medida) string {
	if len(m.Recusas) > 0 || len(m.Autenticacao) > 0 {
		return aos558RecusadoPeloFalso
	}
	if m.Pedidos == 0 || len(m.Caminhos) == 0 {
		return aos558NaoChegouAoFornecedor
	}
	if len(m.Caminhos) > 1 {
		return aos558MaisDeUmCaminho
	}
	for caminho := range m.Caminhos {
		return caminho
	}
	return aos558NaoChegouAoFornecedor
}

// medirSaidaDaImagem preenche a saída e a forma dela — mas só quando o proxy respondeu 200: num
// caso recusado não há saída para comparar, e o campo fica `nao_aplicavel`, como no vídeo.
func (m *aos558Medida) medirSaidaDaImagem(esperado string) {
	if m.HTTP != http.StatusOK {
		return
	}
	m.Saida, m.FormaDaSaida = aos558SaidaDaImagem(m.corpo, esperado)
}

// aos558Sufixo põe a forma da saída entre parênteses, ou nada quando não há.
func aos558Sufixo(forma string) string {
	if forma == "" {
		return ""
	}
	return "(" + forma + ")"
}

// shaDoFalso é o sha do PNG que o falso publicou, tolerante a uma forma que não se leu.
func shaDoFalso(forma *FormaNoFornecedor) string {
	if forma == nil {
		return ""
	}
	return forma.ShaDoPNG
}

// aos558IDDoJob tira o `id` de uma resposta de criação de vídeo, já escapado para entrar num
// segmento de caminho, e devolve também a FORMA dele (o comprimento e se é só alfanumérico) —
// o id em si não vai no relatório. Vazio quando não há id ou quando ele traz bytes que não são
// ASCII imprimível.
func aos558IDDoJob(corpo []byte) (string, string) {
	var resposta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(corpo, &resposta) != nil || resposta.ID == "" || len(resposta.ID) > 512 {
		return "", ""
	}
	simples := true
	for i := 0; i < len(resposta.ID); i++ {
		c := resposta.ID[i]
		if c < 0x20 || c > 0x7E {
			return "", ""
		}
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			simples = false
		}
	}
	forma := fmt.Sprintf("devolvido_pelo_proxy (bytes=%d alfanumerico=%t)", len(resposta.ID), simples)
	return url.PathEscape(resposta.ID), forma
}

// As formas em que o PNG pode voltar ao cliente ([aos558Medida.FormaDaSaida]). Dizer QUAL delas
// foi é o que separa «o proxy traduziu a resposta de chat para a de imagem» de «o proxy devolveu
// a resposta de chat em cru»: as duas davam `intacta`.
const (
	aos558SaidaEmDataB64   = "data_b64_json"
	aos558SaidaEmDataURL   = "data_url"
	aos558SaidaEmChatImage = "chat_message_images"
)

// aos558SaidaDaImagem diz se o PNG que o falso emitiu voltou ao cliente e em que forma voltou.
// Só se chama quando o proxy respondeu 200: num caso recusado não há saída para comparar, e
// dizer «alterada ou perdida» de uma saída que nunca existiu seria uma medida falsa.
func aos558SaidaDaImagem(corpo []byte, esperado string) (string, string) {
	var resposta struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
		Choices []struct {
			Message struct {
				Images []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(corpo, &resposta) != nil {
		return aos558SaidaPerdida, ""
	}
	type candidato struct{ valor, forma string }
	candidatos := []candidato{}
	for _, d := range resposta.Data {
		candidatos = append(candidatos, candidato{d.B64, aos558SaidaEmDataB64}, candidato{d.URL, aos558SaidaEmDataURL})
	}
	for _, c := range resposta.Choices {
		for _, img := range c.Message.Images {
			candidatos = append(candidatos, candidato{img.ImageURL.URL, aos558SaidaEmChatImage})
		}
	}
	for _, c := range candidatos {
		if c.valor == "" {
			continue
		}
		valor := c.valor
		if _, dados, tem := strings.Cut(valor, ";base64,"); tem {
			valor = dados
		}
		cru, err := base64.StdEncoding.DecodeString(valor)
		if err != nil || len(cru) == 0 {
			// Bytes nenhuns nunca são «a imagem chegou»: sem isto, uma data URL vazia dos dois
			// lados casaria o sha256 do vazio e daria `intacta` sem imagem nenhuma.
			continue
		}
		soma := sha256.Sum256(cru)
		if hex.EncodeToString(soma[:]) == esperado {
			return aos558SaidaIntacta, c.forma
		}
	}
	return aos558SaidaPerdida, ""
}
