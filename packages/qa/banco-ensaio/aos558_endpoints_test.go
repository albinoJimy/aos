package bancoensaio

// AOS-558 — o falso do estado regista o CAMINHO e as CHAVES DO TOPO de cada pedido, e sabe
// responder nas formas de imagem e de vídeo. Correm sem Docker.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAOS558_ClassificarCaminho(t *testing.T) {
	casos := []struct{ caminho, quer string }{
		{"/v1/chat/completions", CaminhoDeChat},
		{"/chat/completions", CaminhoDeChat},
		{"/v1/messages", CaminhoDeMensagens},
		{"/v1/images/generations", CaminhoDeImagens},
		{"/v1/images/generations/", CaminhoDeImagens},
		{"/v1/images/edits", CaminhoDeEdicaoDeImagens},
		{"/v1/videos", CaminhoDeVideos},
		{"/videos", CaminhoDeVideos},
		{"/v1/videos/video_abc", CaminhoDeVideoPorID},
		{"/v1/videos/video_abc/content", CaminhoDoConteudoDoVideo},
		{"/v1/videos/characters", CaminhoDeVideoPorID},
		{"/v1/models", CaminhoOutro},
		{"/", CaminhoOutro},
		{"", CaminhoOutro},
	}
	for _, c := range casos {
		if got := ClassificarCaminho(c.caminho); got != c.quer {
			t.Errorf("ClassificarCaminho(%q) = %q, quer %q", c.caminho, got, c.quer)
		}
	}
}

// aos558Falso devolve um falso do estado na forma da OpenRouter, com a imagem de saída ligada.
func aos558Falso() *FalsoDeEstado {
	return &FalsoDeEstado{FormaDoEstado: FormaOpenRouter, ImagemDeSaida: true, Turnos: 1}
}

// aos558Pedir manda um pedido ao falso e devolve a resposta gravada.
func aos558Pedir(f *FalsoDeEstado, metodo, caminho, corpo string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(metodo, caminho, strings.NewReader(corpo))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	f.ServeHTTP(resp, req)
	return resp
}

