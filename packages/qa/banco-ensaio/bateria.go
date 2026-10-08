// Package bancoensaio é o BANCO DE ENSAIO MÍNIMO da fronteira runtime↔modelo (AOS-512, fase A2).
//
// Corre uma BATERIA fixa e versionada de casos sintéticos contra UMA rota e devolve TAXAS: só
// contagens, fichas em vocabulário fechado e identificadores de caso. Não decide nada — não
// aceita nem recusa um modelo (isso é da fase A3).
//
// # O QUE É MEDIDO É O QUE O NÓ ENVIARIA
//
// O pedido não é montado aqui. Cada caso é um run do Agent Runtime real
// ([agentruntime.Runtime]), com as tools mediadas pelo Reference Monitor real, e o pedido sai
// pelo Model Gateway de produção ([modelgateway.NewProduction]) com a projecção nativa do tail
// ([modelgateway.ProjectNativeVersion]). O banco só acrescenta, à volta: um decorador da porta
// do gateway (as variantes de protocolo da experiência e a observação), e um transporte HTTP
// que conta cada pedido contra o tecto do dia ANTES de o enviar.
//
// # O QUE O NÓ DE ENSAIO NÃO TEM
//
// É um nó mínimo: não compõe o PDP/Cedar, o WORM durável, a sandbox, o despacho durável nem o
// `aos-orq`. As tools são locais e sem efeito externo. O que se mede é a fronteira com o
// modelo — a forma do pedido e o que a resposta faz ao loop —, não a governação do nó.
//
// # O ENSAIO NÃO TOCA EM PRODUÇÃO
//
// Nenhum caminho deste módulo fala com o nó de produção, com a fila ou com o Vault. Os
// documentos são sintéticos e vivem na pasta `bateria/`; as chaves do modo com modelo real
// são do dono, ficam num ficheiro fora do repositório e nunca são impressas nem registadas.
package bancoensaio

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed bateria/casos.json bateria/*.txt
var ficheirosDaBateria embed.FS

// pastaDaBateria é a ÚNICA pasta de onde um caso lê: nenhum documento vem de fora dela.
const pastaDaBateria = "bateria"

// Bateria é o conjunto fixo e versionado de casos sintéticos (T1 a T6, desenho A2 §5.1).
type Bateria struct {
	// Versao é a versão declarada no ficheiro dos casos.
	Versao string `json:"versao"`
	// Marca é a marca de documento sintético que TODOS os documentos têm de trazer na primeira
	// linha. Um documento sem ela não entra na bateria ([CarregarBateria] falha).
	Marca string `json:"marca"`
	// System é o system prompt de todos os runs da bateria.
	System string `json:"system"`
	// Casos, pela ordem do ficheiro.
	Casos []Caso `json:"casos"`

	digest     string
	documentos map[string][]byte
}

// Caso é um caso da bateria: um ou mais nós, corridos por ordem.
type Caso struct {
	ID        string `json:"id"`
	Descricao string `json:"descricao"`
	Nos       []No   `json:"nos"`
}

// No é um nó de um caso: UM run do Agent Runtime.
type No struct {
	ID string `json:"id"`
	// Objectivo é a instrução do run, escrita por nós.
	Objectivo string `json:"objectivo"`
	// Tools são os nomes das tools do banco oferecidas ao run ([ToolsDoBanco]). Vazio ⇒ nó sem
	// tools.
	Tools []string `json:"tools,omitempty"`
	// Exige é o contrato de conclusão do run ([agentruntime.Goal.CompletionRequires]).
	Exige []string `json:"exige,omitempty"`
	// EntradaDe é o id de um nó anterior do mesmo caso: a saída dele entra como `plan_input`.
	EntradaDe string `json:"entrada_de,omitempty"`
	// EntradaDoc é o nome de um documento da bateria que entra como `plan_input`.
	EntradaDoc string `json:"entrada_doc,omitempty"`
	// Nega é o nome de um documento cuja leitura o Reference Monitor do nó de ensaio nega
	// neste run (o caso da tool negada).
	Nega string `json:"nega,omitempty"`
	// FactosDe são os documentos de onde saem os [No.Factos]; cada facto tem de ocorrer,
	// literalmente, em pelo menos um deles (preso por teste e por [CarregarBateria]).
	FactosDe []string `json:"factos_de,omitempty"`
	// Factos são os números e nomes EXACTOS que a saída do run tem de conter para contar como
	// «presentes». É o substituto determinista da recusa do objectivo: verificação de factos
	// sobre dados nossos, sem juiz probabilístico.
	Factos []string `json:"factos,omitempty"`
}

// ErrBateria — a bateria embebida não é utilizável. Fail-closed: o banco não corre.
var ErrBateria = errors.New("banco-ensaio: bateria invalida")

// CarregarBateria lê a bateria embebida, valida-a e calcula o seu digest. É a única maneira
// de obter uma [Bateria].
func CarregarBateria() (*Bateria, error) {
	return carregarBateria(ficheirosDaBateria)
}

func carregarBateria(origem fs.FS) (*Bateria, error) {
	cru, err := fs.ReadFile(origem, pastaDaBateria+"/casos.json")
	if err != nil {
		return nil, fmt.Errorf("%w: casos ilegiveis: %v", ErrBateria, err)
	}
	var b Bateria
	dec := json.NewDecoder(strings.NewReader(string(cru)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%w: casos mal formados: %v", ErrBateria, err)
	}
	if b.Marca == "" || b.Versao == "" || len(b.Casos) == 0 {
		return nil, fmt.Errorf("%w: versao, marca e casos sao obrigatorios", ErrBateria)
	}
	entradas, err := fs.ReadDir(origem, pastaDaBateria)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBateria, err)
	}
	b.documentos = map[string][]byte{}
	soma := sha256.New()
	var nomes []string
	for _, e := range entradas {
		if !e.IsDir() {
			nomes = append(nomes, e.Name())
		}
	}
	sort.Strings(nomes)
	for _, nome := range nomes {
		conteudo, err := fs.ReadFile(origem, pastaDaBateria+"/"+nome)
		if err != nil {
			return nil, fmt.Errorf("%w: %s ilegivel: %v", ErrBateria, nome, err)
		}
		// O digest cobre o nome e o conteúdo de TODOS os ficheiros da pasta, por ordem: mudar
		// um byte de um documento ou de um objectivo muda o digest que o relatório leva.
		fmt.Fprintf(soma, "%s\n%d\n", nome, len(conteudo))
		soma.Write(conteudo)
		if strings.HasSuffix(nome, ".txt") {
			if err := validarDocumento(b.Marca, nome, conteudo); err != nil {
				return nil, err
			}
			b.documentos[nome] = conteudo
		}
	}
	b.digest = "sha256:" + hex.EncodeToString(soma.Sum(nil))
	if err := b.validarCasos(); err != nil {
		return nil, err
	}
	return &b, nil
}

// validarDocumento recusa um documento sem a marca de sintético na primeira linha, e um que
// tenha uma linha a abrir (atrás de brancos) por '<', '[' ou '\'.
//
// A segunda regra existe por causa das variantes de protocolo da experiência dos separadores
// ([Braco]): a projecção de produção escapa as linhas de corpo que abrem por '<' ou '\', e a
// variante sem sinais de menor e maior escapa as que abrem por '['. Com documentos que nunca
// têm linhas dessas, o corpo dos segmentos é byte a byte o mesmo em todos os braços, e a
// única coisa que varia entre eles é o que a experiência quer variar.
func validarDocumento(marca, nome string, conteudo []byte) error {
	linhas := strings.Split(string(conteudo), "\n")
	if !strings.HasPrefix(linhas[0], marca) {
		return fmt.Errorf("%w: o documento %s nao abre pela marca de documento sintetico", ErrBateria, nome)
	}
	for i, l := range linhas {
		aparada := strings.TrimLeft(l, " \t\r")
		if aparada != "" && strings.ContainsRune("<[\\", rune(aparada[0])) {
			return fmt.Errorf("%w: o documento %s tem a linha %d a abrir por um caracter reservado dos separadores", ErrBateria, nome, i+1)
		}
	}
	for _, c := range conteudo {
		if c >= 0x80 {
			return fmt.Errorf("%w: o documento %s nao e ASCII", ErrBateria, nome)
		}
	}
	return nil
}

func (b *Bateria) validarCasos() error {
	vistos := map[string]bool{}
	tools := map[string]bool{}
	for _, t := range ToolsDoBanco() {
		tools[t.Nome] = true
	}
	for _, c := range b.Casos {
		if c.ID == "" || vistos[c.ID] || len(c.Nos) == 0 {
			return fmt.Errorf("%w: caso sem id, repetido ou sem nos (%q)", ErrBateria, c.ID)
		}
		vistos[c.ID] = true
		nos := map[string]bool{}
		for _, n := range c.Nos {
			if n.ID == "" || nos[n.ID] || n.Objectivo == "" {
				return fmt.Errorf("%w: %s tem um no sem id, repetido ou sem objectivo", ErrBateria, c.ID)
			}
			if !identificadorSimples(c.ID) || !identificadorSimples(n.ID) {
				return fmt.Errorf("%w: %s/%s nao e um identificador simples", ErrBateria, c.ID, n.ID)
			}
			for _, t := range append(append([]string(nil), n.Tools...), n.Exige...) {
				if !tools[t] {
					return fmt.Errorf("%w: %s/%s usa uma tool que o banco nao tem (%q)", ErrBateria, c.ID, n.ID, t)
				}
			}
			if n.EntradaDe != "" && !nos[n.EntradaDe] {
				return fmt.Errorf("%w: %s/%s consome um no que nao o antecede (%q)", ErrBateria, c.ID, n.ID, n.EntradaDe)
			}
			for _, doc := range append([]string{n.EntradaDoc, n.Nega}, n.FactosDe...) {
				if doc == "" {
					continue
				}
				if _, ok := b.documentos[doc]; !ok {
					return fmt.Errorf("%w: %s/%s refere um documento fora da bateria (%q)", ErrBateria, c.ID, n.ID, doc)
				}
			}
			for _, f := range n.Factos {
				achado := false
				for _, doc := range n.FactosDe {
					if strings.Contains(string(b.documentos[doc]), f) {
						achado = true
					}
				}
				if f == "" || !achado {
					return fmt.Errorf("%w: %s/%s tem um facto que nao esta nos seus documentos", ErrBateria, c.ID, n.ID)
				}
			}
			nos[n.ID] = true
		}
	}
	return nil
}

// identificadorSimples diz se s só tem letras, dígitos e '-': é o que entra num RunID e num
// identificador de caso do relatório.
func identificadorSimples(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Digest é o digest da bateria: nomes e conteúdo de todos os ficheiros da pasta.
func (b *Bateria) Digest() string { return b.digest }

// Documento devolve um documento da bateria pelo nome. Só existem os da pasta embebida.
func (b *Bateria) Documento(nome string) ([]byte, bool) {
	d, ok := b.documentos[nome]
	return d, ok
}

// NomesDosDocumentos devolve os nomes dos documentos, por ordem alfabética.
func (b *Bateria) NomesDosDocumentos() []string {
	var out []string
	for n := range b.documentos {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Caso devolve um caso pelo id.
func (b *Bateria) Caso(id string) (Caso, bool) {
	for _, c := range b.Casos {
		if c.ID == id {
			return c, true
		}
	}
	return Caso{}, false
}
