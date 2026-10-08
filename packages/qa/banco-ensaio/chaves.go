package bancoensaio

import (
	"bufio"
	"errors"
	"fmt"
	"math"
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
	// FornecedorFalso — o provider falso do banco (modos `falso` e `proxy`).
	FornecedorFalso Fornecedor = "falso"
)

// LerFornecedor valida o nome de um fornecedor do modo com modelo real.
func LerFornecedor(s string) (Fornecedor, error) {
	switch Fornecedor(s) {
	case FornecedorKimi, FornecedorAnthropic:
		return Fornecedor(s), nil
	}
	return "", fmt.Errorf("banco-ensaio: fornecedor desconhecido: %q (aceites: kimi, anthropic)", s)
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
	problemaEmFalta           = "em falta"
	problemaMarcadorDoExemplo = "ainda com o marcador do exemplo"
	problemaIlegivel          = "ilegivel"
	problemaZero              = "zero ou negativo"
	problemaForaDaLista       = "nao esta na lista do ficheiro"
	problemaCaracteres        = "com caracteres que o banco nao aceita"
)

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
	c := &Chaves{campos: map[string]string{}}
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
		c.campos[campo] = strings.TrimSpace(valor)
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
	return v, nil
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
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, &ErrCampoDasChaves{Campo: campo, Problema: problemaIlegivel}
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
	usd, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaIlegivel}
	}
	if usd <= 0 {
		return 0, false, &ErrCampoDasChaves{Campo: campo, Problema: problemaZero}
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
func (c *Chaves) Rota(f Fornecedor, modelo string) (RotaReal, error) {
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
		if r.Tectos.PedidosDia, err = c.inteiroPositivo(CampoTectoPedidosAnthro); err != nil {
			return RotaReal{}, err
		}
		if r.Tectos.MicroUSDDia, r.Tectos.TemUSD, err = c.microUSDOpcional(CampoTectoUSDAnthropic); err != nil {
			return RotaReal{}, err
		}
		// A região é uma declaração livre do dono; um marcador do exemplo fica «nao declarada».
		if v := c.campos[CampoAnthropicRegiaoProc]; v != "" && !marcadorDoExemplo(v) {
			r.RegiaoDeclarada = textoDeclarado(v)
		}
	default:
		return RotaReal{}, fmt.Errorf("banco-ensaio: fornecedor desconhecido: %q", string(f))
	}
	return r, nil
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