func TestAOS558_Falso_RegistaOCaminhoDeCadaPedido(t *testing.T) {
	f := aos558Falso()
	aos558Pedir(f, http.MethodPost, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"ping"}]}`)
	aos558Pedir(f, http.MethodPost, "/v1/images/generations", `{"model":"m","prompt":"um gato","n":1,"size":"1024x1024"}`)
	aos558Pedir(f, http.MethodPost, "/v1/videos", `{"model":"m","prompt":"um gato a andar"}`)
	aos558Pedir(f, http.MethodGet, "/v1/videos/"+IDDoJobDeVideo, "")
	aos558Pedir(f, http.MethodGet, "/v1/videos/"+IDDoJobDeVideo+"/content", "")
	aos558Pedir(f, http.MethodPost, "/v1/models", `{}`)

	forma := f.Forma()
	if forma.Pedidos != 6 {
		t.Fatalf("pedidos = %d, quer 6", forma.Pedidos)
	}
	for caminho, quer := range map[string]int{
		CaminhoDeChat: 1, CaminhoDeImagens: 1, CaminhoDeVideos: 1,
		CaminhoDeVideoPorID: 1, CaminhoDoConteudoDoVideo: 1, CaminhoOutro: 1,
	} {
		if forma.Caminhos[caminho] != quer {
			t.Errorf("caminhos[%s] = %d, quer %d (todos: %v)", caminho, forma.Caminhos[caminho], quer, forma.Caminhos)
		}
	}
}

func TestAOS558_Falso_RegistaAsChavesDoTopoSemValores(t *testing.T) {
	f := aos558Falso()
	// Um pedido de chat com tudo o que interessa ao spike: as chaves de roteamento da OpenRouter
	// (`provider`, `plugins`, `transforms`), a modalidade de saída e um segredo numa chave que
	// não está na lista fechada.
	corpo := `{"model":"m","messages":[{"role":"user","content":"ping"}],` +
		`"modalities":["image","text"],"provider":{"order":["Google"],"data_collection":"deny"},` +
		`"plugins":[{"id":"web"}],"transforms":["middle-out"],"chave_desconhecida":"SEGREDO-XPTO"}`
	aos558Pedir(f, http.MethodPost, "/v1/chat/completions", corpo)

	forma := f.Forma()
	for _, chave := range []string{"model", "messages", "modalities", "provider", "plugins", "transforms"} {
		if forma.ChavesDoTopo[chave] != 1 {
			t.Errorf("chaves_do_topo[%s] = %d, quer 1 (todas: %v)", chave, forma.ChavesDoTopo[chave], forma.ChavesDoTopo)
		}
	}
	if forma.ChavesDoTopo["outra"] != 1 {
		t.Errorf("uma chave fora da lista fechada tem de contar como `outra`: %v", forma.ChavesDoTopo)
	}
	// Nem o nome da chave desconhecida nem o seu valor saem no que o falso registou.
	cru, err := json.Marshal(forma)
	if err != nil {
		t.Fatalf("a forma nao serializa: %v", err)
	}
	for _, proibido := range []string{"SEGREDO-XPTO", "chave_desconhecida", "Google", "middle-out", "ping"} {
		if bytes.Contains(cru, []byte(proibido)) {
			t.Errorf("a forma registada deixa sair %q: %s", proibido, cru)
		}
	}
}

func TestAOS558_Falso_RespondeComImagemNaFormaDaOpenRouter(t *testing.T) {
	f := aos558Falso()
	resp := aos558Pedir(f, http.MethodPost, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"um gato"}]}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("http = %d, quer 200", resp.Code)
	}
	var corpo struct {
		Choices []struct {
			Message struct {
				Images []struct {
					Tipo     string `json:"type"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &corpo); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	if len(corpo.Choices) != 1 || len(corpo.Choices[0].Message.Images) != 1 {
		t.Fatalf("quer uma imagem em choices[0].message.images, veio %s", resp.Body.String())
	}
	img := corpo.Choices[0].Message.Images[0]
	if img.Tipo != "image_url" || !strings.HasPrefix(img.ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("a imagem nao vem na forma da OpenRouter: tipo=%q url=%.40q", img.Tipo, img.ImageURL.URL)
	}
	cru, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(img.ImageURL.URL, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("a data URL nao descodifica: %v", err)
	}
	if !bytes.Equal(cru, PNGDoFalso()) {
		t.Fatalf("os bytes da imagem nao sao os de PNGDoFalso()")
	}
	if len(PNGDoFalso()) == 0 || !bytes.HasPrefix(PNGDoFalso(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("PNGDoFalso() nao e um PNG")
	}
}

