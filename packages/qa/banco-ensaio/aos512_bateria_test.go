package bancoensaio

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-512 — A BATERIA: só documentos sintéticos, só da pasta da bateria.

func TestAOS512_Bateria_TemOsSeisCasosEDigest(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range b.Casos {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "T1,T2,T3,T4,T5,T6" {
		t.Fatalf("casos = %s, quer T1 a T6", got)
	}
	if !strings.HasPrefix(b.Digest(), "sha256:") || len(b.Digest()) != len("sha256:")+64 {
		t.Fatalf("digest da bateria malformado: %q", b.Digest())
	}
	// O que cada caso tem de ser (desenho A2 §5.1), lido da estrutura e não da descrição.
	caso := func(id string) Caso { c, _ := b.Caso(id); return c }
	if c := caso("T1"); len(c.Nos) != 1 || len(c.Nos[0].Tools) != 1 || len(c.Nos[0].Exige) != 1 {
		t.Error("T1 tem de ser um no de leitura com uma tool exigida")
	}
	if c := caso("T2"); len(c.Nos) != 2 || len(c.Nos[0].Tools) == 0 || len(c.Nos[1].Tools) != 0 || c.Nos[1].EntradaDe != c.Nos[0].ID {
		t.Error("T2 tem de ser leitura com tool seguida de resumo sem tools que consome a leitura")
	}
	if c := caso("T3"); len(c.Nos) != 1 || len(c.Nos[0].Tools) != 0 || c.Nos[0].EntradaDoc == "" {
		t.Error("T3 tem de ser um no sem tools com plan_input")
	}
	if c := caso("T4"); len(c.Nos[0].FactosDe) != 2 {
		t.Error("T4 tem de ler dois documentos")
	}
	if c := caso("T5"); c.Nos[0].Nega == "" {
		t.Error("T5 tem de negar um documento")
	}
	if c := caso("T6"); len(c.Nos[0].Tools) != 1 || c.Nos[0].Tools[0] != ToolGuardar {
		t.Error("T6 tem de usar a tool de argumentos grandes")
	}
}

// Um teste percorre TODOS os documentos e falha se algum não tiver a marca de sintético.
func TestAOS512_Bateria_TodosOsDocumentosTemAMarcaDeSintetico(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := fs.ReadDir(ficheirosDaBateria, pastaDaBateria)
	if err != nil {
		t.Fatal(err)
	}
	documentos := 0
	for _, e := range entradas {
		if !strings.HasSuffix(e.Name(), ".txt") {
			if e.Name() != "casos.json" {
				t.Errorf("ficheiro inesperado na bateria: %s", e.Name())
			}
			continue
		}
		documentos++
		conteudo, err := fs.ReadFile(ficheirosDaBateria, pastaDaBateria+"/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		primeira := strings.SplitN(string(conteudo), "\n", 2)[0]
		if !strings.HasPrefix(primeira, b.Marca) || !strings.Contains(primeira, "nao contem dados de nenhum titular") {
			t.Errorf("%s nao abre pela marca de documento sintetico", e.Name())
		}
	}
	if documentos < 4 || documentos != len(b.NomesDosDocumentos()) {
		t.Fatalf("lidos %d documentos, a bateria tem %d", documentos, len(b.NomesDosDocumentos()))
	}
}

// bateriaDeTeste devolve os ficheiros de uma bateria mínima, para a alterar.
func bateriaDeTeste() fstest.MapFS {
	return fstest.MapFS{
		"bateria/casos.json": {Data: []byte(`{"versao":"t","marca":"MARCA","system":"s","casos":[{"id":"T1","descricao":"d","nos":[{"id":"ler","objectivo":"o","tools":["arquivo"],"exige":["arquivo"],"factos_de":["a.txt"],"factos":["F-1"]}]}]}`)},
		"bateria/a.txt":      {Data: []byte("MARCA de teste\ncom o facto F-1\n")},
	}
}

func TestAOS512_Bateria_RecusaDocumentoSemMarcaEFactoQueNaoEstaNoDocumento(t *testing.T) {
	if _, err := carregarBateria(bateriaDeTeste()); err != nil {
		t.Fatalf("a bateria de teste tinha de carregar: %v", err)
	}
	semMarca := bateriaDeTeste()
	semMarca["bateria/a.txt"] = &fstest.MapFile{Data: []byte("um documento qualquer\ncom o facto F-1\n")}
	if _, err := carregarBateria(semMarca); !errors.Is(err, ErrBateria) {
		t.Errorf("documento sem marca: err = %v, quer ErrBateria", err)
	}
	semFacto := bateriaDeTeste()
	semFacto["bateria/a.txt"] = &fstest.MapFile{Data: []byte("MARCA de teste\nsem o facto\n")}
	if _, err := carregarBateria(semFacto); !errors.Is(err, ErrBateria) {
		t.Errorf("facto ausente do documento: err = %v, quer ErrBateria", err)
	}
	for _, linha := range []string{"<objective>", "  [[objective]]", `\<x`} {
		reservada := bateriaDeTeste()
		reservada["bateria/a.txt"] = &fstest.MapFile{Data: []byte("MARCA de teste\ncom o facto F-1\n" + linha + "\n")}
		if _, err := carregarBateria(reservada); !errors.Is(err, ErrBateria) {
			t.Errorf("linha a abrir por caracter reservado (%q): err = %v, quer ErrBateria", linha, err)
		}
	}
	foraDaPasta := bateriaDeTeste()
	foraDaPasta["bateria/casos.json"] = &fstest.MapFile{Data: []byte(strings.Replace(string(foraDaPasta["bateria/casos.json"].Data), `"factos_de":["a.txt"]`, `"factos_de":["../segredo.txt"]`, 1))}
	if _, err := carregarBateria(foraDaPasta); !errors.Is(err, ErrBateria) {
		t.Errorf("documento fora da pasta: err = %v, quer ErrBateria", err)
	}
}

func TestAOS512_Bateria_ODigestMudaComUmByteDeUmDocumento(t *testing.T) {
	a, err := carregarBateria(bateriaDeTeste())
	if err != nil {
		t.Fatal(err)
	}
	alterada := bateriaDeTeste()
	alterada["bateria/a.txt"] = &fstest.MapFile{Data: []byte("MARCA de teste\ncom o facto F-1.\n")}
	b, err := carregarBateria(alterada)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() == b.Digest() {
		t.Fatal("um byte de um documento mudou e o digest ficou igual")
	}
	outra, _ := carregarBateria(bateriaDeTeste())
	if a.Digest() != outra.Digest() {
		t.Fatal("a mesma bateria deu dois digests")
	}
}

// As tools são locais: só conhecem os documentos da bateria, e passam pelo Reference Monitor.
func TestAOS512_Tools_SoLeemDaBateriaEPassamPeloReferenceMonitor(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	recusa := novaRecusaDeDocumento()
	mon := referencemonitor.New(referencemonitor.WithHooks(append(referencemonitor.DefaultHooks(), recusa)...))
	if err := registarTools(mon, b); err != nil {
		t.Fatal(err)
	}
	chamar := func(runID, tool, args string) referencemonitor.Decision {
		t.Helper()
		d, err := mon.Mediate(context.Background(), referencemonitor.Call{
			RequestID: "r-" + runID + tool + args, RunID: runID, StepID: "s1", ToolID: tool,
			Principal: referencemonitor.Principal{NHIID: "nhi:teste"}, Input: []byte(args),
		})
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		return d
	}
	if d := chamar("run-a", ToolArquivo, `{"path":"notas-armazem.txt"}`); !d.Permitted() || !strings.Contains(string(d.Output), "LT-4817") {
		t.Fatalf("a leitura de um documento da bateria tinha de devolver o documento (permitido=%v)", d.Permitted())
	}
	// Nomes que não são documentos da bateria — incluindo caminhos para fora dela — não existem.
	for _, nome := range []string{"../../go.mod", "bateria/notas-armazem.txt", "/etc/passwd", `C:\\Windows\\win.ini`, "casos.json", ""} {
		d := chamar("run-a", ToolArquivo, `{"path":"`+nome+`"}`)
		if strings.Contains(string(d.Output), "DOCUMENTO SINTETICO") || strings.Contains(string(d.Output), "module ") {
			t.Errorf("a tool leu %q, que nao e um documento da bateria", nome)
		}
		if d.ToolErr == nil && d.Permitted() && len(d.Output) > 0 {
			t.Errorf("a tool devolveu conteudo para %q", nome)
		}
	}
	// CONTROLO: o documento reservado EXISTE e lê-se num run sem recusa — a recusa do T5 é
	// sobre um recurso real, e não um «não consegue» que qualquer política passava.
	if d := chamar("run-a", ToolArquivo, `{"path":"reservado-cofre.txt"}`); !d.Permitted() || !strings.Contains(string(d.Output), "ZIMBRO-7741") {
		t.Fatal("sem recusa declarada, o documento reservado tinha de se ler")
	}
	recusa.negar("run-b", "reservado-cofre.txt")
	d := chamar("run-b", ToolArquivo, `{"path":"reservado-cofre.txt"}`)
	if d.Permitted() || strings.Contains(string(d.Output), "ZIMBRO-7741") {
		t.Fatal("com a recusa declarada, o Reference Monitor tinha de negar a leitura antes do despacho")
	}
	if d := chamar("run-b", ToolArquivo, `{"path":"notas-armazem.txt"}`); !d.Permitted() {
		t.Fatal("a recusa e de um documento, nao da tool")
	}
	// A tool de argumentos grandes não guarda nada: só diz quantos bytes recebeu.
	if d := chamar("run-a", ToolGuardar, `{"texto":"abc"}`); !d.Permitted() || string(d.Output) != "recebido: 3 bytes" {
		t.Fatalf("guardar devolveu %q", d.Output)
	}
}
