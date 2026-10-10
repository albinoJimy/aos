package bancoensaio

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// O FICHEIRO DE CHAVES DO DONO (AOS-512, decisão D5).
//
// É um ficheiro `CAMPO=valor` que vive FORA do repositório e que só o programa lê, em tempo de
// execução, pelo caminho que lhe dão. Os valores nunca são impressos, registados nem postos em
// argumentos de processo: [Chaves] guarda-os em campos não exportados, não se deixa formatar nem
// serializar, e os erros deste ficheiro nomeiam o CAMPO e nunca o valor.

// Fornecedor é o fornecedor do modelo no modo com modelo real. Vocabulário fechado.
type Fornecedor string

const (
	// FornecedorKimi — o modelo que está em produção, pela rota `openai/…` do proxy.
	FornecedorKimi Fornecedor = "kimi"
	// FornecedorAnthropic — a segunda família, pela rota `anthropic/…` do proxy.
	FornecedorAnthropic Fornecedor = "anthropic"
	// FornecedorOpenRouter — a segunda família servida por um agregador, pela rota
	// `openrouter/…` do proxy (decisão do dono de 2026-10-10: só no banco de ensaio).
	FornecedorOpenRouter Fornecedor = "openrouter"
	// FornecedorFalso — o provider falso do banco (modos `falso` e `proxy`).
	FornecedorFalso Fornecedor = "falso"
)

// LerFornecedor valida o nome de um fornecedor do modo com modelo real.
func LerFornecedor(s string) (Fornecedor, error) {
	switch Fornecedor(s) {
	case FornecedorKimi, FornecedorAnthropic, FornecedorOpenRouter:
		return Fornecedor(s), nil
	}
	return "", fmt.Errorf("banco-ensaio: fornecedor desconhecido: %q (aceites: kimi, anthropic, openrouter)", s)
}

// Os campos do ficheiro de chaves.
const (
	CampoChaveKimi            = "KIMI_API_KEY"
	CampoKimiAPIBase          = "KIMI_API_BASE"
	CampoKimiModelos          = "KIMI_MODELOS"
	CampoChaveAnthropic       = "ANTHROPIC_API_KEY"
	CampoAnthropicModelo      = "ANTHROPIC_MODELO"
	CampoTectoPedidosKimi     = "TECTO_PEDIDOS_DIA_KIMI"
	CampoTectoPedidosAnthro   = "TECTO_PEDIDOS_DIA_ANTHROPIC"
	CampoTectoUSDAnthropic    = "TECTO_USD_DIA_ANTHROPIC"
	CampoAnthropicRegiaoProc  = "ANTHROPIC_REGIAO_DE_PROCESSAMENTO"
	CampoChaveOpenRouter      = "OPENROUTER_API_KEY"
	CampoOpenRouterModelo     = "OPENROUTER_MODELO"
	CampoTectoPedidosOpenR    = "TECTO_PEDIDOS_DIA_OPENROUTER"
	CampoTectoUSDOpenRouter   = "TECTO_USD_DIA_OPENROUTER"
	problemaEmFalta           = "em falta"
	problemaMarcadorDoExemplo = "ainda com o marcador do exemplo"
	problemaIlegivel          = "ilegivel"
	problemaZero              = "zero ou negativo"
	problemaForaDaLista       = "nao esta na lista do ficheiro"
	problemaCaracteres        = "com caracteres que o banco nao aceita"
	problemaAspas             = "entre aspas (o valor escreve-se sem aspas)"
	problemaNaoEInteiro       = "escrito de uma forma que o banco nao aceita (so algarismos, sem sinal nem espacos)"
	problemaAcimaDoMaximo     = "acima do maximo que o banco aceita"
	problemaSemHTTPS          = "sem https, ou nao e um URL"
	problemaURLComExtras      = "com utilizador, porta, query ou fragmento no URL"
	problemaSemAutor          = "sem a forma autor/modelo da OpenRouter (por exemplo anthropic/<modelo>, sem o prefixo openrouter/)"
	problemaHostForaDaLista   = "com um host que nao e do fornecedor (para o aceitar, --destino-fora-da-lista com o host exacto)"
)

