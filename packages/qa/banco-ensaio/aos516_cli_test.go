package bancoensaio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — A QUALIFICAÇÃO DA DEVOLUÇÃO PELA LINHA DE COMANDOS, o perfil de exemplo do Claude e
// o provider falso do estado no wire de mensagens da Anthropic.

const (
	aos516PerfilObrigatorio = `{"requested":"rota-de-ensaio","expected_model":"anthropic/modelo-de-ensaio","wire_class":"openai-chat-completions","capabilities":["tools"],` +
		`"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000},"projection_version":"1.3.0","devolver":"obrigatorio"}`
	aos516PerfilNunca = `{"requested":"rota-de-ensaio","expected_model":"anthropic/modelo-de-ensaio","wire_class":"openai-chat-completions","capabilities":["tools"],` +
		`"params":{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":16000},"projection_version":"1.2.0","devolver":"nunca"}`
)

func aos516Ficheiro(t *testing.T, doc string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "perfil.json")
	if err := os.WriteFile(caminho, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// aos516Relatorio lê o único relatório em JSON da pasta.
func aos516Relatorio(t *testing.T, pasta string) *Relatorio {
	t.Helper()
	achados, _ := filepath.Glob(filepath.Join(pasta, "*.json"))
	if len(achados) != 1 {
		t.Fatalf("relatorios em %s: %v", pasta, achados)
	}
	cru, err := os.ReadFile(achados[0])
	if err != nil {
		t.Fatal(err)
	}
	var r Relatorio
	if err := json.Unmarshal(cru, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// O PERFIL DE EXEMPLO DO CLAUDE, versionado junto do banco, passa pela leitura fechada do
// gateway e é o perfil da rota de ensaio. Não tem chaves; o nome do modelo é um marcador.
func TestAOS516_PerfilDeExemploDoClaude(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("perfis", "claude-devolucao.exemplo.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := modelgateway.ParseRouteProfile(doc)
	if err != nil {
		t.Fatalf("o perfil de exemplo nao passa na leitura fechada do gateway: %v", err)
	}
	if _, err := LerPerfilCandidato(doc); err != nil {
		t.Fatalf("o perfil de exemplo nao e o da rota de ensaio: %v", err)
	}
	if p.StateReturn != modelgateway.StateReturnRequired || p.ProjectionVersion != modelgateway.NativeProjectionVersion130 ||
		p.Params == nil || p.Params.Thinking == nil || p.Params.Thinking.Type != "enabled" || p.Params.MaxTokens == 0 {
		t.Errorf("o perfil de exemplo tem de devolver estado (obrigatorio, 1.3.0) com o raciocinio ligado: %+v", p)
	}
	if p.ExpectedModel != "anthropic/PREENCHER-NOME-DO-MODELO" {
		t.Errorf("o nome do modelo do exemplo tem de ser o marcador a preencher; veio %q", p.ExpectedModel)
	}
	if composicaoDoEstado(&p, "") == nil {
		t.Errorf("o perfil de exemplo nao liga a devolucao no banco")
	}
}

// A LINHA DE COMANDOS, MODO FALSO: a bateria inteira com o provider falso do estado.
func TestAOS516_LinhaDeComandos_Falso(t *testing.T) {
	obrigatorio, nunca := aos516Ficheiro(t, aos516PerfilObrigatorio), aos516Ficheiro(t, aos516PerfilNunca)
	proibidos := append(append(proibidosDeTexto(t), SentinelasDoEstado()...), "resposta final")

	saida := t.TempDir()
	e := executar(t, Ambiente{}, "falso", "--estado", "exige", "--turnos-do-falso", "3", "--perfil", obrigatorio, "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r := aos516Relatorio(t, saida)
	// Os 5 nos com tools da bateria fazem 3 turnos com tool call cada: 15 turnos com estado, e
	// os 15 pedidos seguintes levam-no. Nenhum 4xx; nenhuma recusa.
	d := r.Taxas.Devolucao
	if d == nil || d.TurnosComEstadoCapturado != 15 || d.PedidosComTurnosAnteriores != 15 || d.Devolvidos != 15 || d.Recusas != 0 || d.HTTP4xxComEstado != 0 {
		t.Fatalf("devolucao da bateria = %+v", d)
	}
	aos516Contagens(t, "http", r.Taxas.HTTP, map[string]int{"200": 22})
	if r.FormaNoFornecedor == nil || r.FormaNoFornecedor.Turnos != 30 || len(r.FormaNoFornecedor.Recusas) != 0 {
		t.Errorf("forma no fornecedor = %+v", r.FormaNoFornecedor)
	}
	// Por caso: o relatorio reparte as contagens.
	porCaso := map[string]int{}
	for _, c := range r.PorCaso {
		if c.Taxas.Devolucao != nil {
			porCaso[c.Caso] = c.Taxas.Devolucao.Devolvidos
		}
	}
	aos516Contagens(t, "devolvidos por caso", porCaso, map[string]int{"T1": 3, "T2": 3, "T3": 0, "T4": 3, "T5": 3, "T6": 3})
	verSemFugas(t, "falso com devolucao", tudoOQueFoiEscrito(t, e, saida), proibidos)
	if !strings.Contains(e.stdout, "DEVOLUCAO DO ESTADO OPACO") || !strings.Contains(e.stdout, "FORMA NO PROVIDER FALSO") {
		t.Errorf("o resumo em texto nao mostra a devolucao nem a forma")
	}

	// Perfil `nunca` contra o exigente: o provider da 400 ao segundo pedido de cada no com tools.
	saida = t.TempDir()
	if e = executar(t, Ambiente{}, "falso", "--estado", "exige", "--perfil", nunca, "--saida", saida); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r = aos516Relatorio(t, saida)
	if r.Taxas.Devolucao != nil || r.Taxas.HTTP["400"] != 5 || r.Taxas.Desfechos[DesfechoErroHTTP] != 5 || r.FormaNoFornecedor.Recusas[RecusaEstadoEmFalta] != 5 {
		t.Errorf("perfil nunca contra o exigente: http=%v desfechos=%v forma=%+v", r.Taxas.HTTP, r.Taxas.Desfechos, r.FormaNoFornecedor)
	}
	verSemFugas(t, "falso sem devolucao", tudoOQueFoiEscrito(t, e, saida), proibidos)

	// Perfil `obrigatorio` contra o provider que proibe estado: 400 em pedidos que o levaram.
	saida = t.TempDir()
	if e = executar(t, Ambiente{}, "falso", "--estado", "proibe", "--perfil", obrigatorio, "--saida", saida); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r = aos516Relatorio(t, saida)
	if d := r.Taxas.Devolucao; d == nil || d.HTTP4xxComEstado != 5 || d.Devolvidos != 5 || r.FormaNoFornecedor.Recusas[RecusaEstadoPresente] != 5 {
		t.Errorf("perfil obrigatorio contra o que proibe: devolucao=%+v forma=%+v", d, r.FormaNoFornecedor)
	}
	verSemFugas(t, "falso que proibe", tudoOQueFoiEscrito(t, e, saida), proibidos)

	// As recusas de uso: nenhuma escreve relatorio.
	for nome, args := range map[string][]string{
		"host esperado sem perfil que devolva": {"falso", "--host-esperado", "api.exemplo.test", "--perfil", nunca},
		"host esperado com esquema":            {"falso", "--host-esperado", "https://api.exemplo.test", "--perfil", obrigatorio},
		"estado fora do vocabulario":           {"falso", "--estado", "talvez"},
		"estado e roteiro":                     {"falso", "--estado", "exige", "--roteiro", "cumpre"},
		"turnos do falso sem estado":           {"falso", "--turnos-do-falso", "3"},
		"exige-id fora do subcomando interno":  {"falso", "--estado", "exige", "--exige-id"},
		"proxy com estado e sem perfil":        {"proxy", "--binario-do-falso", "nao-interessa", "--estado", "exige"},
		"proxy com estado e rota openai":       {"proxy", "--binario-do-falso", "nao-interessa", "--estado", "exige", "--perfil", aos516Ficheiro(t, strings.Replace(aos516PerfilObrigatorio, "anthropic/", "openai/", 1))},
	} {
		pasta := t.TempDir()
		lanc := &lancadorDeTeste{t: t}
		e := executar(t, Ambiente{Lancador: lanc}, append(args, "--saida", pasta)...)
		achados, _ := filepath.Glob(filepath.Join(pasta, "*"))
		if e.codigo != SaidaUso || len(achados) != 0 || lanc.lancamentos != 0 {
			t.Errorf("%s: tinha de recusar sem lancar nem escrever; codigo %d, ficheiros %v, lancamentos %d\n%s", nome, e.codigo, achados, lanc.lancamentos, e.stderr)
		}
	}
}

// O MODO PROXY COM O FALSO DO ESTADO pede ao lançador a rota que o perfil espera, e o contentor
// do falso arranca com o modo do estado e a porta publicada só em 127.0.0.1.
func TestAOS516_Proxy_ORotaEADoPerfil(t *testing.T) {
	perfil := aos516Ficheiro(t, strings.Replace(aos516PerfilObrigatorio, "modelo-de-ensaio", "claude-de-ensaio", 1))
	lanc := &lancadorDeTeste{t: t, devolver: &FalsoDeEstado{Turnos: 2, ServidoComo: "anthropic/claude-de-ensaio"}}
	saida := t.TempDir()
	e := executar(t, Ambiente{Lancador: lanc}, "proxy", "--binario-do-falso", "nao-interessa", "--estado", "exige", "--turnos-do-falso", "2", "--perfil", perfil, "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	p := lanc.pedido
	if p.Prefixo != "anthropic" || p.Modelo != "claude-de-ensaio" || p.Estado != EstadoExige || p.TurnosDoFalso != 2 || p.Roteiro != nil {
		t.Errorf("pedido de proxy = %+v", struct {
			Prefixo, Modelo, Estado string
			Turnos                  int
		}{p.Prefixo, p.Modelo, p.Estado, p.TurnosDoFalso})
	}
	if r := aos516Relatorio(t, saida); r.Rota.Modelo != "anthropic/claude-de-ensaio" || r.Taxas.Devolucao == nil || r.Taxas.Devolucao.Devolvidos != 10 {
		t.Errorf("relatorio do modo proxy: rota %+v, devolucao %+v", r.Rota, r.Taxas.Devolucao)
	}

	args := strings.Join(argumentosDoContentorDoFalso("c", "r", PedidoDeProxy{Estado: EstadoProibe, TurnosDoFalso: 4, Roteiro: RoteiroPorOmissao()}, "abc", time.Hour), " ")
	for _, quer := range []string{"-p 127.0.0.1::8080", "--estado proibe --turnos-do-falso 4", "--chave-sha256 abc"} {
		if !strings.Contains(args, quer) {
			t.Errorf("argumentos do contentor do falso do estado sem %q: %s", quer, args)
		}
	}
	if strings.Contains(args, "--roteiro") {
		t.Errorf("o falso do estado nao tem roteiro: %s", args)
	}
	deSempre := strings.Join(argumentosDoContentorDoFalso("c", "r", PedidoDeProxy{Roteiro: []Comportamento{ComportamentoCumpre}}, "abc", time.Hour), " ")
	if strings.Contains(deSempre, "-p ") || strings.Contains(deSempre, "--estado") || !strings.Contains(deSempre, "--roteiro cumpre --chave-sha256 abc --vida 1h0m0s") {
		t.Errorf("os argumentos do falso do roteiro mudaram: %s", deSempre)
	}
	if baseDoFalso("f", "anthropic") != "http://f:8080" || baseDoFalso("f", "openai") != "http://f:8080/v1" {
		t.Errorf("base do falso: %s / %s", baseDoFalso("f", "anthropic"), baseDoFalso("f", "openai"))
	}
}

// O MODO REAL COM UM PERFIL QUE DEVOLVE ESTADO: o perfil tem de esperar a rota da corrida, senão
// recusa antes de lançar o proxy e de enviar seja o que for. Com o perfil certo, a corrida
// compõe a devolução — aqui contra um lançador de teste, sem Docker e sem fornecedor nenhum.
func TestAOS516_Real_OPerfilTemDeEsperarARotaDaCorrida(t *testing.T) {
	chaves := escreverChaves(t, chavesDeTeste())
	pasta := filepath.Dir(chaves)
	precos := filepath.Join(pasta, "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloAnthropicDeTeste+`":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos,
		"--perfil", aos516Ficheiro(t, aos516PerfilObrigatorio), "--saida", filepath.Join(pasta, "r0"))
	if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "expected_model") {
		t.Fatalf("perfil de outra rota: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}
	// --estado nao e do modo real.
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos, "--estado", "exige"); e.codigo != SaidaUso || lanc.lancamentos != 0 {
		t.Fatalf("--estado no modo real: codigo %d, lancamentos %d", e.codigo, lanc.lancamentos)
	}

	certo := aos516Ficheiro(t, strings.Replace(aos516PerfilObrigatorio, "modelo-de-ensaio", modeloAnthropicDeTeste, 1))
	lanc = &lancadorDeTeste{t: t, devolver: &FalsoDeEstado{Turnos: 2, ServidoComo: "anthropic/" + modeloAnthropicDeTeste}}
	saida := filepath.Join(pasta, "r1")
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "anthropic", "--precos", precos, "--perfil", certo, "--saida", saida)
	if e.codigo != SaidaOK || lanc.pedido.Prefixo != "anthropic" || lanc.pedido.Estado != "" {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r := aos516Relatorio(t, saida)
	if d := r.Taxas.Devolucao; d == nil || d.Devolvidos != 10 || d.PedidosComTurnosAnteriores != 10 || d.Recusas != 0 || d.HTTP4xxComEstado != 0 {
		t.Errorf("devolucao no modo real (lancador de teste) = %+v", d)
	}
	// Um fornecedor real nao mostra a forma do que recebeu: o relatorio do modo real nao a tem.
	if r.FormaNoFornecedor != nil || r.Digests.Perfil == "" || r.Protocolo.Estado == nil {
		t.Errorf("relatorio do modo real: forma=%v perfil=%q estado=%v", r.FormaNoFornecedor, r.Digests.Perfil, r.Protocolo.Estado)
	}
	verSemFugas(t, "real com devolucao", tudoOQueFoiEscrito(t, e, saida), append(SentinelasDoEstado(), sentinelaChaveAnthropic, "resposta final"))
}

// O PROVIDER FALSO DO ESTADO NO WIRE DE MENSAGENS: o que aceita, o que recusa e o que regista.
// Os pedidos abaixo têm a forma em que a imagem fixada do proxy os entrega ao fornecedor.
func TestAOS516_FalsoDeEstado_WireDeMensagens(t *testing.T) {
	const cabeca = `{"model":"m","max_tokens":16000,"thinking":{"type":"enabled","budget_tokens":2048},` +
		`"tools":[{"name":"arquivo","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[{"role":"user","content":"Read notas-armazem.txt"}`
	pensa := `{"type":"thinking","thinking":"` + pensamentoDoTurno(0) + `","signature":"` + assinaturaDoTurno(0) + `"}`
	redigido := `{"type":"redacted_thinking","data":"` + redigidoDoTurno(0) + `"}`
	const chamada = `{"type":"tool_use","id":"step-000001-tool-0","name":"arquivo","input":{"path":"notas-armazem.txt"}}`
	const resultado = `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"step-000001-tool-0","content":"x"}]}]}`
	turno := func(blocos ...string) string {
		return cabeca + `,{"role":"assistant","content":[` + strings.Join(blocos, ",") + `]}` + resultado
	}
	pedir := func(f *FalsoDeEstado, corpo string) (int, map[string]any) {
		srv := httptest.NewServer(f)
		defer srv.Close()
		resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(corpo))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// O primeiro pedido: o falso emite raciocinio assinado, um bloco redigido e a tool call.
	exige := &FalsoDeEstado{Turnos: 1}
	codigo, resp := pedir(exige, cabeca+`]}`)
	if codigo != http.StatusOK || resp["stop_reason"] != "tool_use" || len(resp["content"].([]any)) != 3 {
		t.Fatalf("primeiro pedido: %d %v", codigo, resp)
	}
	for nome, c := range map[string]struct {
		corpo  string
		proibe bool
		codigo int
		forma  string
		causa  string
	}{
		"estado de volta, intacto":                    {turno(pensa, redigido, chamada), false, 200, "thinking,redacted_thinking,tool_use", ""},
		"com o bloco de texto que o proxy acrescenta": {turno(pensa, redigido, `{"type":"text","text":"[System: aviso]"}`, chamada), false, 200, "thinking,redacted_thinking,text(aviso_do_proxy),tool_use", ""},
		"com um bloco de texto vazio":                 {turno(pensa, redigido, `{"type":"text","text":""}`, chamada), false, 200, "thinking,redacted_thinking,text(vazio),tool_use", ""},
		"sem o estado":                                {turno(chamada), false, 400, "tool_use", RecusaEstadoEmFalta},
		"sem o bloco redigido":                        {turno(pensa, chamada), false, 400, "thinking,tool_use", RecusaEstadoEmFalta},
		"com um byte da assinatura alterado":          {turno(strings.Replace(pensa, "U0lH-0", "U0lH-1", 1), redigido, chamada), false, 400, "thinking,redacted_thinking,tool_use", RecusaEstadoAlterado},
		"o que proibe, com estado":                    {turno(pensa, redigido, chamada), true, 400, "thinking,redacted_thinking,tool_use", RecusaEstadoPresente},
		"o que proibe, sem estado":                    {turno(chamada), true, 200, "tool_use", ""},
	} {
		f := &FalsoDeEstado{Turnos: 1, Proibe: c.proibe}
		codigo, _ := pedir(f, c.corpo)
		forma := f.Forma()
		if codigo != c.codigo || forma.Wire != WireDeMensagens || forma.Assistant[c.forma] != 1 || forma.Turnos != 1 {
			t.Errorf("%s: codigo %d (quer %d), forma %+v (quer %q)", nome, codigo, c.codigo, forma.Assistant, c.forma)
		}
		if (c.causa == "") != (len(forma.Recusas) == 0) || (c.causa != "" && forma.Recusas[c.causa] != 1) {
			t.Errorf("%s: recusas %v, quer %q", nome, forma.Recusas, c.causa)
		}
		if forma.Parametros["thinking"] != 1 || forma.Parametros["max_tokens"] != 1 {
			t.Errorf("%s: parametros %v", nome, forma.Parametros)
		}
		// A forma nao leva valores: nem o texto do aviso, nem o raciocinio, nem a assinatura.
		cru := string(mustJSON(t, forma))
		for _, s := range append(SentinelasDoEstado(), "aviso]", "notas-armazem") {
			if strings.Contains(cru, s) {
				t.Errorf("%s: a forma leva conteudo: %q", nome, s)
			}
		}
	}
}
