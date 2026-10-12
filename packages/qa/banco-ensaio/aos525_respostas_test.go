package bancoensaio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-525 — AS RESPOSTAS GUARDADAS, A PEDIDO. Com `--guardar-respostas` fica um ficheiro por run
// ao lado do relatório; sem ela a pasta de saída é a de sempre; e, com ela ou sem ela, o
// relatório, o resumo e o digest do relatório são os mesmos, e o texto guardado não aparece em
// mais lado nenhum.

// aos525Corrida é uma execução com a sua pasta de saída lida: os ficheiros do topo (nome ⇒ bytes),
// as subpastas, e as respostas guardadas, pela ordem dos nomes dos ficheiros.
type aos525Corrida struct {
	execucao
	ficheiros map[string][]byte
	subpastas []string
	// pastaDasRespostas é o caminho da pasta das respostas (vazio se não existe); cru é o conteúdo
	// de todos os seus ficheiros, de seguida.
	pastaDasRespostas string
	respostas         []RespostaDoRun
	cru               string
	// indice é o `indice.json` da pasta das respostas; nil se a pasta não o tem.
	indice *IndiceDasRespostas
}

// aos525SHA256 é o `sha256:<hex>` de uns bytes, como o índice das respostas o escreve.
func aos525SHA256(cru []byte) string {
	soma := sha256.Sum256(cru)
	return "sha256:" + hex.EncodeToString(soma[:])
}

// aos525Ler lê o que uma execução deixou na sua pasta de saída.
func aos525Ler(t *testing.T, e execucao) aos525Corrida {
	t.Helper()
	c := aos525Corrida{execucao: e, ficheiros: map[string][]byte{}}
	entradas, err := os.ReadDir(e.saida)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range entradas {
		caminho := filepath.Join(e.saida, d.Name())
		if !d.IsDir() {
			cru, err := os.ReadFile(caminho)
			if err != nil {
				t.Fatal(err)
			}
			c.ficheiros[d.Name()] = cru
			continue
		}
		c.subpastas = append(c.subpastas, d.Name())
		if !strings.HasPrefix(d.Name(), PrefixoDaPastaDasRespostas) {
			continue
		}
		c.pastaDasRespostas = caminho
		dentro, err := os.ReadDir(caminho)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range dentro {
			cru, err := os.ReadFile(filepath.Join(caminho, f.Name()))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(cru))
			dec.DisallowUnknownFields()
			if f.Name() == NomeDoIndiceDasRespostas {
				c.indice = &IndiceDasRespostas{}
				if err := dec.Decode(c.indice); err != nil {
					t.Fatalf("o indice das respostas nao se le: %v", err)
				}
				if runtime.GOOS != "windows" {
					if info, _ := f.Info(); info.Mode().Perm() != 0o600 {
						t.Errorf("o indice tem o modo %o, quer 600", info.Mode().Perm())
					}
				}
				continue
			}
			var r RespostaDoRun
			if err := dec.Decode(&r); err != nil {
				t.Fatalf("%s nao e um ficheiro de respostas: %v", f.Name(), err)
			}
			if runtime.GOOS != "windows" {
				// No Windows o modo de um ficheiro não tem os bits do dono, do grupo e dos outros.
				if info, _ := f.Info(); info.Mode().Perm() != 0o600 {
					t.Errorf("%s tem o modo %o, quer 600", f.Name(), info.Mode().Perm())
				}
			}
			c.respostas = append(c.respostas, r)
			c.cru += string(cru)
		}
		if runtime.GOOS != "windows" {
			if info, _ := d.Info(); info.Mode().Perm() != 0o700 {
				t.Errorf("a pasta das respostas tem o modo %o, quer 700", info.Mode().Perm())
			}
		}
	}
	sort.Strings(c.subpastas)
	return c
}

// aos525Par corre a MESMA linha de comandos duas vezes — sem a opção e com ela —, cada uma na sua
// pasta e com o seu ambiente novo, e devolve as duas. `comSaida` recebe a pasta de saída e
// devolve os argumentos.
func aos525Par(t *testing.T, novoAmb func() Ambiente, comSaida func(saida string) []string) (desligada, ligada aos525Corrida) {
	t.Helper()
	correr := func(extra ...string) aos525Corrida {
		saida := filepath.Join(t.TempDir(), "saida")
		e := executar(t, novoAmb(), append(comSaida(saida), extra...)...)
		e.saida = saida
		return aos525Ler(t, e)
	}
	return correr(), correr("--guardar-respostas")
}

