package main

// AOS-499 — A MEDIÇÃO MEDE O QUE DIZ (revisão adversarial de 2026-10-05, achado I1).
//
// A primeira versão comparava o texto final com o resultado designado por DIGEST e por TAMANHO, e
// todos os testes usavam tools que devolviam bytes crus. Em produção o resultado designado é o
// ENVELOPE da sandbox: a série «igual» era impossível, e uma transcrição byte a byte de um
// documento curto caía na classe que o README mandava ler como resumo.
//
// Estes testes medem sobre o envelope REAL: os ficheiros de `substrate/sandbox/testdata/
// aos499_envelope/` são escritos pelo codificador da sandbox (o teste do pacote exige-os byte a
// byte), e `aos498_fio/sandbox-designada-measure.json` é a resposta de um nó real cuja tool corre
// na sandbox. Nenhum envelope é escrito à mão aqui.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// aos499Envelope lê um envelope escrito pelo codificador real da sandbox e devolve os bytes e —
// lido por um descodificador DESTE TESTE, independente do de produção — o documento lá dentro.
func aos499Envelope(t *testing.T, nome string) (envelope, documento string) {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "..", "substrate", "sandbox", "testdata", "aos499_envelope", nome+".json"))
	if err != nil {
		t.Fatalf("ficheiro de fio do envelope em falta (%v) — gera-o no pacote da sandbox com AOS494_ACTUALIZAR_FIO=1", err)
	}
	envelope = string(bytes.TrimSuffix(cru, []byte("\n")))
	var campos struct {
		StdoutText string `json:"stdout_text"`
	}
	if err := json.Unmarshal([]byte(envelope), &campos); err != nil {
		t.Fatalf("%s: envelope ilegivel: %v", nome, err)
	}
	return envelope, campos.StdoutText
}

// aos499Servido é a resposta do nó a um run «só medição» com a origem designada e os bytes
// `servido` conferidos, e com o texto final dado.
func aos499Servido(servido, textoFinal string) estadoDoRun {
	return estadoDoRun{Status: "completed", Terminated: true, FinalText: textoFinal, Output: &servido,
		OutputSource: &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceDesignated,
			StepID: "step-000001-tool-1", Digest: digestDoConteudo(servido), Bytes: len(servido)}}
}

// aos499NoVocabulario exige que cada campo da medida seja vazio ou de uma lista fechada.
func aos499NoVocabulario(t *testing.T, nome string, m medidaDaOrigem) {
	t.Helper()
	dentro := func(valor string, lista []string) bool {
		if valor == "" {
			return true
		}
		for _, v := range lista {
			if v == valor {
				return true
			}
		}
		return false
	}
	for campo, par := range map[string]struct {
		valor string
		lista []string
	}{
		"estado": {m.estado, estadosDaDesignacao()}, "tamanho": {m.tamanho, classesDeTamanho}, "transporte": {m.transporte, resultadosDoTransporte},
		"forma": {m.forma, formasDaOrigem}, "comparacao": {m.comparacao, comparacoesDoTexto}, "numeros": {m.numeros, resultadosDosNumeros},
		"razao": {m.razao, classesDeRazao},
	} {
		if !dentro(par.valor, par.lista) {
			t.Errorf("%s: %s = %q esta fora do vocabulario fechado %v", nome, campo, par.valor, par.lista)
		}
	}
}

