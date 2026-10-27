package port_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-565 — O FORNECEDOR FIXADO NO PEDIDO, E O FORNECEDOR A MONTANTE NA RESPOSTA.
//
// O que estes testes provam, por ordem de importância:
//
//  1. um pedido cujo perfil não fixa o fornecedor escreve EXACTAMENTE os bytes de hoje —
//     literal, e não «os mesmos de outra chamada»;
//  2. com declaração, a chave vai no FIM e com `allow_fallbacks: false`, que é o que faz da
//     ordem uma fixação e não uma preferência;
//  3. o mesmo nos DOIS caminhos de serialização — o `json.Marshal` de sempre e o corpo montado
//     à mão de um pedido com estado/imagem/vídeo, que é por onde as tools de modelo passam;
//  4. a sonda do fornecedor a montante nunca devolve um vazio ambíguo.

// aos565PNG é uma imagem mínima que passa a validação (assinatura PNG + bytes).
func aos565PNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("IHDR-aos565")...)
}

// aos565Pedido é um pedido mínimo e estável.
func aos565Pedido() port.ChatRequest {
	return port.ChatRequest{Model: "gpt-4o-mini", Messages: []port.Message{{Role: port.RoleUser, Content: "ola"}}}
}

// TestAOS565_WireSemDeclaracaoTemOsBytesDeHoje — A REGRESSÃO SOBRE OS BYTES. O literal é o que o
// wire produz HOJE, medido antes da alteração: é ele, e não uma comparação entre duas chamadas
// do código novo, que prova que um perfil sem declaração não mudou nada. Um campo novo sem
// `omitempty`, ou fora do fim da struct, falha aqui.
func TestAOS565_WireSemDeclaracaoTemOsBytesDeHoje(t *testing.T) {
	const deHoje = `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ola"}]}`
	b, err := aos565Pedido().MarshalWire(false)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != deHoje {
		t.Fatalf("wire = %s\n quero %s", b, deHoje)
	}
	// E o mesmo pelo caminho montado à mão: um pedido com imagem, sem fornecedor fixado, não
	// leva a chave.
	comImagem := aos565Pedido()
	comImagem.Messages[0].Images = []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos565PNG()}}
	b, err = comImagem.MarshalWire(false)
	if err != nil {
		t.Fatalf("marshal com imagem: %v", err)
	}
	if strings.Contains(string(b), `"provider"`) {
		t.Fatalf("um pedido sem declaracao nao leva a chave: %s", b)
	}
}

// TestAOS565_WireComDeclaracao — a chave vai, no fim, com `allow_fallbacks: false` e com a
// retenção quando declarada; e vai nos DOIS caminhos de serialização.
func TestAOS565_WireComDeclaracao(t *testing.T) {
	const quero = `"provider":{"order":["Google","Anthropic"],"allow_fallbacks":false,"data_collection":"deny"}`
	pol := &port.ProviderPolicy{Order: []string{"Google", "Anthropic"}, DataCollection: port.DataCollectionDeny}

	req := aos565Pedido()
	req.Provider = pol
	b, err := req.MarshalWire(false)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), quero) {
		t.Fatalf("wire = %s\n quero conter %s", b, quero)
	}
	// NO FIM: a chave é a última do corpo.
	if !strings.HasSuffix(string(b), quero+"}") {
		t.Fatalf("a chave tem de ser a ultima do corpo: %s", b)
	}

	// O CAMINHO MONTADO À MÃO (estado/imagem/vídeo) escreve a mesma chave. É a passagem por onde
	// as tools de modelo mandam media: esquecê-la ali deixaria o fornecedor por fixar
	// exactamente nessas chamadas.
	comImagem := aos565Pedido()
	comImagem.Provider = pol
	comImagem.Messages[0].Images = []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: aos565PNG()}}
	b, err = comImagem.MarshalWire(false)
	if err != nil {
		t.Fatalf("marshal com imagem: %v", err)
	}
	if !strings.Contains(string(b), quero) {
		t.Fatalf("o caminho montado a mao perdeu a chave: %s", b)
	}

	// Sem retenção declarada, a chave da retenção não vai — e `allow_fallbacks` vai na mesma.
	semRetencao := aos565Pedido()
	semRetencao.Provider = &port.ProviderPolicy{Order: []string{"Google"}}
	b, err = semRetencao.MarshalWire(false)
	if err != nil {
		t.Fatalf("marshal sem retencao: %v", err)
	}
	if !strings.Contains(string(b), `"provider":{"order":["Google"],"allow_fallbacks":false}`) {
		t.Fatalf("wire = %s", b)
	}
}