// aos525SemOCaminho tira de uma saída o caminho da pasta, que muda de corrida para corrida.
func aos525SemOCaminho(c aos525Corrida, s string) string {
	return strings.ReplaceAll(s, c.saida, "<saida>")
}

// aos525ConferirPar confere o que os critérios pedem do par: com a opção desligada não há pasta
// nova; com ela ligada há UMA a mais, e tudo o resto — os ficheiros do topo byte a byte, a saída
// normal, e a de erros tirando a linha que diz onde ficaram as respostas — é igual.
func aos525ConferirPar(t *testing.T, desligada, ligada aos525Corrida) {
	t.Helper()
	if desligada.codigo != ligada.codigo {
		t.Fatalf("os codigos de saida diferem: %d sem a opcao, %d com ela\n%s", desligada.codigo, ligada.codigo, ligada.stderr)
	}
	if desligada.pastaDasRespostas != "" || strings.Contains(desligada.stderr, "respostas guardadas") {
		t.Errorf("sem a opcao ha pasta de respostas, ou fala-se delas: %v\n%s", desligada.subpastas, desligada.stderr)
	}
	if ligada.pastaDasRespostas == "" || len(ligada.subpastas) != len(desligada.subpastas)+1 {
		t.Fatalf("com a opcao tinha de haver UMA pasta a mais, a das respostas: %v contra %v\n%s", ligada.subpastas, desligada.subpastas, ligada.stderr)
	}
	if len(desligada.ficheiros) == 0 || len(desligada.ficheiros) != len(ligada.ficheiros) {
		t.Fatalf("os ficheiros do topo diferem: %d sem a opcao, %d com ela", len(desligada.ficheiros), len(ligada.ficheiros))
	}
	for nome, cru := range desligada.ficheiros {
		if !bytes.Equal(cru, ligada.ficheiros[nome]) {
			t.Errorf("%s nao e byte a byte o mesmo com a opcao ligada", nome)
		}
	}
	if a, b := aos525SemOCaminho(desligada, desligada.stdout), aos525SemOCaminho(ligada, ligada.stdout); a != b {
		t.Errorf("a saida normal mudou com a opcao:\n%s\n---\n%s", a, b)
	}
	var semALinha []string
	linhas := 0
	for _, l := range strings.SplitAfter(aos525SemOCaminho(ligada, ligada.stderr), "\n") {
		if strings.HasPrefix(l, "aos-ensaio: respostas guardadas: ") {
			linhas++
			continue
		}
		semALinha = append(semALinha, l)
	}
	if linhas != 1 || strings.Join(semALinha, "") != aos525SemOCaminho(desligada, desligada.stderr) {
		t.Errorf("a saida de erros so podia ganhar UMA linha, a das respostas (ganhou %d):\n%s", linhas, ligada.stderr)
	}
	quer := "aos-ensaio: respostas guardadas: " + strconv.Itoa(len(ligada.respostas)) + " ficheiro(s), um por run, em " + ligada.pastaDasRespostas + " "
	if !strings.Contains(ligada.stderr, quer) {
		t.Errorf("a linha das respostas tinha de dizer quantas e onde (%q):\n%s", quer, ligada.stderr)
	}
	// O ÍNDICE liga a pasta ao relatório que está ao lado: o nome do ficheiro, o sha256 dos seus
	// bytes em disco e o número de runs.
	i := ligada.indice
	if i == nil {
		t.Fatalf("a pasta das respostas nao tem %s", NomeDoIndiceDasRespostas)
	}
	doRelatorio, tem := ligada.ficheiros[i.Relatorio]
	if !tem || !strings.HasSuffix(i.Relatorio, ".json") || i.Formato != FormatoDoIndiceDasRespostas || i.Runs != len(ligada.respostas) ||
		i.SHA256DoRelatorio != aos525SHA256(doRelatorio) || filepath.Base(ligada.pastaDasRespostas) != PrefixoDaPastaDasRespostas+strings.TrimSuffix(i.Relatorio, ".json") {
		t.Errorf("o indice nao e o do relatorio ao lado: %+v", *i)
	}
}