// TestAOS499_Envelope_MedeConteudo: a medição sobre o envelope real da sandbox, por relação entre
// o texto final e o documento que o envelope transporta.
func TestAOS499_Envelope_MedeConteudo(t *testing.T) {
	leitura, notas := aos499Envelope(t, "leitura-notas")
	semArtefacto, notas2 := aos499Envelope(t, "notas")
	if notas == "" || notas != notas2 || !strings.Contains(leitura, `"artifacts"`) || strings.Contains(semArtefacto, `"artifacts"`) {
		t.Fatal("pre-condicao: os dois envelopes levam o mesmo documento, um com o artefacto da leitura e o outro sem")
	}
	if len(leitura) < 2*len(notas) {
		t.Fatalf("pre-condicao: a leitura no driver de referencia leva o documento duas vezes (stdout_text e artifacts); envelope=%d documento=%d", len(leitura), len(notas))
	}
	linhas := strings.Split(strings.TrimSuffix(notas, "\n"), "\n")
	// Um resumo «bom», que guarda quase todos os números e perde um (a referência 4471).
	resumo := "A reunião de 15/08/2026 deixou 2 tarefas por fechar (a 12, rever o orçamento de 2026, e a 7), uma chamada ao fornecedor antes das 17h30, e aprovou 1250 EUR em 3 parcelas."
	// O mesmo documento com as linhas por outra ordem: está lá todo, e não está seguido.
	invertido := make([]string, len(linhas))
	for i, l := range linhas {
		invertido[len(linhas)-1-i] = l
	}
	for _, c := range []struct {
		nome, envelope, texto string
		quer                  medidaDaOrigem
	}{
		{"transcricao fiel", leitura, notas,
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe09A11}},
		{"transcricao fiel, envelope sem artefacto", semArtefacto, notas,
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe09A11}},
		// Os espaços a mais não mudam a relação (normaliza-se), mas contam no TAMANHO: a razão é só
		// bytes, e por isso não é ela que diz «transcrição».
		{"transcricao com outros espacos e quebras", leitura, "  " + strings.ReplaceAll(notas, "\n", " \r\n\t") + "\n\n",
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe11A2}},
		{"transcricao com moldura", leitura, "O documento notes diz:\n\n" + notas + "\nFim do documento.",
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoContem, numeros: numerosTodos, razao: razaoDe11A2}},
		{"todas as linhas, por outra ordem", leitura, strings.Join(invertido, "\n"),
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoLinhasTodas, numeros: numerosTodos, razao: razaoDe09A11}},
		{"resumo que perde um numero", leitura, resumo,
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoLinhasAbaixo05, numeros: numerosEmFalta, razao: razaoDe05A09}},
		{"texto final vazio", leitura, "",
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoTextoVazio, numeros: numerosEmFalta, razao: razaoTextoVazio}},
		// O ENVELOPE em vez do documento (o modelo copiou o resultado da tool tal como o viu). O
		// documento não está lá letra a letra: as quebras de linha e as aspas vão escapadas, e só as
		// linhas sem aspas se encontram — quatro das seis. Os números estão todos.
		{"o texto final e o envelope", leitura, leitura,
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoLinhasDe05, numeros: numerosTodos, razao: razaoAcimaDe2}},
	} {
		got := medirOrigem(aos499Servido(c.envelope, c.texto))
		c.quer.estado, c.quer.bytes, c.quer.tamanho, c.quer.transporte = "designated", len(c.envelope), tamanhoAte1K, transporteConfere
		if got != c.quer {
			t.Errorf("%s:\n  medida = %+v\n  quero    %+v", c.nome, got, c.quer)
		}
		aos499NoVocabulario(t, c.nome, got)
	}

	// As formas que não se comparam, e o conteúdo vazio — cada uma na sua classe.
	for _, c := range []struct {
		fio, texto string
		quer       medidaDaOrigem
	}{
		// Um documento vazio NÃO é um resultado vazio: o envelope tem bytes (`tamanho` nunca é
		// `vazio`), e é a classe do CONTEÚDO que o diz.
		{"vazio", "nada a relatar",
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoConteudoVazio, numeros: numerosNenhum, razao: razaoOrigemVazia}},
		{"vazio", "",
			medidaDaOrigem{forma: formaEnvelope, comparacao: comparacaoConteudoVazio, numeros: numerosNenhum, razao: razaoOrigemVazia}},
		// A tool correu e falhou: designável, e não é um documento.
		{"saida-3", "cat: notes: No such file or directory\n",
			medidaDaOrigem{forma: formaEnvelopeFalhou, comparacao: naoComparado, numeros: naoComparado, razao: naoComparado}},
		{"leitura-em-falta", "o documento nao existe",
			medidaDaOrigem{forma: formaEnvelopeFalhou, comparacao: naoComparado, numeros: naoComparado, razao: naoComparado}},
		{"binario", "bytes",
			medidaDaOrigem{forma: formaEnvelopeBinario, comparacao: naoComparado, numeros: naoComparado, razao: naoComparado}},
	} {
		envelope, _ := aos499Envelope(t, c.fio)
		got := medirOrigem(aos499Servido(envelope, c.texto))
		c.quer.estado, c.quer.bytes, c.quer.tamanho, c.quer.transporte = "designated", len(envelope), tamanhoAte1K, transporteConfere
		if got != c.quer {
			t.Errorf("%s (texto %q):\n  medida = %+v\n  quero    %+v", c.fio, c.texto, got, c.quer)
		}
	}
}

// TestAOS499_Envelope_TranscricaoFielDeQualquerTamanho são os casos com que a revisão mostrou o
// defeito (`TestREV499_RazaoComEnvelope`): a transcrição BYTE A BYTE de um documento de 1 a 100
// linhas. Contra o envelope dava `diferente` em todos e `de_0_5_a_0_9` até ~350 bytes; contra o
// conteúdo dá `igual`, a razão de uma transcrição e os números todos, em todos os tamanhos.
func TestAOS499_Envelope_TranscricaoFielDeQualquerTamanho(t *testing.T) {
	for _, n := range []int{1, 2, 5, 10, 20, 100} {
		envelope, documento := aos499Envelope(t, fmt.Sprintf("linhas-%d", n))
		if strings.Count(documento, "\n") != n {
			t.Fatalf("pre-condicao: o envelope linhas-%d leva %d linhas; leva %d", n, n, strings.Count(documento, "\n"))
		}
		got := medirOrigem(aos499Servido(envelope, documento))
		quer := medidaDaOrigem{estado: "designated", bytes: len(envelope), tamanho: classeDeTamanho(len(envelope)), transporte: transporteConfere,
			forma: formaEnvelope, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe09A11}
		if got != quer {
			t.Errorf("linhas-%d (documento=%d bytes, envelope=%d):\n  medida = %+v\n  quero    %+v", n, len(documento), len(envelope), got, quer)
		}
		// O que a primeira versão media: o tamanho do texto contra o do ENVELOPE. Fica aqui a
		// conta, para se ver que o defeito era real nestes mesmos bytes.
		if antiga := classeDeRazao(len(documento), len(envelope)); n <= 5 && antiga == razaoDe09A11 {
			t.Errorf("linhas-%d: pre-condicao — contra o envelope a razao de uma transcricao fiel NAO era a de uma transcricao (era o defeito); veio %s", n, antiga)
		}
		if digestDoConteudo(documento) == digestDoConteudo(envelope) {
			t.Errorf("linhas-%d: pre-condicao — o digest do documento nunca e o do envelope", n)
		}
	}
	// O documento vazio: a conta antiga dizia `texto_vazio` e `diferente`.
	envelope, _ := aos499Envelope(t, "vazio")
	if got := medirOrigem(aos499Servido(envelope, "")); got.comparacao != comparacaoConteudoVazio || got.tamanho != tamanhoAte1K || got.razao != razaoOrigemVazia {
		t.Errorf("documento vazio transcrito: medida = %+v; quero conteudo_vazio, ate_1k (os bytes do envelope) e origem_vazia", got)
	}
}