// Os máximos que o banco aceita num tecto do ficheiro de chaves. Um valor acima deles é quase
// de certeza um engano de escrita, e um tecto que não limita nada não é um tecto.
const (
	// MaxTectoDePedidosDia é o maior tecto de pedidos por dia que o banco aceita.
	MaxTectoDePedidosDia = 100_000
	// MaxTectoUSDDia é o maior tecto de gasto por dia, em dólares, que o banco aceita.
	MaxTectoUSDDia = 1000
)

// hostsDoFornecedor é a lista dos hosts para onde a chave de cada fornecedor pode ir. Está no
// código de propósito: o destino da chave não depende só do que estiver escrito no ficheiro.
//
// A Anthropic não tem entrada: o ficheiro de chaves não traz base para ela, e quem escolhe o
// endpoint é o adaptador `anthropic` do proxy. O banco não escreve esse host em lado nenhum —
// o no-bypass do Model Gateway (archlint, AOS-055) proíbe endpoints de provider fora do gateway.
var hostsDoFornecedor = map[Fornecedor][]string{
	FornecedorKimi:       {"api.kimi.com", "api.moonshot.ai", "api.moonshot.cn"},
	FornecedorOpenRouter: {hostDaOpenRouter},
}

// A OpenRouter tem UM destino, e quem o escreve é o banco: o ficheiro de chaves não traz base
// para ela. A base vai explícita para o proxy (`api_base`), para o destino da chave não depender
// do valor por omissão de adaptador nenhum.
const (
	hostDaOpenRouter = "openrouter.ai"
	baseDaOpenRouter = "https://" + hostDaOpenRouter + "/api/v1"
)

// DestinoDaAnthropic é o que o banco mostra como destino da chave da Anthropic.
const DestinoDaAnthropic = "o endpoint por omissao do adaptador anthropic do proxy (o ficheiro de chaves nao tem base para a Anthropic)"

// HostsDoFornecedor devolve os hosts aceites para um fornecedor.
func HostsDoFornecedor(f Fornecedor) []string {
	return append([]string(nil), hostsDoFornecedor[f]...)
}

// Destino diz como validar o destino da chave.
type Destino struct {
	// ForaDaLista é o host, escrito à mão pelo operador (`--destino-fora-da-lista`), de um
	// destino que não está na lista do fornecedor. Tem de ser EXACTAMENTE o host do ficheiro;
	// continua a exigir https. Vazio ⇒ só os hosts da lista.
	ForaDaLista string

	// deTeste é uma base aceite tal e qual, sem validação. Só os testes do próprio pacote a
	// conseguem definir: não há flag nem variável de ambiente que cá chegue.
	deTeste string
}

// ErrCampoDasChaves — um campo do ficheiro de chaves não serve. A mensagem nomeia o CAMPO e o
// problema; nunca leva o valor.
type ErrCampoDasChaves struct {
	Campo    string
	Problema string
}

func (e *ErrCampoDasChaves) Error() string {
	return "banco-ensaio: ficheiro de chaves: o campo " + e.Campo + " esta " + e.Problema
}

// ErrFicheiroDeChaves — o ficheiro de chaves não se conseguiu ler. Não leva conteúdo.
var ErrFicheiroDeChaves = errors.New("banco-ensaio: ficheiro de chaves ilegivel")

// Chaves é o conteúdo do ficheiro de chaves. Os valores ficam em campos não exportados.
type Chaves struct {
	campos map[string]string
	// comAspas são os campos cujo valor veio entre aspas: recusados quando forem pedidos.
	comAspas map[string]bool
}

// String não mostra nada: um `%v` acidental sobre as chaves não as imprime.
func (Chaves) String() string { return "[chaves ocultas]" }