// Sem ImagemDeSaida o falso responde como sempre: um cenário do estado opaco não muda.
func TestAOS558_Falso_SemAImagemDeSaidaNadaMuda(t *testing.T) {
	f := &FalsoDeEstado{FormaDoEstado: FormaOpenRouter, Turnos: 1}
	resp := aos558Pedir(f, http.MethodPost, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"ping"}]}`)
	if strings.Contains(resp.Body.String(), "images") {
		t.Fatalf("sem --imagem-de-saida a resposta nao leva `images`: %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), SentinelaDeTexto) {
		t.Fatalf("a resposta de sempre perdeu a sentinela de texto: %s", resp.Body.String())
	}
}

func TestAOS558_Falso_ServeAFamiliaDoVideoNaFormaDaOpenAI(t *testing.T) {
	f := aos558Falso()
	criar := aos558Pedir(f, http.MethodPost, "/v1/videos", `{"model":"m","prompt":"um gato a andar","seconds":"4"}`)
	if criar.Code != http.StatusOK {
		t.Fatalf("criacao: http = %d, quer 200", criar.Code)
	}
	var job struct {
		ID     string `json:"id"`
		Objeto string `json:"object"`
		Estado string `json:"status"`
	}
	if err := json.Unmarshal(criar.Body.Bytes(), &job); err != nil {
		t.Fatalf("o job nao e JSON: %v", err)
	}
	if job.ID != IDDoJobDeVideo || job.Objeto != "video" || job.Estado != "queued" {
		t.Fatalf("o job nao vem na forma da API de video da OpenAI: %s", criar.Body.String())
	}
	// As chaves do topo de um POST de vídeo contam-se como as do chat.
	if forma := f.Forma(); forma.ChavesDoTopo["prompt"] != 1 || forma.ChavesDoTopo["seconds"] != 1 {
		t.Errorf("as chaves do topo do pedido de video nao foram contadas: %v", forma.ChavesDoTopo)
	}
	estado := aos558Pedir(f, http.MethodGet, "/v1/videos/"+IDDoJobDeVideo, "")
	if !strings.Contains(estado.Body.String(), `"completed"`) {
		t.Fatalf("o estado do job devia vir `completed`: %s", estado.Body.String())
	}
	conteudo := aos558Pedir(f, http.MethodGet, "/v1/videos/"+IDDoJobDeVideo+"/content", "")
	if !bytes.Equal(conteudo.Body.Bytes(), conteudoDoVideoDoFalso) {
		t.Fatalf("o conteudo do video nao e o do falso")
	}
	// Um GET sem corpo não pode ser recusado como «pedido ilegível»: antes do AOS-558 era.
	if recusas := f.Forma().Recusas; len(recusas) > 0 {
		t.Fatalf("a familia do video nao devia produzir recusas: %v", recusas)
	}
}

func TestAOS558_Falso_ServeAGeracaoDeImagemNaFormaDaOpenAI(t *testing.T) {
	f := aos558Falso()
	resp := aos558Pedir(f, http.MethodPost, "/v1/images/generations", `{"model":"m","prompt":"um gato","n":1,"size":"1024x1024","response_format":"b64_json"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("http = %d, quer 200", resp.Code)
	}
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("quer um `data`, veio %s", resp.Body.String())
	}
	cru, err := base64.StdEncoding.DecodeString(out.Data[0].B64)
	if err != nil || !bytes.Equal(cru, PNGDoFalso()) {
		t.Fatalf("o b64_json nao e o PNG do falso (err=%v)", err)
	}
	forma := f.Forma()
	for _, chave := range []string{"prompt", "n", "size", "response_format"} {
		if forma.ChavesDoTopo[chave] != 1 {
			t.Errorf("chaves_do_topo[%s] = %d, quer 1 (todas: %v)", chave, forma.ChavesDoTopo[chave], forma.ChavesDoTopo)
		}
	}
}

// O sha256 do PNG do falso é estável dentro de uma execução: é o que o cenário com o proxy real
// compara com o que lhe volta.
func TestAOS558_PNGDoFalso_Estavel(t *testing.T) {
	a, b := sha256.Sum256(PNGDoFalso()), sha256.Sum256(PNGDoFalso())
	if hex.EncodeToString(a[:]) != hex.EncodeToString(b[:]) {
		t.Fatalf("PNGDoFalso() nao e determinista")
	}
}

// O falso PUBLICA o sha do PNG que emite. Sem isto, o cenário comparava a sua sentinela com a de
// um binário do falso que podia ser de outra árvore, e um falso negativo passava calado.
func TestAOS558_Falso_PublicaOShaDoPNGQueEmite(t *testing.T) {
	soma := sha256.Sum256(PNGDoFalso())
	if forma := aos558Falso().Forma(); forma.ShaDoPNG != hex.EncodeToString(soma[:]) {
		t.Fatalf("sha256_do_png_do_falso = %q, quer %q", forma.ShaDoPNG, hex.EncodeToString(soma[:]))
	}
	// A data URL que o falso emite tem de ser a desses mesmos bytes.
	cru, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURLDoPNGDoFalso(), "data:image/png;base64,"))
	if err != nil || !bytes.Equal(cru, PNGDoFalso()) {
		t.Fatalf("a data URL do falso nao e o PNGDoFalso() (err=%v)", err)
	}
}

