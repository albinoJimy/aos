package bancoensaio

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// AOS-528 — A BATERIA 3.0.0 («DIFÍCIL»).

// aos528DigestDaBateria3 é o digest da bateria 3.0.0. Mudar um byte de um documento ou de um
// objectivo muda-o, e é outra bateria: muda-se a versão com ele.
const aos528DigestDaBateria3 = "sha256:1475fdf043d08e16edb030d3e754827299f492ef59849c50b378e18ebac67c37"

func aos528Bateria3(t *testing.T) *Bateria {
	t.Helper()
	b, err := CarregarBateriaDificil()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAOS528_Bateria3_TemDigestProprioEAsOutrasMantemOSeu(t *testing.T) {
	um, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	dois := aos525Bateria2(t)
	tres := aos528Bateria3(t)
	if um.Digest() != aos525DigestDaBateria1 || dois.Digest() != aos525DigestDaBateria2 {
		t.Errorf("as baterias 1.0.0 e 2.0.0 mudaram: %s, %s", um.Digest(), dois.Digest())
	}
	if tres.Versao != "3.0.0" || tres.Digest() != aos528DigestDaBateria3 {
		t.Errorf("bateria 3.0.0: versao %s, digest %s (quer %s)", tres.Versao, tres.Digest(), aos528DigestDaBateria3)
	}
	if got := aos525DigestDaPasta(t, ficheirosDaBateriaDificil, pastaDaBateriaDificil); got != tres.Digest() {
		t.Errorf("o digest da 3.0.0 nao e o dos seus ficheiros: %s", got)
	}
	if outra := aos528Bateria3(t); outra.Digest() != tres.Digest() {
		t.Error("a mesma bateria deu dois digests")
	}
	if !tres.EDificil() || tres.TemDegraus() || um.EDificil() || dois.EDificil() {
		t.Error("so a 3.0.0 e a bateria dificil, e nao e por degraus")
	}
	if tres.Marca != um.Marca || tres.System != um.System {
		t.Error("a 3.0.0 tem de ter a marca e o prompt de sistema da 1.0.0")
	}
	if len(tres.Casos) < 6 || len(tres.Casos) > 8 {
		t.Errorf("a 3.0.0 tem %d casos, quer de 6 a 8", len(tres.Casos))
	}
	// Documentos e casos novos: nenhum nome de documento, id de caso nem facto das outras duas.
	anteriores := map[string]bool{}
	var textoAnterior strings.Builder
	for _, b := range []*Bateria{um, dois} {
		for _, nome := range b.NomesDosDocumentos() {
			anteriores[nome] = true
			doc, _ := b.Documento(nome)
			textoAnterior.Write(doc)
		}
		for _, c := range b.Casos {
			anteriores["caso:"+c.ID] = true
		}
	}
	marca := strings.SplitN(string(func() []byte { d, _ := um.Documento(um.NomesDosDocumentos()[0]); return d }()), "\n", 2)[0]
	for _, nome := range tres.NomesDosDocumentos() {
		doc, _ := tres.Documento(nome)
		if anteriores[nome] || !strings.HasPrefix(string(doc), marca+"\n") {
			t.Errorf("%s: repete um documento de outra bateria, ou nao abre pela mesma primeira linha", nome)
		}
	}
	for _, c := range tres.Casos {
		if anteriores["caso:"+c.ID] || c.Degrau != 0 || strings.TrimSpace(c.Descricao) == "" {
			t.Errorf("%s: id repetido, degrau, ou sem a capacidade por extenso", c.ID)
		}
		for _, n := range c.Nos {
			if len(n.Factos) == 0 || len(n.FactosProibidos) == 0 || len(n.Exige) == 0 {
				t.Errorf("%s/%s: tem de ter factos exigidos, proibidos e contrato", c.ID, n.ID)
			}
			for _, f := range append(append([]string(nil), n.Factos...), n.FactosProibidos...) {
				if factoNaSaida(textoAnterior.String(), f) {
					t.Errorf("%s/%s: o facto %q tambem esta nos documentos das outras baterias", c.ID, n.ID, f)
				}
			}
		}
	}
}

// O que cada caso pede, lido dos documentos — não da descrição.
func TestAOS528_Bateria3_OsCasosPedemOQueDizem(t *testing.T) {
	b := aos528Bateria3(t)
	doc := func(nome string) string {
		d, ok := b.Documento(nome)
		if !ok {
			t.Fatalf("falta o documento %s", nome)
		}
		return string(d)
	}
	// X1: a cadeia de três documentos — cada passo leva ao seguinte, e só o último tem a resposta.
	if !strings.Contains(doc("ocorrencias-caldeiras.txt"), "3 de marco: a caldeira CB-41 parou") ||
		!strings.Contains(doc("bombas-reserva.txt"), "CB-41: bomba de reserva BR-5520") ||
		!strings.Contains(doc("prateleiras-armazem.txt"), "BR-5520 esta na prateleira P-0917") {
		t.Error("X1 tem de ser uma cadeia: dia -> caldeira -> bomba -> prateleira")
	}
	// X2: quatro documentos, as datas em três formatos, e a mais recente (22 de Abril) é a da
	// revisão com o número MAIS BAIXO; a nota de 9 de Abril vem escrita dia/mês.
	if !strings.Contains(doc("obra-ribeira-b.txt"), "22 de abril de 2026") || !strings.Contains(doc("obra-ribeira-b.txt"), "Revisao numero 3") ||
		!strings.Contains(doc("obra-ribeira-c.txt"), "Revisao numero 7") || !strings.Contains(doc("obra-ribeira-a.txt"), "(dia/mes/ano): 09/04/2026") ||
		!strings.Contains(doc("obra-ribeira-d.txt"), "2026-03-05") {
		t.Error("X2 tem de por a data mais recente, em formatos diferentes, contra o numero de revisao mais alto")
	}
	// X3: a fonte mais recente é a de MENOR autoridade, e quem emitiu cada uma só se lê no texto.
	if !strings.Contains(doc("valvula-a.txt"), "2026-05-30") || !strings.Contains(doc("valvula-b.txt"), "autoridade de seguranca industrial, de 2025-11-03") {
		t.Error("X3 tem de por a fonte mais recente como a de menor autoridade")
	}
	// Nenhum nome de documento diz qual é o certo: sem «autoridade», «adenda», «email» e afins.
	for _, nome := range b.NomesDosDocumentos() {
		for _, pista := range []string{"autoridade", "adenda", "email", "manual", "fabricante", "registo", "circular", "ficha", "interno", "recente"} {
			if strings.Contains(nome, pista) {
				t.Errorf("o nome do documento %s diz %q: e um atalho", nome, pista)
			}
		}
	}
	// X4: o documento longo, entre 40 e 60 KB, com as duas passagens afastadas.
	longo := doc("diario-estaleiro.txt")
	transferencia, tabela := strings.Index(longo, "guindaste GD-62 passou"), strings.Index(longo, "GD-62 operado por Amilcar Fontoura-Reis")
	if len(longo) < 40<<10 || len(longo) > 60<<10 || transferencia < 0 || tabela-transferencia < 40<<10 {
		t.Errorf("X4: %d bytes, transferencia em %d, tabela em %d — quer 40 a 60 KB e as passagens a mais de 40 KB uma da outra", len(longo), transferencia, tabela)
	}
	// X7: o dia da inspecção é o terceiro sábado, mas o documento só dá a data e o dia da semana de
	// outro dia: o calendário é do modelo. E as férias começam depois dele.
	escala := doc("escala-inspecao.txt")
	if !strings.Contains(escala, "o dia 17 de outubro de 2026") || !strings.Contains(escala, "o dia 1 de outubro de 2026 e uma quinta-feira") ||
		strings.Contains(escala, "sabado") || !strings.Contains(doc("disponibilidade-tecnicos.txt"), "de ferias de 19 a 30 de outubro") {
		t.Error("X7 tem de pedir o calendario (sem a lista dos sabados) e a conta das ferias")
	}
}

// aos528BateriaDeTeste é uma bateria difícil mínima, em memória, para as recusas.
func aos528BateriaDeTeste(casos string) fstest.MapFS {
	return fstest.MapFS{
		pastaDaBateriaDificil + "/casos.json": {Data: []byte(`{"versao":"3.0.0","marca":"DOCUMENTO SINTETICO","system":"s","casos":` + casos + `}`)},
		pastaDaBateriaDificil + "/a.txt":      {Data: []byte("DOCUMENTO SINTETICO\nO codigo certo e QA-1001 e o errado e QA-1010.\n")},
	}
}

func TestAOS528_Bateria3_OCarregamentoEFailClosed(t *testing.T) {
	no := func(objectivo, proibidos string) string {
		return `{"id":"n","objectivo":"` + objectivo + `","tools":["arquivo"],"exige":["arquivo"],"factos_de":["a.txt"],"factos":["QA-1001"]` + proibidos + `}`
	}
	bom := `[{"id":"Q1","descricao":"uma capacidade","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-1010"]`) + `]}]`
	if b, err := carregarBateriaDificil(aos528BateriaDeTeste(bom)); err != nil || !b.EDificil() {
		t.Fatalf("a bateria minima tinha de carregar: %v", err)
	}
	for nome, casos := range map[string]string{
		"sem factos proibidos":     `[{"id":"Q1","descricao":"uma capacidade","nos":[` + no("Read a.txt and answer.", ``) + `]}]`,
		"sem a capacidade":         `[{"id":"Q1","descricao":" ","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-1010"]`) + `]}]`,
		"facto no objectivo":       `[{"id":"Q1","descricao":"c","nos":[` + no("Read a.txt and answer QA-1001.", `,"factos_proibidos":["QA-1010"]`) + `]}]`,
		"proibido no objectivo":    `[{"id":"Q1","descricao":"c","nos":[` + no("Read a.txt, not qa-1010.", `,"factos_proibidos":["QA-1010"]`) + `]}]`,
		"objectivo repetido":       `[{"id":"Q1","descricao":"c","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-1010"]`) + `]},{"id":"Q2","descricao":"c","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-1010"]`) + `]}]`,
		"com degrau":               `[{"id":"Q1","degrau":1,"descricao":"c","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-1010"]`) + `]}]`,
		"proibido fora dos docs":   `[{"id":"Q1","descricao":"c","nos":[` + no("Read a.txt and answer.", `,"factos_proibidos":["QA-9999"]`) + `]}]`,
		"facto fora dos docs":      `[{"id":"Q1","descricao":"c","nos":[{"id":"n","objectivo":"Read a.txt.","tools":["arquivo"],"factos_de":["a.txt"],"factos":["QA-7777"],"factos_proibidos":["QA-1010"]}]}]`,
		"tool que o banco nao tem": `[{"id":"Q1","descricao":"c","nos":[{"id":"n","objectivo":"Read a.txt.","tools":["aconselhar"],"factos_de":["a.txt"],"factos":["QA-1001"],"factos_proibidos":["QA-1010"]}]}]`,
	} {
		if _, err := carregarBateriaDificil(aos528BateriaDeTeste(casos)); !errors.Is(err, ErrBateria) {
			t.Errorf("%s: err = %v, quer ErrBateria", nome, err)
		}
	}
}