// TestAOS565_ValidacaoDaDeclaracao — o conjunto fechado, e o `allow_fallbacks` que NÃO se
// declara.
func TestAOS565_ValidacaoDaDeclaracao(t *testing.T) {
	for nome, c := range map[string]struct {
		pol *port.ProviderPolicy
		bom bool
	}{
		"ordem de um":            {&port.ProviderPolicy{Order: []string{"Google"}}, true},
		"ordem com retencao":     {&port.ProviderPolicy{Order: []string{"Together AI"}, DataCollection: port.DataCollectionAllow}, true},
		"nil":                    {nil, true},
		"ordem vazia":            {&port.ProviderPolicy{}, false},
		"ordem so com retencao":  {&port.ProviderPolicy{DataCollection: port.DataCollectionDeny}, false},
		"fornecedor repetido":    {&port.ProviderPolicy{Order: []string{"Google", "Google"}}, false},
		"nome com barra":         {&port.ProviderPolicy{Order: []string{"google/vertex"}}, false},
		"nome vazio":             {&port.ProviderPolicy{Order: []string{""}}, false},
		"nome com espaco a orla": {&port.ProviderPolicy{Order: []string{" Google"}}, false},
		"retencao desconhecida":  {&port.ProviderPolicy{Order: []string{"Google"}, DataCollection: "talvez"}, false},
		"ordem acima do tecto":   {&port.ProviderPolicy{Order: aos565Nomes(port.MaxProviderOrder + 1)}, false},
		"ordem no tecto":         {&port.ProviderPolicy{Order: aos565Nomes(port.MaxProviderOrder)}, true},
	} {
		if err := c.pol.Validate(); (err == nil) != c.bom {
			t.Errorf("%s: err = %v", nome, err)
		}
	}
	// A mensagem nunca repete o valor recusado.
	err := (&port.ProviderPolicy{Order: []string{"nome!invalido"}}).Validate()
	if err == nil || strings.Contains(err.Error(), "nome!invalido") {
		t.Fatalf("a mensagem nao repete o valor: %v", err)
	}
}

// aos565Nomes devolve n nomes de fornecedor distintos e válidos.
func aos565Nomes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "Fornecedor-" + string(rune('a'+i))
	}
	return out
}

// TestAOS565_Allows — a comparação com a ordem é EXACTA. Um nome noutra caixa não é o mesmo
// nome: apertar aqui é o que faz da ordem uma fixação.
func TestAOS565_Allows(t *testing.T) {
	pol := &port.ProviderPolicy{Order: []string{"Google", "Anthropic"}}
	for nome, quero := range map[string]bool{"Google": true, "Anthropic": true, "google": false, "GOOGLE": false, "OpenAI": false, "": false} {
		if pol.Allows(nome) != quero {
			t.Errorf("Allows(%q) = %v, quero %v", nome, !quero, quero)
		}
	}
	var nenhum *port.ProviderPolicy
	if nenhum.Allows("Google") || !nenhum.IsZero() {
		t.Fatal("uma declaracao nil nao permite ninguem e e zero")
	}
}

// TestAOS565_ProbeUpstreamProvider — a sonda sobre o corpo cru. NUNCA devolve um vazio ambíguo:
// o que ela não conseguir ler é `nao_reportado`, que é o caso esperado em produção hoje.
func TestAOS565_ProbeUpstreamProvider(t *testing.T) {
	for nome, c := range map[string]struct {
		corpo string
		quero string
	}{
		"string de topo":            {`{"id":"1","provider":"Google","choices":[]}`, "Google"},
		"sem a chave":               {`{"id":"1","choices":[]}`, port.UpstreamProviderUnreported},
		"corpo ilegivel":            {`nao e json`, port.UpstreamProviderUnreported},
		"corpo vazio":               {``, port.UpstreamProviderUnreported},
		"valor objecto":             {`{"provider":{"name":"Google"}}`, port.UpstreamProviderUnreported},
		"valor numero":              {`{"provider":7}`, port.UpstreamProviderUnreported},
		"valor lista":               {`{"provider":["Google"]}`, port.UpstreamProviderUnreported},
		"valor nulo":                {`{"provider":null}`, port.UpstreamProviderUnreported},
		"string vazia":              {`{"provider":""}`, port.UpstreamProviderUnreported},
		"so espacos":                {`{"provider":"   "}`, port.UpstreamProviderUnreported},
		"ultima ocorrencia ganha":   {`{"provider":"A","provider":"B"}`, "B"},
		"caracteres nao imprimidos": {`{"provider":"Goo\u0000gle"}`, "Google"},
	} {
		if got := port.ProbeUpstreamProvider([]byte(c.corpo)); got != c.quero {
			t.Errorf("%s: %q, quero %q", nome, got, c.quero)
		}
	}
	// O nome corta-se no tecto sem partir um carácter, e sai saneado.
	longo, _ := json.Marshal(map[string]string{"provider": strings.Repeat("ã", port.MaxUpstreamProvider)})
	if got := port.ProbeUpstreamProvider(longo); len(got) > port.MaxUpstreamProvider || !strings.HasPrefix(got, "ã") {
		t.Fatalf("nome longo = %q (%d bytes)", got, len(got))
	}
}

// TestAOS565_RespostaSemSondaNaoAfirmaNada — sem a sonda ligada o campo é VAZIO, que quer dizer
// «não se mediu», e é diferente de `nao_reportado`, que quer dizer «mediu-se e o proxy não o
// declarou». Colapsar os dois faria um nó sem sonda parecer um nó cujo proxy cala o fornecedor.
func TestAOS565_RespostaSemSondaNaoAfirmaNada(t *testing.T) {
	resp, err := port.UnmarshalChatResponse([]byte(`{"id":"1","model":"m","provider":"Google","choices":[{"index":0,"message":{"role":"assistant","content":"oi"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.UpstreamProvider != "" {
		t.Fatalf("o descodificador NAO le o fornecedor — isso e trabalho da sonda; veio %q", resp.UpstreamProvider)
	}
}