// aos525ConferirRuns confere que há UM ficheiro por run, pela ordem da corrida, cada um com a
// identificação da sua observação no relatório, e que o que lá está bate com as contagens dela:
// tantos turnos quantas as respostas, tantas tool calls quantas o relatório contou.
func aos525ConferirRuns(t *testing.T, c aos525Corrida, b *Bateria) {
	t.Helper()
	r := aos516Relatorio(t, c.saida)
	if len(r.Observacoes) == 0 || len(c.respostas) != len(r.Observacoes) {
		t.Fatalf("ficheiros de respostas: %d; observacoes no relatorio: %d", len(c.respostas), len(r.Observacoes))
	}
	comTools, comTexto, doFalso := 0, 0, 0
	for i, o := range r.Observacoes {
		g := c.respostas[i]
		if g.Formato != FormatoDasRespostas || g.Corrida != CorridaPrincipal || g.Ordem != i+1 ||
			g.Caso != o.Caso || g.No != o.No || g.Braco != o.Braco || g.Amostra != o.Amostra || g.Tentativa != o.Tentativa || g.Desfecho != o.Desfecho {
			t.Fatalf("run %d: o ficheiro e de %+v e a observacao e %s/%s braco %s amostra %d tentativa %d (%s)", i+1,
				struct {
					Caso, No, Braco    string
					Amostra, Tentativa int
					Desfecho           string
				}{g.Caso, g.No, g.Braco, g.Amostra, g.Tentativa, g.Desfecho}, o.Caso, o.No, o.Braco, o.Amostra, o.Tentativa, o.Desfecho)
		}
		pedidas := 0
		for n, turno := range g.Turnos {
			if turno.Turno != n+1 {
				t.Errorf("run %d: turno fora de ordem", i+1)
			}
			for _, tc := range turno.ToolCalls {
				pedidas++
				var args struct {
					Path  *string `json:"path"`
					Texto *string `json:"texto"`
				}
				if err := json.Unmarshal([]byte(tc.Argumentos), &args); err != nil {
					t.Fatalf("run %d: os argumentos guardados nao sao os que o falso mandou: %q", i+1, tc.Argumentos)
				}
				switch tc.Nome {
				case ToolArquivo:
					if args.Path == nil {
						t.Errorf("run %d: a tool call de leitura nao traz o argumento path: %q", i+1, tc.Argumentos)
					} else if _, existe := b.Documento(*args.Path); !existe {
						t.Errorf("run %d: o falso pede documentos da bateria, e o argumento guardado nao e nenhum: %q", i+1, tc.Argumentos)
					}
				case ToolGuardar:
					if args.Texto == nil {
						t.Errorf("run %d: a tool call de entrega nao traz o argumento texto", i+1)
					}
				default:
					t.Errorf("run %d: tool call com um nome que o banco nao oferece: %q", i+1, tc.Nome)
				}
			}
		}
		if len(g.Turnos) != o.Turnos || pedidas != o.ToolCalls {
			t.Errorf("run %d (%s/%s): %d turnos e %d tool calls guardados; o relatorio conta %d e %d", i+1, o.Caso, o.No, len(g.Turnos), pedidas, o.Turnos, o.ToolCalls)
		}
		if pedidas > 0 {
			comTools++
		}
		if o.Desfecho == DesfechoCumprido {
			// O texto final é o da última resposta do run.
			if g.TextoFinal == "" || len(g.Turnos) == 0 || g.TextoFinal != g.Turnos[len(g.Turnos)-1].Texto {
				t.Errorf("run %d (%s/%s): cumprido sem o texto final do modelo", i+1, o.Caso, o.No)
			}
			comTexto++
			if strings.HasPrefix(g.TextoFinal, SentinelaDeTexto) {
				doFalso++
			}
		} else if g.TextoFinal != "" {
			t.Errorf("run %d (%s/%s): desfecho %s com texto final", i+1, o.Caso, o.No, o.Desfecho)
		}
	}
	// doFalso: o texto guardado é o que o provider falso ESCREVEU (abre pela sentinela dele).
	if comTools == 0 || comTexto == 0 || doFalso == 0 {
		t.Errorf("a corrida nao exercitou o que se queria conferir: %d runs com tool calls, %d com texto final, %d com o texto do falso", comTools, comTexto, doFalso)
	}
}