// Cada bateria tem as suas experiências: a difícil só na `conselheiro`, e esta só sobre ela.
func TestAOS528_CadaBateriaTemAsSuasExperiencias(t *testing.T) {
	um, _ := CarregarBateria()
	dois := aos525Bateria2(t)
	tres := aos528Bateria3(t)
	if _, err := PlanoDoConselheiro(um, 1, 1, 2).PedidosMaximos(um); !errors.Is(err, ErrPlano) {
		t.Errorf("conselheiro sobre a 1.0.0: err = %v", err)
	}
	if _, err := PlanoDoConselheiro(dois, 1, 1, 2).PedidosMaximos(dois); !errors.Is(err, ErrPlano) {
		t.Errorf("conselheiro sobre a 2.0.0: err = %v", err)
	}
	if _, err := PlanoDaBateria(tres, BracoA, 1).PedidosMaximos(tres); !errors.Is(err, ErrPlano) {
		t.Errorf("bateria sobre a 3.0.0: err = %v", err)
	}
	if _, err := PlanoDosDegraus(tres, 1, 1).PedidosMaximos(tres); !errors.Is(err, ErrPlano) {
		t.Errorf("degraus sobre a 3.0.0: err = %v", err)
	}
	// Os campos novos do plano são só da experiência nova: noutra, recusados; e ausentes do JSON.
	p := PlanoDaBateria(um, BracoA, 1)
	p.MaxConsultas = 2
	if _, err := p.PedidosMaximos(um); !errors.Is(err, ErrPlano) {
		t.Errorf("consultas fora da experiencia conselheiro: err = %v", err)
	}
	for _, plano := range []Plano{PlanoDaBateria(um, BracoA, 1), PlanoDosDegraus(dois, 1, 1), PlanoDosSeparadores(1)} {
		cru, _ := json.Marshal(plano)
		if strings.Contains(string(cru), "conselheiro") || strings.Contains(string(cru), "consultas") {
			t.Errorf("o plano %s ganhou campos do conselheiro no JSON: %s", plano.Experiencia, cru)
		}
	}
	// Os braços do conselheiro só nas formas válidas, e as consultas só de 1 a 5.
	for nome, mexer := range map[string]func(*Plano){
		"sem bracos":         func(p *Plano) { p.BracosDoConselheiro = nil },
		"so com conselheiro": func(p *Plano) { p.BracosDoConselheiro = []string{BracoComConselheiro} },
		"ordem trocada":      func(p *Plano) { p.BracosDoConselheiro = []string{BracoComConselheiro, BracoSoExecutor} },
		"consultas a mais":   func(p *Plano) { p.MaxConsultas = MaxConsultasAceites + 1 },
		"consultas a zero":   func(p *Plano) { p.MaxConsultas = 0 },
		"outro braco":        func(p *Plano) { p.Bracos = []Braco{BracoB} },
		"outros turnos":      func(p *Plano) { p.MaxTurnos = 4 },
		"braco desconhecido": func(p *Plano) { p.BracosDoConselheiro = []string{BracoSoExecutor, "outro"} },
	} {
		p := PlanoDoConselheiro(tres, 1, 1, 2)
		mexer(&p)
		if _, err := p.PedidosMaximos(tres); !errors.Is(err, ErrPlano) {
			t.Errorf("%s: err = %v", nome, err)
		}
	}
}
