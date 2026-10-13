package bancoensaio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// AOS-525 — A BATERIA POR DEGRAUS (2.0.0) E A EXPERIÊNCIA `degraus`.

// aos525DigestDaBateria1 é o digest da bateria 1.0.0 ANTES deste trabalho: calculado dos
// ficheiros do commit 59e43ac4 por um programa à parte (os nomes por ordem, `nome\ntamanho\n` e
// o conteúdo), sem usar o código do banco. Os relatórios e os registos assinados que citam a
// 1.0.0 citam este valor: se mudar, deixaram de conferir.
const aos525DigestDaBateria1 = "sha256:9f78316b922cd1fbdf347a6e36d086c7fb1e4ad12b6397a0458002a246d39a72"

// aos525DigestDaBateria2 é o digest da bateria 2.0.0. Mudar um byte de um documento ou de um
// objectivo muda-o, e é outra bateria: muda-se a versão com ele.
const aos525DigestDaBateria2 = "sha256:fcc9bd8d08ffc3f39eff3a9408db319a8f9ccbb7205d2448f84879cf5fdf0c5a"

// aos525DigestDaPasta refaz o digest de uma bateria a partir dos ficheiros embebidos, por um
// caminho que não passa pelo carregamento do banco.
func aos525DigestDaPasta(t *testing.T, origem fs.FS, pasta string) string {
	t.Helper()
	entradas, err := fs.ReadDir(origem, pasta)
	if err != nil {
		t.Fatal(err)
	}
	var nomes []string
	for _, e := range entradas {
		nomes = append(nomes, e.Name())
	}
	sort.Strings(nomes)
	soma := sha256.New()
	for _, nome := range nomes {
		conteudo, err := fs.ReadFile(origem, pasta+"/"+nome)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(soma, "%s\n%d\n", nome, len(conteudo))
		soma.Write(conteudo)
	}
	return "sha256:" + hex.EncodeToString(soma.Sum(nil))
}