// MODO FALSO. Sem a opção, a pasta de saída tem o relatório e o resumo e mais nada; com ela, tem
// a mais a pasta das respostas, com um ficheiro por run, e o resto é byte a byte o mesmo.
func TestAOS525_Falso_DesligadaNadaMudaELigadaUmFicheiroPorRun(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	desligada, ligada := aos525Par(t, func() Ambiente { return Ambiente{} }, func(saida string) []string {
		return []string{"falso", "--amostras", "1", "--saida", saida}
	})
	if desligada.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", desligada.codigo, desligada.stderr)
	}
	if len(desligada.ficheiros) != 2 || len(desligada.subpastas) != 0 {
		t.Fatalf("sem a opcao a pasta de saida tem o relatorio e o resumo, e mais nada: %d ficheiros, pastas %v", len(desligada.ficheiros), desligada.subpastas)
	}
	aos525ConferirPar(t, desligada, ligada)
	aos525ConferirRuns(t, ligada, b)
	// O nome da pasta é o do relatório: é por ele que as duas coisas se ligam.
	for nome := range ligada.ficheiros {
		if base, eJSON := strings.CutSuffix(nome, ".json"); eJSON && filepath.Base(ligada.pastaDasRespostas) != PrefixoDaPastaDasRespostas+base {
			t.Errorf("a pasta das respostas chama-se %s e o relatorio %s", filepath.Base(ligada.pastaDasRespostas), nome)
		}
	}
	// A opção escrita por extenso como desligada é a omissão.
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, Ambiente{}, "falso", "--amostras", "1", "--guardar-respostas=false", "--saida", saida)
	e.saida = saida
	if c := aos525Ler(t, e); len(c.subpastas) != 0 || len(c.ficheiros) != 2 || strings.Contains(c.stderr, "respostas") {
		t.Errorf("--guardar-respostas=false deixou pastas %v\n%s", c.subpastas, c.stderr)
	}
}

// MODO PROXY, com o lançador de teste a fazer de proxy (sem Docker: a imagem real do proxy não
// corre aqui — isso é dos testes `proxyreal`). O caminho da linha de comandos é o do modo proxy.
func TestAOS525_Proxy_DesligadaNadaMudaELigadaUmFicheiroPorRun(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	desligada, ligada := aos525Par(t, func() Ambiente { return Ambiente{Lancador: &lancadorDeTeste{t: t}} }, func(saida string) []string {
		return []string{"proxy", "--binario-do-falso", "nao-interessa", "--amostras", "1", "--saida", saida}
	})
	if desligada.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", desligada.codigo, desligada.stderr)
	}
	if r := aos516Relatorio(t, ligada.saida); r.Modo != ModoProxy {
		t.Fatalf("o relatorio e do modo %q", r.Modo)
	}
	aos525ConferirPar(t, desligada, ligada)
	aos525ConferirRuns(t, ligada, b)
}

// A QUALIFICAÇÃO pelo subcomando `qualificar`: a opção segue para a corrida, as respostas ficam na
// pasta de saída e FORA de `pacote/`, e o relatório — com o digest de si próprio — é o mesmo.
func TestAOS525_Qualificar_ODigestDoRelatorioNaoMuda(t *testing.T) {
	perfil := aos516Ficheiro(t, aos519PerfilDeProducao)
	desligada, ligada := aos525Par(t, func() Ambiente { return Ambiente{} }, func(saida string) []string {
		return []string{"qualificar", "falso", "--perfil", perfil, "--fatia", "10", "--board", routeentrytest.Board,
			"--regiao", routeentrytest.Region, "--roteiro", "cumpre", "--saida", saida}
	})
	if desligada.codigo != SaidaQualificadoEmEnsaio {
		t.Fatalf("codigo %d\n%s", desligada.codigo, desligada.stderr)
	}
	aos525ConferirPar(t, desligada, ligada)
	digests := map[string]string{}
	for nome, c := range map[string]aos525Corrida{"desligada": desligada, "ligada": ligada} {
		for ficheiro, cru := range c.ficheiros {
			if !strings.HasSuffix(ficheiro, ".json") {
				continue
			}
			d, err := VerificarDigestDoRelatorio(cru)
			if err != nil {
				t.Fatalf("%s: o digest do relatorio nao confere: %v", nome, err)
			}
			digests[nome] = d
		}
	}
	if digests["ligada"] == "" || digests["ligada"] != digests["desligada"] {
		t.Errorf("o digest do relatorio mudou com a opcao: %q contra %q", digests["ligada"], digests["desligada"])
	}
	if r := aos516Relatorio(t, ligada.saida); len(ligada.respostas) != len(r.Observacoes) || len(ligada.respostas) == 0 {
		t.Errorf("respostas guardadas: %d; observacoes: %d", len(ligada.respostas), len(r.Observacoes))
	}
	if len(ligada.subpastas) != 1 {
		t.Errorf("um ensaio nao deixa pacote: a unica pasta e a das respostas, e ha %v", ligada.subpastas)
	}
}