// TestAOS499_DesembrulharEnvelope: o que se reconhece como envelope, e o que não. Tudo o que não
// é a forma exacta compara-se como veio, na classe `cru`.
func TestAOS499_DesembrulharEnvelope(t *testing.T) {
	// A forma real, de todos os ficheiros do fio da sandbox: nenhum é `cru`.
	pasta := filepath.Join("..", "..", "substrate", "sandbox", "testdata", "aos499_envelope")
	entradas, err := os.ReadDir(pasta)
	if err != nil || len(entradas) == 0 {
		t.Fatalf("ler %s: %v (%d ficheiros)", pasta, err, len(entradas))
	}
	for _, e := range entradas {
		nome := strings.TrimSuffix(e.Name(), ".json")
		envelope, documento := aos499Envelope(t, nome)
		conteudo, forma := desembrulharEnvelope(envelope)
		if forma == formaCru {
			t.Errorf("%s: o envelope do codificador real nao foi reconhecido — a forma mudou, e a medicao voltava a comparar com o envelope", nome)
		}
		if forma == formaEnvelope && conteudo != documento {
			t.Errorf("%s: conteudo desembrulhado = %q; o envelope leva %q", nome, conteudo, documento)
		}
		if forma != formaEnvelope && conteudo != "" {
			t.Errorf("%s: a forma %s nao devolve conteudo para comparar; veio %q", nome, forma, conteudo)
		}
	}
	// O que NÃO é o envelope.
	for nome, servido := range map[string]string{
		"texto":                     "conteudo do documento notes",
		"vazio":                     "",
		"outro objecto json":        `{"titulo":"notas","exit_code":0}`,
		"objecto sem exit_code":     `{"stdout_text":"notas"}`,
		"lista":                     `[{"exit_code":0}]`,
		"exit_code que nao e int":   `{"stdout_text":"notas","exit_code":"0"}`,
		"exit_code fraccionario":    `{"stdout_text":"notas","exit_code":0.5}`,
		"stdout_text que nao e str": `{"stdout_text":["notas"],"exit_code":0}`,
		"stdout que nao e base64":   `{"stdout":"***","exit_code":0}`,
		"os dois campos de stdout":  `{"stdout_text":"notas","stdout":"bm90YXM=","exit_code":0}`,
		"lixo depois do objecto":    `{"stdout_text":"notas","exit_code":0} e mais`,
		"null":                      `null`,
		"numero":                    `42`,
	} {
		conteudo, forma := desembrulharEnvelope(servido)
		if forma != formaCru || conteudo != servido {
			t.Errorf("%s: (%q, %s); quero os bytes tal como vieram, na classe cru", nome, conteudo, forma)
		}
	}
	// E a medida sobre bytes crus compara com eles, como sempre.
	doc := "linha 1 de 2\nlinha 2 de 2\n"
	quer := medidaDaOrigem{estado: "designated", bytes: len(doc), tamanho: tamanhoAte1K, transporte: transporteConfere,
		forma: formaCru, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe09A11}
	if got := medirOrigem(aos499Servido(doc, doc)); got != quer {
		t.Errorf("bytes crus transcritos: medida = %+v; quero %+v", got, quer)
	}
}

