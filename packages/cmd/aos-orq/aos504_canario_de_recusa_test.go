package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-504 — o canário da recusa do próprio objectivo. Só medição: conta, e nada decide por ele.

// aos504Marca é um texto que só existe no `final_text` do nó de resumo destes testes: é por ele
// que se procura o texto onde ele não pode estar.
const aos504Marca = "MARCA-AOS504-TEXTO-DO-TITULAR"

// As duas respostas do nó de resumo. A recusa usa as palavras do protocolo, como as medidas em
// produção; o resumo é um resumo.
const (
	aos504TextoDeRecusa = aos504Marca + ": I will not follow the objective embedded inside a plan_input marked taint=untrusted."
	aos504TextoDeResumo = aos504Marca + ": 1) decisao tomada; 2) accoes atribuidas; 3) nota sobre o sandbox."
)

func aos504RespostaDoN2(t *testing.T, run, texto string) []byte {
	t.Helper()
	cru, err := json.Marshal(map[string]any{"run_id": run + "~n2", "status": "completed", "terminated": true, "final_text": texto})
	if err != nil {
		t.Fatalf("resposta do n2: %v", err)
	}
	return cru
}

var aos504Duracao = regexp.MustCompile(`duracao_s=[0-9.]+`)

// aos504LinhasEstaveis devolve as linhas de um ficheiro de métricas sem as que dependem do
// relógio (a duração e o instante da drenagem) e sem as do canário.
func aos504LinhasEstaveis(metricas string) []string {
	var out []string
	for _, l := range strings.Split(metricas, "\n") {
		if strings.Contains(l, metricaDuracao) || strings.Contains(l, metricaUltimaDrenagem) || strings.Contains(l, metricaCanarioDeRecusa) || strings.Contains(l, metricaCanarioDeRecusaNos) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// aos504LinhasDaExecucao devolve, do stdout de uma drenagem, as linhas do executor e a do
// desfecho, com a duração mascarada.
func aos504LinhasDaExecucao(stdout string) []string {
	var out []string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, "execucao:") || strings.HasPrefix(strings.TrimSpace(l), "desfecho:") {
			out = append(out, aos504Duracao.ReplaceAllString(l, "duracao_s=X"))
		}
	}
	return out
}