// A CORRIDA DE CONTROLO da qualificação também se guarda, com o seu nome: os runs dela repetem o
// caso, o nó, a amostra e a tentativa dos da corrida principal, e não se podem confundir.
func TestAOS525_Qualificacao_OControloGuardaSeComOSeuNome(t *testing.T) {
	e, r := aos518Qualificar(t, aos518PerfilObrigatorio, "--estado", "exige", "--guardar-respostas")
	if e.codigo != SaidaQualificadoEmEnsaio || r.QualificacaoDoModelo.Controlo == nil || r.QualificacaoDoModelo.Controlo.Resultado != ControloFalhouComoDevido {
		t.Fatalf("codigo %d, controlo %+v\n%s", e.codigo, r.QualificacaoDoModelo.Controlo, e.stderr)
	}
	c := aos525Ler(t, e)
	porCorrida := map[string]int{}
	for i, g := range c.respostas {
		porCorrida[g.Corrida]++
		if g.Ordem != i+1 || (g.Corrida == CorridaPrincipal && porCorrida[CorridaDeControlo] > 0) {
			t.Fatalf("run %d: fora de ordem (ordem %d, corrida %s)", i+1, g.Ordem, g.Corrida)
		}
	}
	if porCorrida[CorridaPrincipal] != len(r.Observacoes) || porCorrida[CorridaDeControlo] == 0 || len(porCorrida) != 2 {
		t.Errorf("runs por corrida = %v; observacoes da principal = %d", porCorrida, len(r.Observacoes))
	}
	dentro, _ := os.ReadDir(c.pastaDasRespostas)
	if len(dentro) != len(c.respostas)+1 || c.indice == nil || c.indice.Runs != len(c.respostas) {
		t.Errorf("ficheiros %d, runs %d: dois runs ficaram com o mesmo nome", len(dentro), len(c.respostas))
	}
}

// Os marcadores do teste de fuga: um no texto da resposta, outro nos argumentos de uma tool call.
const (
	aos525MarcadorDoTexto       = "MARCADOR-AOS525-TEXTO-DA-RESPOSTA-9f2d41"
	aos525MarcadorDosArgumentos = "marcador-aos525-argumentos-7c1e08.txt"
)

// aos525Fugas é A função de procura do teste de fuga: devolve os sítios (por ordem) em que algum
// dos marcadores aparece.
func aos525Fugas(sitios map[string]string, marcadores ...string) []string {
	var achados []string
	for sitio, conteudo := range sitios {
		for _, m := range marcadores {
			if strings.Contains(conteudo, m) {
				achados = append(achados, sitio+": "+m)
			}
		}
	}
	sort.Strings(achados)
	return achados
}

// aos525ModeloMarcado faz de modelo: com tools por pedir, pede-as (e pede a mais um documento cujo
// nome é o marcador dos argumentos); de resto responde com o marcador do texto.
func aos525ModeloMarcado() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		var p pedidoAoFalso
		_ = json.Unmarshal(corpo, &p)
		comTool := false
		var doUtilizador []string
		for _, m := range p.Messages {
			switch m.Role {
			case "tool":
				comTool = true
			case "user":
				doUtilizador = append(doUtilizador, m.texto())
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if len(p.Tools) > 0 && !comTool {
			_, _ = w.Write(respostaComToolCalls(p, strings.Join(doUtilizador, "\n")+"\n"+aos525MarcadorDosArgumentos, 10))
			return
		}
		_, _ = w.Write(respostaDeTexto(aos525MarcadorDoTexto, 10))
	})
}