func aos525Bateria2(t *testing.T) *Bateria {
	t.Helper()
	b, err := CarregarBateriaPorDegraus()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAOS525_Degraus_ABateria2TemDigestProprioEA1MantemODeAntes(t *testing.T) {
	um, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	dois := aos525Bateria2(t)
	if um.Versao != "1.0.0" || um.Digest() != aos525DigestDaBateria1 {
		t.Errorf("a bateria 1.0.0 mudou: versao %s, digest %s (antes: %s)", um.Versao, um.Digest(), aos525DigestDaBateria1)
	}
	if got := aos525DigestDaPasta(t, ficheirosDaBateria, pastaDaBateria); got != aos525DigestDaBateria1 {
		t.Errorf("os ficheiros da bateria 1.0.0 mudaram: %s", got)
	}
	if um.TemDegraus() || um.Degraus() != 0 {
		t.Error("a bateria 1.0.0 nao e por degraus")
	}
	if dois.Versao != "2.0.0" || dois.Digest() != aos525DigestDaBateria2 {
		t.Errorf("bateria 2.0.0: versao %s, digest %s (quer %s)", dois.Versao, dois.Digest(), aos525DigestDaBateria2)
	}
	if got := aos525DigestDaPasta(t, ficheirosDaBateriaPorDegraus, pastaDaBateriaPorDegraus); got != dois.Digest() {
		t.Errorf("o digest da 2.0.0 nao e o dos seus ficheiros: %s", got)
	}
	if outra := aos525Bateria2(t); outra.Digest() != dois.Digest() {
		t.Error("a mesma bateria deu dois digests")
	}
	if dois.Digest() == um.Digest() {
		t.Error("as duas baterias tem o mesmo digest")
	}
	// A MESMA marca, na primeira linha de todos os documentos, e o mesmo prompt de sistema.
	if dois.Marca != um.Marca || dois.System != um.System {
		t.Error("a 2.0.0 tem de ter a marca e o prompt de sistema da 1.0.0")
	}
	primeiraLinha := func(b *Bateria, nome string) string {
		doc, _ := b.Documento(nome)
		return strings.SplitN(string(doc), "\n", 2)[0]
	}
	marca := primeiraLinha(um, um.NomesDosDocumentos()[0])
	daUm := map[string]bool{}
	var textoDaUm strings.Builder
	for _, nome := range um.NomesDosDocumentos() {
		daUm[nome] = true
		doc, _ := um.Documento(nome)
		textoDaUm.Write(doc)
	}
	if len(dois.NomesDosDocumentos()) != 12 {
		t.Errorf("a 2.0.0 tem %d documentos, quer 12", len(dois.NomesDosDocumentos()))
	}
	for _, nome := range dois.NomesDosDocumentos() {
		if primeiraLinha(dois, nome) != marca {
			t.Errorf("%s nao abre pela mesma primeira linha dos documentos da 1.0.0", nome)
		}
		if daUm[nome] {
			t.Errorf("%s existe nas duas baterias", nome)
		}
	}
	// Nomes e códigos que não existem na 1.0.0: nenhum facto da 2.0.0 ocorre nos documentos dela.
	idsDaUm := map[string]bool{}
	for _, c := range um.Casos {
		idsDaUm[c.ID] = true
	}
	for _, c := range dois.Casos {
		if idsDaUm[c.ID] {
			t.Errorf("o caso %s existe nas duas baterias", c.ID)
		}
		for _, n := range c.Nos {
			for _, f := range append(append([]string(nil), n.Factos...), n.FactosProibidos...) {
				if factoNaSaida(textoDaUm.String(), f) {
					t.Errorf("%s/%s: o facto %q tambem esta nos documentos da 1.0.0", c.ID, n.ID, f)
				}
			}
		}
	}
}

// O que cada degrau tem de ser, lido da estrutura e dos documentos — não da descrição.
func TestAOS525_Degraus_OsSeteDegrausPedemOQueDizem(t *testing.T) {
	b := aos525Bateria2(t)
	if b.Degraus() != 7 || len(b.Casos) != 7 || !b.TemDegraus() {
		t.Fatalf("a 2.0.0 tem %d degraus em %d casos, quer 7 em 7", b.Degraus(), len(b.Casos))
	}
	for i, c := range b.Casos {
		if c.Degrau != i+1 || c.ID != fmt.Sprintf("D%d", i+1) || !strings.Contains(c.Descricao, ": ") {
			t.Errorf("caso %d: id %s, degrau %d, descricao %q", i, c.ID, c.Degrau, c.Descricao)
		}
	}
	caso := func(id string) Caso { c, _ := b.Caso(id); return c }
	doc := func(nome string) string { d, _ := b.Documento(nome); return string(d) }

	if n := caso("D1").Nos[0]; len(caso("D1").Nos) != 1 || len(n.Exige) != 1 || len(n.FactosDe) != 1 || len(n.Factos) != 2 {
		t.Error("D1 tem de ser um no de leitura de um documento, com dois factos")
	}
	// Degrau 2: quatro documentos, e cada facto pedido está num documento DIFERENTE.
	n := caso("D2").Nos[0]
	if len(n.FactosDe) != 4 || len(n.Factos) != 4 || len(documentosNomeados(n.Objectivo)) != 4 {
		t.Fatal("D2 tem de ler quatro documentos e pedir quatro factos")
	}
	for i, f := range n.Factos {
		for j, nome := range n.FactosDe {
			if strings.Contains(doc(nome), f) != (i == j) {
				t.Errorf("D2: o facto %q tem de estar em %s e so nele", f, n.FactosDe[i])
			}
		}
	}
	// Degrau 3: de 25 a 30 KB, o facto no terço do meio, e distractores antes e depois dele.
	n = caso("D3").Nos[0]
	longo := doc(n.FactosDe[0])
	if len(longo) < 25_000 || len(longo) > 30_000 {
		t.Errorf("D3: o documento longo tem %d bytes, quer de 25 a 30 KB", len(longo))
	}
	onde := strings.Index(longo, n.Factos[0])
	if onde < len(longo)/3 || onde > 2*len(longo)/3 || strings.Count(longo, n.Factos[0]) != 1 {
		t.Errorf("D3: o facto tem de estar uma vez, no terco do meio do documento (esta em %d de %d)", onde, len(longo))
	}
	antes, depois := 0, 0
	for _, p := range n.FactosProibidos {
		switch i := strings.Index(longo, p); {
		case i < 0:
		case i < onde:
			antes++
		default:
			depois++
		}
	}
	if len(n.FactosProibidos) != 3 || antes == 0 || depois == 0 {
		t.Errorf("D3: quer tres distractores, com o facto entre eles (antes %d, depois %d)", antes, depois)
	}
	// Degrau 4: o nome está num documento, a chave que o escolhe está no outro, e os
	// distractores são os outros nomes do documento dos nomes.
	n = caso("D4").Nos[0]
	if len(n.FactosDe) != 2 || len(n.Factos) != 1 || len(n.FactosProibidos) != 3 {
		t.Fatal("D4 tem de cruzar dois documentos, com um facto e tres distractores")
	}
	escala, nomes := doc(n.FactosDe[0]), doc(n.FactosDe[1])
	if strings.Contains(escala, n.Factos[0]) || !strings.Contains(nomes, n.Factos[0]) || !strings.Contains(escala, "cais 6") || strings.Contains(nomes, "cais") {
		t.Error("D4: a resposta so pode sair de juntar os dois documentos")
	}
	for _, p := range n.FactosProibidos {
		if !strings.Contains(nomes, p) {
			t.Errorf("D4: o distractor %q nao esta na lista dos coordenadores", p)
		}
	}
	// Degrau 5: o documento negado EXISTE e tem o facto (a recusa é sobre um recurso real), e o
	// objectivo nomeia a cópia depois dele.
	n = caso("D5").Nos[0]
	nomeados := documentosNomeados(n.Objectivo)
	if n.Nega == "" || len(nomeados) != 2 || nomeados[0] != n.Nega || nomeados[1] != n.FactosDe[0] {
		t.Fatalf("D5 tem de negar o primeiro documento nomeado e dar a copia como segundo: %v", nomeados)
	}
	if !strings.Contains(doc(n.Nega), n.Factos[0]) || !strings.Contains(doc(n.FactosDe[0]), n.Factos[0]) {
		t.Error("D5: o facto tem de estar no documento negado e na copia")
	}
	// Degrau 6: o facto vedado está no mesmo documento do facto pedido.
	n = caso("D6").Nos[0]
	if len(n.Factos) != 1 || len(n.FactosProibidos) != 1 || !strings.Contains(doc(n.FactosDe[0]), n.FactosProibidos[0]) || !strings.Contains(n.Objectivo, "Do not mention") {
		t.Error("D6 tem de pedir um facto e vedar outro do mesmo documento")
	}
	// Degrau 7: quatro nós, cada um a consumir o anterior, só o primeiro com tool, e o mesmo
	// facto pedido em todos.
	c := caso("D7")
	if len(c.Nos) != 4 || len(c.Nos[0].Tools) != 1 {
		t.Fatal("D7 tem de ter quatro nos, o primeiro com a tool")
	}
	for i := 1; i < len(c.Nos); i++ {
		if c.Nos[i].EntradaDe != c.Nos[i-1].ID || len(c.Nos[i].Tools) != 0 || strings.Join(c.Nos[i].Factos, "|") != strings.Join(c.Nos[0].Factos, "|") {
			t.Errorf("D7/%s tem de consumir o no anterior, sem tools, com os mesmos factos", c.Nos[i].ID)
		}
	}
}

// aos525BateriaDeTeste devolve os ficheiros de uma bateria por degraus mínima e válida.
func aos525BateriaDeTeste() fstest.MapFS {
	return fstest.MapFS{
		"bateria2/casos.json": {Data: []byte(`{"versao":"t","marca":"MARCA","system":"s","casos":[` +
			`{"id":"D1","degrau":1,"descricao":"base: d","nos":[{"id":"ler","objectivo":"read a.txt","tools":["arquivo"],"exige":["arquivo"],"factos_de":["a.txt"],"factos":["F-1"],"factos_proibidos":["G-2"]}]},` +
			`{"id":"D2","degrau":2,"descricao":"outro: d","nos":[{"id":"ler","objectivo":"look at a.txt again","tools":["arquivo"],"exige":["arquivo"],"factos_de":["a.txt"],"factos":["F-1"]}]}]}`)},
		"bateria2/a.txt": {Data: []byte("MARCA de teste\ncom o facto F-1 e o distractor G-2\n")},
	}
}

func TestAOS525_Degraus_OCarregamentoEFailClosed(t *testing.T) {
	if _, err := carregarBateriaPorDegraus(aos525BateriaDeTeste()); err != nil {
		t.Fatalf("a bateria de teste tinha de carregar: %v", err)
	}
	casos := func(de, para string) fstest.MapFS {
		m := aos525BateriaDeTeste()
		cru := string(m["bateria2/casos.json"].Data)
		if strings.Count(cru, de) != 1 {
			t.Fatalf("a troca %q nao ocorre uma vez na bateria de teste", de)
		}
		m["bateria2/casos.json"] = &fstest.MapFile{Data: []byte(strings.Replace(cru, de, para, 1))}
		return m
	}
	documento := func(texto string) fstest.MapFS {
		m := aos525BateriaDeTeste()
		m["bateria2/a.txt"] = &fstest.MapFile{Data: []byte(texto)}
		return m
	}
	// Cada recusa tem de ser pela SUA razão: a mensagem do erro diz qual.
	type recusa struct {
		ficheiros fstest.MapFS
		razao     string
	}
	// Um proibido que só se confunde com o exigido depois da dobra (maiúsculas): está no documento.
	pelaDobra := casos(`"factos":["F-1"],"factos_proibidos":["G-2"]`, `"factos":["facto F-1"],"factos_proibidos":["FACTO F-1"]`)
	pelaDobra["bateria2/a.txt"] = &fstest.MapFile{Data: []byte("MARCA de teste\ncom o facto F-1 e o distractor G-2, e o FACTO F-1 em maiusculas\n")}
	// E um que só se confunde na forma larga dos proibidos (sem hífenes nem espaços).
	semHifen := casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":["F 1"]`)
	semHifen["bateria2/a.txt"] = &fstest.MapFile{Data: []byte("MARCA de teste\ncom o facto F-1 e o distractor G-2, e F 1 sem hifen\n")}
	for nome, r := range map[string]recusa{
		"documento sem marca":                    {documento("um documento qualquer\ncom o facto F-1 e o distractor G-2\n"), "nao abre pela marca"},
		"documento que nao e ASCII":              {documento("MARCA de teste\ncom o facto F-1 e o distractor G-2, com acentuação\n"), "nao e ASCII"},
		"facto exigido fora dos documentos":      {documento("MARCA de teste\nso o distractor G-2\n"), "um facto que nao esta nos seus documentos"},
		"proibido fora dos documentos":           {documento("MARCA de teste\nso o facto F-1\n"), "nao e um distractor"},
		"proibido igual ao exigido":              {casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":["F-1"]`), "igual a um facto exigido"},
		"proibido igual ao exigido, pela dobra":  {pelaDobra, "igual a um facto exigido"},
		"proibido igual ao exigido, sem o hifen": {semHifen, "igual a um facto exigido"},
		"proibido contido no exigido":            {casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":["F-"]`), "igual a um facto exigido"},
		"exigido contido no proibido":            {casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":["facto F-1 e"]`), "igual a um facto exigido"},
		"proibido vazio":                         {casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":[""]`), "facto proibido vazio"},
		"proibido com espacos nas pontas":        {casos(`"factos_proibidos":["G-2"]`, `"factos_proibidos":[" G-2"]`), "facto proibido vazio"},
		"degrau em falta":                        {casos(`"id":"D2","degrau":2,`, `"id":"D2",`), "D2 nao tem degrau"},
		"degrau a zero":                          {casos(`"degrau":1,`, `"degrau":0,`), "D1 nao tem degrau"},
		"degrau negativo":                        {casos(`"degrau":1,`, `"degrau":-1,`), "D1 nao tem degrau"},
		"degrau saltado":                         {casos(`"degrau":2,`, `"degrau":3,`), "falta o degrau 2"},
		"degraus fora de ordem":                  {casos(`"id":"D1","degrau":1,`, `"id":"D1","degrau":3,`), "fora de ordem"},
		"degrau sem a capacidade por extenso":    {casos(`"descricao":"outro: d"`, `"descricao":" "`), "nao diz por extenso"},
		"facto exigido escrito no objectivo":     {casos(`"objectivo":"read a.txt"`, `"objectivo":"read a.txt and say f-1"`), "escrito no proprio objectivo"},
		"facto proibido escrito no objectivo":    {casos(`"objectivo":"read a.txt"`, `"objectivo":"read a.txt, not G-2"`), "escrito no proprio objectivo"},
		"dois nos com o mesmo objectivo":         {casos(`"objectivo":"look at a.txt again"`, `"objectivo":"read a.txt"`), "objectivo igual ao de outro no"},
		"um objectivo contido noutro":            {casos(`"objectivo":"look at a.txt again"`, `"objectivo":"first read a.txt, then stop"`), "objectivo igual ao de outro no"},
		"campo desconhecido":                     {casos(`"degrau":2,`, `"degrau":2,"dificuldade":9,`), "casos mal formados"},
		"documento referido fora da bateria":     {casos(`"factos_de":["a.txt"],"factos":["F-1"]}]}]}`, `"factos_de":["../b.txt"],"factos":["F-1"]}]}]}`), "documento fora da bateria"},
		"pasta de outra bateria":                 {bateriaDeTeste(), "casos ilegiveis"},
	} {
		_, err := carregarBateriaPorDegraus(r.ficheiros)
		if !errors.Is(err, ErrBateria) || !strings.Contains(err.Error(), r.razao) {
			t.Errorf("%s: err = %v, quer ErrBateria com %q", nome, err, r.razao)
		}
	}
	// O campo do degrau só vale na bateria por degraus: a outra recusa-o, em vez de o ignorar.
	comDegrau := bateriaDeTeste()
	comDegrau["bateria/casos.json"] = &fstest.MapFile{Data: []byte(strings.Replace(string(comDegrau["bateria/casos.json"].Data), `"id":"T1",`, `"id":"T1","degrau":1,`, 1))}
	if _, err := carregarBateria(comDegrau); !errors.Is(err, ErrBateria) {
		t.Errorf("degrau numa bateria sem degraus: err = %v, quer ErrBateria", err)
	}
	// Os factos proibidos valem em qualquer bateria, com a mesma validação.
	comProibido := func(valor string) fstest.MapFS {
		m := bateriaDeTeste()
		m["bateria/casos.json"] = &fstest.MapFile{Data: []byte(strings.Replace(string(m["bateria/casos.json"].Data), `"factos":["F-1"]`, `"factos":["F-1"],"factos_proibidos":["`+valor+`"]`, 1))}
		return m
	}
	if _, err := carregarBateria(comProibido("teste")); err != nil {
		t.Errorf("um proibido que esta no documento tinha de carregar: %v", err)
	}
	if _, err := carregarBateria(comProibido("ausente")); !errors.Is(err, ErrBateria) {
		t.Errorf("proibido fora dos documentos, bateria sem degraus: err = %v, quer ErrBateria", err)
	}
}

// ambienteDosDegraus é um nó de ensaio com a bateria por degraus, ligado a um provider falso.
type ambienteDosDegraus struct {
	bateria *Bateria
	falso   *ProviderFalso
	no      *NoDeEnsaio
}

func novoAmbienteDosDegraus(t *testing.T, roteiro []Comportamento, contador *Contador) *ambienteDosDegraus {
	t.Helper()
	b := aos525Bateria2(t)
	falso := NovoProviderFalso(roteiro)
	falso.GuardarCorpos()
	srv := httptest.NewServer(falso)
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste", Contador: contador})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	return &ambienteDosDegraus{bateria: b, falso: falso, no: no}
}

func (a *ambienteDosDegraus) cfg(amostras int, contador *Contador) CfgDaCorrida {
	return CfgDaCorrida{
		Modo: ModoFalso, Plano: PlanoDosDegraus(a.bateria, amostras, 525), Bateria: a.bateria, No: a.no, Contador: contador,
		Rota:    RotaDoRelatorio{Fornecedor: string(FornecedorFalso), Modelo: "modelo-falso-do-banco", Digest: DigestDaRota("falso", "modelo-falso-do-banco", "")},
		Relogio: relogioFixo,
	}
}

// aos525Correr corre uma passagem pela bateria por degraus contra o falso com o roteiro dado.
func aos525Correr(t *testing.T, roteiro []Comportamento) *Relatorio {
	t.Helper()
	return aos525CorrerVezes(t, roteiro, 1)
}

// aos525CorrerVezes é [aos525Correr] com o número de passagens dado.
func aos525CorrerVezes(t *testing.T, roteiro []Comportamento, amostras int) *Relatorio {
	t.Helper()
	a := novoAmbienteDosDegraus(t, roteiro, nil)
	r, err := Correr(context.Background(), a.cfg(amostras, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.Terminou != TerminouCompleta || r.Taxas.PorDegrau == nil || len(r.Taxas.PorDegrau.Degraus) != 7 {
		t.Fatalf("a corrida dos degraus terminou %s, com o bloco por degrau %+v", r.Terminou, r.Taxas.PorDegrau)
	}
	return r
}

// aos525Roteiro devolve um roteiro de `conversas` posições em que todas cumprem, salvo as dadas.
// A posição é a ordem de chegada da conversa: um run por nó, pela ordem da bateria.
func aos525Roteiro(conversas int, excepto map[int]Comportamento) []Comportamento {
	out := make([]Comportamento, conversas)
	for i := range out {
		out[i] = ComportamentoDegrauCumpre
		if c, tem := excepto[i]; tem {
			out[i] = c
		}
	}
	return out
}

// fracao é uma contagem esperada: numerador e denominador.
type fracao struct{ n, d int }

func (f fracao) confere(t Taxa) bool { return t.Numerador == f.n && t.Denominador == f.d }

// O CRITÉRIO 6: cada degrau tem um modelo falso que o cumpre e um que o falha, e o relatório
// distingue-os — pelo agregado e pela RAZÃO (contrato, facto ausente, resposta errada).
func TestAOS525_Degraus_CadaDegrauTemQuemOCumpraEQuemOFalhe(t *testing.T) {
	// QUEM CUMPRE: os sete degraus saem cumpridos, sem ausências nem respostas erradas.
	cumpre := aos525Correr(t, []Comportamento{ComportamentoDegrauCumpre})
	nosDoDegrau := []int{1, 1, 1, 1, 1, 1, 4}
	for i, d := range cumpre.Taxas.PorDegrau.Degraus {
		if d.Degrau != i+1 || d.Passagens != 1 || !(fracao{1, 1}).confere(d.DegrauCumprido) || !(fracao{nosDoDegrau[i], nosDoDegrau[i]}).confere(d.NosCumpridos) ||
			d.SemToolCallNaPrimeira.Numerador != 0 || d.FactosAusentes.Numerador != 0 || d.RespostasErradas.Numerador != 0 {
			t.Errorf("com o falso que cumpre, o degrau %d tinha de sair cumprido: %+v", d.Degrau, d)
		}
		if d.DegrauCumprido.Valor == nil || d.DegrauCumprido.IC95Inferior == nil || d.DegrauCumprido.IC95Superior == nil || *d.DegrauCumprido.Valor != 1 {
			t.Errorf("degrau %d: o agregado tem de trazer a taxa e o intervalo de 95%%", d.Degrau)
		}
		if d.Capacidade == "" || len(d.Casos) != 1 {
			t.Errorf("degrau %d sem a capacidade por extenso ou sem o seu caso", d.Degrau)
		}
	}
	// O degrau 5 faz-se em TRÊS pedidos: a leitura negada, a da cópia, e a resposta.
	for _, o := range cumpre.Observacoes {
		if o.Caso == "D5" && (o.Pedidos != 3 || o.ToolCalls != 2 || o.Desfecho != DesfechoCumprido) {
			t.Errorf("D5 com o falso que cumpre: %d pedidos e %d tool calls, quer 3 e 2 (a segunda chamada)", o.Pedidos, o.ToolCalls)
		}
		if o.Caso == "D2" && (o.Pedidos != 2 || o.ToolCalls != 4) {
			t.Errorf("D2 com o falso que cumpre: %d pedidos e %d tool calls, quer 2 e 4 (no mesmo passo)", o.Pedidos, o.ToolCalls)
		}
	}

	// QUEM FALHA, degrau a degrau. A posição no roteiro é a conversa: D1 a D6 são as conversas 0
	// a 5 e os quatro nós de D7 as 6 a 9 (uma nova tentativa é outra conversa).
	aposDenial := string(agentruntime.OutcomeContractAfterDenial)
	semChamada := string(agentruntime.OutcomeContractNoCall)
	for _, tc := range []struct {
		nome    string
		degrau  int
		roteiro []Comportamento
		// o que o degrau que falha tem de mostrar
		nos, semTool, ausentes, erradas fracao
		desfecho                        string
		runs                            int
	}{
		{"D1 pelo contrato: nunca pede a tool", 1,
			aos525Roteiro(12, map[int]Comportamento{0: ComportamentoTexto, 1: ComportamentoTexto, 2: ComportamentoTexto}),
			fracao{0, 1}, fracao{1, 1}, fracao{0, 0}, fracao{0, 0}, semChamada, 3},
		{"D1 por um facto ausente", 1, aos525Roteiro(10, map[int]Comportamento{0: ComportamentoDegrauOmite}),
			fracao{1, 1}, fracao{0, 1}, fracao{1, 1}, fracao{0, 1}, DesfechoCumprido, 1},
		{"D1 por juntar o outro codigo do documento", 1, aos525Roteiro(10, map[int]Comportamento{0: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D2 por so ler o primeiro dos quatro", 2, aos525Roteiro(10, map[int]Comportamento{1: ComportamentoDegrauUmaLeitura}),
			fracao{1, 1}, fracao{0, 1}, fracao{1, 1}, fracao{0, 1}, DesfechoCumprido, 1},
		{"D2 por juntar um valor que a pergunta nao pede", 2, aos525Roteiro(10, map[int]Comportamento{1: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D5 por juntar outro codigo da copia", 5, aos525Roteiro(10, map[int]Comportamento{4: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D3 por devolver um distractor", 3, aos525Roteiro(10, map[int]Comportamento{2: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D4 por devolver o coordenador de outro turno", 4, aos525Roteiro(10, map[int]Comportamento{3: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D5 por nao fazer a segunda chamada", 5, aos525Roteiro(10, map[int]Comportamento{4: ComportamentoDegrauUmaLeitura}),
			fracao{0, 1}, fracao{0, 1}, fracao{0, 0}, fracao{0, 0}, aposDenial, 1},
		{"D6 por mencionar o que a instrucao veda", 6, aos525Roteiro(10, map[int]Comportamento{5: ComportamentoDegrauDistractor}),
			fracao{1, 1}, fracao{0, 1}, fracao{0, 1}, fracao{1, 1}, DesfechoCumprido, 1},
		{"D7 por o terceiro no deixar cair o facto", 7, aos525Roteiro(10, map[int]Comportamento{8: ComportamentoDegrauOmite}),
			fracao{4, 4}, fracao{0, 1}, fracao{2, 4}, fracao{0, 3}, DesfechoCumprido, 4},
	} {
		t.Run(tc.nome, func(t *testing.T) {
			r := aos525Correr(t, tc.roteiro)
			for _, d := range r.Taxas.PorDegrau.Degraus {
				if d.Degrau != tc.degrau {
					// Os outros degraus não são tocados: saem cumpridos.
					if !(fracao{1, 1}).confere(d.DegrauCumprido) || d.FactosAusentes.Numerador != 0 || d.RespostasErradas.Numerador != 0 || d.Interrompidas != 0 {
						t.Errorf("o degrau %d nao era o que falhava e nao saiu cumprido: %+v", d.Degrau, d)
					}
					continue
				}
				if !(fracao{0, 1}).confere(d.DegrauCumprido) || *d.DegrauCumprido.Valor != 0 || d.Interrompidas != 0 {
					t.Errorf("o degrau %d tinha de sair NAO cumprido: %+v", d.Degrau, d.DegrauCumprido)
				}
				if !tc.nos.confere(d.NosCumpridos) || !tc.semTool.confere(d.SemToolCallNaPrimeira) || !tc.ausentes.confere(d.FactosAusentes) || !tc.erradas.confere(d.RespostasErradas) {
					t.Errorf("o degrau %d falhou pela razao errada: nos %d/%d, sem tool %d/%d, ausentes %d/%d, erradas %d/%d",
						d.Degrau, d.NosCumpridos.Numerador, d.NosCumpridos.Denominador, d.SemToolCallNaPrimeira.Numerador, d.SemToolCallNaPrimeira.Denominador,
						d.FactosAusentes.Numerador, d.FactosAusentes.Denominador, d.RespostasErradas.Numerador, d.RespostasErradas.Denominador)
				}
			}
			runs := 0
			for _, o := range r.Observacoes {
				if o.Caso != fmt.Sprintf("D%d", tc.degrau) {
					continue
				}
				runs++
				if o.Desfecho != tc.desfecho {
					t.Errorf("%s/%s fechou %s, quer %s", o.Caso, o.No, o.Desfecho, tc.desfecho)
				}
			}
			if runs != tc.runs {
				t.Errorf("o degrau %d teve %d runs, quer %d", tc.degrau, runs, tc.runs)
			}
		})
	}
}

// A cadeia do degrau 7 é uma cadeia: o facto que um nó deixa cair NÃO reaparece no seguinte, e
// um nó que não conclui POR CULPA DO MODELO deixa os seguintes por correr — que contam como não
// cumpridos, e não como interrompidos.
func TestAOS525_Degraus_NaCadeiaOFactoPerdidoNaoVolta(t *testing.T) {
	r := aos525Correr(t, aos525Roteiro(10, map[int]Comportamento{8: ComportamentoDegrauOmite}))
	factos := map[string]string{}
	for _, o := range r.Observacoes {
		if o.Caso == "D7" {
			factos[o.No] = o.Factos
		}
	}
	if factos["ler"] != FactosPresentes || factos["extrair"] != FactosPresentes || factos["reescrever"] != FactosAusentes || factos["resumir"] != FactosAusentes {
		t.Errorf("D7: o facto perde-se no terceiro no e nao volta no quarto: %v", factos)
	}
	// O segundo nó responde vazio nas três tentativas: não conclui, e os dois seguintes não correm.
	r = aos525Correr(t, aos525Roteiro(10, map[int]Comportamento{7: ComportamentoVazia, 8: ComportamentoVazia, 9: ComportamentoVazia}))
	d7 := r.Taxas.PorDegrau.Degraus[6]
	if !(fracao{0, 1}).confere(d7.DegrauCumprido) || !(fracao{1, 4}).confere(d7.NosCumpridos) || d7.Interrompidas != 0 || r.Taxas.Desfechos[string(agentruntime.OutcomeEmptyOutput)] != 3 {
		t.Errorf("D7 com o segundo no vazio: cumprido %d/%d, nos %d/%d, interrompidas %d (quer 0/1, 1/4 e 0); desfechos %v",
			d7.DegrauCumprido.Numerador, d7.DegrauCumprido.Denominador, d7.NosCumpridos.Numerador, d7.NosCumpridos.Denominador, d7.Interrompidas, r.Taxas.Desfechos)
	}
}

// UM ERRO PASSAGEIRO NÃO É UM DEGRAU FALHADO. Uma passagem em que um nó fechou por uma causa
// passageira — aqui um HTTP 500 — sai de todas as taxas do degrau e conta à parte, com a causa,
// pela classificação que a qualificação usa. Um degrau só com passagens interrompidas não tem taxa.
func TestAOS525_Degraus_UmErroPassageiroInterrompeAPassagemENaoFalhaODegrau(t *testing.T) {
	semTaxa := func(nome string, d TaxasDoDegrau) {
		t.Helper()
		for campo, taxa := range map[string]Taxa{"cumprido": d.DegrauCumprido, "nos": d.NosCumpridos, "sem tool": d.SemToolCallNaPrimeira, "ausentes": d.FactosAusentes, "erradas": d.RespostasErradas} {
			if !(fracao{0, 0}).confere(taxa) || taxa.Valor != nil {
				t.Errorf("%s: a taxa %q tinha de ser 0/0, sem valor: %d/%d", nome, campo, taxa.Numerador, taxa.Denominador)
			}
		}
		if d.Passagens != 1 || d.Interrompidas != 1 || d.InterrompidasPorCausa[InterrompidoPor5xx] != 1 || len(d.InterrompidasPorCausa) != 1 {
			t.Errorf("%s: %d passagem(ns), %d interrompida(s), causas %v", nome, d.Passagens, d.Interrompidas, d.InterrompidasPorCausa)
		}
	}
	// O único pedido de D3 leva 500.
	r := aos525Correr(t, aos525Roteiro(10, map[int]Comportamento{2: ComportamentoErro500}))
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if d.Degrau == 3 {
			semTaxa("D3 com um 500", d)
			continue
		}
		if !(fracao{1, 1}).confere(d.DegrauCumprido) || d.Interrompidas != 0 || len(d.InterrompidasPorCausa) != 0 {
			t.Errorf("o degrau %d nao foi interrompido e tinha de sair cumprido: %+v", d.Degrau, d)
		}
	}
	linhas := strings.Split(ResumoEmTexto(r), "\n")
	for i, l := range linhas {
		if strings.HasPrefix(l, "POR DEGRAU (") {
			if cabeca := strings.Fields(linhas[i+1]); len(cabeca) < 3 || cabeca[2] != "interrompidas" {
				t.Errorf("a tabela do resumo nao tem a coluna das interrompidas: %q", linhas[i+1])
			}
			// A linha do degrau sem amostra vem MARCADA, com o total ao lado das interrompidas, e a
			// legenda da marca vem a seguir à tabela; as outras linhas não têm marca.
			if campos := strings.Fields(linhas[i+4]); strings.Join(campos[:10], " ") != "! 3 0/0 1 de 1 0/0 0/0 0/0 0/0" {
				t.Errorf("linha do degrau 3 no resumo: %q", linhas[i+4])
			}
			if campos := strings.Fields(linhas[i+3]); strings.Join(campos[:5], " ") != "2 1/1 0 de 1" {
				t.Errorf("linha do degrau 2 no resumo: %q", linhas[i+3])
			}
			if !strings.HasPrefix(linhas[i+9], "  ! = amostra reduzida por interrupcoes") || !strings.Contains(linhas[i+9], "a taxa nao e fiavel") {
				t.Errorf("falta a legenda da marca de amostra reduzida: %q", linhas[i+9])
			}
		}
	}
	// Um nó INTERMÉDIO de D7 leva 500 (o `extrair`): os dois seguintes não correm, e a passagem
	// inteira sai — não fica «um nó em quatro».
	r = aos525Correr(t, aos525Roteiro(8, map[int]Comportamento{7: ComportamentoErro500}))
	semTaxa("D7 com um 500 no segundo no", r.Taxas.PorDegrau.Degraus[6])
	// Com duas passagens, uma interrompida e uma inteira: o degrau mede-se sobre a que ficou.
	r = aos525CorrerVezes(t, aos525Roteiro(18, map[int]Comportamento{7: ComportamentoErro500}), 2)
	d7 := r.Taxas.PorDegrau.Degraus[6]
	if d7.Passagens != 2 || d7.Interrompidas != 1 || !(fracao{1, 1}).confere(d7.DegrauCumprido) || !(fracao{4, 4}).confere(d7.NosCumpridos) || !(fracao{0, 4}).confere(d7.FactosAusentes) {
		t.Errorf("D7 com uma passagem interrompida em duas: %+v", d7)
	}
	// CONTROLO: um 400 ao segundo pedido fala do PEDIDO, não é passageiro — o degrau falha.
	r = aos525Correr(t, aos525Roteiro(10, map[int]Comportamento{2: ComportamentoRejeitaSegundo}))
	if d3 := r.Taxas.PorDegrau.Degraus[2]; d3.Interrompidas != 0 || !(fracao{0, 1}).confere(d3.DegrauCumprido) || !(fracao{0, 1}).confere(d3.NosCumpridos) {
		t.Errorf("D3 com um 400 de parametro tinha de falhar, e nao de sair da amostra: %+v", d3)
	}
}

// O DEGRAU 2 É VOLUME, NÃO PARALELISMO. Um modelo que lê os quatro documentos um por turno
// cumpre-o: são quatro turnos de leitura e um de resposta, e a experiência dá seis.
func TestAOS525_Degraus_LerUmDocumentoPorTurnoCumpreODegrau2(t *testing.T) {
	b := aos525Bateria2(t)
	d2, _ := b.Caso("D2")
	if strings.Contains(d2.Nos[0].Objectivo, "same step") || strings.Contains(d2.Descricao, "mesmo passo") {
		t.Error("D2 nao pode exigir as quatro leituras no mesmo passo")
	}
	if MaxTurnosDosDegraus != 6 || MaxTurnosPorOmissao != 4 {
		t.Fatalf("turnos: degraus %d (quer 6), as outras experiencias %d (quer 4)", MaxTurnosDosDegraus, MaxTurnosPorOmissao)
	}
	r := aos525Correr(t, []Comportamento{ComportamentoDegrauUmPorTurno})
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if !(fracao{1, 1}).confere(d.DegrauCumprido) || d.RespostasErradas.Numerador != 0 || d.FactosAusentes.Numerador != 0 {
			t.Errorf("com uma leitura por turno, o degrau %d tinha de sair cumprido: %+v", d.Degrau, d)
		}
	}
	for _, o := range r.Observacoes {
		// Cinco pedidos: com os quatro turnos das outras experiências este run não cabia.
		if o.Caso == "D2" && (o.Pedidos != 5 || o.ToolCalls != 4 || o.Desfecho != DesfechoCumprido || o.Pedidos <= MaxTurnosPorOmissao) {
			t.Errorf("D2 com uma leitura por turno: %d pedidos, %d tool calls, %s (quer 5, 4 e cumprido)", o.Pedidos, o.ToolCalls, o.Desfecho)
		}
		if o.Pedidos > MaxTurnosDosDegraus {
			t.Errorf("%s/%s fez %d pedidos, mais do que o tecto de turnos", o.Caso, o.No, o.Pedidos)
		}
	}
}

// OS FACTOS EXIGIDOS SÃO OS QUE O OBJECTIVO PEDE. O digest prende os bytes; esta tabela prende o
// SENTIDO: para cada facto, a frase do objectivo que o pede e a frase do documento que o dá —
// e, para cada proibido, a frase do documento de onde vem, que é outra.
func TestAOS525_Degraus_OsFactosSaoOsQueOObjectivoPede(t *testing.T) {
	b := aos525Bateria2(t)
	type facto struct{ valor, pedido, noDocumento string }
	type no struct {
		caso, id  string
		exigidos  []facto
		proibidos []facto
	}
	cadeia := []facto{
		{"AV-3157", "the fault reference", "A avaria com a referencia AV-3157 foi registada"},
		{"Baltasar Girao-Neves", "the name of the technician", "entregue ao tecnico Baltasar Girao-Neves"},
	}
	foraDaCadeia := []facto{
		{"EM-2290", "", "a ementa da semana tem a referencia EM-2290"},
		{"PB-6612", "", "o cartao de acesso PB-6612"},
	}
	tabela := []no{
		{"D1", "ler", []facto{
			{"RM-5206", "the code of the consignment", "A remessa com o codigo RM-5206 foi recebida"},
			{"Duarte Meireles-Sardo", "the name of the checker who received it", "pelo conferente Duarte Meireles-Sardo"},
		}, []facto{{"FD-30771", "", "folha de divergencias com o numero FD-30771"}}},
		{"D2", "ler-quatro", []facto{
			{"SL-3391", "the door seal number of the north chamber", "camara fria norte"},
			{"Ines Portocarrero-Lima", "the name of the technician of the south chamber", "camara fria sul"},
			{"4127", "the pallet count of the east chamber", "camara fria leste"},
			{"OM-60518", "the open maintenance order of the west chamber", "camara fria oeste"},
		}, []facto{
			{"SL-9313", "", "camara fria sul"}, {"SL-1939", "", "camara fria leste"}, {"SL-3919", "", "camara fria oeste"},
			{"Horacio Bettencourt-Rua", "", "camara fria norte"}, {"Vasco Almada-Teixeira", "", "camara fria leste"}, {"Graca Sequeira-Nobre", "", "camara fria oeste"},
			{"3058", "", "camara fria norte"}, {"2274", "", "camara fria sul"}, {"1863", "", "camara fria oeste"},
			{"OM-71204", "", "camara fria norte"}, {"OM-12470", "", "camara fria sul"}, {"OM-40721", "", "camara fria leste"},
		}},
		{"D3", "achar", []facto{{"KV-7302", "the batch that was held for inspection at cais 7", "o lote KV-7302 ficou retido para inspeccao no cais 7,"}},
			[]facto{
				{"KV-7032", "", "o lote KV-7032 ficou retido para inspeccao no cais 2,"},
				{"KV-3702", "", "o lote KV-3702 ficou retido para inspeccao no cais 5,"},
				{"KV-7320", "", "o lote KV-7320 ficou retido para inspeccao no cais 9,"},
			}},
		{"D4", "cruzar", []facto{{"Filipa Noronha-Teles", "the person who coordinates the shift assigned to cais 6", "O turno Ambar e coordenado por Filipa Noronha-Teles."}},
			[]facto{
				{"Rodrigo Vilarinho-Paz", "", "O turno Dolomite e coordenado por Rodrigo Vilarinho-Paz."},
				{"Anselmo Queiroga-Brito", "", "O turno Cobalto e coordenado por Anselmo Queiroga-Brito."},
				{"Leonor Vasques-Maia", "", "O turno Basalto e coordenado por Leonor Vasques-Maia."},
			}},
		{"D5", "outro-caminho", []facto{{"CF-27745", "the reconciliation code of the inventory", "O codigo de reconciliacao do inventario e o CF-27745."}},
			[]facto{{"AC-90436", "", "A acta da conferencia anterior tem o\nnumero AC-90436."}}},
		{"D6", "restricao", []facto{{"LF-8843", "the code of the batch", "O lote LF-8843 foi entregue"}},
			[]facto{{"Quelhas-Bordalo", "", "pelo fornecedor Quelhas-Bordalo e Filhos"}}},
		{"D7", "ler", []facto{
			{"AV-3157", "reply with the full text of the document", cadeia[0].noDocumento},
			{"Baltasar Girao-Neves", "reply with the full text of the document", cadeia[1].noDocumento},
		}, nil},
		{"D7", "extrair", cadeia, foraDaCadeia},
		{"D7", "reescrever", cadeia, foraDaCadeia},
		{"D7", "resumir", cadeia, foraDaCadeia},
	}
	nos := 0
	for _, c := range b.Casos {
		nos += len(c.Nos)
	}
	if nos != len(tabela) {
		t.Fatalf("a bateria tem %d nos e a tabela %d", nos, len(tabela))
	}
	valores := func(l []facto) string {
		var out []string
		for _, f := range l {
			out = append(out, f.valor)
		}
		return strings.Join(out, "|")
	}
	for _, linha := range tabela {
		c, _ := b.Caso(linha.caso)
		var n No
		for _, candidato := range c.Nos {
			if candidato.ID == linha.id {
				n = candidato
			}
		}
		if strings.Join(n.Factos, "|") != valores(linha.exigidos) || strings.Join(n.FactosProibidos, "|") != valores(linha.proibidos) {
			t.Errorf("%s/%s: exigidos %v e proibidos %v nao sao os da tabela", linha.caso, linha.id, n.Factos, n.FactosProibidos)
			continue
		}
		var documentos strings.Builder
		for _, nome := range n.FactosDe {
			doc, _ := b.Documento(nome)
			documentos.Write(doc)
		}
		// O documento que tem a frase tem também o valor: é dali que ele vem.
		daFrase := func(f facto) bool {
			for _, nome := range n.FactosDe {
				doc, _ := b.Documento(nome)
				if strings.Contains(string(doc), f.noDocumento) && strings.Contains(string(doc), f.valor) {
					return true
				}
			}
			return false
		}
		for _, f := range linha.exigidos {
			if !strings.Contains(n.Objectivo, f.pedido) {
				t.Errorf("%s/%s: o objectivo nao pede %q (%q)", linha.caso, linha.id, f.valor, f.pedido)
			}
			if !daFrase(f) {
				t.Errorf("%s/%s: o facto %q nao vem da frase %q", linha.caso, linha.id, f.valor, f.noDocumento)
			}
		}
		// O QUE É PROIBIDO TEM DE ESTAR VEDADO NO OBJECTIVO, que é o que o modelo lê (a descrição
		// do caso não chega ao modelo). Sem isto, uma resposta certa que citasse o contexto — o
		// técnico ao lado do selo, a folha de divergências — contava como errada sem a instrução
		// o ter pedido. A excepção são os dois últimos nós da cadeia: os seus proibidos só lhes
		// chegam se o `extrair` errou, e o que medem é o erro a propagar-se.
		exclusao := false
		for _, frase := range []string{"and with no other", "Do not mention", "leave out everything else"} {
			exclusao = exclusao || strings.Contains(n.Objectivo, frase)
		}
		semExclusao := linha.caso == "D7" && (linha.id == "reescrever" || linha.id == "resumir")
		if len(n.FactosProibidos) > 0 && exclusao == semExclusao {
			t.Errorf("%s/%s: tem factos proibidos e o objectivo tem exclusao explicita = %v (quer %v)", linha.caso, linha.id, exclusao, !semExclusao)
		}
		// Nenhum objectivo traz um facto escrito, exigido ou proibido, de nó nenhum da bateria.
		for _, outra := range tabela {
			for _, f := range append(append([]facto(nil), outra.exigidos...), outra.proibidos...) {
				if proibidoNaSaida(n.Objectivo, f.valor) {
					t.Errorf("%s/%s: o objectivo traz escrito o facto %q", linha.caso, linha.id, f.valor)
				}
			}
		}
		for _, f := range linha.proibidos {
			if !daFrase(f) {
				t.Errorf("%s/%s: o proibido %q nao vem da frase %q", linha.caso, linha.id, f.valor, f.noDocumento)
			}
		}
		// A resposta certa — os factos exigidos, por qualquer ordem, colados ou separados — não
		// dispara proibido nenhum, nem na forma larga em que os proibidos se comparam.
		for i := range n.Factos {
			certa := strings.Join(append(append([]string(nil), n.Factos[i:]...), n.Factos[:i]...), " ")
			colada := strings.ReplaceAll(certa, " ", "")
			if verificarProibidos(certa, n.FactosProibidos) != ProibidosNenhum || verificarProibidos(colada, n.FactosProibidos) != ProibidosNenhum || verificarFactos(certa, n.Factos) != FactosPresentes {
				t.Errorf("%s/%s: a resposta certa %q dispara um proibido ou perde um facto", linha.caso, linha.id, certa)
			}
		}
		// E despejar os documentos do nó dispara-os, onde os há.
		if len(n.FactosProibidos) > 0 && verificarProibidos(documentos.String(), n.FactosProibidos) != ProibidosPresente {
			t.Errorf("%s/%s: os documentos inteiros na saida tinham de contar como resposta errada", linha.caso, linha.id)
		}
	}
	// Em D2 as quatro fichas têm os mesmos quatro campos: o valor pedido de cada tipo é um, e os
	// outros três do mesmo tipo são proibidos.
	d2, _ := b.Caso("D2")
	for _, prefixo := range []string{"SL-", "OM-"} {
		pedidos, vedados := 0, 0
		for _, f := range d2.Nos[0].Factos {
			if strings.HasPrefix(f, prefixo) {
				pedidos++
			}
		}
		for _, f := range d2.Nos[0].FactosProibidos {
			if strings.HasPrefix(f, prefixo) {
				vedados++
			}
		}
		if pedidos != 1 || vedados != 3 {
			t.Errorf("D2, campo %s: %d pedido(s) e %d proibido(s), quer 1 e 3", prefixo, pedidos, vedados)
		}
	}
}

// DESPEJAR O DOCUMENTO NÃO CUMPRE DEGRAU NENHUM. O falso de sempre (`cumpre`) devolve tudo o que
// as tools entregaram e o que o pedido trazia — o eco. Na bateria por degraus isso falha os SETE
// degraus por resposta errada: é para isso que servem os factos proibidos, e é por isso que a
// experiência tem o seu roteiro.
func TestAOS525_Degraus_OFalsoDeSempreDespejaOsDistractores(t *testing.T) {
	r := aos525Correr(t, []Comportamento{ComportamentoCumpre})
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if !(fracao{0, 1}).confere(d.DegrauCumprido) || d.RespostasErradas.Numerador == 0 || d.FactosAusentes.Numerador != 0 || d.Interrompidas != 0 {
			t.Errorf("degrau %d com o falso que despeja o documento: cumprido %d/%d, erradas %d/%d, ausentes %d", d.Degrau,
				d.DegrauCumprido.Numerador, d.DegrauCumprido.Denominador, d.RespostasErradas.Numerador, d.RespostasErradas.Denominador, d.FactosAusentes.Numerador)
		}
	}
	// No degrau 7 o primeiro nó PEDE o texto inteiro, e não tem proibidos; os três seguintes têm.
	if d7 := r.Taxas.PorDegrau.Degraus[6]; !(fracao{3, 3}).confere(d7.RespostasErradas) || !(fracao{4, 4}).confere(d7.NosCumpridos) {
		t.Errorf("D7 com o eco: erradas %d/%d, nos %d/%d (quer 3/3 e 4/4)", d7.RespostasErradas.Numerador, d7.RespostasErradas.Denominador, d7.NosCumpridos.Numerador, d7.NosCumpridos.Denominador)
	}
	// E o que não pede a tool falha os degraus com tool pelo contrato; a cadeia não passa do primeiro nó.
	r = aos525Correr(t, []Comportamento{ComportamentoTexto})
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if !(fracao{0, 1}).confere(d.DegrauCumprido) || d.NosCumpridos.Numerador != 0 || !(fracao{1, 1}).confere(d.SemToolCallNaPrimeira) {
			t.Errorf("degrau %d com o falso que nao pede a tool: %+v", d.Degrau, d)
		}
	}
	// Os comportamentos dos degraus não servem a bateria 1.0.0: o falso recusa o pedido, em vez
	// de responder com factos de outra bateria. (A linha de comandos nem os deixa chegar aqui.)
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoDegrauCumpre}, nil)
	um := a.correr(t, PlanoDaBateria(a.bateria, BracoA, 1), nil)
	if um.Taxas.Desfechos[DesfechoErroHTTP] != um.Taxas.N || um.Taxas.HTTP["400"] != um.Taxas.N || um.Taxas.N == 0 {
		t.Errorf("um comportamento dos degraus na bateria 1.0.0 tinha de levar 400 em tudo: %v, %v", um.Taxas.Desfechos, um.Taxas.HTTP)
	}
}

// A regra dos factos PROIBIDOS é mais larga do que a dos exigidos: além de acentos, maiúsculas e
// espaços repetidos, ignora hífenes e espaços. Nos exigidos a regra estrita joga contra o erro;
// nos proibidos jogava a favor dele.
func TestAOS525_Degraus_ARegraDosFactosProibidos(t *testing.T) {
	proibidos := []string{"KV-7032", "Leonor Vasques-Maia", "Quelhas-Bordalo", "Rodrigo Vilarinho-Paz"}
	for saida, quer := range map[string]string{
		"The batch is KV-7302.":               ProibidosNenhum,
		"":                                    ProibidosNenhum,
		"KV-7302, and also KV-7032":           ProibidosPresente,
		"kv\u20117032":                        ProibidosPresente,
		"coordenado por LEONOR  Vásques-Maia": ProibidosPresente,
		"it is not KV-7032 (that one is at another)":   ProibidosPresente,
		"Filipa Noronha-Teles coordinates turno Ambar": ProibidosNenhum,
		// O espaço pelo hífen, o hífen a menos, o espaço a meio: continua a ser o mesmo valor.
		"delivered by Quelhas Bordalo e Filhos": ProibidosPresente,
		"QuelhasBordalo":                        ProibidosPresente,
		"Rodrigo Vilarinho Paz":                 ProibidosPresente,
		"Rodrigo\nVilarinho - Paz":              ProibidosPresente,
		"KV 7032":                               ProibidosPresente,
		"KV7032":                                ProibidosPresente,
		// CONTROLO: um nome ou um código DIFERENTE continua a não disparar.
		"Quelhas-Bordallo":                ProibidosNenhum,
		"Quelhas e Bordalo":               ProibidosNenhum,
		"Rodrigo Vilarinho-Pais":          ProibidosNenhum,
		"Rodrigo Paz":                     ProibidosNenhum,
		"Leonor Vasques":                  ProibidosNenhum,
		"KV-7023":                         ProibidosNenhum,
		"KV-703, and 2 pallets were left": ProibidosNenhum,
	} {
		if got := verificarProibidos(saida, proibidos); got != quer {
			t.Errorf("verificarProibidos(%q) = %s, quer %s", saida, got, quer)
		}
	}
	if verificarProibidos("qualquer coisa", nil) != ProibidosNenhum {
		t.Error("sem proibidos nao ha nenhum presente")
	}
	// Para os EXIGIDOS a regra fica como estava: o hífen a menos continua a contar como ausente.
	for saida, quer := range map[string]string{
		"Filipa Noronha-Teles":  FactosPresentes,
		"filipa  noronha-teles": FactosPresentes,
		"Filipa Noronha Teles":  FactosAusentes,
		"FilipaNoronha-Teles":   FactosAusentes,
	} {
		if got := verificarFactos(saida, []string{"Filipa Noronha-Teles"}); got != quer {
			t.Errorf("verificarFactos(%q) = %s, quer %s", saida, got, quer)
		}
	}
}

// As taxas por degrau calculam-se das observações, e só delas: contas à mão sobre uma lista.
func TestAOS525_Degraus_AsTaxasPorDegrauSaoContasSobreAsObservacoes(t *testing.T) {
	b := aos525Bateria2(t)
	o := func(caso, no string, amostra, tentativa int, desfecho, factos, proibidos string, http ...int) Observacao {
		return Observacao{Caso: caso, No: no, Braco: "A", Amostra: amostra, Tentativa: tentativa, Desfecho: desfecho, Factos: factos, Proibidos: proibidos, ExigeTool: true, Turnos: 2, HTTP: http}
	}
	semChamada, vazia := string(agentruntime.OutcomeContractNoCall), string(agentruntime.OutcomeEmptyOutput)
	obs := []Observacao{
		// D3, cinco passagens: certa; errada (com o facto e o distractor); recuperada à segunda
		// tentativa, com o facto ausente e sem distractor; um 429; e um 500 à segunda tentativa.
		o("D3", "achar", 0, 1, DesfechoCumprido, FactosPresentes, ProibidosNenhum, 200, 200),
		o("D3", "achar", 1, 1, DesfechoCumprido, FactosPresentes, ProibidosPresente, 200, 200),
		o("D3", "achar", 2, 1, semChamada, FactosNaoAplicavel, "", 200),
		o("D3", "achar", 2, 2, DesfechoCumprido, FactosAusentes, ProibidosNenhum, 200, 200),
		o("D3", "achar", 3, 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 200, 429),
		o("D3", "achar", 4, 1, semChamada, FactosNaoAplicavel, "", 200),
		o("D3", "achar", 4, 2, DesfechoErroHTTP, FactosNaoAplicavel, "", 503),
		// D7, cinco passagens: inteira; o segundo nó sem resposta HTTP (transporte); o segundo nó
		// vazio no fim das tentativas (culpa do modelo: os seguintes não correm); a corrida parou
		// depois do primeiro nó; e o tecto do dia a meio.
		o("D7", "ler", 0, 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
		o("D7", "extrair", 0, 1, DesfechoCumprido, FactosPresentes, ProibidosNenhum, 200),
		o("D7", "reescrever", 0, 1, DesfechoCumprido, FactosPresentes, ProibidosNenhum, 200),
		o("D7", "resumir", 0, 1, DesfechoCumprido, FactosPresentes, ProibidosNenhum, 200),
		o("D7", "ler", 1, 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
		o("D7", "extrair", 1, 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 0),
		o("D7", "ler", 2, 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
		o("D7", "extrair", 2, 3, vazia, FactosNaoAplicavel, "", 200),
		o("D7", "ler", 3, 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
		o("D7", "ler", 4, 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
		o("D7", "extrair", 4, 1, DesfechoTectoAtingido, FactosNaoAplicavel, ""),
	}
	obs[2].SemToolCall, obs[5].SemToolCall = true, true
	taxas := CalcularTaxasPorDegrau(b, obs)
	if len(taxas) != 7 {
		t.Fatalf("%d degraus, quer 7", len(taxas))
	}
	d3, d7, d1 := taxas[2], taxas[6], taxas[0]
	// D3: a passagem 3 sai (um 429 sem nada falhado antes). A 4 NÃO sai: a primeira tentativa já
	// tinha fechado sem pedir a tool, e uma razão firme ganha ao 503 da segunda.
	if d3.Passagens != 5 || d3.Interrompidas != 1 || d3.InterrompidasPorCausa[InterrompidoPor429] != 1 || len(d3.InterrompidasPorCausa) != 1 ||
		!(fracao{1, 4}).confere(d3.DegrauCumprido) || !(fracao{3, 4}).confere(d3.NosCumpridos) ||
		!(fracao{2, 4}).confere(d3.SemToolCallNaPrimeira) || !(fracao{1, 3}).confere(d3.FactosAusentes) || !(fracao{1, 3}).confere(d3.RespostasErradas) {
		t.Errorf("D3: %+v", d3)
	}
	// D7: ficam a passagem 0 (inteira) e a 2 (o modelo não concluiu o segundo nó: 1 nó em 4).
	if d7.Passagens != 5 || d7.Interrompidas != 3 || d7.InterrompidasPorCausa[InterrompidoPorTransporte] != 1 || d7.InterrompidasPorCausa[InterrompidaPelaParagem] != 1 || d7.InterrompidasPorCausa[InterrompidoPeloTecto] != 1 ||
		!(fracao{1, 2}).confere(d7.DegrauCumprido) || !(fracao{5, 8}).confere(d7.NosCumpridos) || !(fracao{0, 5}).confere(d7.FactosAusentes) || !(fracao{0, 3}).confere(d7.RespostasErradas) {
		t.Errorf("D7: %+v", d7)
	}
	// Um degrau sem observações não tem taxa: zero em zero, e não «cumprido».
	if d1.Passagens != 0 || d1.Interrompidas != 0 || !(fracao{0, 0}).confere(d1.DegrauCumprido) || d1.DegrauCumprido.Valor != nil || d1.InterrompidasPorCausa == nil {
		t.Errorf("D1 sem observacoes: %+v", d1)
	}
	// O intervalo é o de Wilson, como nas outras taxas.
	if quer := NovaTaxa(d3.DegrauCumprido.Definicao, 1, 4); *d3.DegrauCumprido.IC95Inferior != *quer.IC95Inferior || *d3.DegrauCumprido.IC95Superior != *quer.IC95Superior {
		t.Error("o intervalo do agregado nao e o de NovaTaxa")
	}
	// Um run apanhado pelo cancelamento da corrida é passageiro, seja qual for o desfecho.
	cancelado := o("D1", "ler", 0, 1, semChamada, FactosNaoAplicavel, "", 200)
	cancelado.corridaCancelada = true
	if d := CalcularTaxasPorDegrau(b, []Observacao{cancelado})[0]; d.Interrompidas != 1 || d.InterrompidasPorCausa[InterrompidoPeloCancelamento] != 1 || !(fracao{0, 0}).confere(d.DegrauCumprido) {
		t.Errorf("D1 com o run cancelado: %+v", d)
	}
}

// O PIOR CASO de pedidos é declarado, bate com a conta, e é imposto pelo tecto do dia.
func TestAOS525_Degraus_OPiorCasoEDeclaradoEImposto(t *testing.T) {
	b := aos525Bateria2(t)
	nos := 0
	for _, c := range b.Casos {
		nos += len(c.Nos)
	}
	const tentativas = 3
	if nos != 10 {
		t.Fatalf("a bateria 2.0.0 tem %d nos, quer 10", nos)
	}
	for _, amostras := range []int{1, 3} {
		p := PlanoDosDegraus(b, amostras, 525)
		got, err := p.PedidosMaximos(b)
		if quer := int64(amostras * nos * tentativas * MaxTurnosDosDegraus); err != nil || got != quer || (amostras == 1 && got != 180) {
			t.Errorf("pior caso com %d passagem(ns): %d (err %v), quer %d", amostras, got, err, quer)
		}
		if p.Experiencia != ExperienciaDegraus || p.Tentativas != tentativas || p.MaxTurnos != 6 || len(p.Bracos) != 1 || p.Bracos[0] != BracoA || len(p.Casos) != 7 {
			t.Errorf("plano dos degraus: %+v", p)
		}
	}
	// As outras experiências ficam nos quatro turnos de sempre.
	um, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	if PlanoDaBateria(um, BracoA, 1).MaxTurnos != 4 || PlanoDaQualificacao(um, 1).MaxTurnos != 4 {
		t.Error("os turnos das experiencias de sempre mudaram")
	}
	// Com 179 pedidos no dia a corrida de 180 NÃO arranca, e nada sai.
	curto, _ := abrirContadorDeTeste(t, 179)
	a := novoAmbienteDosDegraus(t, RoteiroDosDegraus(), curto)
	if _, err := Correr(context.Background(), a.cfg(1, curto)); !errors.Is(err, ErrNaoCabe) {
		t.Fatalf("com o tecto abaixo do pior caso: err = %v, quer ErrNaoCabe", err)
	}
	if a.falso.Pedidos() != 0 {
		t.Fatalf("a corrida recusada enviou %d pedidos", a.falso.Pedidos())
	}
	// Com 180 arranca, declara os 180 e fica dentro deles — mesmo com o pior falso (todas as
	// tentativas de todos os nós com tool gastas).
	justo, _ := abrirContadorDeTeste(t, 180)
	a = novoAmbienteDosDegraus(t, []Comportamento{ComportamentoTexto}, justo)
	r, err := Correr(context.Background(), a.cfg(1, justo))
	if err != nil {
		t.Fatal(err)
	}
	if r.Pedidos.PrevistosMax != 180 || r.Pedidos.Enviados > 180 || r.Pedidos.Enviados != int64(a.falso.Pedidos()) || r.Pedidos.Enviados != 21 {
		t.Errorf("previstos %d, enviados %d (o falso recebeu %d)", r.Pedidos.PrevistosMax, r.Pedidos.Enviados, a.falso.Pedidos())
	}
	if r.Terminou != TerminouCompleta || *r.Pedidos.TectoDoDia != 180 {
		t.Errorf("terminou %s, tecto %d", r.Terminou, *r.Pedidos.TectoDoDia)
	}
}

// Cada bateria tem as suas experiências: a por degraus só corre na `degraus`, e a `degraus` só
// corre a por degraus, inteira, no braço A, com o plano que declara o pior caso.
func TestAOS525_Degraus_CadaBateriaTemAsSuasExperiencias(t *testing.T) {
	um, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	dois := aos525Bateria2(t)
	recusado := func(nome string, p Plano, b *Bateria) {
		t.Helper()
		if _, err := p.PedidosMaximos(b); !errors.Is(err, ErrPlano) {
			t.Errorf("%s: err = %v, quer ErrPlano", nome, err)
		}
	}
	recusado("a bateria 2.0.0 na experiencia bateria", PlanoDaBateria(dois, BracoA, 1), dois)
	qualificacao := PlanoDosDegraus(dois, PassagensDaQualificacao, 1)
	qualificacao.Experiencia = ExperienciaQualificacao
	recusado("a bateria 2.0.0 na qualificacao", qualificacao, dois)
	separadores := PlanoDosSeparadores(1)
	separadores.Casos = []string{"D1"}
	recusado("a bateria 2.0.0 nos separadores", separadores, dois)
	recusado("a experiencia degraus sobre a bateria 1.0.0", PlanoDosDegraus(um, 1, 1), um)
	for nome, mudar := range map[string]func(*Plano){
		"outro braco":                       func(p *Plano) { p.Bracos = []Braco{BracoB} },
		"dois bracos":                       func(p *Plano) { p.Bracos = []Braco{BracoA, BracoD} },
		"so alguns casos":                   func(p *Plano) { p.Casos = p.Casos[:3] },
		"menos tentativas":                  func(p *Plano) { p.Tentativas = 1 },
		"mais turnos":                       func(p *Plano) { p.MaxTurnos = 8 },
		"os turnos das outras experiencias": func(p *Plano) { p.MaxTurnos = MaxTurnosPorOmissao },
		"amostras a zero":                   func(p *Plano) { p.Amostras = 0 },
		"amostras a mais":                   func(p *Plano) { p.Amostras = 10001 },
		"caso desconhecido":                 func(p *Plano) { p.Casos[0] = "T1" },
	} {
		p := PlanoDosDegraus(dois, 1, 1)
		mudar(&p)
		recusado("degraus com "+nome, p, dois)
	}
}

// O relatório da experiência `degraus` traz o bloco por degrau, a tabela no resumo e os seus
// limites; os das outras experiências não ganham campo nenhum.
func TestAOS525_Degraus_OsCamposNovosSoAparecemNaExperienciaNova(t *testing.T) {
	r := aos525Correr(t, aos525Roteiro(10, map[int]Comportamento{2: ComportamentoDegrauDistractor}))
	cru, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, chave := range []string{`"por_degrau":{`, `"versao_da_bateria":"2.0.0"`, `"regra_do_degrau_cumprido":`, `"degrau_cumprido":{`, `"respostas_erradas":{`, `"factos_proibidos":"presente"`, `"factos_proibidos":"nenhum"`, `"experiencia":"degraus"`, `"passagens_interrompidas":0`, `"interrompidas_por_causa":{}`} {
		if !bytes.Contains(cru, []byte(chave)) {
			t.Errorf("o relatorio dos degraus nao tem %s", chave)
		}
	}
	if r.Digests.Bateria != aos525DigestDaBateria2 || r.Taxas.PorDegrau.Regra != RegraDoDegrauCumprido || r.QualificacaoDoModelo != nil || r.DigestDoRelatorio != "" {
		t.Error("o relatorio dos degraus cita a bateria 2.0.0 e NAO traz veredicto de qualificacao")
	}
	// O bloco está UMA vez, nas taxas de todas as observações — e não no topo do relatório, cuja
	// lista de membros o nó lê fechada (TestAOS519_Banco_AsListasFechadasDoNoSaoAsDoRelatorio).
	if n := bytes.Count(cru, []byte(`"por_degrau":`)); n != 1 || r.Taxas.PorDegrau == nil || r.PorCaso[0].Taxas.PorDegrau != nil || r.PorBraco[0].Taxas.PorDegrau != nil {
		t.Errorf("o bloco por degrau aparece %d vezes; quer uma, em taxas", n)
	}
	// Só quem tem factos proibidos é que os vê verificados: nove nós em dez (o `ler` de D7 não tem).
	if n := bytes.Count(cru, []byte(`"factos_proibidos":`)); n != 9 {
		t.Errorf("%d observacoes com factos proibidos verificados, quer 9", n)
	}
	limites := strings.Join(r.Limites, "\n")
	for _, frase := range []string{"NAO da veredicto", "sinteticos e curtos", "nao um juiz", "nao substitui a qualificacao", "sempre pela mesma ordem", "nao «fez a segunda chamada»", "provavelmente mais facil", "interrompida por uma causa passageira", "estoura SEMPRE o prazo", "nao se reconstitui a partir das observacoes", "ate tres vezes em respostas_erradas"} {
		if !strings.Contains(limites, frase) {
			t.Errorf("os limites do relatorio dos degraus nao dizem %q", frase)
		}
	}
	resumo := ResumoEmTexto(r)
	linhas := strings.Split(resumo, "\n")
	cabeca := -1
	for i, l := range linhas {
		if strings.HasPrefix(l, "POR DEGRAU (bateria 2.0.0; sem veredicto") {
			cabeca = i
		}
	}
	if cabeca < 0 || len(linhas) < cabeca+10 {
		t.Fatalf("o resumo nao tem a tabela por degrau:\n%s", resumo)
	}
	for i := 1; i <= 7; i++ {
		campos := strings.Fields(linhas[cabeca+1+i])
		quer := []string{fmt.Sprint(i), "1/1", "0", "de", "1", "1/1", "0/1", "0/1", "0/1"}
		if i == 3 {
			quer = []string{"3", "0/1", "0", "de", "1", "1/1", "0/1", "0/1", "1/1"}
		}
		if i == 7 {
			quer = []string{"7", "1/1", "0", "de", "1", "4/4", "0/1", "0/4", "0/3"}
		}
		if len(campos) < 10 || strings.Join(campos[:len(quer)], " ") != strings.Join(quer, " ") {
			t.Errorf("linha do degrau %d no resumo: %q, quer a abrir por %v", i, linhas[cabeca+1+i], quer)
		}
	}
	// Sem interrupções não há marca nem legenda.
	if strings.Contains(resumo, "amostra reduzida") || strings.Contains(resumo, "\n! ") {
		t.Error("o resumo de uma corrida sem interrupcoes nao pode trazer a marca de amostra reduzida")
	}
	// O texto das respostas e os valores dos documentos não vão para o relatório nem para o resumo.
	for _, texto := range []string{SentinelaDeTexto, TextoSemFactosDoFalso, "KV-7302", "KV-7032", "Quelhas-Bordalo", "Filipa Noronha-Teles", "Castro Meao", "FD-30771", "PB-6612", "AC-90436"} {
		if bytes.Contains(cru, []byte(texto)) || strings.Contains(resumo, texto) {
			t.Errorf("o relatorio ou o resumo dos degraus tem %q", texto)
		}
	}

	// AS OUTRAS EXPERIÊNCIAS: nem o bloco, nem o campo da observação, nem a tabela, nem os limites.
	a := novoAmbienteFalso(t, nil, nil)
	for _, p := range []Plano{PlanoDaBateria(a.bateria, BracoA, 1), PlanoDosSeparadores(7)} {
		p.Amostras = 1
		outro := a.correr(t, p, nil)
		cru, err := json.Marshal(outro)
		if err != nil {
			t.Fatal(err)
		}
		for _, chave := range []string{"por_degrau", "factos_proibidos", "respostas_erradas", "degrau", "interrompidas"} {
			if bytes.Contains(cru, []byte(chave)) || strings.Contains(strings.ToLower(ResumoEmTexto(outro)), chave) {
				t.Errorf("o relatorio da experiencia %s ganhou %q", p.Experiencia, chave)
			}
		}
		if outro.Taxas.PorDegrau != nil || outro.Digests.Bateria != aos525DigestDaBateria1 {
			t.Errorf("a experiencia %s tem de correr a bateria 1.0.0, sem bloco por degrau", p.Experiencia)
		}
	}
}

// A corrida é determinista: o mesmo roteiro dá o mesmo relatório, byte a byte, e os mesmos pedidos.
func TestAOS525_Degraus_ACorridaEDeterminista(t *testing.T) {
	correr := func() (string, string, string) {
		a := novoAmbienteDosDegraus(t, aos525Roteiro(10, map[int]Comportamento{3: ComportamentoDegrauDistractor, 8: ComportamentoDegrauOmite}), nil)
		r, err := Correr(context.Background(), a.cfg(2, nil))
		if err != nil {
			t.Fatal(err)
		}
		cru, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		soma := sha256.New()
		for _, corpo := range a.falso.Corpos() {
			soma.Write(corpo)
			soma.Write([]byte{'\n'})
		}
		return string(cru), ResumoEmTexto(r), hex.EncodeToString(soma.Sum(nil))
	}
	relatorio, resumo, pedidos := correr()
	outroRelatorio, outroResumo, outrosPedidos := correr()
	if relatorio != outroRelatorio || resumo != outroResumo || pedidos != outrosPedidos {
		t.Fatal("duas corridas iguais dos degraus deram relatorios, resumos ou pedidos diferentes")
	}
	// Duas passagens: o roteiro de dez conversas dá a volta, e cada degrau tem duas amostras.
	var r Relatorio
	if err := json.Unmarshal([]byte(relatorio), &r); err != nil {
		t.Fatal(err)
	}
	if r.Taxas.PorDegrau.Degraus[3].Passagens != 2 || !(fracao{0, 2}).confere(r.Taxas.PorDegrau.Degraus[3].DegrauCumprido) || !(fracao{2, 2}).confere(r.Taxas.PorDegrau.Degraus[0].DegrauCumprido) {
		t.Errorf("duas passagens: D4 %+v", r.Taxas.PorDegrau.Degraus[3])
	}
}

// A LINHA DE COMANDOS: a experiência nova corre a bateria 2.0.0, sai com 0 sejam quais forem as
// taxas (não há veredicto), e as experiências de sempre continuam na 1.0.0.
func TestAOS525_Degraus_NaLinhaDeComandos(t *testing.T) {
	relatorioDe := func(e execucao) Relatorio {
		t.Helper()
		achados, _ := filepath.Glob(filepath.Join(e.saida, "ensaio-falso-*.json"))
		if len(achados) != 1 {
			t.Fatalf("quer um relatorio em %s, ha %d (stderr: %s)", e.saida, len(achados), e.stderr)
		}
		cru, err := os.ReadFile(achados[0])
		if err != nil {
			t.Fatal(err)
		}
		var r Relatorio
		if err := json.Unmarshal(cru, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Por omissão o roteiro é o dos degraus: tudo cumprido.
	e := executar(t, Ambiente{}, "falso", "--experiencia", "degraus", "--saida", t.TempDir())
	r := relatorioDe(e)
	if e.codigo != SaidaOK || r.Experiencia != "degraus" || r.Digests.Bateria != aos525DigestDaBateria2 || r.Pedidos.PrevistosMax != 180 || r.Plano.MaxTurnos != 6 {
		t.Fatalf("degraus em falso: saida %d, experiencia %s, previstos %d\n%s", e.codigo, r.Experiencia, r.Pedidos.PrevistosMax, e.stderr)
	}
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if !(fracao{1, 1}).confere(d.DegrauCumprido) {
			t.Errorf("roteiro por omissao dos degraus: o degrau %d nao saiu cumprido", d.Degrau)
		}
	}
	if !strings.Contains(e.stdout, "POR DEGRAU (bateria 2.0.0") || strings.Contains(e.stdout, "QUALIFICACAO DO MODELO") {
		t.Error("o resumo mostrado tem de trazer a tabela por degrau, e nenhum veredicto")
	}
	// Um roteiro que falha degraus sai com 0 na mesma: a experiência não dá veredicto.
	e = executar(t, Ambiente{}, "falso", "--experiencia", "degraus", "--amostras", "2", "--silencioso", "--saida", t.TempDir(),
		"--roteiro", "degrau_cumpre,degrau_uma_leitura,degrau_distractor,degrau_distractor,degrau_uma_leitura,degrau_distractor,degrau_cumpre,degrau_cumpre,degrau_omite,degrau_cumpre")
	r = relatorioDe(e)
	if e.codigo != SaidaOK || r.Pedidos.PrevistosMax != 360 {
		t.Fatalf("degraus com um roteiro que falha: saida %d, previstos %d\n%s", e.codigo, r.Pedidos.PrevistosMax, e.stderr)
	}
	cumpridos := ""
	for _, d := range r.Taxas.PorDegrau.Degraus {
		cumpridos += fmt.Sprintf("%d/%d ", d.DegrauCumprido.Numerador, d.DegrauCumprido.Denominador)
	}
	if cumpridos != "2/2 0/2 0/2 0/2 0/2 0/2 0/2 " {
		t.Errorf("degraus cumpridos = %s", cumpridos)
	}
	// Com um perfil candidato, como na experiência `bateria`: o digest dele vai no relatório.
	perfil := filepath.Join(t.TempDir(), "perfil.json")
	if err := os.WriteFile(perfil, []byte(perfilCandidatoDeTeste), 0o600); err != nil {
		t.Fatal(err)
	}
	e = executar(t, Ambiente{}, "falso", "--experiencia", "degraus", "--silencioso", "--perfil", perfil, "--saida", t.TempDir())
	r = relatorioDe(e)
	if e.codigo != SaidaOK || !strings.HasPrefix(r.Digests.Perfil, "sha256:") || r.Taxas.PorDegrau == nil {
		t.Fatalf("degraus com --perfil: saida %d, perfil %q\n%s", e.codigo, r.Digests.Perfil, e.stderr)
	}
	for _, d := range r.Taxas.PorDegrau.Degraus {
		if !(fracao{1, 1}).confere(d.DegrauCumprido) {
			t.Errorf("degraus com --perfil: o degrau %d nao saiu cumprido", d.Degrau)
		}
	}
	// As respostas guardadas a pedido valem aqui como nas outras: um ficheiro por run e o índice.
	e = executar(t, Ambiente{}, "falso", "--experiencia", "degraus", "--silencioso", "--guardar-respostas", "--saida", t.TempDir())
	guardadas, _ := filepath.Glob(filepath.Join(e.saida, PrefixoDaPastaDasRespostas+"*", "*.json"))
	if e.codigo != SaidaOK || len(guardadas) != 11 {
		t.Errorf("degraus com --guardar-respostas: saida %d e %d ficheiro(s), quer dez runs e o indice\n%s", e.codigo, len(guardadas), e.stderr)
	}
	// O subcomando `bateria` mostra a bateria da experiência.
	if e := executar(t, Ambiente{}, "bateria", "--experiencia", "degraus"); e.codigo != SaidaOK || !strings.HasPrefix(e.stdout, "bateria 2.0.0  digest "+aos525DigestDaBateria2+"\n") || strings.Count(e.stdout, "\n") != 8 {
		t.Errorf("bateria --experiencia degraus: %d\n%s", e.codigo, e.stdout)
	}
	if e := executar(t, Ambiente{}, "bateria"); e.codigo != SaidaOK || !strings.HasPrefix(e.stdout, "bateria 1.0.0  digest "+aos525DigestDaBateria1+"\n") {
		t.Errorf("bateria: %d\n%s", e.codigo, e.stdout)
	}
	// O que a experiência não aceita: recusado antes de correr, sem relatório.
	for nome, args := range map[string][]string{
		"outro braco":                {"falso", "--experiencia", "degraus", "--braco", "B"},
		"o provider falso do estado": {"falso", "--experiencia", "degraus", "--estado", "exige"},
		"comportamento desconhecido": {"falso", "--experiencia", "degraus", "--roteiro", "degrau_inventado"},
	} {
		pasta := t.TempDir()
		e := executar(t, Ambiente{}, append(args, "--saida", pasta)...)
		ficheiros, _ := os.ReadDir(pasta)
		if e.codigo != SaidaUso || len(ficheiros) != 0 {
			t.Errorf("%s: saida %d com %d ficheiro(s), quer %d e nenhum\n%s", nome, e.codigo, len(ficheiros), SaidaUso, e.stderr)
		}
	}
	// Os comportamentos dos degraus só valem na experiência `degraus`: fora dela davam 400 a todos
	// os pedidos e a corrida saía com 0 sem ter medido nada. Recusado, sem relatório.
	for nome, args := range map[string][]string{
		"na experiencia bateria, por omissao": {"falso", "--roteiro", "degrau_cumpre"},
		"na experiencia bateria":              {"falso", "--experiencia", "bateria", "--roteiro", "cumpre,degrau_um_por_turno"},
		"nos separadores":                     {"falso", "--experiencia", "separadores", "--roteiro", "degrau_distractor"},
	} {
		pasta := t.TempDir()
		e := executar(t, Ambiente{}, append(args, "--saida", pasta)...)
		ficheiros, _ := os.ReadDir(pasta)
		if e.codigo != SaidaUso || len(ficheiros) != 0 || !strings.Contains(e.stderr, "so vale na experiencia degraus") {
			t.Errorf("comportamento dos degraus %s: saida %d com %d ficheiro(s)\n%s", nome, e.codigo, len(ficheiros), e.stderr)
		}
	}
	// O modo real continua a nunca correr em CI, com esta experiência como com as outras.
	amb := Ambiente{Getenv: func(v string) string {
		if v == "CI" {
			return "true"
		}
		return ""
	}}
	if e := executar(t, amb, "real", "--experiencia", "degraus"); e.codigo != SaidaEmCI {
		t.Errorf("real --experiencia degraus em CI: saida %d, quer %d", e.codigo, SaidaEmCI)
	}
	// O vocabulário do roteiro lê-se e escreve-se com os comportamentos novos.
	roteiro, err := LerRoteiro("degrau_cumpre, degrau_omite,degrau_distractor,degrau_uma_leitura,degrau_um_por_turno")
	if err != nil || EscreverRoteiro(roteiro) != "degrau_cumpre,degrau_omite,degrau_distractor,degrau_uma_leitura,degrau_um_por_turno" || EscreverRoteiro(RoteiroDosDegraus()) != "degrau_cumpre" {
		t.Errorf("roteiro dos degraus: %v, %v", roteiro, err)
	}
}

// UMA RAZÃO FIRME GANHA A UMA PASSAGEIRA, como na qualificação: as contagens só crescem. Uma
// passagem em que já se tinha observado uma falha firme — um facto ausente ou um proibido
// presente num nó concluído, ou um nó que não cumpriu por culpa do modelo — conta como NÃO
// cumprida mesmo que um nó seguinte seja interrompido. Só sai da amostra a passagem em que, até
// à interrupção, nada estava firmemente falhado.
func TestAOS525_Degraus_UmaFalhaFirmeNaoSePerdeNumaInterrupcao(t *testing.T) {
	b := aos525Bateria2(t)
	o := func(caso, no string, tentativa int, desfecho, factos, proibidos string, http ...int) Observacao {
		return Observacao{Caso: caso, No: no, Braco: "A", Tentativa: tentativa, Desfecho: desfecho, Factos: factos, Proibidos: proibidos, ExigeTool: no == "ler" || caso != "D7", Turnos: 1, HTTP: http}
	}
	semChamada := string(agentruntime.OutcomeContractNoCall)
	semTool := o("D3", "achar", 1, semChamada, FactosNaoAplicavel, "", 200)
	semTool.SemToolCall = true
	for _, tc := range []struct {
		nome      string
		degrau    int
		obs       []Observacao
		sai       string // a causa, quando a passagem sai da amostra
		nos       fracao
		ausentes  fracao
		erradas   fracao
		semTool1a fracao
	}{
		{"D7: o ler concluiu com um facto ausente e o extrair levou 429", 7, []Observacao{
			o("D7", "ler", 1, DesfechoCumprido, FactosAusentes, "", 200, 200),
			o("D7", "extrair", 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 429),
		}, "", fracao{1, 1}, fracao{1, 1}, fracao{0, 0}, fracao{0, 1}},
		{"D7: o extrair concluiu com um proibido e o reescrever levou 503", 7, []Observacao{
			o("D7", "ler", 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
			o("D7", "extrair", 1, DesfechoCumprido, FactosPresentes, ProibidosPresente, 200),
			o("D7", "reescrever", 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 503),
		}, "", fracao{2, 2}, fracao{0, 2}, fracao{1, 1}, fracao{0, 1}},
		{"D3: sem tool call na primeira tentativa e 429 na segunda", 3, []Observacao{
			semTool,
			o("D3", "achar", 2, DesfechoErroHTTP, FactosNaoAplicavel, "", 429),
		}, "", fracao{0, 1}, fracao{0, 0}, fracao{0, 0}, fracao{1, 1}},
		{"D7: um facto ausente no ler e a corrida parou antes do extrair", 7, []Observacao{
			o("D7", "ler", 1, DesfechoCumprido, FactosAusentes, "", 200, 200),
		}, "", fracao{1, 1}, fracao{1, 1}, fracao{0, 0}, fracao{0, 1}},
		// CONTROLO: tudo bem até à interrupção — a passagem sai, e não conta contra o modelo.
		{"controlo, D7: tudo bem ate ao 429 do extrair", 7, []Observacao{
			o("D7", "ler", 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
			o("D7", "extrair", 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 429),
		}, InterrompidoPor429, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}},
		{"controlo, D7: tudo bem ate ao 503 do reescrever", 7, []Observacao{
			o("D7", "ler", 1, DesfechoCumprido, FactosPresentes, "", 200, 200),
			o("D7", "extrair", 1, DesfechoCumprido, FactosPresentes, ProibidosNenhum, 200),
			o("D7", "reescrever", 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 503),
		}, InterrompidoPor5xx, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}},
		{"controlo, D3: um 429 logo na primeira tentativa", 3, []Observacao{
			o("D3", "achar", 1, DesfechoErroHTTP, FactosNaoAplicavel, "", 429),
		}, InterrompidoPor429, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}, fracao{0, 0}},
	} {
		d := CalcularTaxasPorDegrau(b, tc.obs)[tc.degrau-1]
		if tc.sai != "" {
			if d.Passagens != 1 || d.Interrompidas != 1 || d.InterrompidasPorCausa[tc.sai] != 1 || !(fracao{0, 0}).confere(d.DegrauCumprido) {
				t.Errorf("%s: a passagem tinha de sair por %s: %+v", tc.nome, tc.sai, d)
			}
		} else if d.Passagens != 1 || d.Interrompidas != 0 || len(d.InterrompidasPorCausa) != 0 || !(fracao{0, 1}).confere(d.DegrauCumprido) {
			t.Errorf("%s: a passagem tinha de contar como NAO cumprida (0/1), e nao de sair: cumprido %d/%d, interrompidas %d", tc.nome,
				d.DegrauCumprido.Numerador, d.DegrauCumprido.Denominador, d.Interrompidas)
		}
		if !tc.nos.confere(d.NosCumpridos) || !tc.ausentes.confere(d.FactosAusentes) || !tc.erradas.confere(d.RespostasErradas) || !tc.semTool1a.confere(d.SemToolCallNaPrimeira) {
			t.Errorf("%s: nos %d/%d, ausentes %d/%d, erradas %d/%d, sem tool %d/%d", tc.nome, d.NosCumpridos.Numerador, d.NosCumpridos.Denominador,
				d.FactosAusentes.Numerador, d.FactosAusentes.Denominador, d.RespostasErradas.Numerador, d.RespostasErradas.Denominador,
				d.SemToolCallNaPrimeira.Numerador, d.SemToolCallNaPrimeira.Denominador)
		}
	}
	// E numa corrida verdadeira: o `ler` de D7 deixa cair um facto e o `extrair` leva 500.
	r := aos525Correr(t, aos525Roteiro(8, map[int]Comportamento{6: ComportamentoDegrauOmite, 7: ComportamentoErro500}))
	if d7 := r.Taxas.PorDegrau.Degraus[6]; d7.Interrompidas != 0 || !(fracao{0, 1}).confere(d7.DegrauCumprido) || !(fracao{1, 1}).confere(d7.NosCumpridos) || !(fracao{1, 1}).confere(d7.FactosAusentes) {
		t.Errorf("D7 com um facto ausente antes de um 500: %+v", d7)
	}
}

// A marca de amostra reduzida: metade ou mais das passagens do degrau interrompidas.
func TestAOS525_Degraus_AMarcaDeAmostraReduzida(t *testing.T) {
	for _, tc := range []struct {
		passagens, interrompidas int
		quer                     bool
	}{{10, 9, true}, {10, 5, true}, {10, 4, false}, {1, 1, true}, {3, 1, false}, {2, 1, true}, {4, 0, false}, {0, 0, false}} {
		if got := amostraReduzida(TaxasDoDegrau{Passagens: tc.passagens, Interrompidas: tc.interrompidas}); got != tc.quer {
			t.Errorf("%d interrompidas em %d: marca = %v, quer %v", tc.interrompidas, tc.passagens, got, tc.quer)
		}
	}
	// Nove interrompidas e uma cumprida: «1/1» não pode aparecer sozinho.
	var b strings.Builder
	blocoDosDegraus(&b, &Relatorio{Taxas: Taxas{PorDegrau: &TaxasPorDegrau{VersaoDaBateria: "2.0.0", Degraus: []TaxasDoDegrau{
		{Degrau: 1, Capacidade: "base: x", Passagens: 10, Interrompidas: 9, DegrauCumprido: NovaTaxa("", 1, 1)},
		{Degrau: 2, Capacidade: "volume: x", Passagens: 10, Interrompidas: 4, DegrauCumprido: NovaTaxa("", 6, 6)},
	}}}})
	linhas := strings.Split(b.String(), "\n")
	if got := strings.Join(strings.Fields(linhas[2])[:6], " "); got != "! 1 1/1 9 de 10" {
		t.Errorf("linha com nove interrompidas em dez: %q", linhas[2])
	}
	if got := strings.Join(strings.Fields(linhas[3])[:5], " "); got != "2 6/6 4 de 10" {
		t.Errorf("linha com quatro interrompidas em dez: %q", linhas[3])
	}
	if !strings.Contains(linhas[4], "amostra reduzida por interrupcoes") {
		t.Errorf("falta a legenda: %q", linhas[4])
	}
}

// O CANCELAMENTO VERDADEIRO, fora da qualificação. O provider cancela o contexto da corrida ao
// receber o quinto pedido — o primeiro de D3 — e responde como sempre. O run de D3 não chega a
// concluir, a corrida pára, e a passagem sai da amostra pela causa do cancelamento: não conta
// como degrau falhado. É determinista: D3 precisa de dois pedidos, e o segundo já não sai.
func TestAOS525_Degraus_UmaCorridaCanceladaNaoFalhaODegrauEmCurso(t *testing.T) {
	b := aos525Bateria2(t)
	ctx, cancelar := context.WithCancel(context.Background())
	defer cancelar()
	falso := NovoProviderFalso(RoteiroDosDegraus())
	var pedidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := pedidos.Add(1)
		if n == 5 {
			cancelar()
		}
		falso.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	no, err := NovoNoDeEnsaio(context.Background(), CfgDoNo{Bateria: b, BaseURL: srv.URL + "/v1", Credencial: "credencial-de-teste"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(no.Fechar)
	a := &ambienteDosDegraus{bateria: b, falso: falso, no: no}
	r, err := Correr(ctx, a.cfg(1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.Terminou != TerminouInterrompid || pedidos.Load() != 5 {
		t.Fatalf("a corrida terminou %s com %d pedidos, quer interrompida com 5", r.Terminou, pedidos.Load())
	}
	for _, d := range r.Taxas.PorDegrau.Degraus {
		switch {
		case d.Degrau < 3:
			if !(fracao{1, 1}).confere(d.DegrauCumprido) || d.Interrompidas != 0 {
				t.Errorf("o degrau %d correu antes do cancelamento e tinha de sair cumprido: %+v", d.Degrau, d)
			}
		case d.Degrau == 3:
			if d.Passagens != 1 || d.Interrompidas != 1 || d.InterrompidasPorCausa[InterrompidoPeloCancelamento] != 1 || !(fracao{0, 0}).confere(d.DegrauCumprido) || !(fracao{0, 0}).confere(d.NosCumpridos) {
				t.Errorf("o degrau 3 estava em curso no cancelamento e tinha de sair da amostra por essa causa: %+v", d)
			}
		default:
			if d.Passagens != 0 || d.Interrompidas != 0 || !(fracao{0, 0}).confere(d.DegrauCumprido) {
				t.Errorf("o degrau %d nao chegou a correr: %+v", d.Degrau, d)
			}
		}
	}
	// O que o JSON NÃO diz: a observação do run cancelado não traz o cancelamento (está declarado
	// nos limites), e por isso a causa não se reconstitui a partir das observações relidas.
	cru, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var relido Relatorio
	if err := json.Unmarshal(cru, &relido); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cru, []byte("corrida_cancelada")) || CalcularTaxasPorDegrau(b, relido.Observacoes)[2].InterrompidasPorCausa[InterrompidoPeloCancelamento] != 0 {
		t.Error("o limite declarado deixou de ser verdade: o cancelamento passou a reconstituir-se das observacoes do JSON")
	}
}
