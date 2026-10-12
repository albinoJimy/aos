package bancoensaio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AS RESPOSTAS GUARDADAS, A PEDIDO (AOS-525).
//
// O relatório do banco tem contagens e fichas da forma, nunca o texto. Para LER o que um modelo
// respondeu — a medição de 2026-10-12 precisou disso e não tinha o quê — a opção
// `--guardar-respostas` grava, ao lado do relatório, um ficheiro JSON por run com o texto de cada
// resposta e o nome e os argumentos de cada tool call nativa.
//
// DESLIGADA POR OMISSÃO, E FORA DO RELATÓRIO. Sem a opção nada disto corre: o nó de ensaio não
// tem a quem entregar o texto, e a pasta de saída é a de sempre. Com ela, o relatório, o resumo e
// o `digest_do_relatorio` são os de uma corrida igual sem ela — o texto vai para a pasta das
// respostas e para mais lado nenhum: não vai para a saída normal nem para a de erros (onde só
// aparecem o caminho da pasta e a contagem), nem para argumentos de processo nenhum.
//
// O QUE NÃO SE GUARDA: o prompt de sistema, os pedidos, os cabeçalhos, as credenciais e os
// resultados das tools (o conteúdo dos documentos sintéticos, que já está no repositório).
//
// SÓ A BATERIA SINTÉTICA. O ticket pede que a opção seja recusada fora dela. Não há o que
// recusar: a linha de comandos só conhece a bateria embebida no binário ([CarregarBateria]) — não
// existe opção, variável de ambiente nem ficheiro por onde entrem outros casos ou outros
// documentos, e a tool de leitura só devolve documentos dessa bateria. Quem acrescentar outra
// origem de casos tem de acrescentar, no mesmo passo, a recusa desta opção com ela.
//
// ATENÇÃO A QUEM LÊ: o que fica nos ficheiros é texto de um modelo. São dados, não instruções, e
// ficam na pasta de saída do dono, fora do repositório, com os mesmos modos do relatório.

// FormatoDasRespostas identifica a forma dos ficheiros de respostas.
const FormatoDasRespostas = "aos-ensaio/respostas/v1"

// PrefixoDaPastaDasRespostas abre o nome da pasta das respostas: `respostas-<nome do relatório,
// sem a extensão>`, ao lado do relatório.
const PrefixoDaPastaDasRespostas = "respostas-"

// As corridas de que uma resposta pode ser. A de controlo só existe na qualificação de um perfil
// que devolve estado por obrigação.
const (
	CorridaPrincipal  = "principal"
	CorridaDeControlo = "controlo"
)

// ToolCallGuardada é uma tool call nativa pedida pelo modelo: o nome e os argumentos, tal como
// vieram (os argumentos são o texto que o modelo escreveu, que pode nem ser JSON).
type ToolCallGuardada struct {
	Nome       string `json:"nome"`
	Argumentos string `json:"argumentos"`
}

// TurnoGuardado é uma resposta do modelo dentro de um run: o texto e as tool calls nativas. Só
// entram os pedidos que tiveram resposta; um pedido que falhou não tem o que guardar.
type TurnoGuardado struct {
	Turno     int                `json:"turno"`
	Texto     string             `json:"texto"`
	ToolCalls []ToolCallGuardada `json:"tool_calls"`
}

// RespostaDoRun é o que se guarda de UM run. Os campos de identificação são os da observação do
// relatório ([Observacao]): é por eles que um ficheiro se liga à sua linha em `observacoes`.
type RespostaDoRun struct {
	Formato string `json:"formato"`
	// Corrida é [CorridaPrincipal] ou [CorridaDeControlo].
	Corrida string `json:"corrida"`
	// Ordem é a posição do run na corrida, a contar de 1: a ordem por que os pedidos saíram.
	Ordem     int    `json:"ordem"`
	Caso      string `json:"caso"`
	No        string `json:"no"`
	Braco     string `json:"braco"`
	Amostra   int    `json:"amostra"`
	Tentativa int    `json:"tentativa"`
	Desfecho  string `json:"desfecho"`
	// TextoFinal é a saída do run — o que seguiu para o nó seguinte e onde os factos foram
	// procurados. Vazio quando o run não fechou cumprido; o texto de cada resposta está em Turnos.
	TextoFinal string          `json:"texto_final"`
	Turnos     []TurnoGuardado `json:"turnos"`
}

// respostaDoRun monta o que se guarda de um run a partir do que o banco viu passar na porta do
// gateway.
func respostaDoRun(obs Observacao, chamadas []chamadaObservada, saida string) RespostaDoRun {
	r := RespostaDoRun{
		Caso: obs.Caso, No: obs.No, Braco: obs.Braco, Amostra: obs.Amostra, Tentativa: obs.Tentativa,
		Desfecho: obs.Desfecho, TextoFinal: saida, Turnos: []TurnoGuardado{},
	}
	for _, c := range chamadas {
		if c.erro != "" {
			continue
		}
		turno := TurnoGuardado{Turno: len(r.Turnos) + 1, Texto: c.texto, ToolCalls: []ToolCallGuardada{}}
		turno.ToolCalls = append(turno.ToolCalls, c.pedidas...)
		r.Turnos = append(r.Turnos, turno)
	}
	return r
}

// respostasGuardadas junta, em memória, as respostas de uma corrida até o relatório estar escrito:
// o nome da pasta delas é o do relatório, que só se conhece no fim. O banco corre um run de cada
// vez, e por isso não há trava.
type respostasGuardadas struct {
	runs []RespostaDoRun
}