// O TESTE DE FUGA. Uma resposta com um marcador único no texto e outro nos argumentos de uma tool
// call: os dois ficam nos ficheiros de respostas e em MAIS LADO NENHUM — nem no relatório, nem no
// resumo, nem na saída normal, nem na de erros.
func TestAOS525_Fuga_OTextoGuardadoSoEstaNosFicheirosDeRespostas(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	lanc := &lancadorDeTeste{t: t, devolver: aos525ModeloMarcado()}
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, Ambiente{Lancador: lanc}, "proxy", "--binario-do-falso", "nao-interessa", "--amostras", "1", "--guardar-respostas", "--saida", saida)
	e.saida = saida
	if e.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
	c := aos525Ler(t, e)
	foraDasRespostas := map[string]string{"stdout": e.stdout, "stderr": e.stderr}
	for nome, cru := range c.ficheiros {
		foraDasRespostas[nome] = string(cru)
	}
	if len(c.ficheiros) != 2 {
		t.Fatalf("esperava o relatorio e o resumo: %d ficheiros", len(c.ficheiros))
	}
	if achados := aos525Fugas(foraDasRespostas, aos525MarcadorDoTexto, aos525MarcadorDosArgumentos); len(achados) != 0 {
		t.Errorf("FUGA — o texto guardado aparece fora dos ficheiros de respostas: %v", achados)
	}

	// CONTROLO NEGATIVO 1: a mesma função encontra os dois marcadores onde eles têm de estar. Sem
	// isto, o teste passava com um marcador que o modelo falso nunca chegou a escrever.
	nasRespostas := aos525Fugas(map[string]string{"respostas": c.cru}, aos525MarcadorDoTexto, aos525MarcadorDosArgumentos)
	if len(nasRespostas) != 2 {
		t.Fatalf("os marcadores tinham de estar nos ficheiros de respostas: %v", nasRespostas)
	}
	// CONTROLO NEGATIVO 2: se o texto guardado fosse para o relatório, a mesma função apanhava-o —
	// aqui com o relatório verdadeiro e o texto de um run posto lá dentro.
	textoDeUmRun := ""
	for _, g := range c.respostas {
		if g.TextoFinal != "" {
			textoDeUmRun = g.TextoFinal
		}
	}
	for nome, conteudo := range foraDasRespostas {
		adulterado := map[string]string{}
		for k, v := range foraDasRespostas {
			adulterado[k] = v
		}
		adulterado[nome] = conteudo + textoDeUmRun
		if achados := aos525Fugas(adulterado, aos525MarcadorDoTexto, aos525MarcadorDosArgumentos); len(achados) != 1 || !strings.HasPrefix(achados[0], nome+": ") {
			t.Errorf("uma fuga para %s nao era apanhada: %v", nome, achados)
		}
	}

	// Onde estão: o do texto no texto final e no último turno; o dos argumentos numa tool call.
	noTexto, nosArgumentos := 0, 0
	for _, g := range c.respostas {
		if g.TextoFinal == aos525MarcadorDoTexto {
			noTexto++
		}
		for _, turno := range g.Turnos {
			for _, tc := range turno.ToolCalls {
				if strings.Contains(tc.Argumentos, aos525MarcadorDosArgumentos) {
					nosArgumentos++
				}
			}
		}
	}
	if noTexto == 0 || nosArgumentos == 0 {
		t.Errorf("marcador no texto final de %d runs e nos argumentos de %d tool calls", noTexto, nosArgumentos)
	}
	// O QUE NÃO SE GUARDA: o prompt de sistema e a credencial da rota.
	if achados := aos525Fugas(map[string]string{"respostas": c.cru}, b.System, chaveMestraDeTeste); len(achados) != 0 {
		t.Errorf("os ficheiros de respostas levam o que nao deviam: %v", achados)
	}
	// O RASTO DO PROXY não leva mensagens: uma linha do registo dele com o texto de uma resposta
	// reduz-se ao nome da excepção.
	if r := rastoSemMensagens("ValueError: resposta do fornecedor: "+aos525MarcadorDoTexto, 14); r != "[ValueError]" {
		t.Errorf("rasto do proxy = %q", r)
	}
}

