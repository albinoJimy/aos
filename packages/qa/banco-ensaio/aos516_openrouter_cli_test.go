package bancoensaio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// AOS-516 — O FORNECEDOR OPENROUTER NO MODO REAL (contra um lançador de teste: sem Docker e sem
// fornecedor nenhum), a forma da OpenRouter pela linha de comandos e os erros dela.

const (
	sentinelaChaveOpenRouter = "SENTINELA-CHAVE-OPENROUTER-5c1d77e2ab90"
	modeloOpenRouterDeTeste  = "autor-de-teste/modelo-openrouter-de-teste"
)

// chavesComOpenRouter devolve os campos de teste com os da OpenRouter.
func chavesComOpenRouter() map[string]string {
	c := chavesDeTeste()
	c[CampoChaveOpenRouter], c[CampoOpenRouterModelo] = sentinelaChaveOpenRouter, modeloOpenRouterDeTeste
	c[CampoTectoPedidosOpenR], c[CampoTectoUSDOpenRouter] = "200", "3"
	return c
}

// AS GUARDAS DO FICHEIRO DE CHAVES valem para a OpenRouter como para os outros: campo em falta
// nomeado, marcador do exemplo recusado, aspas recusadas, e o valor nunca na mensagem.
func TestAOS516_OpenRouter_GuardasDoFicheiroDeChaves(t *testing.T) {
	ler := func(campos map[string]string) (RotaReal, error) {
		c, err := LerChaves(escreverChaves(t, campos))
		if err != nil {
			t.Fatal(err)
		}
		return c.Rota(FornecedorOpenRouter, "", Destino{})
	}
	r, err := ler(chavesComOpenRouter())
	if err != nil {
		t.Fatal(err)
	}
	if r.Modelo != modeloOpenRouterDeTeste || r.Destino != "https://openrouter.ai" || r.Tectos.PedidosDia != 200 || !r.Tectos.TemUSD || r.Tectos.MicroUSDDia != 3_000_000 {
		t.Errorf("rota = %+v, tectos %+v", r, r.Tectos)
	}
	if r.apiKey != sentinelaChaveOpenRouter || r.apiBase != "https://openrouter.ai/api/v1" {
		t.Error("a rota nao leva a chave do ficheiro ou a base fixa da OpenRouter")
	}
	if s := fmt.Sprintf("%v %+v %#v", r, r, r); strings.Contains(s, sentinelaChaveOpenRouter) {
		t.Errorf("a rota formatada mostra a chave: %s", s)
	}
	com := func(campo, valor string) map[string]string {
		c := chavesComOpenRouter()
		if valor == "" {
			delete(c, campo)
		} else {
			c[campo] = valor
		}
		return c
	}
	for nome, c := range map[string]struct {
		campos   map[string]string
		campo    string
		problema string
	}{
		"chave em falta":                   {com(CampoChaveOpenRouter, ""), CampoChaveOpenRouter, problemaEmFalta},
		"chave com o marcador do exemplo":  {com(CampoChaveOpenRouter, "<a-chave-da-openrouter>"), CampoChaveOpenRouter, problemaMarcadorDoExemplo},
		"chave entre aspas":                {com(CampoChaveOpenRouter, `"`+sentinelaChaveOpenRouter+`"`), CampoChaveOpenRouter, problemaAspas},
		"modelo em falta":                  {com(CampoOpenRouterModelo, ""), CampoOpenRouterModelo, problemaEmFalta},
		"modelo com o marcador do exemplo": {com(CampoOpenRouterModelo, "<autor/modelo>"), CampoOpenRouterModelo, problemaMarcadorDoExemplo},
		"modelo sem autor":                 {com(CampoOpenRouterModelo, "modelo-sem-autor"), CampoOpenRouterModelo, problemaSemAutor},
		"modelo com o prefixo do proxy":    {com(CampoOpenRouterModelo, "openrouter/autor-de-teste"), CampoOpenRouterModelo, problemaSemAutor},
		"modelo com tres partes":           {com(CampoOpenRouterModelo, "openrouter/autor/modelo"), CampoOpenRouterModelo, problemaSemAutor},
		"modelo com espacos":               {com(CampoOpenRouterModelo, "autor/um modelo"), CampoOpenRouterModelo, problemaCaracteres},
		"tecto de pedidos em falta":        {com(CampoTectoPedidosOpenR, ""), CampoTectoPedidosOpenR, problemaEmFalta},
		"tecto de pedidos a zero":          {com(CampoTectoPedidosOpenR, "0"), CampoTectoPedidosOpenR, problemaZero},
		"tecto em dolares ilegivel":        {com(CampoTectoUSDOpenRouter, "tres"), CampoTectoUSDOpenRouter, problemaIlegivel},
	} {
		_, err := ler(c.campos)
		var doCampo *ErrCampoDasChaves
		if !errors.As(err, &doCampo) || doCampo.Campo != c.campo || doCampo.Problema != c.problema {
			t.Errorf("%s: erro = %v, quer o campo %s %s", nome, err, c.campo, c.problema)
		}
		if err != nil && strings.Contains(err.Error(), sentinelaChaveOpenRouter) {
			t.Errorf("%s: o erro leva a chave", nome)
		}
	}
	// O destino e fixo: nao ha opcao que o mude.
	c, _ := LerChaves(escreverChaves(t, chavesComOpenRouter()))
	if _, err := c.Rota(FornecedorOpenRouter, "", Destino{ForaDaLista: "outro.exemplo.test"}); err == nil || !strings.Contains(err.Error(), "destino e fixo") {
		t.Errorf("--destino-fora-da-lista com a OpenRouter: %v", err)
	}
	// O tecto em dolares e opcional, como o da Anthropic.
	if r, err := ler(com(CampoTectoUSDOpenRouter, "")); err != nil || r.Tectos.TemUSD {
		t.Errorf("sem tecto em dolares: %v / %+v", err, r.Tectos)
	}
	if f, err := LerFornecedor("openrouter"); err != nil || f != FornecedorOpenRouter {
		t.Errorf("LerFornecedor: %v %v", f, err)
	}
	if got := HostsDoFornecedor(FornecedorOpenRouter); len(got) != 1 || got[0] != "openrouter.ai" {
		t.Errorf("hosts da OpenRouter = %v", got)
	}
}