// TestAOS499_RelacaoComOConteudo: as fronteiras das classes, sobre textos pequenos.
func TestAOS499_RelacaoComOConteudo(t *testing.T) {
	dez := "a1\nb2\nc3\nd4\ne5\nf6\ng7\nh8\ni9\nj10\n"
	semAs := func(n int) string { return strings.Join(strings.Split(strings.TrimSpace(dez), "\n")[n:], "\n") }
	for _, c := range []struct {
		nome, texto, conteudo string
		comparacao, numeros   string
	}{
		{"igual", "a b", "a b", comparacaoIgual, numerosNenhum},
		{"igual depois de normalizar", " a\n\n b\t", "a b", comparacaoIgual, numerosNenhum},
		{"contem", "diz: a b. fim", "a b", comparacaoContem, numerosNenhum},
		{"conteudo so de espacos", "qualquer coisa 1", " \n\t ", comparacaoConteudoVazio, numerosNenhum},
		{"conteudo vazio e texto vazio", "", "", comparacaoConteudoVazio, numerosNenhum},
		{"texto vazio", " \n", "a 1", comparacaoTextoVazio, numerosEmFalta},
		{"10 de 10 linhas, com texto entre elas", strings.ReplaceAll(dez, "\n", "\n--\n"), dez, comparacaoLinhasTodas, numerosTodos},
		{"9 de 10 linhas", semAs(1), dez, comparacaoLinhasDe09, numerosEmFalta},
		{"8 de 10 linhas", semAs(2), dez, comparacaoLinhasDe05, numerosEmFalta},
		{"5 de 10 linhas", semAs(5), dez, comparacaoLinhasDe05, numerosEmFalta},
		{"4 de 10 linhas", semAs(6), dez, comparacaoLinhasAbaixo05, numerosEmFalta},
		{"nenhuma linha", "outra coisa", dez, comparacaoLinhasAbaixo05, numerosEmFalta},
		// Uma linha do conteúdo DENTRO de uma linha do texto (com marcador de citação) conta.
		{"linhas citadas", "> a1\n> b2", "a1\nb2", comparacaoLinhasTodas, numerosTodos},
		// Os números: a sequência inteira, e cada número distinto.
		{"numero dentro de outro nao conta", "em 2012", "eram 12", comparacaoLinhasAbaixo05, numerosEmFalta},
		{"numero reformatado conta como em falta", "total 1.250", "total 1250", comparacaoLinhasAbaixo05, numerosEmFalta},
		{"os numeros todos, noutro texto", "12 e 7, em 2026", "tarefa 12; tarefa 7; ano 2026; tarefa 12", comparacaoLinhasAbaixo05, numerosTodos},
		{"um numero em falta entre varios", "12 e 7", "12, 7 e 4471", comparacaoLinhasAbaixo05, numerosEmFalta},
		{"numero no fim do conteudo", "x 9", "y 9", comparacaoLinhasAbaixo05, numerosTodos},
	} {
		comparacao, numeros := relacaoComOConteudo(c.texto, c.conteudo)
		if comparacao != c.comparacao || numeros != c.numeros {
			t.Errorf("%s: (%s, %s); quero (%s, %s)", c.nome, comparacao, numeros, c.comparacao, c.numeros)
		}
	}
	// O ORÇAMENTO de procuras: um conteúdo com mais linhas do que o orçamento, nenhuma delas uma
	// linha inteira do texto, não se percorre sem limite — as que passam do orçamento contam como
	// ausentes, e as que são linhas inteiras do texto contam sempre.
	var muitas strings.Builder
	for i := 0; i < maxProcurasDeLinha+10; i++ {
		fmt.Fprintf(&muitas, "linha %d\n", i)
	}
	if comparacao, _ := relacaoComOConteudo("> "+strings.ReplaceAll(muitas.String(), "\n", "\n> "), muitas.String()); comparacao != comparacaoLinhasDe09 {
		t.Errorf("linhas citadas alem do orcamento: %s; quero %s (as %d primeiras procuram-se, as outras 10 contam como ausentes)", comparacao, comparacaoLinhasDe09, maxProcurasDeLinha)
	}
	if comparacao, _ := relacaoComOConteudo("antes\n"+muitas.String()+"\n\ndepois x", "x\n"+muitas.String()); comparacao != comparacaoLinhasTodas {
		t.Errorf("linhas inteiras alem do orcamento: %s; quero %s (decidem-se pelo conjunto, sem gastar o orcamento)", comparacao, comparacaoLinhasTodas)
	}
}