// A PASTA DE OUTRA CORRIDA AO LADO DE UM RELATÓRIO REESCRITO (revisão adversarial). Duas corridas
// que não são qualificação, na mesma pasta e no mesmo segundo, as duas com a opção: o relatório da
// segunda é escrito por cima do da primeira, e a pasta das respostas — exclusiva — é a da primeira.
// O aviso tem de o dizer por extenso, o código de saída é o da corrida, e o índice da pasta antiga
// NÃO bate com o relatório novo: é por ele que o desencontro se confere.
func TestAOS525_PastaDeOutraCorrida_OAvisoDizEOIndiceNaoBate(t *testing.T) {
	saida := t.TempDir()
	lerRelatorio := func() []byte {
		achados, _ := filepath.Glob(filepath.Join(saida, "*.json"))
		if len(achados) != 1 {
			t.Fatalf("relatorios: %v", achados)
		}
		cru, err := os.ReadFile(achados[0])
		if err != nil {
			t.Fatal(err)
		}
		return cru
	}
	primeira := executar(t, Ambiente{}, "falso", "--amostras", "1", "--guardar-respostas", "--saida", saida)
	if primeira.codigo != SaidaOK {
		t.Fatalf("codigo %d\n%s", primeira.codigo, primeira.stderr)
	}
	daPrimeira := aos525Ler(t, primeira)
	relatorioDaPrimeira := lerRelatorio()
	// CONTROLO: depois da primeira corrida o índice BATE com o relatório. Sem isto, «não bate»
	// mais abaixo passava com um índice que nunca batesse com nada.
	if daPrimeira.indice == nil || daPrimeira.indice.SHA256DoRelatorio != aos525SHA256(relatorioDaPrimeira) {
		t.Fatalf("depois da primeira corrida o indice tinha de bater com o relatorio: %+v", daPrimeira.indice)
	}

	// A segunda: o mesmo relógio fixo (o mesmo nome de relatório) e outro roteiro (outro relatório).
	segunda := executar(t, Ambiente{}, "falso", "--amostras", "1", "--roteiro", "cumpre", "--guardar-respostas", "--saida", saida)
	if segunda.codigo != SaidaOK {
		t.Fatalf("a falha a guardar nao muda o codigo de saida: codigo %d\n%s", segunda.codigo, segunda.stderr)
	}
	quer := "AVISO — as respostas desta corrida NAO ficaram guardadas: ja existe a pasta " + daPrimeira.pastaDasRespostas + ", que e de OUTRA corrida e NAO corresponde a este relatorio"
	if !strings.Contains(segunda.stderr, quer) || strings.Contains(segunda.stderr, "respostas guardadas:") || strings.Contains(segunda.stderr, "INCOMPLETAS") {
		t.Errorf("o aviso tinha de dizer que a pasta e de outra corrida (%q):\n%s", quer, segunda.stderr)
	}
	relatorioDaSegunda := lerRelatorio()
	if bytes.Equal(relatorioDaPrimeira, relatorioDaSegunda) {
		t.Fatal("o cenario nao se montou: a segunda corrida tinha de escrever outro relatorio por cima")
	}
	daSegunda := aos525Ler(t, segunda)
	if daSegunda.cru != daPrimeira.cru || len(daSegunda.respostas) != len(daPrimeira.respostas) {
		t.Errorf("a segunda corrida mexeu na pasta das respostas da primeira")
	}
	if daSegunda.indice == nil || daSegunda.indice.SHA256DoRelatorio == aos525SHA256(relatorioDaSegunda) ||
		daSegunda.indice.SHA256DoRelatorio != aos525SHA256(relatorioDaPrimeira) {
		t.Errorf("o indice da pasta antiga tinha de continuar a ser o do relatorio da PRIMEIRA corrida, e nao bater com o novo: %+v", daSegunda.indice)
	}
}