// GoString idem, para `%#v`.
func (Chaves) GoString() string { return "[chaves ocultas]" }

// MarshalJSON recusa serializar as chaves.
func (Chaves) MarshalJSON() ([]byte, error) {
	return nil, errors.New("banco-ensaio: as chaves nao se serializam")
}

// LerChaves lê o ficheiro de chaves pelo caminho dado. Não valida campos — isso é de
// [Chaves.Rota], que sabe quais o fornecedor escolhido exige.
func LerChaves(caminho string) (*Chaves, error) {
	if strings.TrimSpace(caminho) == "" {
		return nil, fmt.Errorf("%w: o caminho do ficheiro de chaves e obrigatorio", ErrFicheiroDeChaves)
	}
	f, err := os.Open(caminho)
	if err != nil {
		// O erro do sistema leva o caminho (que quem chamou escolheu) e nada do conteúdo.
		return nil, fmt.Errorf("%w: %v", ErrFicheiroDeChaves, err)
	}
	defer f.Close()
	c := &Chaves{campos: map[string]string{}, comAspas: map[string]bool{}}
	linhaDoCampo := map[string]int{}
	leitor := bufio.NewScanner(f)
	leitor.Buffer(make([]byte, 0, 64<<10), 1<<20)
	n := 0
	for leitor.Scan() {
		n++
		linha := strings.TrimSpace(strings.TrimPrefix(leitor.Text(), string(rune(0xFEFF))))
		if linha == "" || strings.HasPrefix(linha, "#") {
			continue
		}
		campo, valor, ok := strings.Cut(linha, "=")
		campo = strings.TrimSpace(campo)
		if !ok || campo == "" {
			// Só o número da linha: a linha pode ser um valor colado sem o nome do campo.
			return nil, fmt.Errorf("%w: a linha %d nao tem a forma CAMPO=valor", ErrFicheiroDeChaves, n)
		}
		if anterior, repetido := linhaDoCampo[campo]; repetido {
			// Um campo repetido não é «ganha o último»: com dois tectos no ficheiro, o tecto
			// seria o que a última linha dissesse. O erro leva o nome e as linhas, nunca valores.
			return nil, &ErrCampoDasChaves{Campo: campo, Problema: fmt.Sprintf("repetido (linhas %d e %d)", anterior, n)}
		}
		linhaDoCampo[campo] = n
		valor = strings.TrimSpace(valor)
		if len(valor) > 0 && (strings.ContainsAny(valor[:1], "\"'`") || strings.ContainsAny(valor[len(valor)-1:], "\"'`")) {
			c.comAspas[campo] = true
		}
		c.campos[campo] = valor
	}
	if err := leitor.Err(); err != nil {
		return nil, fmt.Errorf("%w: erro de leitura", ErrFicheiroDeChaves)
	}
	return c, nil
}

// valor devolve um campo obrigatório, ou o erro que o nomeia.
func (c *Chaves) valor(campo string) (string, error) {
	v, ok := c.campos[campo]
	if !ok || v == "" {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaEmFalta}
	}
	if marcadorDoExemplo(v) {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaMarcadorDoExemplo}
	}
	if c.comAspas[campo] {
		// As aspas iriam tal e qual para o fornecedor (`Bearer "sk-…"`), que responderia 401.
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaAspas}
	}
	return v, nil
}