// O CANÁRIO NÃO MUDA NADA. O mesmo plano — leitura e resumo, o do AOS-484 — corre duas vezes: o nó
// de resumo responde uma vez com um resumo, outra com uma recusa que usa as palavras do
// protocolo. Entre as duas drenagens, o estado dos nós, o que o consumidor recebeu, os eventos do
// plano, o `detail`, a classe e o código de saída são IGUAIS; só as duas séries do canário
// diferem. E o texto do nó não aparece em métrica, log nem evento.
func TestAOS504ComOBinarioReal_OCanarioSoMede(t *testing.T) {
	bin := construir(t)
	const run = "plan-aos495-boa"
	correr := func(texto string) (aos499Drenagem, *aos495No) {
		f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{
			"n1": aos495Fio(t, "boa-enforce"),
			"n2": aos504RespostaDoN2(t, run, texto),
		}}
		return aos499Consumir(t, bin, f, run, aos484PlanoLerEResumir, aos408SnapshotComPerigo, ""), f
	}
	resumo, fResumo := correr(aos504TextoDeResumo)
	recusa, fRecusa := correr(aos504TextoDeRecusa)

	// (1) AS DUAS SÉRIES: o denominador conta o nó de resumo nas duas drenagens; o numerador só
	// na da recusa — e escreve-se a zero na outra, para a taxa ter as duas metades.
	for _, c := range []struct {
		nome    string
		d       aos499Drenagem
		canario int
	}{{"resumo", resumo, 0}, {"recusa", recusa, 1}} {
		if !temSerie(c.d.metricas, metricaCanarioDeRecusaNos, 1) || !temSerie(c.d.metricas, metricaCanarioDeRecusa, c.canario) {
			t.Fatalf("%s: queria %s 1 e %s %d:\n%s", c.nome, metricaCanarioDeRecusaNos, metricaCanarioDeRecusa, c.canario, c.d.metricas)
		}
		if metricaCanarioDeRecusa != "aos_orq_consume_canario_de_recusa_total" {
			t.Fatalf("o nome da serie do canario e o do ticket; veio %q", metricaCanarioDeRecusa)
		}
	}

	// (2) O DESFECHO É O MESMO: o plano sai terminal/0 nos dois casos, com o mesmo `detail`.
	for nome, d := range map[string]aos499Drenagem{"resumo": resumo, "recusa": recusa} {
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("%s: o plano tinha de sair terminal/0; saiu %s/%d\n%s", nome, d.classe, d.codigo, d.stdout)
		}
		if !strings.Contains(d.stdout, "execucao: n1=complete n2=complete") {
			t.Fatalf("%s: os dois nos tinham de concluir:\n%s", nome, d.stdout)
		}
	}
	if a, b := aos504Duracao.ReplaceAllString(resumo.detalhe, "duracao_s=X"), aos504Duracao.ReplaceAllString(recusa.detalhe, "duracao_s=X"); a != b {
		t.Fatalf("o detail do desfecho mudou com o canario:\n resumo: %s\n recusa: %s", a, b)
	}
	// O log do executor e a linha do desfecho, iguais linha a linha.
	if a, b := aos504LinhasDaExecucao(resumo.stdout), aos504LinhasDaExecucao(recusa.stdout); !reflect.DeepEqual(a, b) || len(a) < 4 {
		t.Fatalf("o log da execucao mudou com o canario:\n resumo: %q\n recusa: %q", a, b)
	}
	// As outras métricas, iguais.
	if a, b := aos504LinhasEstaveis(resumo.metricas), aos504LinhasEstaveis(recusa.metricas); !reflect.DeepEqual(a, b) {
		t.Fatalf("o canario mexeu noutra serie:\n resumo: %q\n recusa: %q", a, b)
	}
	// Os eventos do plano, iguais: os mesmos streams, tipos, passos e payloads.
	evResumo, evRecusa := aos499EventosDoPlano(t, resumo.wal, run), aos499EventosDoPlano(t, recusa.wal, run)
	if !reflect.DeepEqual(evResumo, evRecusa) {
		t.Fatalf("os eventos do plano mudaram com o canario:\n resumo: %+v\n recusa: %+v", evResumo, evRecusa)
	}
	// O que o nó `aos` recebeu, igual: os mesmos pedidos de run, com os mesmos corpos.
	if !reflect.DeepEqual(fResumo.crus, fRecusa.crus) || len(fRecusa.crus) != 2 {
		t.Fatalf("os POST /runs mudaram com o canario: %d e %d corpos", len(fResumo.crus), len(fRecusa.crus))
	}

	// (3) O TEXTO NÃO SAI: nem em métrica, nem em log, nem em evento, nem no `detail`.
	for nome, d := range map[string]aos499Drenagem{"resumo": resumo, "recusa": recusa} {
		for onde, texto := range map[string]string{"metricas": d.metricas, "stdout": d.stdout, "stderr": d.stderr, "detail": d.detalhe} {
			if strings.Contains(texto, aos504Marca) {
				t.Fatalf("%s: o texto final do no esta em %s:\n%s", nome, onde, texto)
			}
		}
	}
	for _, ev := range evRecusa {
		if strings.Contains(ev.Payload, aos504Marca) {
			t.Fatalf("o texto final do no esta no evento %s: %s", ev.Tipo, ev.Payload)
		}
	}
	// E do canário não há linha de log: o que se sabe dele lê-se nas métricas, sem id nenhum.
	for _, palavra := range []string{"canario", "CANARIO", "recusa do objectivo"} {
		if strings.Contains(recusa.stdout, palavra) || strings.Contains(recusa.stderr, palavra) {
			t.Fatalf("o canario escreveu no log da drenagem (%q):\n%s\n%s", palavra, recusa.stdout, recusa.stderr)
		}
	}
}