// O MODO REAL COM A OPENROUTER, contra um lançador de teste: a rota do proxy é
// `openrouter/<autor>/<modelo>` com a base fixa, o destino mostra-se antes de enviar, o contador
// é o do fornecedor `openrouter`, o perfil tem de esperar a rota da corrida, e a chave não
// aparece em saída nenhuma.
func TestAOS516_Real_OpenRouter(t *testing.T) {
	chaves := escreverChaves(t, chavesComOpenRouter())
	pasta := filepath.Dir(chaves)
	precos := filepath.Join(pasta, "precos.json")
	if err := os.WriteFile(precos, []byte(`{"modelos":{"`+modeloOpenRouterDeTeste+`":{"entrada_micro_usd_por_mtok":3000000,"saida_micro_usd_por_mtok":15000000}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rota := PrefixoDaOpenRouter + "/" + modeloOpenRouterDeTeste

	// --so-plano: valida tudo, mostra o destino e nao lanca nem envia.
	lanc := &lancadorDeTeste{t: t}
	e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "openrouter", "--precos", precos, "--so-plano")
	if e.codigo != SaidaOK || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "DESTINO DA CHAVE: https://openrouter.ai ") || !strings.Contains(e.stderr, "restam 200 de 200") {
		t.Fatalf("--so-plano: codigo %d, lancamentos %d\n%s", e.codigo, lanc.lancamentos, e.stderr)
	}

	// Sem precos, com um tecto em dolares: recusa (fail-closed), como com a Anthropic.
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "openrouter", "--so-plano"); e.codigo != SaidaRecusada || lanc.lancamentos != 0 {
		t.Fatalf("tecto em dolares sem --precos: codigo %d\n%s", e.codigo, e.stderr)
	}

	// Um perfil que espera OUTRA rota (a anthropic/ directa, ou a forma openai/): recusa.
	for _, outra := range []string{"anthropic/" + modeloOpenRouterDeTeste, "openai/" + modeloOpenRouterDeTeste, modeloOpenRouterDeTeste} {
		e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "openrouter", "--precos", precos,
			"--perfil", aos516Ficheiro(t, aos516PerfilOpenRouter(outra, aos516ParamsEffort, "obrigatorio")), "--saida", filepath.Join(pasta, "r0"))
		if e.codigo != SaidaRecusada || lanc.lancamentos != 0 || !strings.Contains(e.stderr, "expected_model") {
			t.Fatalf("perfil de outra rota (%s): codigo %d, lancamentos %d\n%s", outra, e.codigo, lanc.lancamentos, e.stderr)
		}
	}
	if e := executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "openrouter", "--precos", precos, "--destino-fora-da-lista", "outro.exemplo.test", "--so-plano"); e.codigo != SaidaRecusada {
		t.Fatalf("--destino-fora-da-lista com a OpenRouter: codigo %d", e.codigo)
	}

	// A corrida, com o perfil certo, contra o falso na forma da OpenRouter.
	lanc = &lancadorDeTeste{t: t, devolver: &FalsoDeEstado{Turnos: 2, FormaDoEstado: FormaOpenRouter, ServidoComo: rota}}
	saida := filepath.Join(pasta, "r1")
	e = executar(t, Ambiente{Lancador: lanc}, "real", "--chaves", chaves, "--fornecedor", "openrouter", "--precos", precos,
		"--perfil", aos516Ficheiro(t, aos516PerfilOpenRouter(rota, aos516ParamsEffort, "obrigatorio")), "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	p := lanc.pedido
	if p.Prefixo != PrefixoDaOpenRouter || p.Modelo != modeloOpenRouterDeTeste || p.apiKey != sentinelaChaveOpenRouter || p.apiBase != "https://openrouter.ai/api/v1" || p.Estado != "" || p.BinarioDoFalso != "" {
		t.Errorf("pedido de proxy: prefixo %q, modelo %q, estado %q", p.Prefixo, p.Modelo, p.Estado)
	}
	r := aos516Relatorio(t, saida)
	if r.Rota.Fornecedor != "openrouter" || r.Rota.Modelo != modeloOpenRouterDeTeste || r.Sonda == nil || r.Sonda.Resultado != SondaOK {
		t.Errorf("relatorio: rota %+v, sonda %+v", r.Rota, r.Sonda)
	}
	if d := r.Taxas.Devolucao; d == nil || d.AceitesPeloFornecedor != 10 || d.Recusas != 0 || d.ComEstado.HTTP4xx != 0 {
		t.Errorf("devolucao no modo real (lancador de teste) = %+v", d)
	}
	if r.FormaNoFornecedor != nil {
		t.Error("um fornecedor real nao mostra a forma do que recebeu")
	}
	// O contador e o do fornecedor openrouter: nao gasta o tecto de mais ninguem.
	contador, err := os.ReadFile(filepath.Join(pasta, "contador.json"))
	if err != nil || !strings.Contains(string(contador), `"openrouter"`) || strings.Contains(string(contador), `"anthropic"`) || strings.Contains(string(contador), `"kimi"`) {
		t.Errorf("contador = %s (%v)", contador, err)
	}
	verSemFugas(t, "real com a OpenRouter", tudoOQueFoiEscrito(t, e, saida, pasta), append(SentinelasDoEstado(), sentinelaChaveOpenRouter, "resposta final", "Bearer "))

	// A configuracao do proxy para esta rota: o adaptador openrouter, com a chave e a base por
	// variavel de ambiente — nenhuma das duas escrita no ficheiro.
	cfg, err := configDoProxy(PrefixoDaOpenRouter, modeloOpenRouterDeTeste, true)
	if err != nil || !strings.Contains(cfg, "model: "+rota+"\n") || !strings.Contains(cfg, "api_base: os.environ/"+envBaseDaRota) || strings.Contains(cfg, "openrouter.ai") {
		t.Errorf("configuracao do proxy: %v\n%s", err, cfg)
	}
}

// A FORMA DA OPENROUTER PELA LINHA DE COMANDOS, no modo falso: obrigatório + exigente ⇒
// `cumprida`; `nunca` + exigente ⇒ `nao_cumprida`. E o modo proxy pede ao lançador a rota que o
// perfil espera, com a forma.
func TestAOS516_CLI_AFormaDaOpenRouter(t *testing.T) {
	rota := "openrouter/autor-de-ensaio/modelo-de-ensaio"
	obrigatorio := aos516Ficheiro(t, aos516PerfilOpenRouter(rota, aos516ParamsEffort, "obrigatorio"))
	nunca := aos516Ficheiro(t, aos516PerfilOpenRouter(rota, aos516ParamsEffort, "nunca"))
	proibidos := append(append(proibidosDeTexto(t), SentinelasDoEstado()...), "reasoning-text-", "anthropic-claude-v1")

	saida := t.TempDir()
	e := executar(t, Ambiente{}, "falso", "--estado", "exige", "--forma-do-falso", "openrouter", "--turnos-do-falso", "3", "--perfil", obrigatorio, "--saida", saida)
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r := aos516Relatorio(t, saida)
	aos516Veredicto(t, r, QualificacaoCumprida)
	if d := r.Taxas.Devolucao; d == nil || d.TurnosComRaciocinioCapturado != 15 || d.AceitesPeloFornecedor != 15 {
		t.Errorf("devolucao da bateria = %+v", d)
	}
	f := r.FormaNoFornecedor
	if f == nil || f.Estado["reasoning_details.intacto"] != 30 || f.Estado["reasoning_details.bytes_iguais"] != 30 || len(f.Recusas) != 0 || f.Parametros["reasoning_effort"] != 22 {
		t.Errorf("forma no fornecedor = %+v", f)
	}
	if !strings.Contains(e.stdout, "QUALIFICACAO DA DEVOLUCAO: CUMPRIDA") || !strings.Contains(e.stdout, "parametros de raciocinio dos pedidos") {
		t.Errorf("o resumo nao mostra o veredicto ou os parametros:\n%s", e.stdout)
	}
	verSemFugas(t, "falso na forma da OpenRouter", tudoOQueFoiEscrito(t, e, saida), proibidos)

	saida = t.TempDir()
	if e = executar(t, Ambiente{}, "falso", "--estado", "exige", "--forma-do-falso", "openrouter", "--perfil", nunca, "--saida", saida); e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	r = aos516Relatorio(t, saida)
	if q := r.Qualificacao; q == nil || q.Veredicto != QualificacaoNaoCumprida || r.FormaNoFornecedor.Recusas[RecusaEstadoEmFalta] != 5 {
		t.Errorf("perfil nunca: qualificacao %+v, forma %+v", q, r.FormaNoFornecedor)
	}
	verSemFugas(t, "falso na forma da OpenRouter, sem devolucao", tudoOQueFoiEscrito(t, e, saida), proibidos)

	// O modo proxy: a rota e a do perfil, pelas duas formas de rota que se medem.
	for _, prefixo := range []string{"openrouter", "openai"} {
		esperado := prefixo + "/autor-de-ensaio/modelo-de-ensaio"
		lanc := &lancadorDeTeste{t: t, devolver: &FalsoDeEstado{Turnos: 2, FormaDoEstado: FormaOpenRouter, ServidoComo: esperado}}
		e := executar(t, Ambiente{Lancador: lanc}, "proxy", "--binario-do-falso", "nao-interessa", "--estado", "exige", "--forma-do-falso", "openrouter",
			"--perfil", aos516Ficheiro(t, aos516PerfilOpenRouter(esperado, aos516ParamsEffort, "obrigatorio")), "--saida", t.TempDir())
		p := lanc.pedido
		if e.codigo != SaidaOK || p.Prefixo != prefixo || p.Modelo != "autor-de-ensaio/modelo-de-ensaio" || p.FormaDoFalso != FormaOpenRouter || p.Estado != EstadoExige {
			t.Errorf("modo proxy (%s): codigo %d, prefixo %q, modelo %q, forma %q\n%s", prefixo, e.codigo, p.Prefixo, p.Modelo, p.FormaDoFalso, e.stderr)
		}
	}
	args := strings.Join(argumentosDoContentorDoFalso("c", "r", PedidoDeProxy{Estado: EstadoExige, TurnosDoFalso: 3, FormaDoFalso: FormaOpenRouter}, "abc", time.Hour), " ")
	if !strings.Contains(args, "--estado exige --turnos-do-falso 3 --forma-do-falso openrouter --chave-sha256 abc") {
		t.Errorf("argumentos do contentor do falso: %s", args)
	}
	if baseDoFalso("f", PrefixoDaOpenRouter) != "http://f:8080/v1" {
		t.Errorf("base do falso para a rota openrouter: %s", baseDoFalso("f", PrefixoDaOpenRouter))
	}

	// As recusas de uso: nenhuma lanca nem escreve relatorio.
	for nome, args := range map[string][]string{
		"forma sem estado":                     {"falso", "--forma-do-falso", "openrouter"},
		"forma fora do vocabulario":            {"falso", "--estado", "exige", "--forma-do-falso", "outra"},
		"forma no modo real":                   {"real", "--chaves", "nao-existe", "--fornecedor", "openrouter", "--forma-do-falso", "openrouter"},
		"proxy com a forma e rota anthropic":   {"proxy", "--binario-do-falso", "x", "--estado", "exige", "--forma-do-falso", "openrouter", "--perfil", aos516Ficheiro(t, aos516PerfilObrigatorio)},
		"proxy com a forma e modelo sem autor": {"proxy", "--binario-do-falso", "x", "--estado", "exige", "--forma-do-falso", "openrouter", "--perfil", aos516Ficheiro(t, aos516PerfilOpenRouter("openrouter/modelo-sem-autor", aos516ParamsEffort, "obrigatorio"))},
		"proxy sem a forma e rota openrouter":  {"proxy", "--binario-do-falso", "x", "--estado", "exige", "--perfil", obrigatorio},
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

// O PERFIL DE EXEMPLO DA OPENROUTER, versionado junto do banco, passa pela leitura fechada do
// gateway e é o perfil da rota de ensaio. Não tem chaves; o nome do modelo é um marcador, e a
// rota que espera é a forma que o banco usa no modo real.
func TestAOS516_PerfilDeExemploDaOpenRouter(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("perfis", "claude-openrouter.exemplo.json"))
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
	if p.StateReturn != modelgateway.StateReturnRequired || p.ProjectionVersion != modelgateway.NativeProjectionVersion130 || p.Params == nil || p.Params.MaxTokens == 0 {
		t.Errorf("o perfil de exemplo tem de devolver estado (obrigatorio, 1.3.0) com um parametro de raciocinio: %+v", p)
	}
	// Declara as duas coisas que a medicao atras do proxy mostrou serem precisas nesta rota: o
	// estado volta no topo da mensagem, e o raciocinio pede-se na forma `reasoning`.
	if p.StateReturnAt != modelgateway.StateReturnAtTop || p.Params.Reasoning == nil || p.Params.Thinking != nil || p.Params.ReasoningEffort != "" {
		t.Errorf("o perfil de exemplo tem de declarar devolver_em topo e params.reasoning: %+v / %+v", p, p.Params)
	}
	modelo, daOpenRouter := strings.CutPrefix(p.ExpectedModel, PrefixoDaOpenRouter+"/anthropic/")
	if !daOpenRouter || !strings.Contains(modelo, "PREENCHER") {
		t.Errorf("expected_model = %q: quer %s/anthropic/<marcador>", p.ExpectedModel, PrefixoDaOpenRouter)
	}
	for _, proibido := range []string{"sk-", "api_key", "Bearer"} {
		if strings.Contains(string(doc), proibido) {
			t.Errorf("o perfil de exemplo tem %q", proibido)
		}
	}
}

// OS ERROS DA OPENROUTER caem no vocabulário fechado: directos (a OpenRouter manda o código como
// número, sem `type`) e na forma em que a imagem fixada do proxy os reembrulha.
func TestAOS516_Erros_DaOpenRouter(t *testing.T) {
	for nome, c := range map[string]struct {
		status int
		corpo  string
		quer   string
	}{
		"401 directo":                          {401, errosDaOpenRouter["401"].corpo, TipoChaveRecusada},
		"402 directo":                          {402, errosDaOpenRouter["402"].corpo, TipoSaldoInsuficiente},
		"429 directo":                          {429, errosDaOpenRouter["429"].corpo, TipoLimiteDeRitmo},
		"404 directo":                          {404, errosDaOpenRouter["404"].corpo, TipoModeloDesconhecido},
		"400 de modelo, directo":               {400, errosDaOpenRouter["400"].corpo, TipoModeloDesconhecido},
		"402 so pelo codigo HTTP":              {402, `{"error":{"message":"x"}}`, TipoSaldoInsuficiente},
		"402 so pelo codigo do corpo (numero)": {400, `{"error":{"message":"x","code":402}}`, TipoSaldoInsuficiente},
		"402 so pelo codigo do corpo (texto)":  {500, `{"error":{"message":"x","code":"402"}}`, TipoSaldoInsuficiente},
		"401 so pelo codigo do corpo":          {500, `{"error":{"message":"x","code":"401"}}`, TipoChaveRecusada},
		"saldo num 429 ganha ao ritmo":         {429, `{"error":{"message":"Rate limit exceeded: insufficient credits","code":429}}`, TipoSaldoInsuficiente},
		"429 sem frase conhecida":              {429, `{"error":{"message":"Provider returned error","code":429}}`, TipoOutro},
		"codigo que nao e HTTP":                {400, `{"error":{"message":"x","code":40200}}`, TipoOutro},
		"corpo ilegivel com 402":               {402, `<html>`, TipoSaldoInsuficiente},
	} {
		if tem := classificarErro(c.status, []byte(c.corpo)); tem != c.quer {
			t.Errorf("%s: classificado %q, quer %q", nome, tem, c.quer)
		}
	}
}

// A REVISÃO DA CLASSIFICAÇÃO (AOS-516): o que NÃO é modelo desconhecido, o que NÃO é chave
// recusada, e a frase de ritmo fora de um 429.
func TestAOS516_Erros_DaOpenRouter_Revisao(t *testing.T) {
	msg := func(m string, codigo string) string {
		if codigo == "" {
			return `{"error":{"message":"` + m + `"}}`
		}
		return `{"error":{"message":"` + m + `","code":` + codigo + `}}`
	}
	for nome, c := range map[string]struct {
		status int
		corpo  string
		quer   string
	}{
		// (a) «no endpoints found»: so a forma que nomeia o modelo, e mais nada, e modelo desconhecido.
		"sem endpoints, nomeia o modelo":            {404, msg("No endpoints found for autor/modelo-x.", "404"), TipoModeloDesconhecido},
		"sem endpoints, nomeia o modelo, sem ponto": {404, msg("No endpoints found for autor/modelo-x", "404"), TipoModeloDesconhecido},
		"sem endpoints pela politica de dados":      {404, msg("No endpoints found matching your data policy. Enable prompt training here", "404"), TipoRotaIndisponivel},
		"sem endpoints com tools":                   {404, msg("No endpoints found that support tool use. To learn more about provider routing", "404"), TipoRotaIndisponivel},
		"sem endpoints, modelo e politica":          {404, msg("No endpoints found for autor/modelo-x matching your data policy", "404"), TipoRotaIndisponivel},
		"sem endpoints, sem mais nada":              {404, msg("No endpoints found", "404"), TipoRotaIndisponivel},
		"sem endpoints, for sem modelo":             {404, msg("No endpoints found for ", "404"), TipoRotaIndisponivel},
		"modelo invalido":                           {400, msg("autor/modelo-x is not a valid model ID", "400"), TipoModeloDesconhecido},
		// (b) um 403 de moderacao nao e a chave.
		"403 de moderacao":                         {403, msg("autor/modelo-x requires moderation and your input was flagged", "403"), TipoOutro},
		"403 de moderacao so no codigo do corpo":   {500, msg("Your input was flagged for a category", `"403"`), TipoOutro},
		"403 sem frase de moderacao":               {403, msg("Key disabled", "403"), TipoChaveRecusada},
		"moderacao num 401 continua a ser a chave": {401, msg("input was flagged", "401"), TipoChaveRecusada},
		// (c) a frase de ritmo so conta num 429.
		"frase de ritmo num 400":                {400, msg("Rate limit exceeded: limit_rpm", ""), TipoOutro},
		"frase de ritmo num 500 com codigo 400": {500, msg("Rate limit exceeded: limit_rpm", "400"), TipoOutro},
		"frase de ritmo num 403":                {403, msg("Rate limit exceeded for this key", "403"), TipoChaveRecusada},
		"frase de ritmo num 429":                {429, msg("Rate limit exceeded: limit_rpm", ""), TipoLimiteDeRitmo},
		// (d) error.code 429 le-se como o codigo HTTP.
		"frase de ritmo, 429 so no corpo (numero)": {500, msg("Rate limit exceeded: limit_rpm", "429"), TipoLimiteDeRitmo},
		"frase de ritmo, 429 so no corpo (texto)":  {400, msg("Rate limit exceeded: limit_rpm", `"429"`), TipoLimiteDeRitmo},
		"429 no corpo sem frase":                   {500, msg("Provider returned error", "429"), TipoOutro},
		"saldo com 429 so no corpo":                {500, msg("Rate limit exceeded: insufficient credits", "429"), TipoSaldoInsuficiente},
	} {
		if tem := classificarErro(c.status, []byte(c.corpo)); tem != c.quer {
			t.Errorf("%s: classificado %q, quer %q", nome, tem, c.quer)
		}
	}
	// O tipo novo esta no vocabulario, tem estado final proprio na sonda e um conselho que diz
	// o que verificar na conta.
	noVocabulario := false
	for _, v := range TiposDeErro() {
		noVocabulario = noVocabulario || v == TipoRotaIndisponivel
	}
	if !noVocabulario || terminouDaSonda(Sonda{Resultado: TipoRotaIndisponivel}) != TerminouRotaIndisponivel {
		t.Errorf("rota_indisponivel: no vocabulario %v, estado final %q", noVocabulario, terminouDaSonda(Sonda{Resultado: TipoRotaIndisponivel}))
	}
	if c := conselhoDaParagem(&Relatorio{Terminou: TerminouRotaIndisponivel}); !strings.Contains(c, "politica de dados") || !strings.Contains(c, "nao adianta") {
		t.Errorf("conselho da paragem: %q", c)
	}
}

// `OPENROUTER_MODELO` é exactamente `<autor>/<modelo>`, cada segmento a começar por letra ou
// algarismo, e o prefixo do adaptador do proxy é recusado sem distinguir maiúsculas.
func TestAOS516_OpenRouter_FormaDoNomeDoModelo(t *testing.T) {
	for nome, aceite := range map[string]bool{
		"anthropic/claude-sonnet-4.5": true, "autor-1/modelo_2:thinking": true, "a/b": true, "0x/9y": true,
		"../x": false, "./x": false, "anthropic/..": false, "anthropic/.": false, "anthropic/-": false, "-/x": false, ".autor/x": false, "autor/.modelo": false,
		"autor/-modelo": false, "autor/_modelo": false, ":autor/x": false, "autor/:x": false,
		"OpenRouter/x": false, "OPENROUTER/x": false, "openrouter/x": false, "openRouter/anthropic": false,
		"modelo-sem-autor": false, "/x": false, "x/": false, "a/b/c": false, "": false, "/": false,
	} {
		if got := modeloDaOpenRouterAceite(nome); got != aceite {
			t.Errorf("modeloDaOpenRouterAceite(%q) = %v, quer %v", nome, got, aceite)
		}
	}
	// Pelo ficheiro de chaves: o erro nomeia o campo e nao repete o valor.
	for _, mau := range []string{"../x", "./x", "anthropic/..", "anthropic/-", "OpenRouter/x"} {
		campos := chavesComOpenRouter()
		campos[CampoOpenRouterModelo] = mau
		c, err := LerChaves(escreverChaves(t, campos))
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Rota(FornecedorOpenRouter, "", Destino{})
		var doCampo *ErrCampoDasChaves
		if !errors.As(err, &doCampo) || doCampo.Campo != CampoOpenRouterModelo || doCampo.Problema != problemaSemAutor || strings.Contains(err.Error(), mau) {
			t.Errorf("%q: erro = %v", mau, err)
		}
	}
}