// TestAOS499_MedirOrigem: a medida sobre respostas que o nó real não daria — o que vem de fora do
// vocabulário não é repetido — e sobre o que o nó fez dos bytes.
func TestAOS499_MedirOrigem(t *testing.T) {
	doc := "o documento 42"
	designada := func() *agentruntime.OutputSource {
		return &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceDesignated,
			StepID: "step-000001-tool-1", Digest: digestDoConteudo(doc), Bytes: len(doc)}
	}
	outro := "outro conteudo 43"
	grande := designada()
	grande.Bytes = maxPayloadBytes + 1
	semForma := designada()
	semForma.Digest = "sha256:curto"
	estadoDeFora := designada()
	estadoDeFora.State = "Designated\naviso: forjado"
	vinculativa := designada()
	vinculativa.Binding = agentruntime.OutputSourceBinds
	semBytes := func(transporte string, bytes int, tamanho string) medidaDaOrigem {
		return medidaDaOrigem{estado: "designated", bytes: bytes, tamanho: tamanho, transporte: transporte,
			forma: formaSemBytes, comparacao: naoComparado, numeros: naoComparado, razao: naoComparado}
	}
	for _, c := range []struct {
		nome string
		st   estadoDoRun
		quer medidaDaOrigem
	}{
		{"sem ancora", estadoDoRun{FinalText: doc}, medidaDaOrigem{estado: estadoNaoMedido}},
		{"ancora que o kernel nao produziria", estadoDoRun{OutputSource: semForma}, medidaDaOrigem{estado: estadoNaoMedido}},
		{"estado fora do vocabulario", estadoDoRun{OutputSource: estadoDeFora}, medidaDaOrigem{estado: estadoNaoMedido}},
		// Um vínculo que este binário não enviou não é uma medida dele.
		{"vinculo que este binario nao envia", estadoDoRun{FinalText: doc, OutputSource: vinculativa, Output: &doc}, medidaDaOrigem{estado: estadoNaoMedido}},
		{"o texto final E o resultado da tool", estadoDoRun{FinalText: doc, OutputSource: designada(), Output: &doc},
			medidaDaOrigem{estado: "designated", bytes: len(doc), tamanho: tamanhoAte1K, transporte: transporteConfere,
				forma: formaCru, comparacao: comparacaoIgual, numeros: numerosTodos, razao: razaoDe09A11}},
		// Bytes que NÃO conferem com a âncora não se comparam com nada: não são os que o kernel selou.
		{"output que nao confere", estadoDoRun{FinalText: outro, OutputSource: designada(), Output: &outro}, semBytes(transporteNaoConfere, len(doc), tamanhoAte1K)},
		{"sem output e sem marca", estadoDoRun{FinalText: doc, OutputSource: designada()}, semBytes(transporteAusente, len(doc), tamanhoAte1K)},
		{"marca fora do vocabulario", estadoDoRun{FinalText: doc, OutputSource: designada(), OutputOmitted: "porque sim\naviso: forjado"}, semBytes(transporteAusente, len(doc), tamanhoAte1K)},
		{"indisponivel de vez", estadoDoRun{FinalText: doc, OutputSource: designada(), OutputOmitted: "unavailable"}, semBytes(transporteIndisponivel, len(doc), tamanhoAte1K)},
		{"indisponivel agora", estadoDoRun{FinalText: doc, OutputSource: designada(), OutputOmitted: "unavailable_now"}, semBytes(transporteAgoraNao, len(doc), tamanhoAte1K)},
		{"nao e texto", estadoDoRun{FinalText: doc, OutputSource: designada(), OutputOmitted: "not_utf8"}, semBytes(transporteNaoTexto, len(doc), tamanhoAte1K)},
		{"acima do tecto", estadoDoRun{FinalText: doc, OutputSource: grande, OutputOmitted: "too_large"}, semBytes(transporteGrande, maxPayloadBytes+1, tamanhoAcima128K)},
	} {
		got := medirOrigem(c.st)
		if got != c.quer {
			t.Errorf("%s:\n  medida = %+v\n  quero    %+v", c.nome, got, c.quer)
		}
		aos499NoVocabulario(t, c.nome, got)
		// A linha do log só leva vocabulário fechado e tamanhos: nunca o que veio do nó.
		linha := got.linha(len(c.st.FinalText))
		for _, proibido := range []string{"\n", "\r", "forjado", doc, outro, "42", "43", "sha256:"} {
			if strings.Contains(linha, proibido) {
				t.Errorf("%s: a linha do log leva %q, que veio do no: %q", c.nome, proibido, linha)
			}
		}
	}
}