// soAlgarismos diz se s é uma sequência não vazia de algarismos, e mais nada.
func soAlgarismos(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validarDestino valida a base da API de um fornecedor e devolve o destino público
// (`https://host`) que o banco mostra antes de enviar seja o que for.
//
// Exige https; recusa utilizador, porta, query e fragmento; e o host tem de estar na lista do
// fornecedor ([hostsDoFornecedor]) ou ser, letra a letra, o que o operador escreveu em
// `--destino-fora-da-lista`. Um erro de escrita ou uma linha colada do sítio errado não manda a
// chave para outro lado. Os erros nomeiam o campo e nunca o valor.
func validarDestino(f Fornecedor, campo, base string, d Destino) (string, error) {
	u, err := url.Parse(base)
	if d.deTeste != "" && base == d.deTeste {
		if err != nil || u.Host == "" {
			return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaSemHTTPS}
		}
		return u.Scheme + "://" + u.Host, nil
	}
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaSemHTTPS}
	}
	if u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(base, "#") {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaURLComExtras}
	}
	host := strings.ToLower(u.Hostname())
	naLista := false
	for _, h := range hostsDoFornecedor[f] {
		if h == host {
			naLista = true
		}
	}
	switch {
	case naLista && d.ForaDaLista == "":
	case !naLista && d.ForaDaLista == host:
	case naLista:
		return "", fmt.Errorf("banco-ensaio: --destino-fora-da-lista foi dado, e o destino do ficheiro de chaves ja e um host do fornecedor: retire a opcao")
	default:
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaHostForaDaLista}
	}
	return "https://" + host, nil
}

// marcadorDoExemplo diz se o valor ainda é o do ficheiro de exemplo: `<…>`.
func marcadorDoExemplo(v string) bool {
	return strings.HasPrefix(v, "<") || strings.HasSuffix(v, ">")
}

// inteiroPositivo lê um tecto de pedidos. Ausente, ilegível, zero ou negativo ⇒ erro: não há
// tecto por omissão.
func (c *Chaves) inteiroPositivo(campo string) (int64, error) {
	v, err := c.valor(campo)
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(v, "-") && soAlgarismos(v[1:]) {
		return 0, &ErrCampoDasChaves{Campo: campo, Problema: problemaZero}
	}
	if !soAlgarismos(v) {
		// `+500`, `5 00`, `1e3`, `500 # nota`: nada disto é um tecto escrito com clareza.
		return 0, &ErrCampoDasChaves{Campo: campo, Problema: problemaNaoEInteiro}
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil || n > MaxTectoDePedidosDia {
		return 0, &ErrCampoDasChaves{Campo: campo, Problema: problemaAcimaDoMaximo}
	}
	if n <= 0 {
		return 0, &ErrCampoDasChaves{Campo: campo, Problema: problemaZero}
	}
	return n, nil
}

// microUSDOpcional lê um tecto em dólares e devolve-o em micro-USD. Campo ausente ⇒ (0, false):
// o tecto em dólares é opcional no ficheiro do dono. Presente e ilegível, zero ou negativo ⇒
// erro (um tecto escrito e que não se lê não passa a «sem tecto»).
func (c *Chaves) microUSDOpcional(campo string) (int64, bool, error) {
	v, ok := c.campos[campo]
	if !ok || v == "" {
		return 0, false, nil
	}
	if marcadorDoExemplo(v) {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaMarcadorDoExemplo}
	}
	if c.comAspas[campo] {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaAspas}
	}
	inteira, decimal, temDecimal := strings.Cut(strings.ReplaceAll(v, ",", "."), ".")
	if !soAlgarismos(inteira) || (temDecimal && !soAlgarismos(decimal)) {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaIlegivel}
	}
	usd, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaIlegivel}
	}
	if usd <= 0 {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaZero}
	}
	if usd > MaxTectoUSDDia {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaAcimaDoMaximo}
	}
	return int64(math.Round(usd * 1e6)), true, nil
}

// RotaReal é a rota do modo com modelo real, lida do ficheiro de chaves: o que o proxy efémero
// precisa para falar com o fornecedor, e os tectos do dia. Os segredos ficam em campos não
// exportados.
type RotaReal struct {
	Fornecedor Fornecedor
	// Modelo é o nome do modelo no fornecedor. Vai no relatório.
	Modelo string
	// Tectos do dia para este fornecedor.
	Tectos Tectos
	// RegiaoDeclarada é a região que o dono declarou (só Anthropic). É copiada para o
	// relatório como DECLARAÇÃO e não tem efeito no ensaio.
	RegiaoDeclarada string
	// Destino é para onde a chave vai: `https://host`, já validado (Kimi, OpenRouter), ou a frase
	// [DestinoDaAnthropic]. O host de um fornecedor
	// público não é segredo, e o banco mostra-o antes de enviar; o caminho da base não vai.
	Destino string

	apiKey  string
	apiBase string
}

