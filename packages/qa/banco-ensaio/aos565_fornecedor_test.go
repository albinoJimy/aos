package bancoensaio

// AOS-565 — O FORNECEDOR EFECTIVO NO SENTIDO DE VOLTA: a parte do instrumento que corre SEM
// Docker.
//
// Aqui mede-se o INSTRUMENTO, não o proxy: que o falso emite as três sentinelas quando se lhe
// liga a bandeira, que NÃO as emite quando não se liga (um corpo igual ao de sempre, byte a
// byte), e que o classificador que lê o que chegou ao cliente separa as quatro saídas possíveis
// — chegou no topo, chegou no topo com outro valor, chegou noutro sítio, desapareceu.
//
// A medição contra o proxy fixado está em `aos565_proxyreal_test.go` e precisa de Docker.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// As saídas possíveis de [aos565Classificar]. Vocabulário fechado.
const (
	// aos565NoTopoIntacta — a chave está no topo da resposta, com a sentinela que o falso emitiu.
	aos565NoTopoIntacta = "intacta_no_topo"
	// aos565NoTopoOutroValor — a chave está no topo, e o valor não é a sentinela: alguém a
	// reescreveu pelo caminho.
	aos565NoTopoOutroValor = "no_topo_com_outro_valor"
	// aos565NoutroCaminho — a chave não está no topo, mas a sentinela aparece noutro sítio do
	// corpo: chegou com outro nome, ou aninhada. Os caminhos vão na medida.
	aos565NoutroCaminho = "noutro_caminho"
	// aos565Removida — a sentinela não aparece em lado nenhum do corpo.
	aos565Removida = "removida"
	// aos565CorpoNaoJSON — o corpo da resposta não é um objecto JSON; não há topo que classificar.
	aos565CorpoNaoJSON = "corpo_nao_e_objecto_json"
	// aos565NaoMedida — o pedido NÃO chegou ao fornecedor, logo o falso nunca emitiu sentinela
	// nenhuma. Dizer «removida» de uma sentinela que ninguém emitiu seria uma medida falsa — é a
	// mesma classe de defeito que a revisão do AOS-558 corrigiu no instrumento dele (§7.1).
	aos565NaoMedida = "nao_medida_o_pedido_nao_chegou_ao_fornecedor"
)

// aos565Chegada é o que aconteceu a UMA sentinela entre o fornecedor e o cliente.
type aos565Chegada struct {
	Classe string `json:"classe"`
	// Caminhos são os sítios do corpo onde a sentinela apareceu (`provider`,
	// `choices[0].message.provider`, …). Vazio quando não apareceu.
	Caminhos []string `json:"caminhos,omitempty"`
}

// aos565Classificar diz o que aconteceu à sentinela que o falso pôs na chave de topo `chave`.
// Devolve a classe e TODOS os caminhos do corpo onde a sentinela aparece — é por eles que se vê
// «chegou com outro nome».
func aos565Classificar(corpo []byte, chave, sentinela string) aos565Chegada {
	var topo map[string]any
	if json.Unmarshal(corpo, &topo) != nil {
		return aos565Chegada{Classe: aos565CorpoNaoJSON}
	}
	var caminhos []string
	aos565Procurar(topo, "", sentinela, &caminhos)
	sort.Strings(caminhos)
	valor, tem := topo[chave]
	texto, eTexto := valor.(string)
	switch {
	case tem && eTexto && texto == sentinela:
		return aos565Chegada{Classe: aos565NoTopoIntacta, Caminhos: caminhos}
	case tem:
		return aos565Chegada{Classe: aos565NoTopoOutroValor, Caminhos: caminhos}
	case len(caminhos) > 0:
		return aos565Chegada{Classe: aos565NoutroCaminho, Caminhos: caminhos}
	}
	return aos565Chegada{Classe: aos565Removida}
}

// aos565Procurar junta a `out` o caminho de cada texto do documento que contenha a sentinela.
// Desce por objectos e por listas: uma chave de topo que o proxy mova para dentro de outra
// aparece aqui, e sem isto «removida» e «mudou de sítio» mediam o mesmo.
func aos565Procurar(v any, prefixo, sentinela string, out *[]string) {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, sentinela) {
			*out = append(*out, prefixo)
		}
	case map[string]any:
		for k, vv := range t {
			caminho := k
			if prefixo != "" {
				caminho = prefixo + "." + k
			}
			aos565Procurar(vv, caminho, sentinela, out)
		}
	case []any:
		for i, vv := range t {
			aos565Procurar(vv, prefixo+"["+strconv.Itoa(i)+"]", sentinela, out)
		}
	}
}

// aos565Falso devolve um falso do estado na forma da OpenRouter, com a bandeira dada.
func aos565Falso(comFornecedor bool) *FalsoDeEstado {
	return &FalsoDeEstado{FormaDoEstado: FormaOpenRouter, Turnos: 1, FornecedorNaResposta: comFornecedor}
}

const aos565PedidoSemTools = `{"model":"m","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`