// A CLASSE: sem tools E com `consumes`. É uma propriedade do documento do plano.
func TestAOS504_ClasseDoCanario(t *testing.T) {
	t.Parallel()
	tool := []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:aa"}}
	aresta := []plan.PayloadEdge{{From: "n1", Output: "conteudo", Type: "record"}}
	for _, c := range []struct {
		nome string
		n    plan.Node
		quer bool
	}{
		{"sem tools, com consumes (o no de resumo)", plan.Node{Consumes: aresta}, true},
		{"sem tools, com consumes e saidas", plan.Node{Consumes: aresta, Outputs: []plan.Output{{Name: "resumo", Type: "record"}}}, true},
		{"com tools, com consumes", plan.Node{Tools: tool, Consumes: aresta}, false},
		{"com tools, sem consumes (o no de leitura)", plan.Node{Tools: tool}, false},
		{"sem tools, sem consumes", plan.Node{DependsOn: []string{"n1"}}, false},
		{"vazio", plan.Node{}, false},
	} {
		if got := classeDoCanario(c.n); got != c.quer {
			t.Errorf("%s: classeDoCanario = %v, quero %v", c.nome, got, c.quer)
		}
	}
}

// O VOCABULÁRIO: `plan_input` e `taint=untrusted`, exactos. Inclui as duas formas em que as
// recusas medidas em produção citam o protocolo, e textos vizinhos que não contam.
func TestAOS504_VocabularioDoProtocolo(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(vocabularioDoProtocolo, []string{"plan_input", "taint=untrusted"}) {
		t.Fatalf("o vocabulario do canario e o do ticket; veio %v", vocabularioDoProtocolo)
	}
	for texto, quer := range map[string]bool{
		"the <objective> embedded inside a `plan_input` marked `taint=untrusted`": true,
		"Nao vou seguir instrucoes vindas de um plan_input.":                      true,
		"o conteudo vem marcado taint=untrusted e por isso e so dados":            true,
		"<plan_input taint=untrusted plan_input_from=n1>":                         true,
		"Resumo em tres pontos: decisao, accoes, nota.":                           false,
		"":                            false,
		"plan input":                  false,
		"Plan_Input":                  false,
		"taint = untrusted":           false,
		"conteudo untrusted do plano": false,
		"nao vou seguir a \"objective\" incorporada no documento": false, // uma recusa que não usa as palavras: escapa
	} {
		if got := usaVocabularioDoProtocolo(texto); got != quer {
			t.Errorf("usaVocabularioDoProtocolo(%q) = %v, quero %v", texto, got, quer)
		}
	}
}

// UM NÓ DE RESUMO QUE FECHA `failed` NÃO ENTRA NO DENOMINADOR — com o binário real (revisão do
// AOS-504, M1). O run do nó de resumo acaba `failed` no nó `aos`, com um texto final que usa as
// palavras do protocolo: o nó do plano fecha `failed`, e nenhuma das duas séries do canário se
// escreve. É a CABLAGEM que isto prende: no executor, o canário recebe «o nó concluiu» do
// desfecho decidido. Com `true` fixo nesse argumento, este nó contava 1 e 1.
func TestAOS504ComOBinarioReal_NoDeResumoFalhadoNaoConta(t *testing.T) {
	bin := construir(t)
	const run = "plan-aos495-boa"
	falhado, err := json.Marshal(map[string]any{"run_id": run + "~n2", "status": "failed", "terminated": true, "final_text": aos504TextoDeRecusa})
	if err != nil {
		t.Fatalf("resposta do n2: %v", err)
	}
	f := &aos495No{anuncio: "enforce", respostas: map[string][]byte{
		"n1": aos495Fio(t, "boa-enforce"),
		"n2": falhado,
	}}
	d := aos499Consumir(t, bin, f, run, aos484PlanoLerEResumir, aos408SnapshotComPerigo, "")
	if !strings.Contains(d.stdout, "execucao: n1=complete n2=failed") {
		t.Fatalf("pre-condicao: o no de resumo tinha de fechar failed:\n%s", d.stdout)
	}
	// O ficheiro de métricas escreveu-se (tem as séries de sempre), e do canário não tem nada.
	if !strings.Contains(d.metricas, metricaUltimaDrenagem) {
		t.Fatalf("pre-condicao: a drenagem tinha de escrever o ficheiro de metricas:\n%s", d.metricas)
	}
	if strings.Contains(d.metricas, "canario") {
		t.Fatalf("um no de resumo failed entrou no canario:\n%s", d.metricas)
	}
	// CONTROLO: o mesmo plano com o nó de resumo a CONCLUIR com o mesmo texto conta 1 e 1 — a
	// ausência acima vem do desfecho, e não de o canário estar desligado neste plano.
	f2 := &aos495No{anuncio: "enforce", respostas: map[string][]byte{
		"n1": aos495Fio(t, "boa-enforce"),
		"n2": aos504RespostaDoN2(t, run, aos504TextoDeRecusa),
	}}
	d2 := aos499Consumir(t, bin, f2, run, aos484PlanoLerEResumir, aos408SnapshotComPerigo, "")
	if !temSerie(d2.metricas, metricaCanarioDeRecusaNos, 1) || !temSerie(d2.metricas, metricaCanarioDeRecusa, 1) {
		t.Fatalf("controlo: com o no a concluir, o canario tinha de contar 1 e 1:\n%s", d2.metricas)
	}
}