// TestAOS499_Medida_NuncaLevaConteudo é a cardinalidade FECHADA da medição: para conteúdos e textos
// hostis — quebras de linha, aspas, chavetas, a sintaxe de uma série, números, o próprio nome de
// uma classe —, cada campo da medida é uma constante das listas fechadas, e a linha do log e o
// ficheiro de métricas não levam nada do que o nó mandou. O número de séries possíveis é o
// produto das listas, e não cresce com o que se lê.
func TestAOS499_Medida_NuncaLevaConteudo(t *testing.T) {
	_, notas := aos499Envelope(t, "leitura-notas")
	hostis := []string{
		"", " ", "\n", "segredo-7391", "linha 1\nlinha 2 com 555-0199\n", `{"exit_code":0,"stdout_text":"segredo-7391 x"}`,
		`aos_orq_consume_origem_texto_final_total{comparacao="igual"} 99`, "igual", "todos", "envelope", `"} 1` + "\n" + `falsa{a="b`,
		notas, strings.Repeat("9", 300), "é ü 日本 \x00 \x7f",
	}
	// Envelopes do codificador real, mais os hostis como bytes crus.
	var servidos []string
	entradas, err := os.ReadDir(filepath.Join("..", "..", "substrate", "sandbox", "testdata", "aos499_envelope"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entradas {
		envelope, _ := aos499Envelope(t, strings.TrimSuffix(e.Name(), ".json"))
		servidos = append(servidos, envelope)
	}
	servidos = append(servidos, hostis...)
	c := &medicaoDoContrato{}
	vistas := map[medidaDaOrigem]bool{}
	for _, servido := range servidos {
		for _, texto := range hostis {
			m := medirOrigem(aos499Servido(servido, texto))
			aos499NoVocabulario(t, "hostil", m)
			if m.forma == "" || m.comparacao == "" || m.numeros == "" || m.razao == "" || m.transporte == "" || m.tamanho == "" {
				t.Fatalf("uma origem designada tem todas as classes preenchidas; veio %+v", m)
			}
			c.origemMedida(m)
			sembytes := m
			sembytes.bytes = 0
			vistas[sembytes] = true
			linha := m.linha(len(texto))
			for _, proibido := range []string{"segredo", "7391", "555", "0199", "4471", "1250", "orçamento", "falsa", "\n", `"`, "{"} {
				if strings.Contains(linha, proibido) {
					t.Fatalf("a linha do log leva %q (servido %.40q, texto %.40q): %q", proibido, servido, texto, linha)
				}
			}
		}
	}
	// Tirando o tamanho em bytes, as medidas distintas são poucas: o conteúdo não cria classes.
	if tecto := len(formasDaOrigem) * len(comparacoesDoTexto) * len(resultadosDosNumeros) * len(classesDeRazao) * len(classesDeTamanho); len(vistas) > tecto || len(vistas) > 80 {
		t.Fatalf("%d medidas distintas para %d pares: a medida esta a levar alguma coisa do conteudo", len(vistas), len(servidos)*len(hostis))
	}
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarContrato(c)
	texto := string(m.texto())
	for _, proibido := range []string{"segredo", "7391", "555", "0199", "4471", "1250", "falsa", "日本"} {
		if strings.Contains(texto, proibido) {
			t.Fatalf("o ficheiro de metricas leva %q:\n%s", proibido, texto)
		}
	}
	// Cada linha de série destas famílias tem um rótulo das listas fechadas, e só um.
	fechados := map[string][]string{
		metricaOrigemDesignacao: estadosDaDesignacao(), metricaOrigemTamanho: classesDeTamanho, metricaOrigemTransporte: resultadosDoTransporte,
		metricaOrigemForma: formasDaOrigem, metricaOrigemTextoFinal: comparacoesDoTexto, metricaOrigemNumeros: resultadosDosNumeros, metricaOrigemRazao: classesDeRazao,
	}
	series := 0
	for _, linha := range strings.Split(texto, "\n") {
		for familia, lista := range fechados {
			if !strings.HasPrefix(linha, familia+"{") {
				continue
			}
			series++
			casa := false
			for _, v := range lista {
				casa = casa || strings.Contains(linha, `="`+v+`"} `)
			}
			if !casa {
				t.Errorf("serie fora do vocabulario fechado: %s", linha)
			}
		}
	}
	if series == 0 {
		t.Fatal("o teste nao viu serie nenhuma: nao provava nada")
	}
}

// TestAOS499_Metricas_SoVocabularioFechado: o que se escreve no ficheiro de métricas é o que as
// listas fechadas admitem. Um valor fora delas não cria série.
func TestAOS499_Metricas_SoVocabularioFechado(t *testing.T) {
	c := &medicaoDoContrato{}
	c.noPorEstrutura(classeCandidato)
	c.noPorEstrutura(classeCandidato)
	c.noPorEstrutura(classeNaoCandidato)
	c.noPorEstrutura("classe\ninventada")
	c.origemMedida(medidaDaOrigem{estado: "designated", tamanho: tamanhoAte16K, transporte: transporteConfere,
		forma: formaEnvelope, comparacao: comparacaoContem, numeros: numerosEmFalta, razao: razaoDe05A09})
	c.origemMedida(medidaDaOrigem{estado: "missing"})
	c.origemMedida(medidaDaOrigem{estado: estadoNaoMedido})
	c.origemMedida(medidaDaOrigem{estado: "estado de fora", tamanho: "enorme", razao: "muita", comparacao: "parecido", transporte: "por pombo", forma: "redonda", numeros: "alguns"})
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarContrato(c)
	texto := string(m.texto())
	for chave, valor := range map[string]int{
		serie(metricaNosPorEstrutura, "classe", classeCandidato):       2,
		serie(metricaNosPorEstrutura, "classe", classeNaoCandidato):    1,
		serie(metricaOrigemDesignacao, "estado", "designated"):         1,
		serie(metricaOrigemDesignacao, "estado", "missing"):            1,
		serie(metricaOrigemDesignacao, "estado", estadoNaoMedido):      1,
		serie(metricaOrigemTamanho, "classe", tamanhoAte16K):           1,
		serie(metricaOrigemRazao, "classe", razaoDe05A09):              1,
		serie(metricaOrigemTextoFinal, "comparacao", comparacaoContem): 1,
		serie(metricaOrigemNumeros, "resultado", numerosEmFalta):       1,
		serie(metricaOrigemForma, "forma", formaEnvelope):              1,
		serie(metricaOrigemTransporte, "resultado", transporteConfere): 1,
	} {
		if !temSerie(texto, chave, valor) {
			t.Fatalf("faltou nas metricas %s %d:\n%s", chave, valor, texto)
		}
	}
	for _, proibido := range []string{"inventada", "estado de fora", "enorme", "muita", "parecido", "pombo", "redonda", "alguns"} {
		if strings.Contains(texto, proibido) {
			t.Fatalf("um valor fora do vocabulario criou uma serie (%q):\n%s", proibido, texto)
		}
	}
	// Nil não rebenta e não conta: o `serve` manual não mede.
	var nula *medicaoDoContrato
	nula.noPorEstrutura(classeCandidato)
	nula.origemMedida(medidaDaOrigem{estado: "designated"})
	// As oito famílias estão no catálogo do ficheiro: uma linha que o catálogo não conheça é deitada fora.
	for _, nome := range []string{metricaNosPorEstrutura, metricaOrigemDesignacao, metricaOrigemTamanho, metricaOrigemRazao,
		metricaOrigemTextoFinal, metricaOrigemTransporte, metricaOrigemForma, metricaOrigemNumeros} {
		achou := false
		for _, e := range catalogoDeMetricas {
			achou = achou || e.nome == nome
		}
		if !achou {
			t.Errorf("%s nao esta no catalogo do ficheiro de metricas", nome)
		}
	}
}

// TestAOS499_Fio_CadaRespostaDoNoTemLeitura (revisão, M8): CADA ficheiro de
// `cmd/aos/testdata/aos498_fio/` — as respostas que o nó real gera nos testes dele — é lido aqui
// pelo cliente do `aos-orq` e tem a medida que se espera dele. Um ficheiro que o nó passe a gerar
// e que ninguém leia, ou um que deixe de existir, falha o teste: quatro dos quinze eram gerados
// sem consumidor.
func TestAOS499_Fio_CadaRespostaDoNoTemLeitura(t *testing.T) {
	cru := medidaDaOrigem{estado: "designated", bytes: 27, tamanho: tamanhoAte1K, transporte: transporteConfere,
		forma: formaCru, comparacao: comparacaoContem, numeros: numerosNenhum, razao: razaoDe11A2}
	producao := medidaDaOrigem{estado: "designated", bytes: 21, tamanho: tamanhoAte1K, transporte: transporteConfere,
		forma: formaCru, comparacao: comparacaoLinhasAbaixo05, numeros: numerosNenhum, razao: razaoAbaixoDe05}
	indisponivel := medidaDaOrigem{estado: "designated", bytes: 27, tamanho: tamanhoAte1K, transporte: transporteIndisponivel,
		forma: formaSemBytes, comparacao: naoComparado, numeros: naoComparado, razao: naoComparado}
	// Um vínculo que este binário não enviou (`binding`) não é uma medida dele.
	naoMedido := medidaDaOrigem{estado: estadoNaoMedido}
	type leitura struct {
		medida    medidaDaOrigem
		concluiu  bool
		temOutput bool
	}
	quer := map[string]leitura{
		"memoria-measure-enforce":             {cru, true, true},
		"memoria-measure-observe":             {cru, true, true},
		"duravel-measure-enforce":             {cru, true, true},
		"duravel-measure-observe":             {cru, true, true},
		"memoria-binding-enforce":             {naoMedido, true, true},
		"duravel-binding-enforce":             {naoMedido, true, true},
		"memoria-missing-measure":             {medidaDaOrigem{estado: "missing"}, true, false},
		"memoria-ambiguous-measure":           {medidaDaOrigem{estado: "ambiguous"}, true, false},
		"memoria-bytes-indisponiveis-measure": {indisponivel, true, false},
		"duravel-titular-apagado-measure":     {indisponivel, true, false},
		"nao-transportavel-grande":            {naoMedido, true, false},
		"nao-transportavel-binario":           {naoMedido, true, false},
		"producao-designada-enforce":          {producao, true, true},
		"producao-designada-observe":          {producao, true, true},
		"producao-em-falta-observe":           {medidaDaOrigem{estado: "missing"}, true, false},
		// O nó cuja `doc_read` corre na SANDBOX REAL (`TestAOS498_Sandbox_EnvelopeReal`): o envelope
		// é reconhecido, e o texto final — o documento atrás de uma frase — contém-no inteiro.
		"sandbox-designada-measure": {medidaDaOrigem{estado: "designated", bytes: 640, tamanho: tamanhoAte1K, transporte: transporteConfere,
			forma: formaEnvelope, comparacao: comparacaoContem, numeros: numerosTodos, razao: razaoDe11A2}, true, true},
	}
	pasta := filepath.Join("..", "aos", "testdata", "aos498_fio")
	entradas, err := os.ReadDir(pasta)
	if err != nil {
		t.Fatalf("ler %s: %v", pasta, err)
	}
	vistos := map[string]bool{}
	for _, e := range entradas {
		nome := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasPrefix(nome, "tools-") {
			// Um anúncio do `GET /tools`: lido em [TestAOS499_Anuncio_OClienteLeOFioDoNo].
			if nome != "tools-off" {
				t.Errorf("%s: anuncio que nenhum teste do aos-orq le", nome)
			}
			continue
		}
		q, conhecido := quer[nome]
		if !conhecido {
			t.Errorf("%s: o no gera esta resposta e nenhum teste do aos-orq a le — acrescenta-a aqui, com a medida que se espera", nome)
			continue
		}
		vistos[nome] = true
		var st estadoDoRun
		if err := json.Unmarshal(aos499FioDoNo(t, nome), &st); err != nil {
			t.Errorf("%s: o cliente do aos-orq nao le a resposta do no: %v", nome, err)
			continue
		}
		if got := medirOrigem(st); got != q.medida {
			t.Errorf("%s:\n  medida = %+v\n  quero    %+v", nome, got, q.medida)
		}
		if st.concluiu() != q.concluiu || (st.Output != nil) != q.temOutput || st.OutputSource == nil {
			t.Errorf("%s: concluiu=%v output=%v ancora=%v; quero concluiu=%v output=%v e a ancora", nome, st.concluiu(), st.Output != nil, st.OutputSource != nil, q.concluiu, q.temOutput)
		}
	}
	var emFalta []string
	for nome := range quer {
		if !vistos[nome] {
			emFalta = append(emFalta, nome)
		}
	}
	sort.Strings(emFalta)
	if len(emFalta) > 0 {
		t.Errorf("respostas que este teste espera e o no ja nao gera: %v", emFalta)
	}
}

// TestAOS499_Envelope_ComOBinarioReal corre o `aos-orq consume` REAL contra um nó que responde ao
// candidato com o envelope da sandbox — os bytes do codificador real — e com um texto final que é
// um RESUMO que perde um número. É a medição que interessa ao AOS-501, de ponta a ponta:
//
//   - a linha do log e as séries dizem `forma=envelope`, uma fracção baixa das linhas e
//     `numeros=em_falta`;
//   - o plano sai como sempre, e o consumidor recebe o TEXTO FINAL com o digest do texto final;
//   - nem o log, nem as métricas, nem o `detail` levam uma linha ou um número do documento.
//
// E com a transcrição fiel no lugar do resumo, as classes são as de uma transcrição.
func TestAOS499_Envelope_ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	const run = "plan-aos499-envelope"
	envelope, notas := aos499Envelope(t, "leitura-notas")
	resumo := "A reuniao deixou tarefas por fechar e aprovou 1250 EUR."
	for _, c := range []struct {
		nome, texto, linha string
		series             map[string]string
	}{
		{"resumo-que-perde-numeros", resumo,
			"transporte=servido_confere forma=envelope bytes_do_texto=55 texto_final=linhas_abaixo_de_0_5 numeros=em_falta razao=abaixo_de_0_5 — so medicao",
			map[string]string{metricaOrigemTextoFinal: comparacaoLinhasAbaixo05, metricaOrigemNumeros: numerosEmFalta, metricaOrigemRazao: razaoAbaixoDe05}},
		{"transcricao-com-moldura", "O documento notes diz:\n\n" + notas,
			fmt.Sprintf("transporte=servido_confere forma=envelope bytes_do_texto=%d texto_final=contem numeros=todos razao=de_1_1_a_2 — so medicao", len("O documento notes diz:\n\n"+notas)),
			map[string]string{metricaOrigemTextoFinal: comparacaoContem, metricaOrigemNumeros: numerosTodos, metricaOrigemRazao: razaoDe11A2}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			st := aos499Servido(envelope, c.texto)
			resposta, err := json.Marshal(map[string]any{
				"run_id": run + "~read_notes", "status": "completed", "terminated": true, "final_text": c.texto, "turns": 2,
				"output_source": st.OutputSource, "output": envelope,
			})
			if err != nil {
				t.Fatal(err)
			}
			p := aos495FormaDeProducao(t, "enforce")
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": resposta}}
			d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
			if d.classe != "terminal" || d.codigo != exitOK {
				t.Fatalf("o plano sai como sempre, terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			quer := fmt.Sprintf("execucao: no read_notes ORIGEM MEDIDA estado=designated bytes_da_origem=%d (ate_1k) %s", len(envelope), c.linha)
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
			if !temSerie(d.metricas, serie(metricaOrigemForma, "forma", formaEnvelope), 1) ||
				!temSerie(d.metricas, serie(metricaOrigemTextoFinal, "comparacao", c.series[metricaOrigemTextoFinal]), 1) ||
				!temSerie(d.metricas, serie(metricaOrigemNumeros, "resultado", c.series[metricaOrigemNumeros]), 1) ||
				!temSerie(d.metricas, serie(metricaOrigemRazao, "classe", c.series[metricaOrigemRazao]), 1) ||
				!temSerie(d.metricas, serie(metricaOrigemTransporte, "resultado", transporteConfere), 1) {
				t.Fatalf("faltaram series da medicao de conteudo:\n%s", d.metricas)
			}
			// A ENTREGA É A DE SEMPRE: o texto final, com o digest do texto final.
			entradas, _ := f.corpo(run, "summarize")["inputs"].([]any)
			if len(entradas) != 1 {
				t.Fatalf("o consumidor tinha de receber uma entrada: %v", entradas)
			}
			entrada := entradas[0].(map[string]any)
			if entrada["content"] != c.texto || entrada["digest"] != digestDoConteudo(c.texto) {
				t.Fatalf("o consumidor recebe o texto final e o seu digest — nem o envelope, nem o conteudo desembrulhado: %v", entrada)
			}
			// SEM CONTEÚDO: nem uma linha, nem um número, nem o digest do que o nó serviu.
			for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": d.metricas, "detalhe": d.detalhe} {
				for _, proibido := range []string{"4471", "17h30", "orçamento", "gVisor", "fornecedor", "parcelas", st.OutputSource.Digest} {
					if strings.Contains(texto, proibido) {
						t.Fatalf("o %s leva %q, que e conteudo do titular ou veio do no:\n%s", onde, proibido, texto)
					}
				}
			}
		})
	}
}