// Um pedido recusado por CHAVE conta — senão era indistinguível de um pedido que o proxy nunca
// mandou, e quem mede leria «não encaminhou» de um pedido encaminhado.
func TestAOS558_Falso_ContaO401DeChaveRecusada(t *testing.T) {
	f := aos558Falso()
	f.ExigirChave(sha256.Sum256([]byte("a-chave-certa")))
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"m","prompt":"x"}`))
	req.Header.Set("Authorization", "Bearer a-chave-errada")
	resp := httptest.NewRecorder()
	f.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("http = %d, quer 401", resp.Code)
	}
	forma := f.Forma()
	if forma.Autenticacao[RecusaDeChave] != 1 {
		t.Fatalf("respostas_401_por_causa[%s] = %d, quer 1 (todas: %v)", RecusaDeChave, forma.Autenticacao[RecusaDeChave], forma.Autenticacao)
	}
	// O pedido recusado antes de ser lido não entra nos pedidos nem nos caminhos.
	if forma.Pedidos != 0 || len(forma.Caminhos) != 0 {
		t.Fatalf("um 401 nao conta como pedido lido: pedidos=%d caminhos=%v", forma.Pedidos, forma.Caminhos)
	}
}

// `--imagem-de-saida` não vale sem `--estado`: sem ele o handler servido é o do roteiro, que não
// a conhece, e um flag aceite e ignorado em silêncio dava um ensaio a medir o que não ligou.
// Fora do subcomando interno é recusado sempre, como o `--exige-id`.
func TestAOS558_CLI_ImagemDeSaidaExigeEstadoEOSubcomandoInterno(t *testing.T) {
	e := executar(t, Ambiente{}, "falso-provider", "--escuta", "127.0.0.1:0", "--imagem-de-saida")
	if e.codigo != SaidaUso || !strings.Contains(e.stderr, "--imagem-de-saida") {
		t.Errorf("falso-provider sem --estado: codigo %d (quer %d), stderr %q", e.codigo, SaidaUso, e.stderr)
	}
	e = executar(t, Ambiente{}, "falso", "--saida", t.TempDir(), "--imagem-de-saida")
	if e.codigo != SaidaUso || !strings.Contains(e.stderr, "--imagem-de-saida") {
		t.Errorf("fora do subcomando interno: codigo %d (quer %d), stderr %q", e.codigo, SaidaUso, e.stderr)
	}
}

// Um corpo que não é JSON num POST de vídeo não pode escrever no mapa dos CAMINHOS: quem mede
// decide por `len(caminhos) > 1`, e um segundo nome para o mesmo pedido estragava a medida.
func TestAOS558_Falso_CorpoNaoJsonNoVideoNaoEstragaOsCaminhos(t *testing.T) {
	f := aos558Falso()
	req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader("--limite\r\nnao-e-json\r\n--limite--"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=limite")
	resp := httptest.NewRecorder()
	f.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("http = %d, quer 200", resp.Code)
	}
	forma := f.Forma()
	if len(forma.Caminhos) != 1 || forma.Caminhos[CaminhoDeVideos] != 1 {
		t.Fatalf("caminhos = %v, quer so {%s: 1}", forma.Caminhos, CaminhoDeVideos)
	}
	if forma.ChavesDoTopo["corpo_nao_json"] != 1 {
		t.Fatalf("chaves_do_topo[corpo_nao_json] = %d, quer 1 (todas: %v)", forma.ChavesDoTopo["corpo_nao_json"], forma.ChavesDoTopo)
	}
}