// String não mostra os segredos.
func (r RotaReal) String() string {
	return "rota " + string(r.Fornecedor) + " [segredos ocultos]"
}

// GoString idem.
func (r RotaReal) GoString() string { return r.String() }

// Segredos devolve os valores que NUNCA podem aparecer numa saída: a chave e a base da API.
// Serve ao redactor das saídas e aos testes de fuga.
func (r RotaReal) Segredos() []string {
	var out []string
	for _, s := range []string{r.apiKey, r.apiBase} {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Rota valida os campos que o fornecedor exige e devolve a rota. `modelo` vazio ⇒ o primeiro
// da lista do ficheiro (Kimi) ou o único (Anthropic); não vazio ⇒ tem de ser um dos do ficheiro.
// O destino da chave é validado contra a lista de hosts do fornecedor ([validarDestino]).
func (c *Chaves) Rota(f Fornecedor, modelo string, d Destino) (RotaReal, error) {
	r := RotaReal{Fornecedor: f}
	var err error
	switch f {
	case FornecedorKimi:
		if r.apiKey, err = c.valor(CampoChaveKimi); err != nil {
			return RotaReal{}, err
		}
		if r.apiBase, err = c.valor(CampoKimiAPIBase); err != nil {
			return RotaReal{}, err
		}
		if r.Destino, err = validarDestino(f, CampoKimiAPIBase, r.apiBase, d); err != nil {
			return RotaReal{}, err
		}
		lista, lerr := c.valor(CampoKimiModelos)
		if lerr != nil {
			return RotaReal{}, lerr
		}
		if r.Modelo, err = escolherModelo(CampoKimiModelos, strings.Split(lista, ","), modelo); err != nil {
			return RotaReal{}, err
		}
		if r.Tectos.PedidosDia, err = c.inteiroPositivo(CampoTectoPedidosKimi); err != nil {
			return RotaReal{}, err
		}
	case FornecedorAnthropic:
		if r.apiKey, err = c.valor(CampoChaveAnthropic); err != nil {
			return RotaReal{}, err
		}
		unico, lerr := c.valor(CampoAnthropicModelo)
		if lerr != nil {
			return RotaReal{}, lerr
		}
		if r.Modelo, err = escolherModelo(CampoAnthropicModelo, []string{unico}, modelo); err != nil {
			return RotaReal{}, err
		}
		if d.ForaDaLista != "" {
			// A rota da Anthropic não tem base no ficheiro: o destino é o do adaptador do proxy.
			return RotaReal{}, errors.New("banco-ensaio: --destino-fora-da-lista nao se aplica a Anthropic (o destino e fixo)")
		}
		r.Destino = DestinoDaAnthropic
		if r.Tectos.PedidosDia, err = c.inteiroPositivo(CampoTectoPedidosAnthro); err != nil {
			return RotaReal{}, err
		}
		if r.Tectos.MicroUSDDia, r.Tectos.TemUSD, err = c.microUSDOpcional(CampoTectoUSDAnthropic); err != nil {
			return RotaReal{}, err
		}
		// A região é uma declaração livre do dono; um marcador do exemplo fica «nao declarada».
		if v := c.campos[CampoAnthropicRegiaoProc]; v != "" && !marcadorDoExemplo(v) && !c.comAspas[CampoAnthropicRegiaoProc] {
			r.RegiaoDeclarada = textoDeclarado(v)
		}
	case FornecedorOpenRouter:
		if r.apiKey, err = c.valor(CampoChaveOpenRouter); err != nil {
			return RotaReal{}, err
		}
		unico, lerr := c.valor(CampoOpenRouterModelo)
		if lerr != nil {
			return RotaReal{}, lerr
		}
		if r.Modelo, err = escolherModelo(CampoOpenRouterModelo, []string{unico}, modelo); err != nil {
			return RotaReal{}, err
		}
		if !modeloDaOpenRouterAceite(r.Modelo) {
			return RotaReal{}, &ErrCampoDasChaves{Campo: CampoOpenRouterModelo, Problema: problemaSemAutor}
		}
		if d.ForaDaLista != "" {
			return RotaReal{}, errors.New("banco-ensaio: --destino-fora-da-lista nao se aplica a OpenRouter (o destino e fixo)")
		}
		r.apiBase = baseDaOpenRouter
		if r.Destino, err = validarDestino(f, CampoChaveOpenRouter, r.apiBase, d); err != nil {
			return RotaReal{}, err
		}
		if r.Tectos.PedidosDia, err = c.inteiroPositivo(CampoTectoPedidosOpenR); err != nil {
			return RotaReal{}, err
		}
		if r.Tectos.MicroUSDDia, r.Tectos.TemUSD, err = c.microUSDOpcional(CampoTectoUSDOpenRouter); err != nil {
			return RotaReal{}, err
		}
	default:
		return RotaReal{}, fmt.Errorf("banco-ensaio: fornecedor desconhecido: %q", string(f))
	}
	return r, nil
}

// modeloDaOpenRouterAceite diz se o nome tem a forma `<autor>/<modelo>` da OpenRouter:
// exactamente dois segmentos, cada um a começar por uma letra ou um algarismo (`..`, `.`, `-` e
// afins não são nomes), e sem o prefixo do adaptador do proxy, em qualquer caixa — é o banco que
// escreve `openrouter/` à frente, e um nome que já o trouxesse, ou que não tivesse autor, dava
// uma rota que o proxy lê de outra maneira.
func modeloDaOpenRouterAceite(m string) bool {
	autor, modelo, ok := strings.Cut(m, "/")
	return ok && segmentoDeNomeAceite(autor) && segmentoDeNomeAceite(modelo) && !strings.Contains(modelo, "/") && !strings.EqualFold(autor, PrefixoDaOpenRouter)
}

// segmentoDeNomeAceite diz se s não é vazio e começa por uma letra ou um algarismo ASCII.
func segmentoDeNomeAceite(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// escolherModelo escolhe o modelo da lista do ficheiro e valida o seu alfabeto: o nome vai para
// a configuração do proxy, e um nome com espaços, aspas ou quebras de linha não entra.
func escolherModelo(campo string, lista []string, pedido string) (string, error) {
	var nomes []string
	for _, n := range lista {
		if n = strings.TrimSpace(n); n != "" {
			nomes = append(nomes, n)
		}
	}
	if len(nomes) == 0 {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaEmFalta}
	}
	escolhido := nomes[0]
	if pedido != "" {
		escolhido = ""
		for _, n := range nomes {
			if n == pedido {
				escolhido = n
			}
		}
		if escolhido == "" {
			return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaForaDaLista}
		}
	}
	if !nomeDeModeloAceite(escolhido) {
		return "", &ErrCampoDasChaves{Campo: campo, Problema: problemaCaracteres}
	}
	return escolhido, nil
}

// nomeDeModeloAceite — letras, dígitos e `. _ : / -`, até 128 bytes.
func nomeDeModeloAceite(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("._:/-", c) >= 0) {
			return false
		}
	}
	return true
}

// textoDeclarado saneia um texto livre do dono para o relatório: só imprimíveis ASCII, 64 bytes.
func textoDeclarado(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && b.Len() < 64 {
			b.WriteRune(r)
		} else if r >= 0x80 && b.Len() < 64 {
			b.WriteByte('?')
		}
	}
	return b.String()
}