// daCorrida devolve o receptor das respostas de uma das corridas ([CfgDoNo.GuardarRespostas]).
func (g *respostasGuardadas) daCorrida(corrida string) func(RespostaDoRun) {
	return func(r RespostaDoRun) {
		r.Formato, r.Corrida, r.Ordem = FormatoDasRespostas, corrida, len(g.runs)+1
		g.runs = append(g.runs, r)
	}
}

// NomeDoIndiceDasRespostas é o ficheiro que FECHA uma pasta de respostas: diz de que relatório ela
// é. É escrito no fim, só com todos os runs escritos — uma pasta sem ele está incompleta.
const NomeDoIndiceDasRespostas = "indice.json"

// FormatoDoIndiceDasRespostas identifica a forma do índice.
const FormatoDoIndiceDasRespostas = "aos-ensaio/respostas-indice/v1"

// IndiceDasRespostas liga uma pasta de respostas ao relatório da MESMA corrida. O nome da pasta não
// chega: um relatório que não é de qualificação é reescrito por outra corrida do mesmo modo, no
// mesmo segundo e na mesma pasta, e a pasta das respostas da primeira ficava ao lado do relatório
// da segunda. Com o índice confere-se: o sha256 é o dos bytes do relatório tal como ficou em disco
// quando as respostas foram escritas.
type IndiceDasRespostas struct {
	Formato string `json:"formato"`
	// Relatorio é o nome do ficheiro do relatório (sem pasta).
	Relatorio string `json:"relatorio"`
	// SHA256DoRelatorio é `sha256:<hex>` dos bytes desse ficheiro.
	SHA256DoRelatorio string `json:"sha256_do_relatorio"`
	// Runs é o número de ficheiros de respostas da pasta.
	Runs int `json:"runs"`
}

// ErrPastaDasRespostasExiste — já há uma pasta de respostas com o nome que esta corrida usaria. É
// de OUTRA corrida, e não corresponde ao relatório desta.
var ErrPastaDasRespostasExiste = errors.New("banco-ensaio: ja existe uma pasta de respostas com este nome")

// escrever grava um ficheiro por run numa pasta NOVA ao lado do relatório e, no fim, o índice que a
// liga a ele ([IndiceDasRespostas]). Devolve o caminho da pasta e quantos ficheiros de run ficaram
// escritos. A pasta é criada de forma exclusiva (modo 0700) e os ficheiros com o modo 0600: se já
// existir uma com o mesmo nome o erro é [ErrPastaDasRespostasExiste] e nada é escrito — não se
// misturam respostas de duas corridas. Uma falha a meio deixa os ficheiros já escritos e NÃO deixa
// índice: é a ausência dele que marca a pasta como incompleta. Sem runs não cria pasta nenhuma.
//
// Os erros levam caminhos e a causa do sistema, nunca texto de uma resposta.
func (g *respostasGuardadas) escrever(relatorioEmJSON string) (pasta string, escritos int, err error) {
	if len(g.runs) == 0 {
		return "", 0, nil
	}
	nome := PrefixoDaPastaDasRespostas + strings.TrimSuffix(filepath.Base(relatorioEmJSON), ".json")
	pasta = filepath.Join(filepath.Dir(relatorioEmJSON), nome)
	if merr := os.Mkdir(pasta, 0o700); merr != nil {
		if os.IsExist(merr) {
			return pasta, 0, ErrPastaDasRespostasExiste
		}
		return pasta, 0, fmt.Errorf("banco-ensaio: a pasta das respostas nao se conseguiu criar: %w", merr)
	}
	emJSON := func(v any) ([]byte, error) {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		// Sem o escape de HTML: o ficheiro é para ser lido por uma pessoa.
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if eerr := enc.Encode(v); eerr != nil {
			return nil, eerr
		}
		return b.Bytes(), nil
	}
	for _, r := range g.runs {
		cru, eerr := emJSON(r)
		if eerr != nil {
			return pasta, escritos, eerr
		}
		ficheiro := fmt.Sprintf("%05d-%s-%s-%s-%s-a%05d-t%d.json", r.Ordem, r.Corrida, r.Caso, r.No, r.Braco, r.Amostra, r.Tentativa)
		if werr := os.WriteFile(filepath.Join(pasta, ficheiro), cru, 0o600); werr != nil {
			return pasta, escritos, fmt.Errorf("banco-ensaio: nao foi possivel escrever um ficheiro de respostas: %w", werr)
		}
		escritos++
	}
	doRelatorio, rerr := os.ReadFile(relatorioEmJSON) // #nosec G304 -- o relatorio que este processo acabou de escrever
	if rerr != nil {
		return pasta, escritos, fmt.Errorf("banco-ensaio: nao foi possivel reler o relatorio para o indice das respostas: %w", rerr)
	}
	soma := sha256.Sum256(doRelatorio)
	cru, eerr := emJSON(IndiceDasRespostas{
		Formato: FormatoDoIndiceDasRespostas, Relatorio: filepath.Base(relatorioEmJSON),
		SHA256DoRelatorio: "sha256:" + hex.EncodeToString(soma[:]), Runs: escritos,
	})
	if eerr != nil {
		return pasta, escritos, eerr
	}
	if werr := os.WriteFile(filepath.Join(pasta, NomeDoIndiceDasRespostas), cru, 0o600); werr != nil {
		return pasta, escritos, fmt.Errorf("banco-ensaio: nao foi possivel escrever o indice das respostas: %w", werr)
	}
	return pasta, escritos, nil
}