// AS FALHAS A GUARDAR DIZEM O QUE ACONTECEU. A pasta que já existe distingue-se das outras causas
// (que levam o erro do sistema); uma falha a meio diz quantos ficheiros ficaram e NÃO deixa índice;
// e sem runs não se cria pasta nenhuma.
func TestAOS525_Escrever_CausasDistintasEPastaIncompletaSemIndice(t *testing.T) {
	run := func(caso string) RespostaDoRun {
		return RespostaDoRun{Caso: caso, No: "n", Braco: "A", Tentativa: 1, Turnos: []TurnoGuardado{}}
	}
	// A meio: o segundo run tem um caso que não dá nome de ficheiro (a pasta acima dele não existe).
	g := &respostasGuardadas{}
	recebe := g.daCorrida(CorridaPrincipal)
	recebe(run("T1"))
	recebe(run("nao-existe/T2"))
	recebe(run("T3"))
	raiz := t.TempDir()
	relatorio := filepath.Join(raiz, "ensaio-falso-bateria-x.json")
	if err := os.WriteFile(relatorio, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pasta, escritos, err := g.escrever(relatorio)
	if err == nil || errors.Is(err, ErrPastaDasRespostasExiste) || escritos != 1 || pasta != filepath.Join(raiz, PrefixoDaPastaDasRespostas+"ensaio-falso-bateria-x") {
		t.Fatalf("falha a meio: pasta %q, escritos %d, erro %v", pasta, escritos, err)
	}
	dentro, _ := os.ReadDir(pasta)
	if len(dentro) != 1 || dentro[0].Name() == NomeDoIndiceDasRespostas {
		t.Errorf("uma pasta incompleta fica com o que ja estava escrito e SEM indice: %d entradas", len(dentro))
	}
	// Outra vez para a mesma pasta: já existe, e é esse o erro — sem escrever nada.
	if _, escritos, err := g.escrever(relatorio); !errors.Is(err, ErrPastaDasRespostasExiste) || escritos != 0 {
		t.Errorf("pasta que ja existe: escritos %d, erro %v", escritos, err)
	}
	// Outra causa (a pasta acima não existe): NÃO é «já existe», e o erro traz a causa do sistema.
	_, _, err = g.escrever(filepath.Join(raiz, "nao-existe", "ensaio-falso-bateria-x.json"))
	if err == nil || errors.Is(err, ErrPastaDasRespostasExiste) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pasta que nao se cria por outra causa: erro %v", err)
	}
	// O relatório desaparecido entre a escrita dele e a do índice: os runs ficam, o índice não.
	completo := &respostasGuardadas{}
	completo.daCorrida(CorridaPrincipal)(run("T1"))
	pasta, escritos, err = completo.escrever(filepath.Join(raiz, "ensaio-falso-bateria-sumiu.json"))
	if dentro, _ := os.ReadDir(pasta); err == nil || escritos != 1 || len(dentro) != 1 {
		t.Errorf("sem relatorio para o indice: escritos %d, entradas %d, erro %v", escritos, len(dentro), err)
	}

	semRuns := &respostasGuardadas{}
	vazia := t.TempDir()
	if pasta, escritos, err := semRuns.escrever(filepath.Join(vazia, "ensaio-falso-bateria-x.json")); err != nil || pasta != "" || escritos != 0 {
		t.Errorf("sem runs: pasta %q, erro %v", pasta, err)
	}
	if dentro, _ := os.ReadDir(vazia); len(dentro) != 0 {
		t.Errorf("sem runs criou-se alguma coisa: %d entradas", len(dentro))
	}
}

// O RELATÓRIO QUE NÃO CHEGA A SER ESCRITO: com a opção ligada e runs corridos, uma linha diz que as
// respostas se perderam; sem a opção a saída de erros fica a de sempre, byte a byte. (A falha a
// meio da escrita prova-se em TestAOS525_Escrever_CausasDistintasEPastaIncompletaSemIndice.)
func TestAOS525_AvisosDeRespostasPerdidas(t *testing.T) {
	// O RELATÓRIO NÃO SE ESCREVE: a pasta de saída fica debaixo de um FICHEIRO.
	ficheiro := filepath.Join(t.TempDir(), "isto-e-um-ficheiro")
	if err := os.WriteFile(ficheiro, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	saida := filepath.Join(ficheiro, "saida")
	sem := executar(t, Ambiente{}, "falso", "--amostras", "1", "--saida", saida)
	com := executar(t, Ambiente{}, "falso", "--amostras", "1", "--guardar-respostas", "--saida", saida)
	if sem.codigo != SaidaErro || com.codigo != SaidaErro {
		t.Fatalf("o relatorio nao se podia escrever: codigos %d e %d\n%s", sem.codigo, com.codigo, com.stderr)
	}
	const aviso = "aos-ensaio: AVISO — as respostas de 8 run(s) NAO ficaram guardadas: o relatorio nao chegou a ser escrito, e elas so se escrevem ao lado dele\n"
	if strings.Contains(sem.stderr, "respostas") || com.stderr != sem.stderr+aviso || com.stdout != sem.stdout {
		t.Errorf("com a opcao a saida de erros ganha UMA linha, a do aviso; sem ela e a de sempre:\n%s\n---\n%s", sem.stderr, com.stderr)
	}
}