func TestAOS565_Falso_EmiteAsTresSentinelas(t *testing.T) {
	f := aos565Falso(true)
	resp := aos558Pedir(f, http.MethodPost, "/v1/chat/completions", aos565PedidoSemTools)
	if resp.Code != http.StatusOK {
		t.Fatalf("o falso respondeu %d", resp.Code)
	}
	corpo := resp.Body.Bytes()
	if got := aos565Classificar(corpo, ChaveDoFornecedorNaResposta, SentinelaDoFornecedor); got.Classe != aos565NoTopoIntacta {
		t.Errorf("provider: classe = %q (caminhos %v), quer %q", got.Classe, got.Caminhos, aos565NoTopoIntacta)
	}
	if got := aos565Classificar(corpo, ChaveDaSondaDeTopo, SentinelaDaSonda); got.Classe != aos565NoTopoIntacta {
		t.Errorf("aos_sonda: classe = %q (caminhos %v), quer %q", got.Classe, got.Caminhos, aos565NoTopoIntacta)
	}
	if got := resp.Header().Get(CabecalhoDaSondaDoFornecedor); got != SentinelaDoCabecalho {
		t.Errorf("cabecalho %s = %q, quer %q", CabecalhoDaSondaDoFornecedor, got, SentinelaDoCabecalho)
	}
}

// TestAOS565_Falso_SemBandeiraOCorpoEODeSempre é a regressão: sem a bandeira, o corpo é o de
// hoje BYTE A BYTE e não há cabeçalho novo. Sem isto, a bandeira podia mudar o que os cenários
// do estado opaco medem e ninguém dava por isso.
func TestAOS565_Falso_SemBandeiraOCorpoEODeSempre(t *testing.T) {
	semBandeira := aos558Pedir(aos565Falso(false), http.MethodPost, "/v1/chat/completions", aos565PedidoSemTools)
	if semBandeira.Header().Get(CabecalhoDaSondaDoFornecedor) != "" {
		t.Errorf("sem a bandeira o falso nao pode declarar o cabecalho da sonda")
	}
	for _, sentinela := range []string{SentinelaDoFornecedor, SentinelaDaSonda, SentinelaDoCabecalho} {
		if strings.Contains(semBandeira.Body.String(), sentinela) {
			t.Errorf("sem a bandeira o corpo nao pode levar a sentinela %q", sentinela)
		}
	}
	// O corpo com a bandeira é o mesmo documento MAIS as duas chaves, e nada além delas.
	comBandeira := aos558Pedir(aos565Falso(true), http.MethodPost, "/v1/chat/completions", aos565PedidoSemTools)
	var sem, com map[string]json.RawMessage
	if err := json.Unmarshal(semBandeira.Body.Bytes(), &sem); err != nil {
		t.Fatalf("o corpo sem a bandeira nao e JSON: %v", err)
	}
	if err := json.Unmarshal(comBandeira.Body.Bytes(), &com); err != nil {
		t.Fatalf("o corpo com a bandeira nao e JSON: %v", err)
	}
	delete(com, ChaveDoFornecedorNaResposta)
	delete(com, ChaveDaSondaDeTopo)
	if len(com) != len(sem) {
		t.Fatalf("a bandeira mudou mais do que as duas chaves: %d chaves contra %d", len(com), len(sem))
	}
	for k, v := range sem {
		if string(com[k]) != string(v) {
			t.Errorf("a chave %q mudou com a bandeira", k)
		}
	}
}

// TestAOS565_Classificar separa as saídas possíveis. É o instrumento a ser medido: um
// classificador que confundisse «removida» com «mudou de nome» dava um veredicto errado com
// uma corrida perfeitamente boa.
func TestAOS565_Classificar(t *testing.T) {
	const s = SentinelaDoFornecedor
	casos := []struct {
		nome, corpo, classe string
		caminhos            []string
	}{
		{"no topo", `{"provider":"` + s + `","id":"gen"}`, aos565NoTopoIntacta, []string{"provider"}},
		{"no topo com outro valor", `{"provider":"Google","id":"gen"}`, aos565NoTopoOutroValor, nil},
		{"com outro nome", `{"served_by":"` + s + `"}`, aos565NoutroCaminho, []string{"served_by"}},
		{"aninhada numa lista", `{"choices":[{"message":{"provider":"` + s + `"}}]}`, aos565NoutroCaminho, []string{"choices[0].message.provider"}},
		{"removida", `{"id":"gen","choices":[]}`, aos565Removida, nil},
		{"corpo que nao e objecto", `[1,2,3]`, aos565CorpoNaoJSON, nil},
	}
	for _, c := range casos {
		got := aos565Classificar([]byte(c.corpo), ChaveDoFornecedorNaResposta, s)
		if got.Classe != c.classe {
			t.Errorf("%s: classe = %q, quer %q", c.nome, got.Classe, c.classe)
		}
		if strings.Join(got.Caminhos, ",") != strings.Join(c.caminhos, ",") {
			t.Errorf("%s: caminhos = %v, quer %v", c.nome, got.Caminhos, c.caminhos)
		}
	}
}

// TestAOS565_CLI_BandeiraSoNaFormaOpenRouter: as sentinelas saem só na forma da OpenRouter, pelo
// que a bandeira noutra forma é recusada em vez de aceite e ignorada.
func TestAOS565_CLI_BandeiraSoNaFormaOpenRouter(t *testing.T) {
	casos := []struct {
		nome string
		args []string
		quer int
	}{
		{"sem estado", []string{"falso-provider", "--escuta", "127.0.0.1:0", "--fornecedor-na-resposta"}, SaidaUso},
		{"sem a forma da openrouter", []string{"falso-provider", "--escuta", "127.0.0.1:0", "--estado", EstadoExige, "--fornecedor-na-resposta"}, SaidaUso},
		{"num modo que nao e o falso-provider", []string{"--fornecedor-na-resposta"}, SaidaUso},
	}
	for _, c := range casos {
		var saida strings.Builder
		if got := Executar(context.Background(), c.args, &saida, &saida, Ambiente{}); got != c.quer {
			t.Errorf("%s: saida = %d, quer %d (%s)", c.nome, got, c.quer, saida.String())
		}
	}
}