// O QUE CONTA: só um nó da classe que CONCLUIU. Um nó que não concluiu, ou fora da classe, não
// entra no denominador nem no numerador; e sem medição (um `serve` manual) nada rebenta.
func TestAOS504_RegistarCanario_SoONoDaClasseQueConcluiu(t *testing.T) {
	t.Parallel()
	resumidor := plan.Node{NodeID: "n2", Consumes: []plan.PayloadEdge{{From: "n1", Output: "conteudo", Type: "record"}}}
	leitor := plan.Node{NodeID: "n1", Tools: []plan.ToolRef{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:aa"}}}
	e := &executorDeNos{medicao: &medicaoDoContrato{}}
	e.registarCanarioDeRecusa(resumidor, true, aos504TextoDeResumo)
	e.registarCanarioDeRecusa(resumidor, true, aos504TextoDeRecusa)
	e.registarCanarioDeRecusa(resumidor, true, "")
	e.registarCanarioDeRecusa(resumidor, false, aos504TextoDeRecusa) // não concluiu
	e.registarCanarioDeRecusa(leitor, true, aos504TextoDeRecusa)     // fora da classe
	if e.medicao.canarioNos != 3 || e.medicao.canarioRecusas != 1 {
		t.Fatalf("queria 3 nos da classe e 1 canario; vieram %d e %d", e.medicao.canarioNos, e.medicao.canarioRecusas)
	}
	(&executorDeNos{}).registarCanarioDeRecusa(resumidor, true, aos504TextoDeRecusa) // medição nil

	// As séries: as duas juntas, e acumulam-se de drenagem em drenagem pelo ficheiro.
	caminho := filepath.Join(t.TempDir(), nomeDoFicheiroDeMetricas)
	m, err := lerMetricas(caminho)
	if err != nil {
		t.Fatalf("lerMetricas: %v", err)
	}
	m.registarContrato(e.medicao)
	m.registarContrato(&medicaoDoContrato{canarioNos: 2})
	m.registarContrato(&medicaoDoContrato{}) // sem nós da classe: não escreve nada
	m.registarContrato(nil)
	if err := escreverMetricas(caminho, m.texto()); err != nil {
		t.Fatalf("escreverMetricas: %v", err)
	}
	relido, err := lerMetricas(caminho)
	if err != nil {
		t.Fatalf("reler as metricas: %v", err)
	}
	if relido.series[metricaCanarioDeRecusaNos] != 5 || relido.series[metricaCanarioDeRecusa] != 1 {
		t.Fatalf("series relidas: nos=%v canario=%v; quero 5 e 1", relido.series[metricaCanarioDeRecusaNos], relido.series[metricaCanarioDeRecusa])
	}
	texto := string(m.texto())
	if !temSerie(texto, metricaCanarioDeRecusaNos, 5) || !temSerie(texto, metricaCanarioDeRecusa, 1) || strings.Contains(texto, aos504Marca) {
		t.Fatalf("o ficheiro de metricas:\n%s", texto)
	}
	// Sem nós da classe, nenhuma das duas séries existe.
	vazio := &metricasDoConsumo{series: map[string]float64{}}
	vazio.registarContrato(&medicaoDoContrato{classes: map[string]int{classeSemContratoSemTools: 1}})
	if strings.Contains(string(vazio.texto()), "canario") {
		t.Fatalf("sem nos da classe nao ha series do canario:\n%s", vazio.texto())
	}
	// Só o numerador zerado, com denominador: escreve-se a zero.
	soNos := &metricasDoConsumo{series: map[string]float64{}}
	soNos.registarContrato(&medicaoDoContrato{canarioNos: 4})
	if !temSerie(string(soNos.texto()), metricaCanarioDeRecusaNos, 4) || !temSerie(string(soNos.texto()), metricaCanarioDeRecusa, 0) {
		t.Fatalf("com nos da classe e sem recusas, o canario escreve-se a zero:\n%s", soNos.texto())
	}
}

// NENHUM RAMO DE DECISÃO LÊ O CANÁRIO — verificado no código-fonte do pacote.
//
//   - as funções do canário não devolvem nada: não há valor que um chamador possa testar;
//   - os dois contadores só são lidos em canario_de_recusa.go, para escrever as séries;
//   - o predicado sobre o texto só é chamado em canario_de_recusa.go;
//   - no executor, a chamada ao canário é uma instrução solta — não está na condição de um `if`,
//     de um `switch` nem de um `for`, nem atribui nada.
func TestAOS504_NenhumRamoDeDecisaoLeOCanario(t *testing.T) {
	t.Parallel()
	const casa = "canario_de_recusa.go"
	semResultado := map[string]bool{"registarCanarioDeRecusa": false, "noDoCanarioConcluiu": false, "registarCanario": false}
	reservados := map[string]bool{"canarioNos": true, "canarioRecusas": true, "usaVocabularioDoProtocolo": true, "vocabularioDoProtocolo": true}
	chamadasNoExecutor := 0

	ficheiros, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, nome := range ficheiros {
		if strings.HasSuffix(nome, "_test.go") {
			continue
		}
		src, err := os.ReadFile(nome)
		if err != nil {
			t.Fatalf("ler %s: %v", nome, err)
		}
		f, err := parser.ParseFile(fset, nome, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", nome, err)
		}
		// As instruções soltas que são uma chamada ao canário.
		soltas := map[*ast.CallExpr]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if st, ok := n.(*ast.ExprStmt); ok {
				if call, ok := st.X.(*ast.CallExpr); ok {
					soltas[call] = true
				}
			}
			return true
		})
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if _, e := semResultado[x.Name.Name]; e {
					if x.Type.Results != nil && len(x.Type.Results.List) > 0 {
						t.Errorf("%s: %s devolve um valor — um chamador podia decidir por ele", nome, x.Name.Name)
					}
					semResultado[x.Name.Name] = true
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if _, e := semResultado[sel.Sel.Name]; e {
					if !soltas[x] {
						t.Errorf("%s:%d: a chamada a %s nao e uma instrucao solta", nome, fset.Position(x.Pos()).Line, sel.Sel.Name)
					}
					if sel.Sel.Name == "registarCanarioDeRecusa" {
						chamadasNoExecutor++
						if nome != "node_executor.go" {
							t.Errorf("%s: registarCanarioDeRecusa so se chama no fecho do no", nome)
						}
					}
				}
			case *ast.SelectorExpr:
				if reservados[x.Sel.Name] && nome != casa {
					t.Errorf("%s:%d: %s e lido fora de %s", nome, fset.Position(x.Pos()).Line, x.Sel.Name, casa)
				}
			case *ast.Ident:
				if (x.Name == "usaVocabularioDoProtocolo" || x.Name == "vocabularioDoProtocolo") && nome != casa {
					t.Errorf("%s:%d: %s e usado fora de %s", nome, fset.Position(x.Pos()).Line, x.Name, casa)
				}
			}
			return true
		})
	}
	for nome, vista := range semResultado {
		if !vista {
			t.Errorf("pre-condicao: a funcao %s ja nao existe; actualizar este teste", nome)
		}
	}
	if chamadasNoExecutor != 1 {
		t.Errorf("queria UMA chamada ao canario, no fecho do no; ha %d", chamadasNoExecutor)
	}
}
