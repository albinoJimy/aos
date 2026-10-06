package main

// AOS-501 — O `aos-orq` ENTREGA POR REFERÊNCIA AS SAÍDAS DECLARADAS.
//
// Os testes de processo correm o `aos-orq consume` REAL contra o nó `aos` falso do AOS-495. O
// que esse nó responde no caso central não é inventado aqui: `packages/cmd/aos/testdata/
// aos501_fio/` são as respostas de um nó REAL, cuja tool corre na sandbox, ao corpo do
// `POST /runs` que ESTE binário enviou (`testdata/aos501_fio/post-runs-read_notes.json`) — e o
// corpo do `POST /runs` do consumidor, com o documento nos `inputs`, é gerado aqui e entregue
// byte a byte ao nó real (`TestAOS501_Fio_*`, do lado do nó). Regeneram-se com
// `AOS494_ACTUALIZAR_FIO=1`, por esta ordem: este pacote (o corpo do produtor), o nó (as
// respostas), este pacote outra vez (o corpo do consumidor), e o nó outra vez.
//
// O que os testes prendem:
//   - em `on`, com a origem declarada, o consumidor recebe o que a TOOL devolveu (de um envelope
//     da sandbox, só o texto do documento), conferido inteiro contra a âncora, e o texto final do
//     produtor não aparece em lado nenhum da entrega;
//   - cada falha fecha o produtor `failed` com causa própria, e o consumidor não corre;
//   - a declaração e o vínculo ficam no log do plano, e é do log que quem recolhe decide;
//   - fora de `on` nada muda, e em `on` um plano sem `from_tool` corre como sempre.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/decompose"
	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	plannerprompt "github.com/aos-ref/control-plane/orchestrator/plannerprompt"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/control-plane/runlifecycle"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	arstate "github.com/aos-ref/kernel/agent-runtime/state"
)

const aos501Run = "plan-aos501-fio"

// aos501FioDoNo lê uma resposta do nó real, gravada pelos testes do AOS-501 do nó.
func aos501FioDoNo(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos", "testdata", "aos501_fio", nome+".json"))
	if err != nil && os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
		t.Skipf("a resposta do no ainda nao foi gerada (%v): corre o teste do no com AOS494_ACTUALIZAR_FIO=1, e este outra vez", err)
	}
	if err != nil || len(bytes.TrimSpace(cru)) == 0 {
		t.Fatalf("ficheiro do fio %s: err=%v bytes=%d — gera-o no no com AOS494_ACTUALIZAR_FIO=1", nome, err, len(cru))
	}
	return bytes.TrimSpace(cru)
}

// aos501FioDoPost compara o corpo de um `POST /runs` com o ficheiro que o teste do NÓ consome.
func aos501FioDoPost(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos501_fio", nome+".json")
	cru = bytes.TrimSpace(cru)
	if os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatalf("criar a pasta do fio: %v", err)
		}
		if err := os.WriteFile(caminho, append(cru, '\n'), 0o644); err != nil {
			t.Fatalf("escrever %s: %v", caminho, err)
		}
		return
	}
	quer, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
	}
	if !bytes.Equal(bytes.TrimSpace(quer), cru) {
		t.Fatalf("o POST /runs deixou de levar o que o no aceita nos testes dele (%s):\n  fio:   %s\n  agora: %s", caminho, bytes.TrimSpace(quer), cru)
	}
}

// aos501Plano é o plano na forma de produção (AOS-495) com a saída do leitor a DECLARAR A ORIGEM:
// `from_tool: doc_read`, carimbado 1.3.0. É a única diferença para o plano de sempre.
func aos501Plano(t *testing.T, p aos495Producao) string {
	t.Helper()
	plano := p.plano
	for _, troca := range [][2]string{
		{`"plan_version": "1.2.0"`, `"plan_version": "1.3.0"`},
		{`"outputs":[{"name":"conteudo","type":"record","taint":"untrusted"}]`, `"outputs":[{"name":"conteudo","type":"record","taint":"untrusted","from_tool":"doc_read"}]`},
	} {
		if strings.Count(plano, troca[0]) != 1 {
			t.Fatalf("pre-condicao: o plano de producao tem uma vez %s", troca[0])
		}
		plano = strings.Replace(plano, troca[0], troca[1], 1)
	}
	return plano
}

// aos501Ancora escreve a âncora (`output_source`) da tool `doc_read` com o vínculo e o estado
// dados; em `designated`, com o passo, o digest e o tamanho de `servido`.
func aos501Ancora(vinculo, estado, servido string) string {
	a := `"output_source":{"tool":"doc_read","binding":"` + vinculo + `","state":"` + estado + `"`
	if estado == "designated" {
		a += fmt.Sprintf(`,"step_id":"step-000001-tool-1","digest":%q,"bytes":%d`, digestDoConteudo(servido), len(servido))
	}
	return a + "}"
}

// aos501TextoFinal é o que o modelo do produtor escreve nos casos montados à mão: um resumo, que
// nunca pode chegar ao consumidor nem ao log do plano.
const aos501TextoFinal = "RESUMO-DO-MODELO a reuniao aprovou um total em parcelas"

// aos501Concluido monta a resposta de um run `read_notes` CONCLUÍDO, com o texto final do modelo
// e os campos dados a seguir (a âncora, `output`, marcas).
func aos501Concluido(campos ...string) []byte {
	corpo := `{"run_id":"` + aos501Run + `~read_notes","status":"completed","terminated":true,"final_text":"` + aos501TextoFinal + `","turns":2`
	for _, c := range campos {
		corpo += "," + c
	}
	return []byte(corpo + "}")
}

// aos501Servido é o campo `output` com os bytes dados.
func aos501Servido(t *testing.T, servido string) string {
	t.Helper()
	cru, err := json.Marshal(servido)
	if err != nil {
		t.Fatal(err)
	}
	return `"output":` + string(cru)
}

// aos501Evento devolve o payload do único evento do tipo dado, ou falha; `quantos` devolve a
// contagem.
func aos501Eventos(eventos []aos499Evento, tipo string) []aos499Evento {
	var out []aos499Evento
	for _, e := range eventos {
		if e.Tipo == tipo {
			out = append(out, e)
		}
	}
	return out
}

// aos501MetricasSemRelogio tira do ficheiro de métricas as duas séries cujo VALOR é do relógio — o
// instante da última drenagem, em segundos Unix, e a soma das durações — para se poder procurar
// nele conteúdo do documento.
//
// PORQUE EXISTE. Os testes de «sem conteúdo» procuram no ficheiro números do documento de fio
// («1250», o total aprovado). O instante da última drenagem é um número de dez dígitos, e entre
// 1791250000 e 1791259999 — 2 h 47 min de 2026-10-06 — contém «1250»: nessa janela os testes
// falhavam todos, sem defeito nenhum. Encontrado ao correr as mutações da revisão dentro dela. O
// valor de uma série do relógio não é conteúdo do titular; o que se quer provar é que as séries
// que CONTAM não levam nada do documento, e essas ficam.
func aos501MetricasSemRelogio(metricas string) string {
	var linhas []string
	for _, l := range strings.Split(metricas, "\n") {
		if strings.HasPrefix(l, metricaUltimaDrenagem+" ") || strings.HasPrefix(l, metricaDuracao+"_sum") {
			continue
		}
		linhas = append(linhas, l)
	}
	return strings.Join(linhas, "\n")
}

// aos501NuncaAparece exige que `proibido` não esteja em nenhum sítio da ENTREGA nem do que sai do
// processo: os corpos dos `POST /runs`, os eventos do plano, o log, as métricas e o `detail`. Das
// métricas ficam de fora as duas séries do relógio ([aos501MetricasSemRelogio]).
func aos501NuncaAparece(t *testing.T, oQue, proibido string, d aos499Drenagem, f *aos495No, eventos []aos499Evento) {
	t.Helper()
	for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe} {
		if levaProibido(texto, proibido) {
			t.Fatalf("%s aparece em %s:\n%s", oQue, onde, texto)
		}
	}
	for _, e := range eventos {
		if levaProibido(e.Payload, proibido) {
			t.Fatalf("%s aparece no evento %s do plano:\n%s", oQue, e.Tipo, e.Payload)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, cru := range f.crus {
		if levaProibido(string(cru), proibido) {
			t.Fatalf("%s aparece no POST /runs de %s:\n%s", oQue, id, cru)
		}
	}
}

// aos501Entrada devolve a única entrada (`inputs[0]`) do `POST /runs` do consumidor.
func aos501Entrada(t *testing.T, f *aos495No) map[string]any {
	t.Helper()
	corpo := f.corpo(aos501Run, "summarize")
	if corpo == nil {
		t.Fatal("o consumidor nao foi submetido")
	}
	entradas, _ := corpo["inputs"].([]any)
	if len(entradas) != 1 {
		t.Fatalf("o consumidor tinha de receber uma entrada: %v", corpo["inputs"])
	}
	return entradas[0].(map[string]any)
}

// TestAOS501ComOBinarioReal corre os cenários de processo sobre UM só binário.
func TestAOS501ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	comOrigem := aos501Plano(t, p)

	// (1) O CORPO DO PRODUTOR. Em `on`, o nó cujo plano declara a origem leva `output_from_tool` e
	// o vínculo `binding`; o facto fica no log ANTES de o run ter resposta. O ficheiro de fio é o
	// que o nó real consome.
	t.Run("On_OCorpoDoProdutorLevaAOrigemEOVinculoBinding", func(t *testing.T) {
		f := &aos495No{catalogo: p.catalogo}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		aos501FioDoPost(t, "post-runs-read_notes", f.crus[aos501Run+"~read_notes"])
		corpo := f.corpo(aos501Run, "read_notes")
		tool, vinculo, presente := origemEnviada(corpo)
		if !presente || tool != "doc_read" || vinculo != "binding" {
			t.Fatalf("o produtor tinha de declarar a origem doc_read com o vinculo binding; levou %q/%q (presente=%v)", tool, vinculo, presente)
		}
		if c, ok := contratoEnviado(corpo); !ok || len(c) != 1 || c[0] != "doc_read" {
			t.Fatalf("o contrato de conclusao do produtor continua a ir: %v", c)
		}
		for _, quer := range []string{
			"saida por referencia (AOS-501): modo on, ENTREGA ACTIVA",
			"planeador: prompt 1.5.0",
			"execucao: no read_notes saida por referencia: a saida conteudo declara a origem (a tool doc_read) — o run leva o vinculo binding, e o facto fica no log do plano\n",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
		}
		// O FACTO NO LOG DO PLANO, derivado do documento aprovado.
		eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
		declarados := aos501Eventos(eventos, plannerevents.EventOutputSourceDeclared)
		if len(declarados) != 1 || declarados[0].Passo != "planstep:output_source_declared:read_notes" || declarados[0].Stream != aos501Run+"-plan" {
			t.Fatalf("o log do plano tinha de ter UM facto plan.output_source_declared para read_notes: %+v", declarados)
		}
		var facto plannerevents.OutputSourceDeclaredPayload
		if err := json.Unmarshal([]byte(declarados[0].Payload), &facto); err != nil ||
			facto.NodeID != "read_notes" || facto.Output != "conteudo" || facto.Tool != "doc_read" || facto.Binding != plannerevents.OutputSourceBindingBinds {
			t.Fatalf("o facto diz o no, a saida, a tool e o vinculo: %+v (%v)", facto, err)
		}
		// O nó falso respondeu SEM âncora (um run que não foi julgado como vinculativo): não se
		// entrega, o produtor falha com causa, e o consumidor não corre.
		if d.classe != "terminal" || d.codigo != exitNosFalhados || !strings.HasSuffix(d.detalhe, " causa="+causaEntradaPorCumprir+":1,"+causaOrigemSemVinculo+":1") {
			t.Fatalf("sem ancora o plano sai terminal/13 com a causa origem_sem_vinculo; saiu %s/%d %q", d.classe, d.codigo, d.detalhe)
		}
		if f.corpo(aos501Run, "summarize") != nil {
			t.Fatal("o consumidor correu sem a saida por referencia do produtor")
		}
		if len(aos501Eventos(eventos, plannerevents.EventPayloadPublished)) != 0 {
			t.Fatal("publicou-se um payload sem origem designada")
		}
	})

	// (2) PONTA A PONTA COM A TOOL REAL DA SANDBOX — o caso medido em produção. A resposta é a de
	// um nó real ao corpo do teste (1): o modelo falso chamou a `doc_read` da sandbox e escreveu um
	// RESUMO que perde um número. O consumidor recebe o DOCUMENTO INTEIRO, com o número.
	t.Run("On_Sandbox_OConsumidorRecebeODocumentoENaoOResumo", func(t *testing.T) {
		fio := aos501FioDoNo(t, "sandbox-binding-resumo")
		var st estadoDoRun
		if err := json.Unmarshal(fio, &st); err != nil || !st.concluiu() || st.Output == nil || st.OutputSource == nil ||
			st.OutputSource.Binding != agentruntime.OutputSourceBinds || st.OutputSource.State != agentruntime.OutputSourceDesignated {
			t.Fatalf("pre-condicao: o fio traz o run concluido, com a ancora binding/designated e os bytes (%v)", err)
		}
		documento, forma := desembrulharEnvelope(*st.Output)
		if forma != formaEnvelope || documento == *st.Output || !strings.Contains(documento, "1250") || strings.Contains(st.FinalText, "1250") {
			t.Fatalf("pre-condicao: os bytes designados sao o ENVELOPE da sandbox, o documento tem o numero 1250 e o resumo do modelo perde-o (forma=%s, texto=%q)", forma, st.FinalText)
		}
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": fio}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("o plano tinha de sair terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		// O QUE O CONSUMIDOR RECEBE: o texto do documento, uma vez — não o envelope, não o resumo.
		entrada := aos501Entrada(t, f)
		if entrada["content"] != documento {
			t.Fatalf("o consumidor tinha de receber o documento que a tool devolveu:\n  quero: %q\n  veio:  %q", documento, entrada["content"])
		}
		if entrada["digest"] != digestDoConteudo(documento) || entrada["from"] != "read_notes" || entrada["output"] != "conteudo" {
			t.Fatalf("a entrada leva o digest do que foi ENTREGUE e a proveniencia do contrato: %v", entrada)
		}
		if entrada["content"] == *st.Output || entrada["digest"] == st.OutputSource.Digest {
			t.Fatal("entregou-se o envelope inteiro (o documento duas vezes), e nao so o texto")
		}
		aos501FioDoPost(t, "post-runs-summarize", f.crus[aos501Run+"~summarize"])

		// O EVENTO: o digest publicado é o do entregue; a âncora e a forma da extracção ficam lá.
		eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
		publicados := aos501Eventos(eventos, plannerevents.EventPayloadPublished)
		if len(publicados) != 1 {
			t.Fatalf("queria um plan.payload_published: %+v", publicados)
		}
		var pub plannerevents.PayloadPublishedPayload
		if err := json.Unmarshal([]byte(publicados[0].Payload), &pub); err != nil {
			t.Fatal(err)
		}
		querSource := plannerevents.PayloadSource{Kind: plannerevents.PayloadSourceToolResult, Tool: "doc_read",
			StepID: st.OutputSource.StepID, AnchorDigest: st.OutputSource.Digest, AnchorBytes: st.OutputSource.Bytes,
			Extraction: plannerevents.PayloadExtractionSandboxStdoutText}
		if pub.Source == nil || *pub.Source != querSource {
			t.Fatalf("source do evento = %+v; quero %+v", pub.Source, querSource)
		}
		if pub.Record.Digest != digestDoConteudo(documento) || pub.Record.Stream != aos501Run+"~read_notes" || pub.Taint != plan.TaintUntrusted {
			t.Fatalf("o evento publica o digest do ENTREGUE, o run filho e o taint untrusted: %+v", pub)
		}
		if pub.Record.Digest == pub.Source.AnchorDigest {
			t.Fatal("o digest publicado e o do envelope, e nao o do que foi entregue")
		}
		// A ordem no log: a declaração antes da publicação.
		iDecl, iPub := -1, -1
		for i, e := range eventos {
			switch e.Tipo {
			case plannerevents.EventOutputSourceDeclared:
				iDecl = i
			case plannerevents.EventPayloadPublished:
				iPub = i
			}
		}
		if iDecl < 0 || iDecl > iPub {
			t.Fatalf("o facto da declaracao tem de estar no log antes da publicacao (decl=%d pub=%d)", iDecl, iPub)
		}
		// O TEXTO FINAL DO PRODUTOR não aparece em lado nenhum; nem o documento fora dos `inputs`.
		aos501NuncaAparece(t, "o texto final do produtor", strings.TrimSpace(st.FinalText), d, f, eventos)
		for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": aos501MetricasSemRelogio(d.metricas), "detalhe": d.detalhe} {
			for _, proibido := range []string{"1250", "tarefa 12", st.OutputSource.Digest, pub.Record.Digest} {
				if levaProibido(texto, proibido) {
					t.Fatalf("o %s leva %q — conteudo do titular, ou um digest dele:\n%s", onde, proibido, texto)
				}
			}
		}
		for _, e := range eventos {
			if strings.Contains(e.Payload, "1250") {
				t.Fatalf("o evento %s leva conteudo do documento:\n%s", e.Tipo, e.Payload)
			}
		}
		for _, quer := range []string{
			fmt.Sprintf("execucao: payload read_notes/conteudo publicado (record) POR REFERENCIA: extraccao=sandbox_stdout_text bytes_da_ancora=%d bytes_entregues=%d\n", st.OutputSource.Bytes, len(documento)),
			"execucao: read_notes=complete summarize=complete",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
		}
		for chave, valor := range map[string]int{
			serie(metricaEntregaPorReferencia, "resultado", resultadoEntregue):                   1,
			serie(metricaEntregaExtraccao, "extraccao", "sandbox_stdout_text"):                   1,
			serie(metricaDesfechos, "classe", "terminal", "codigo", fmt.Sprint(exitOK)):          1,
			serie(metricaNosPorContrato, "classe", classeComContratoSaidaAberta):                 1,
			serie(metricaDesfechos, "classe", "terminal", "codigo", fmt.Sprint(exitNosFalhados)): 0,
		} {
			if valor > 0 && !temSerie(d.metricas, chave, valor) {
				t.Fatalf("faltou nas metricas %s %d:\n%s", chave, valor, d.metricas)
			}
			if valor == 0 && strings.Contains(d.metricas, chave+" ") {
				t.Fatalf("as metricas tem %s e nao deviam:\n%s", chave, d.metricas)
			}
		}
		// Em `on` não se mede por estrutura: as séries do AOS-499 não aparecem.
		if strings.Contains(d.metricas, metricaNosPorEstrutura) || strings.Contains(d.stdout, "ORIGEM MEDIDA") {
			t.Fatalf("em on nao ha medicao por estrutura:\n%s", d.metricas)
		}
	})

	// (3) O RESULTADO CRU: uma tool que não corre na sandbox. Entrega-se tal como veio, e o digest
	// publicado é o da âncora.
	t.Run("On_Cru_EntregaSeTalComoATollODevolveu", func(t *testing.T) {
		const cru = "linha 1 do registo 4471\nlinha 2\n"
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{
			"read_notes": aos501Concluido(aos501Ancora("binding", "designated", cru), aos501Servido(t, cru))}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("o plano tinha de sair terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		entrada := aos501Entrada(t, f)
		if entrada["content"] != cru || entrada["digest"] != digestDoConteudo(cru) {
			t.Fatalf("o consumidor recebe o resultado cru, byte a byte: %v", entrada)
		}
		eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
		var pub plannerevents.PayloadPublishedPayload
		if evs := aos501Eventos(eventos, plannerevents.EventPayloadPublished); len(evs) != 1 || json.Unmarshal([]byte(evs[0].Payload), &pub) != nil {
			t.Fatalf("queria um plan.payload_published legivel: %+v", evs)
		}
		if pub.Source == nil || pub.Source.Extraction != plannerevents.PayloadExtractionRaw || pub.Record.Digest != pub.Source.AnchorDigest || pub.Source.AnchorBytes != len(cru) {
			t.Fatalf("com o resultado cru, a extraccao e raw e o digest publicado e o da ancora: %+v", pub)
		}
		aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d, f, eventos)
		if !temSerie(d.metricas, serie(metricaEntregaExtraccao, "extraccao", "raw"), 1) {
			t.Fatalf("faltou a serie da extraccao raw:\n%s", d.metricas)
		}
	})

	// (3-bis) NUM NÓ POR REFERÊNCIA, A REGRA DA SAÍDA VAZIA AVALIA O QUE SE ENTREGA, E NÃO O TEXTO.
	// O modelo do produtor concluiu sem escrever nada; a tool devolveu o documento. O nó conclui e
	// o consumidor recebe o documento — em `off`, o mesmo run fechava o nó com `saida_vazia`.
	t.Run("On_TextoFinalVazio_ASaidaEOResultadoDaTool", func(t *testing.T) {
		const cru = "linha unica do registo 77\n"
		resposta := []byte(`{"run_id":"` + aos501Run + `~read_notes","status":"completed","terminated":true,"final_text":"","turns":2,` +
			aos501Ancora("binding", "designated", cru) + "," + aos501Servido(t, cru) + "}")
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": resposta}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("com o texto final vazio e a origem designada o plano conclui; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
		}
		if entrada := aos501Entrada(t, f); entrada["content"] != cru {
			t.Fatalf("o consumidor recebe o resultado da tool: %v", entrada)
		}
	})

	// (4) CADA FALHA TEM CAUSA PRÓPRIA, E O CONSUMIDOR NÃO CORRE. O run do produtor CONCLUIU com o
	// texto final do modelo em todos os casos: é exactamente quando seria fácil cair para ele.
	envelopeBom, documento := aos499Envelope(t, "notas")
	envelopeFalhou, _ := aos499Envelope(t, "saida-3")
	envelopeVazio, _ := aos499Envelope(t, "vazio")
	envelopeBinario, _ := aos499Envelope(t, "binario")
	if documento == "" {
		t.Fatal("pre-condicao: o envelope de fio transporta um documento")
	}
	semRun := `{"run_id":"` + aos501Run + `~read_notes","status":"failed","error":"origem","turns":1,"outcome_reason":`
	type falha struct {
		resposta []byte
		causa    string
	}
	falhas := map[string]falha{
		"origem-em-falta":       {aos501Concluido(aos501Ancora("binding", "missing", "")), causaOrigemEmFalta},
		"origem-ambigua":        {aos501Concluido(aos501Ancora("binding", "ambiguous", "")), causaOrigemAmbigua},
		"origem-inaplicavel":    {aos501Concluido(aos501Ancora("binding", "inapplicable", "")), causaOrigemInaplicavel},
		"enforce-em-falta":      {[]byte(semRun + `"output_source_missing",` + aos501Ancora("binding", "missing", "") + "}"), "output_source_missing"},
		"enforce-ambigua":       {[]byte(semRun + `"output_source_ambiguous",` + aos501Ancora("binding", "ambiguous", "") + "}"), "output_source_ambiguous"},
		"bytes-nao-conferem":    {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), aos501Servido(t, strings.Replace(envelopeBom, "1250", "9999", 1))), causaOrigemNaoConfere},
		"tamanho-nao-confere":   {aos501Concluido(strings.Replace(aos501Ancora("binding", "designated", envelopeBom), fmt.Sprintf(`"bytes":%d`, len(envelopeBom)), `"bytes":7`, 1), aos501Servido(t, envelopeBom)), causaOrigemNaoConfere},
		"tool-falhou":           {aos501Concluido(aos501Ancora("binding", "designated", envelopeFalhou), aos501Servido(t, envelopeFalhou)), causaOrigemToolFalhou},
		"texto-vazio":           {aos501Concluido(aos501Ancora("binding", "designated", envelopeVazio), aos501Servido(t, envelopeVazio)), causaOrigemVazia},
		"cru-so-espacos":        {aos501Concluido(aos501Ancora("binding", "designated", " \n\t "), aos501Servido(t, " \n\t ")), causaOrigemVazia},
		"stdout-binario":        {aos501Concluido(aos501Ancora("binding", "designated", envelopeBinario), aos501Servido(t, envelopeBinario)), causaOrigemNaoTransportavel},
		"acima-do-tecto":        {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), `"output_omitted":"too_large"`), causaOrigemNaoTransportavel},
		"nao-e-texto":           {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), `"output_omitted":"not_utf8"`), causaOrigemNaoTransportavel},
		"custodia-de-vez":       {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), `"output_omitted":"unavailable"`, `"output_unavailable":true`), causaOrigemIndisponivel},
		"bytes-sem-marca":       {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom)), causaOrigemIndisponivel},
		"marca-desconhecida":    {aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), `"output_omitted":"qualquer coisa\nforjada"`), causaOrigemIndisponivel},
		"vinculo-so-medicao":    {aos501Concluido(aos501Ancora("measure", "designated", envelopeBom), aos501Servido(t, envelopeBom)), causaOrigemSemVinculo},
		"ancora-de-outra-tool":  {aos501Concluido(strings.Replace(aos501Ancora("binding", "designated", envelopeBom), `"tool":"doc_read"`, `"tool":"web_post"`, 1), aos501Servido(t, envelopeBom)), causaOrigemSemVinculo},
		"ancora-mal-formada":    {aos501Concluido(`"output_source":{"tool":"doc_read","binding":"binding","state":"Designated","step_id":"zzz","digest":"nada","bytes":-4}`, aos501Servido(t, envelopeBom)), causaOrigemSemVinculo},
		"sem-ancora-com-output": {aos501Concluido(aos501Servido(t, envelopeBom)), causaOrigemSemVinculo},
	}
	// E as duas que o NÓ REAL, com a tool na sandbox, respondeu ao corpo do teste (1): a leitura de
	// um documento que não existe (a tool corre e falha) e a de um documento vazio.
	falhas["fio-do-no-tool-falhou"] = falha{aos501FioDoNo(t, "sandbox-binding-tool-falhou"), causaOrigemToolFalhou}
	falhas["fio-do-no-documento-vazio"] = falha{aos501FioDoNo(t, "sandbox-binding-vazio"), causaOrigemVazia}
	// SEM QUEDA PARA O TEXTO, COM OS BYTES PRESENTES (revisão adversarial de 2026-10-06, I3). O
	// run NÃO CONCLUIU — e a resposta traz a âncora `designated` e os bytes, que conferem. É o caso
	// mais directo do critério, e não tinha teste: os de cima trazem todos âncora `missing` e sem
	// `output`. Nunca se entrega; a causa é a do run, `run_nao_concluido`.
	designadaComBytes := aos501Ancora("binding", "designated", envelopeBom) + "," + aos501Servido(t, envelopeBom)
	falhas["run-failed-com-ancora-designada-e-bytes"] = falha{[]byte(`{"run_id":"` + aos501Run + `~read_notes","status":"failed","error":"falhou a meio","final_text":"` + aos501TextoFinal + `","turns":2,` + designadaComBytes + "}"), causaRunNaoConcluido}
	falhas["run-completed-sem-terminated-com-bytes"] = falha{[]byte(`{"run_id":"` + aos501Run + `~read_notes","status":"completed","final_text":"` + aos501TextoFinal + `","turns":2,` + designadaComBytes + "}"), causaRunNaoConcluido}
	falhas["run-timed-out-com-ancora-designada-e-bytes"] = falha{[]byte(`{"run_id":"` + aos501Run + `~read_notes","status":"timed_out","turns":2,` + designadaComBytes + "}"), causaRunNaoConcluido}
	// A RESPOSTA É SOBRE O RUN PEDIDO (M4): a âncora e os bytes conferem entre si, e o `run_id`
	// da resposta é de outro run.
	falhas["resposta-de-outro-run"] = falha{[]byte(`{"run_id":"OUTRO-RUN~x","status":"completed","terminated":true,"final_text":"` + aos501TextoFinal + `","turns":2,` + designadaComBytes + "}"), causaOrigemSemVinculo}
	falhas["resposta-sem-run-id"] = falha{[]byte(`{"status":"completed","terminated":true,"final_text":"` + aos501TextoFinal + `","turns":2,` + designadaComBytes + "}"), causaOrigemSemVinculo}
	// O PASSO QUE A ÂNCORA ACEITA E O EVENTO RECUSA (M3): fecha o nó com causa. Abortava o `serve`
	// com um erro não classificado (transitório/1), em todas as gerações.
	for nome, passo := range map[string]string{"passo-de-200-bytes": strings.Repeat("a", 200) + "-tool-1", "passo-com-espaco": "passo com espaco-tool-1"} {
		falhas[nome] = falha{aos501Concluido(strings.Replace(aos501Ancora("binding", "designated", envelopeBom), "step-000001-tool-1", passo, 1), aos501Servido(t, envelopeBom)), causaOrigemSemVinculo}
	}
	for nome, c := range falhas {
		t.Run("On_Falha/"+nome, func(t *testing.T) {
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": c.resposta}}
			d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
			if d.classe != "terminal" || d.codigo != exitNosFalhados {
				t.Fatalf("o plano tinha de sair terminal/13; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			// Duas causas, uma por nó: a do produtor, e a do consumidor que não chegou a correr.
			if !strings.HasSuffix(d.detalhe, " erro=nos_falhados causa="+causaEntradaPorCumprir+":1,"+c.causa+":1") {
				t.Fatalf("o detail tinha de acabar com a causa do produtor (%s:1) e a do consumidor que nao correu; veio %q", c.causa, d.detalhe)
			}
			// O CONSUMIDOR NÃO CORRE, e nada se publica.
			if f.corpo(aos501Run, "summarize") != nil {
				t.Fatalf("o consumidor correu com o produtor falhado por %s: %v", c.causa, f.corpo(aos501Run, "summarize"))
			}
			eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
			if evs := aos501Eventos(eventos, plannerevents.EventPayloadPublished); len(evs) != 0 {
				t.Fatalf("publicou-se um payload com o produtor falhado por %s: %+v", c.causa, evs)
			}
			// O aviso fixo da conclusão por cumprir é das causas dessa classe; um run que não concluiu
			// sem razão de veredicto (`run_nao_concluido`) sai 13 sem ele, como sempre saiu.
			aviso := "aviso: run=" + aos501Run + " geracao=1 classe=terminal codigo=13\n"
			if causaDaConclusao(c.causa) {
				aviso = "aviso: run=" + aos501Run + " geracao=1 classe=terminal codigo=13 causa=" + causaConclusaoNaoCumprida + "\n"
			}
			for _, quer := range []string{
				"execucao: no read_notes failed (run " + aos501Run + "~read_notes) causa=" + c.causa + " ",
				"execucao: no summarize NAO corre — o contrato read_notes/conteudo ficou por cumprir\n",
				"execucao: read_notes=failed summarize=failed",
				aviso,
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			// A CAUSA NÃO LEVA CONTEÚDO, e o texto final não aparece em lado nenhum.
			aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d, f, eventos)
			aos501NuncaAparece(t, "o texto final do produtor (o resumo do fio do no)", "Resumo das notas", d, f, eventos)
			for _, proibido := range []string{documento, "1250", "9999", "forjada", "zzz", "web_post", "cat: notes", digestDoConteudo(envelopeBom),
				"OUTRO-RUN", "falhou a meio", "passo com espaco", strings.Repeat("a", 40)} {
				aos501NuncaAparece(t, "conteudo ou um valor vindo do no ("+proibido+")", proibido, d, f, eventos)
			}
			if strings.HasPrefix(c.causa, "origem_") && !temSerie(d.metricas, serie(metricaEntregaPorReferencia, "resultado", c.causa), 1) {
				t.Fatalf("faltou nas metricas a causa %s:\n%s", c.causa, d.metricas)
			}
			// E NUNCA `entregue`, nem uma extracção: em nenhum destes casos se entregou nada.
			if strings.Contains(d.metricas, serie(metricaEntregaPorReferencia, "resultado", resultadoEntregue)+" ") || strings.Contains(d.metricas, metricaEntregaExtraccao+"{") {
				t.Fatalf("as metricas dizem que se entregou num no que fechou failed por %s:\n%s", c.causa, d.metricas)
			}
			if strings.Contains(d.stdout, "POR REFERENCIA") {
				t.Fatalf("o log diz que se publicou por referencia com o produtor falhado por %s:\n%s", c.causa, d.stdout)
			}
		})
	}

	// (4-bis) O FACTO FICA NO LOG ANTES DO PEDIDO AO NÓ (revisão adversarial, M2). O `POST /runs` do
	// produtor falha — o nó responde 500 — e o `serve` aborta. O facto da declaração JÁ ESTÁ no
	// log do plano, sem que run nenhum tenha sido aceite: quem recolher este nó, se ele vier a
	// existir, sabe com que vínculo foi pedido. A ordem contrária — pedir primeiro e registar
	// depois — deixava um run vinculativo sem facto, e este teste falhava.
	t.Run("On_OFactoFicaNoLogAntesDoPedidoAoNo", func(t *testing.T) {
		f := &aos495No{catalogo: p.catalogo, falhaNoPost: map[string]int{"read_notes": 1 << 30}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if f.submissoes == 0 || f.corpo(aos501Run, "read_notes") != nil {
			t.Fatalf("pre-condicao: o POST /runs do produtor foi tentado e recusado pelo no (submissoes=%d, aceite=%v)\n%s", f.submissoes, f.corpo(aos501Run, "read_notes") != nil, d.stdout)
		}
		if d.codigo == exitOK || d.codigo == exitNosFalhados {
			t.Fatalf("com o POST recusado o serve nao conclui nem fecha o no; saiu %s/%d %q", d.classe, d.codigo, d.detalhe)
		}
		eventos := aos499EventosDoPlano(t, d.wal, aos501Run)
		declarados := aos501Eventos(eventos, plannerevents.EventOutputSourceDeclared)
		if len(declarados) != 1 || declarados[0].Passo != "planstep:output_source_declared:read_notes" {
			t.Fatalf("o facto plan.output_source_declared tinha de estar no log ANTES de o no aceitar o run; o log tem %+v\n%s", declarados, d.stdout)
		}
		var facto plannerevents.OutputSourceDeclaredPayload
		if err := json.Unmarshal([]byte(declarados[0].Payload), &facto); err != nil || facto.Binding != plannerevents.OutputSourceBindingBinds || facto.Tool != "doc_read" || facto.ContractDigest == "" {
			t.Fatalf("o facto diz o vinculo binding, a tool e o digest do contrato: %+v (%v)", facto, err)
		}
		if len(aos501Eventos(eventos, plannerevents.EventPayloadPublished)) != 0 || f.corpo(aos501Run, "summarize") != nil {
			t.Fatal("sem run do produtor nada se publica e o consumidor nao corre")
		}
	})

	// (5) A INDISPONIBILIDADE TRANSITÓRIA NÃO FECHA O NÓ. Com o vínculo vinculativo o nó responde
	// 503 enquanto os bytes não se lêem; o `serve` volta a ler, e entrega quando houver o quê.
	t.Run("On_503NaoFechaONoDoPlano", func(t *testing.T) {
		f := &aos495No{catalogo: p.catalogo, ilegivelAntes: map[string]int{"read_notes": 3}, respostas: map[string][]byte{
			"read_notes": aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), aos501Servido(t, envelopeBom))}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "terminal" || d.codigo != exitOK || f.estados503 != 3 {
			t.Fatalf("depois de tres 503 o plano tinha de concluir e entregar; saiu %s/%d %q (503=%d)\n%s", d.classe, d.codigo, d.detalhe, f.estados503, d.stdout)
		}
		if entrada := aos501Entrada(t, f); entrada["content"] != documento {
			t.Fatalf("o consumidor recebe o documento depois de o no voltar a servir: %v", entrada)
		}
	})

	// (6) EM `on`, UM PLANO SEM `from_tool` CORRE EXACTAMENTE COMO EM `off`: os mesmos eventos, os
	// mesmos corpos de `POST /runs`, o mesmo desfecho. Também contra um nó que responde com os
	// campos da origem (que ninguém pediu).
	t.Run("On_PlanoSemOrigemCorreComoEmOff", func(t *testing.T) {
		for nome, resposta := range map[string][]byte{
			"resposta-de-sempre":            nil,
			"resposta-com-campos-da-origem": aos501Concluido(aos501Ancora("binding", "designated", envelopeBom), aos501Servido(t, envelopeBom)),
			"texto-vazio":                   []byte(`{"run_id":"` + aos501Run + `~read_notes","status":"completed","terminated":true,"final_text":"","turns":2}`),
		} {
			correr := func(modo string) (aos499Drenagem, []aos499Evento, *aos495No) {
				f := &aos495No{catalogo: p.catalogo}
				if resposta != nil {
					f.respostas = map[string][]byte{"read_notes": resposta}
				}
				d := aos499Consumir(t, bin, f, aos501Run, p.plano, p.snapshot, modo)
				return d, aos499EventosDoPlano(t, d.wal, aos501Run), f
			}
			off, eventosOff, fOff := correr("off")
			on, eventosOn, fOn := correr("on")
			if on.codigo != off.codigo || on.classe != off.classe {
				t.Fatalf("%s: o desfecho mudou em on: %s/%d, e em off era %s/%d", nome, on.classe, on.codigo, off.classe, off.codigo)
			}
			// A ÚNICA DIFERENÇA NOS EVENTOS É A PROVENIÊNCIA DO PROMPT. Em `on` (com a entrega activa) o
			// planeador recebe o prompt 1.5.0, e o `planner_meta.prompt_version` do documento — e
			// portanto o `plan_hash` — dizem-no. Igualam-se esses dois valores e exige-se o resto
			// byte a byte.
			hashDe := func(eventos []aos499Evento) string {
				for _, e := range aos501Eventos(eventos, plannerevents.EventProposed) {
					var prop struct {
						PlanHash string `json:"plan_hash"`
					}
					if json.Unmarshal([]byte(e.Payload), &prop) == nil {
						return prop.PlanHash
					}
				}
				t.Fatalf("%s: o log nao tem plan.proposed", nome)
				return ""
			}
			hOn, hOff := hashDe(eventosOn), hashDe(eventosOff)
			if hOn == hOff || hOn == "" {
				t.Fatalf("%s: pre-condicao: o plano decomposto sob o prompt 1.5.0 tem outro plan_hash (on=%s off=%s)", nome, hOn, hOff)
			}
			for i := range eventosOn {
				eventosOn[i].Payload = strings.ReplaceAll(eventosOn[i].Payload, hOn, hOff)
				eventosOn[i].Payload = strings.ReplaceAll(eventosOn[i].Payload, `"prompt_version":"1.5.0"`, `"prompt_version":"1.4.0"`)
			}
			if len(eventosOn) != len(eventosOff) {
				t.Fatalf("%s: em on o plano deixou %d eventos, e em off %d", nome, len(eventosOn), len(eventosOff))
			}
			for i := range eventosOn {
				if eventosOn[i] != eventosOff[i] {
					t.Fatalf("%s: o evento %d do plano mudou em on:\n  on:  %+v\n  off: %+v", nome, i, eventosOn[i], eventosOff[i])
				}
			}
			for _, no := range []string{"read_notes", "summarize"} {
				if !bytes.Equal(fOn.crus[aos501Run+"~"+no], fOff.crus[aos501Run+"~"+no]) {
					t.Fatalf("%s: o POST /runs de %s mudou em on:\n  on:  %s\n  off: %s", nome, no, fOn.crus[aos501Run+"~"+no], fOff.crus[aos501Run+"~"+no])
				}
			}
			if _, _, presente := origemEnviada(fOn.corpo(aos501Run, "read_notes")); presente {
				t.Fatalf("%s: em on, um no cujo plano nao declara a origem nao a declara ao no aos", nome)
			}
			if len(aos501Eventos(eventosOn, plannerevents.EventOutputSourceDeclared)) != 0 {
				t.Fatalf("%s: um plano sem origem nao deixa factos de declaracao no log", nome)
			}
			// O LOG DO EXECUTOR — as linhas `execucao:`, que dizem o que cada nó fez — é o mesmo, linha
			// a linha. (O resto do log da drenagem leva pastas temporárias e durações.) O que muda é
			// só o que se DIZ no arranque: o banner da saída por referência e o prompt do planeador.
			doExecutor := func(stdout string) string {
				var linhas []string
				for _, l := range strings.Split(stdout, "\n") {
					if strings.HasPrefix(strings.TrimSpace(l), "execucao:") {
						linhas = append(linhas, l)
					}
				}
				return strings.Join(linhas, "\n")
			}
			if a, b := doExecutor(on.stdout), doExecutor(off.stdout); a != b || a == "" {
				t.Fatalf("%s: o log do executor mudou em on:\n--- on\n%s\n--- off\n%s", nome, a, b)
			}
			if !strings.Contains(on.stdout, "saida por referencia (AOS-501): modo on, ENTREGA ACTIVA") || !strings.Contains(off.stdout, "saida por referencia (AOS-499): modo off") {
				t.Fatalf("%s: cada modo diz o seu banner:\n%s", nome, on.stdout)
			}
			// AS MÉTRICAS: em `on` há UMA série a mais — a omissão do planeador (um candidato por
			// estrutura cujo plano não declarou a origem). Tirando-a, e a duração, são as mesmas.
			if !temSerie(on.metricas, metricaCandidatosSemOrigem, 1) || strings.Contains(off.metricas, metricaCandidatosSemOrigem) {
				t.Fatalf("%s: em on conta-se o candidato cujo plano nao declarou a origem, e em off nao:\n%s", nome, on.metricas)
			}
			semVolatil := func(metricas string) string {
				var linhas []string
				for _, l := range strings.Split(metricas, "\n") {
					if strings.Contains(l, metricaCandidatosSemOrigem) || strings.Contains(l, metricaDuracao) || strings.Contains(l, metricaUltimaDrenagem) {
						continue
					}
					linhas = append(linhas, l)
				}
				return strings.Join(linhas, "\n")
			}
			if a, b := semVolatil(on.metricas), semVolatil(off.metricas); a != b {
				t.Fatalf("%s: as metricas mudaram em on para alem da serie da omissao:\n--- on\n%s\n--- off\n%s", nome, a, b)
			}
		}
	})

	// (7) `on` CONTRA UM NÓ QUE NÃO ANUNCIA O VÍNCULO VINCULATIVO: o plano com origem não corre,
	// e nunca cai para o texto do modelo. No `--goal` o planeador insiste e o plano é recusado; no
	// `--plan-doc` a recusa é antes da posse, com causa própria. Um plano sem origem corre.
	for nome, catalogo := range map[string][]byte{
		"no-anterior-sem-anuncio":   bytes.Replace(p.catalogo, []byte(`,"output_source":{"bindings":["measure","binding"],"max_bytes":131072}`), nil, 1),
		"no-que-so-anuncia-measure": bytes.Replace(p.catalogo, []byte(`"bindings":["measure","binding"]`), []byte(`"bindings":["measure"]`), 1),
	} {
		t.Run("On_NoSemBinding/"+nome, func(t *testing.T) {
			if bytes.Contains(catalogo, []byte(`"binding"]`)) || bytes.Equal(catalogo, p.catalogo) {
				t.Fatal("pre-condicao: este catalogo nao anuncia o vinculo binding")
			}
			f := &aos495No{catalogo: catalogo}
			d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
			if d.classe != "terminal" || d.codigo != exitPlanoRecusado || f.submissoes != 0 {
				t.Fatalf("o planeador que insiste na origem contra um no sem binding acaba recusado (9), sem submeter nada; saiu %s/%d %q (%d submissoes)\n%s", d.classe, d.codigo, d.detalhe, f.submissoes, d.stdout)
			}
			if !strings.Contains(d.stdout, "saida por referencia (AOS-501): modo on, ENTREGA NAO ACTIVA") || strings.Contains(d.stdout, "planeador: prompt 1.5.0") {
				t.Fatalf("o banner diz que a entrega nao esta activa, e o planeador fica com o prompt corrente:\n%s", d.stdout)
			}
			// `--plan-doc`: antes da posse, com causa própria, sem abrir o Event Store.
			srv := f.servidor(t)
			dir := t.TempDir()
			cred, snap, doc, wal := filepath.Join(dir, "nhi.jwt"), filepath.Join(dir, "snap.json"), filepath.Join(dir, "plano.json"), filepath.Join(dir, "serve.wal")
			escrever(t, cred, "nhi-do-operador")
			escrever(t, snap, p.snapshot)
			escrever(t, doc, comOrigem)
			env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE=", "AOS_ORQ_SAIDA_POR_REFERENCIA=on"}
			r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "plan-aos501-doc", "--plan-doc", doc, "--snapshot", snap)
			if r.code != exitDocumentoRecusado || !strings.Contains(r.stderr, "nao anuncia o vinculo vinculativo") || strings.Contains(r.stdout, "posse:") {
				t.Fatalf("o --plan-doc com origem tinha de sair 10 antes da posse; saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("o Event Store foi aberto antes da recusa (stat: %v)", err)
			}
			if f.submissoes != 0 {
				t.Fatalf("um plano recusado submeteu runs ao no: %d", f.submissoes)
			}
			if tipoDoErro(errNoSemEntregaPorReferencia) != "no_sem_saida_por_referencia" || codigoDe(errNoSemEntregaPorReferencia) != exitDocumentoRecusado {
				t.Fatal("a recusa tem causa propria (no_sem_saida_por_referencia) e a saida 10")
			}
			// E um plano SEM origem corre contra este nó, em `on`, como sempre.
			f2 := &aos495No{catalogo: catalogo}
			d2 := aos499Consumir(t, bin, f2, aos501Run, p.plano, p.snapshot, "on")
			if d2.classe != "terminal" || d2.codigo != exitOK || f2.corpo(aos501Run, "summarize") == nil {
				t.Fatalf("um plano sem origem corre em on contra um no sem binding; saiu %s/%d %q", d2.classe, d2.codigo, d2.detalhe)
			}
		})
	}

	// (7-bis) UM ANÚNCIO QUE NÃO SE LEU NÃO É UM «NÃO», também em `on`: o `serve` pára antes da
	// posse, o desfecho é transitório, o pedido volta à fila e nada se submete. Tratá-lo como
	// «o nó não anuncia `binding`» fechava como terminal um plano com origem por uma leitura
	// falhada.
	t.Run("On_AnuncioIlegivel_PedidoVoltaAFila", func(t *testing.T) {
		f := &aos495No{catalogo: p.catalogo, toolsNaLeitura: func(n int) (int, []byte) {
			if n >= 3 {
				return 503, []byte(`{"error":"indisponivel"}`)
			}
			return 0, nil
		}}
		d := aos499Consumir(t, bin, f, aos501Run, comOrigem, p.snapshot, "on")
		if d.classe != "transitorio" || d.codigo != exitErro || !strings.HasSuffix(d.detalhe, " erro=anuncio_ilegivel") || f.submissoes != 0 {
			t.Fatalf("um anuncio que nao se leu da transitorio/1 e nenhuma submissao; veio %s/%d %q, %d submissao(oes)\n%s", d.classe, d.codigo, d.detalhe, f.submissoes, d.stdout)
		}
		if strings.Contains(d.stdout, "saida por referencia (AOS-501)") {
			t.Fatalf("com o anuncio por ler nao ha banner da entrega:\n%s", d.stdout)
		}
	})

	// (8) FORA DE `on` A GUARDA DA LINHA 1.3.0 FICA, nos dois modos e sem a variável. (Os outros
	// caminhos da guarda estão em aos500_origem_no_plano_test.go, que corre sem a variável.)
	for _, modo := range []string{"", "off", "observe"} {
		t.Run("ForaDeOn_APlanoComOrigemNaoCorre/modo="+modo, func(t *testing.T) {
			f := &aos495No{catalogo: p.catalogo}
			srv := f.servidor(t)
			dir := t.TempDir()
			cred, snap, doc, wal := filepath.Join(dir, "nhi.jwt"), filepath.Join(dir, "snap.json"), filepath.Join(dir, "plano.json"), filepath.Join(dir, "serve.wal")
			escrever(t, cred, "nhi-do-operador")
			escrever(t, snap, p.snapshot)
			escrever(t, doc, comOrigem)
			env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
			if modo != "" {
				env = append(env, "AOS_ORQ_SAIDA_POR_REFERENCIA="+modo)
			}
			r := correrComEnv(t, env, bin, "serve", "--wal", wal, "--run", "plan-aos501-guarda", "--plan-doc", doc, "--snapshot", snap)
			if r.code != exitDocumentoRecusado || !strings.Contains(r.stderr, "NAO CORRE: este binario ainda nao entrega por referencia") {
				t.Fatalf("modo %q: um plano com origem tinha de sair 10 com a recusa do AOS-500; saiu %d\n%s", modo, r.code, r.stderr)
			}
			if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) || f.submissoes != 0 {
				t.Fatalf("modo %q: a recusa e antes da posse e de qualquer submissao (stat: %v, submissoes: %d)", modo, err, f.submissoes)
			}
			// Pelo `consume` (o planeador insiste): recusado em todas as tentativas, saída 9.
			f2 := &aos495No{catalogo: p.catalogo}
			d := aos499Consumir(t, bin, f2, aos501Run, comOrigem, p.snapshot, modo)
			if d.classe != "terminal" || d.codigo != exitPlanoRecusado || f2.submissoes != 0 {
				t.Fatalf("modo %q: pelo consume o plano com origem acaba recusado (9), sem submissoes; saiu %s/%d %q", modo, d.classe, d.codigo, d.detalhe)
			}
			if strings.Contains(d.stdout, "planeador: prompt 1.5.0") || strings.Contains(d.stdout, "AOS-501") {
				t.Fatalf("modo %q: fora de on o planeador nao recebe o prompt 1.5.0 e o banner e o do AOS-499:\n%s", modo, d.stdout)
			}
		})
	}
}

// aos501Sessao é uma pasta de trabalho do `consume` que sobrevive a várias drenagens: o mesmo
// WAL, a mesma pasta de planos e o mesmo nó falso. É o que deixa testar a RETOMA.
type aos501Sessao struct {
	bin, dir, wal, snap, fix string
	env                      []string
	f                        *aos495No
}

func aos501Abrir(t *testing.T, bin string, f *aos495No, plano, snapshot string) *aos501Sessao {
	t.Helper()
	srv := f.servidor(t)
	dir := t.TempDir()
	s := &aos501Sessao{bin: bin, dir: dir, f: f, wal: filepath.Join(dir, "consume.wal"),
		snap: filepath.Join(dir, "snap.json"), fix: filepath.Join(dir, "plano.json")}
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	escrever(t, s.snap, snapshot)
	escrever(t, s.fix, plano)
	s.env = []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE="}
	return s
}

// drenar oferece o pedido na geração dada e corre UMA drenagem no modo dado. Devolve o desfecho
// reportado ao nó nessa drenagem.
func (s *aos501Sessao) drenar(t *testing.T, geracao int, modo, prazo string) aos499Drenagem {
	t.Helper()
	s.f.mu.Lock()
	antes := len(s.f.desfechos)
	s.f.ofertas = append(s.f.ofertas, pedidoReclamado{RunID: aos501Run, Objective: "ler o documento notes e resumi-lo", Geracao: geracao})
	s.f.mu.Unlock()
	r := correrComEnv(t, append(append([]string{}, s.env...), "AOS_ORQ_SAIDA_POR_REFERENCIA="+modo), s.bin, "consume", "--wal", s.wal,
		"--snapshot", s.snap, "--decompose-fixture", s.fix, "--poll-interval", "20ms", "--max", "1", "--plan-timeout", prazo)
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if len(s.f.desfechos) != antes+1 {
		t.Fatalf("queria mais um desfecho reportado ao no, vieram %d\n%s\n%s", len(s.f.desfechos)-antes, r.stdout, r.stderr)
	}
	d := s.f.desfechos[len(s.f.desfechos)-1]
	codigo, _ := d["codigo_saida"].(float64)
	classe, _ := d["classe"].(string)
	detalhe, _ := d["detalhe"].(string)
	return aos499Drenagem{wal: s.wal, aos495Desfecho: aos495Desfecho{codigo: int(codigo), classe: classe, detalhe: detalhe, stdout: r.stdout, stderr: r.stderr}}
}

// TestAOS501_RetomaComOBinarioReal: a declaração e o vínculo ficam no LOG, e é dele que outro
// `serve` decide.
func TestAOS501_RetomaComOBinarioReal(t *testing.T) {
	bin := construir(t)
	p := aos495FormaDeProducao(t, "enforce")
	comOrigem := aos501Plano(t, p)
	envelope, documento := aos499Envelope(t, "notas")
	aCorrer := []byte(`{"run_id":"` + aos501Run + `~read_notes","status":"running"}`)
	designada := aos501Concluido(aos501Ancora("binding", "designated", envelope), aos501Servido(t, envelope))

	// (1) SUBMETIDO POR UM `serve`, RECOLHIDO POR OUTRO. O primeiro submete o produtor com o
	// vínculo vinculativo e sai com o run ainda a correr (8); o segundo, noutro processo, recolhe-o
	// e entrega por referência — a partir do facto gravado, e não de memória nenhuma.
	t.Run("SubmetidoPorUmServeRecolhidoPorOutro", func(t *testing.T) {
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": aCorrer}}
		s := aos501Abrir(t, bin, f, comOrigem, p.snapshot)
		d1 := s.drenar(t, 1, "on", "1s")
		if d1.codigo != exitNosEmVoo {
			t.Fatalf("a primeira drenagem tinha de sair com o no em voo (8); saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
		}
		if evs := aos501Eventos(aos499EventosDoPlano(t, s.wal, aos501Run), plannerevents.EventOutputSourceDeclared); len(evs) != 1 {
			t.Fatalf("o facto da declaracao tinha de estar no log depois da primeira drenagem: %+v", evs)
		}
		f.mu.Lock()
		f.respostas["read_notes"] = designada
		f.mu.Unlock()
		d2 := s.drenar(t, 2, "on", "30s")
		if d2.classe != "terminal" || d2.codigo != exitOK {
			t.Fatalf("a retoma tinha de recolher o no e concluir o plano; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		if strings.Contains(d2.stdout, "declara a origem (a tool doc_read)") {
			t.Fatalf("a retoma nao volta a submeter o produtor: recolhe-o\n%s", d2.stdout)
		}
		if entrada := aos501Entrada(t, f); entrada["content"] != documento {
			t.Fatalf("o consumidor recebe o documento, entregue por referencia a partir do facto do log: %v", entrada)
		}
		aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d2, f, aos499EventosDoPlano(t, s.wal, aos501Run))
	})

	// (1-bis) O NÓ DEIXA DE ANUNCIAR `binding` COM UM PLANO COM ORIGEM EM VOO (revisão adversarial de
	// 2026-10-06, I2). É o que acontece a um nó `aos` reiniciado com `AOS_COMPLETION_VERDICT=off`,
	// ou revertido para antes do AOS-498, com o `aos-orq` em `on`. O runbook dizia que os planos em
	// voo fechavam `origem_sem_vinculo`; o código faz outra coisa, e é ESTA que o runbook
	// (`deploy/server/README.md`) passa a descrever:
	//
	//   - a geração seguinte fecha `terminal`, código 10, `erro=no_sem_saida_por_referencia`, ANTES
	//     da posse — sem recolher o run que já corria;
	//   - o documento guardado é apagado; o nó do plano fica como estava (`running`) no grafo; o
	//     run filho fica órfão no nó `aos`; nada se publica e o consumidor não corre;
	//   - é DE VEZ: com o anúncio reposto, a geração seguinte não retoma o plano.
	//
	// NÃO SE MUDA O COMPORTAMENTO: este teste prende o que o operador vê.
	for nome, catalogo := range map[string][]byte{
		"no-que-deixa-de-anunciar-a-origem": bytes.Replace(p.catalogo, []byte(`,"output_source":{"bindings":["measure","binding"],"max_bytes":131072}`), nil, 1),
		"no-que-so-anuncia-measure":         bytes.Replace(p.catalogo, []byte(`"bindings":["measure","binding"]`), []byte(`"bindings":["measure"]`), 1),
	} {
		t.Run("ONoDeixaDeAnunciarBindingComPlanoEmVoo/"+nome, func(t *testing.T) {
			if bytes.Equal(catalogo, p.catalogo) || bytes.Contains(catalogo, []byte(`"binding"]`)) {
				t.Fatal("pre-condicao: este catalogo nao anuncia o vinculo binding")
			}
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": aCorrer}}
			s := aos501Abrir(t, bin, f, comOrigem, p.snapshot)
			d1 := s.drenar(t, 1, "on", "1s")
			if d1.codigo != exitNosEmVoo || f.corpo(aos501Run, "read_notes") == nil {
				t.Fatalf("pre-condicao: a primeira drenagem submete o produtor e sai com ele em voo (8); saiu %s/%d %q", d1.classe, d1.codigo, d1.detalhe)
			}
			if guardados, _ := os.ReadDir(filepath.Join(s.dir, "planos")); len(guardados) != 1 {
				t.Fatalf("pre-condicao: o documento do plano em voo esta guardado (%d ficheiros)", len(guardados))
			}
			eventosAntes := aos499EventosDoPlano(t, s.wal, aos501Run)
			// O run do produtor CONCLUIU entretanto, com a âncora e os bytes: havia o que entregar.
			f.mu.Lock()
			f.respostas["read_notes"] = designada
			f.catalogo = catalogo
			f.mu.Unlock()
			d2 := s.drenar(t, 2, "on", "30s")
			if d2.classe != "terminal" || d2.codigo != exitDocumentoRecusado || !strings.HasSuffix(d2.detalhe, " erro=no_sem_saida_por_referencia") {
				t.Fatalf("com o no sem binding a geracao seguinte fecha terminal/10 no_sem_saida_por_referencia; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
			}
			if !strings.Contains(d2.stdout, "saida por referencia (AOS-501): modo on, ENTREGA NAO ACTIVA") {
				t.Fatalf("o banner diz que a entrega nao esta activa:\n%s", d2.stdout)
			}
			// ANTES DA POSSE: não toma o run, não recolhe o nó, não reconstrói nem publica nada.
			for _, proibido := range []string{"posse:", "execucao:", "POR REFERENCIA", causaOrigemSemVinculo} {
				if strings.Contains(d2.stdout, proibido) || strings.Contains(d2.detalhe, proibido) {
					t.Fatalf("a recusa e antes da posse e nao e origem_sem_vinculo; o log tem %q:\n%s", proibido, d2.stdout)
				}
			}
			eventosDepois := aos499EventosDoPlano(t, s.wal, aos501Run)
			if len(eventosDepois) != len(eventosAntes) {
				t.Fatalf("a geracao recusada nao escreve no log do plano: %d eventos antes, %d depois", len(eventosAntes), len(eventosDepois))
			}
			if len(aos501Eventos(eventosDepois, plannerevents.EventPayloadPublished)) != 0 || f.corpo(aos501Run, "summarize") != nil {
				t.Fatal("nada se publica e o consumidor nao corre")
			}
			// O DOCUMENTO É APAGADO, e o run filho fica no nó `aos`, sem ninguém que o recolha.
			if guardados, _ := os.ReadDir(filepath.Join(s.dir, "planos")); len(guardados) != 0 {
				t.Fatalf("o documento do plano recusado e apagado; ficaram %d", len(guardados))
			}
			if f.corpo(aos501Run, "read_notes") == nil {
				t.Fatal("pre-condicao: o run filho do produtor continua a existir no no aos (orfao)")
			}
			// É DE VEZ: com o anúncio reposto, o plano não volta.
			f.mu.Lock()
			f.catalogo = p.catalogo
			f.mu.Unlock()
			d3 := s.drenar(t, 3, "on", "30s")
			if d3.classe != "terminal" || d3.codigo == exitOK || f.corpo(aos501Run, "summarize") != nil {
				t.Fatalf("com o anuncio reposto o plano nao retoma: fica terminal, e o consumidor nao corre; saiu %s/%d %q\n%s", d3.classe, d3.codigo, d3.detalhe, d3.stdout)
			}
			t.Logf("depois de repor o anuncio: %s/%d %q", d3.classe, d3.codigo, d3.detalhe)
		})
	}

	// O PLANO DE TRÊS NÓS da reidratação: `rever` consome a saída por referência de `read_notes` e
	// só corre DEPOIS de `summarize`. Deixa o primeiro `serve` acabar com o payload publicado e um
	// consumidor ainda por despachar — e a largar a posse (sai com 8), para a retoma não ter de
	// esperar pelo TTL do lease.
	tresNos := comOrigem
	for _, troca := range [][2]string{
		{`"budget_total": {"tokens": 100, "cost_micro_usd": 100}`, `"budget_total": {"tokens": 150, "cost_micro_usd": 150}`},
		{`"consumes":[{"from":"read_notes","output":"conteudo","type":"record"}]}
  ]`, `"consumes":[{"from":"read_notes","output":"conteudo","type":"record"}]},
    {"node_id":"rever","role":"reviewer","objective":"rever o documento lido","depends_on":["summarize","read_notes"],
     "tools":[],
     "budget_estimate":{"tokens":50,"cost_micro_usd":50},
     "consumes":[{"from":"read_notes","output":"conteudo","type":"record"}]}
  ]`},
	} {
		if strings.Count(tresNos, troca[0]) != 1 {
			t.Fatalf("pre-condicao: o plano com origem tem uma vez %q", troca[0])
		}
		tresNos = strings.Replace(tresNos, troca[0], troca[1], 1)
	}
	resumoACorrer := []byte(`{"run_id":"` + aos501Run + `~summarize","status":"running"}`)
	// primeiraDrenagem publica o payload de `read_notes`, entrega-o a `summarize` (que fica a
	// correr) e sai com 8: `rever` ainda não foi despachado.
	primeiraDrenagem := func(t *testing.T) (*aos501Sessao, *aos495No) {
		t.Helper()
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": designada, "summarize": resumoACorrer}}
		s := aos501Abrir(t, bin, f, tresNos, p.snapshot)
		d1 := s.drenar(t, 1, "on", "1s")
		if d1.codigo != exitNosEmVoo || !strings.Contains(d1.stdout, "payload read_notes/conteudo publicado (record) POR REFERENCIA") {
			t.Fatalf("a primeira drenagem publica o payload e sai com um no em voo (8); saiu %s/%d %q\n%s", d1.classe, d1.codigo, d1.detalhe, d1.stdout)
		}
		if f.corpo(aos501Run, "summarize") == nil || f.corpo(aos501Run, "rever") != nil {
			t.Fatal("pre-condicao: `summarize` foi submetido e `rever` ainda nao")
		}
		f.mu.Lock()
		delete(f.respostas, "summarize")
		f.mu.Unlock()
		return s, f
	}
	entradaDe := func(t *testing.T, f *aos495No, no string) map[string]any {
		t.Helper()
		corpo := f.corpo(aos501Run, no)
		if corpo == nil {
			t.Fatalf("o no %s nao foi submetido", no)
		}
		entradas, _ := corpo["inputs"].([]any)
		if len(entradas) != 1 {
			t.Fatalf("o no %s tinha de receber uma entrada: %v", no, corpo["inputs"])
		}
		return entradas[0].(map[string]any)
	}

	// (2) O `serve` ACABA ENTRE A PUBLICAÇÃO E O DESPACHO DE UM CONSUMIDOR. O seguinte reconstrói o
	// payload do log: relê o resultado designado do run filho, confere tudo o que o evento registou
	// e refaz a extracção. O consumidor despachado pela retoma recebe o MESMO documento.
	t.Run("ReidrataOPayloadPorReferencia", func(t *testing.T) {
		s, f := primeiraDrenagem(t)
		d2 := s.drenar(t, 2, "on", "30s")
		if d2.classe != "terminal" || d2.codigo != exitOK || !strings.Contains(d2.stdout, "1 de 1 payload(s) reconstruido(s) do log") {
			t.Fatalf("a retoma tinha de reconstruir o payload e concluir; saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
		}
		antes, depois := entradaDe(t, f, "summarize"), entradaDe(t, f, "rever")
		if depois["content"] != documento || depois["digest"] != digestDoConteudo(documento) || !reflect.DeepEqual(antes["content"], depois["content"]) {
			t.Fatalf("o consumidor despachado pela retoma recebe o MESMO documento que o primeiro recebeu:\n  antes:  %v\n  depois: %v", antes, depois)
		}
		aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d2, f, aos499EventosDoPlano(t, s.wal, aos501Run))
	})

	// (3) NA REIDRATAÇÃO, O QUE NÃO SE CONFIRMA NÃO ENTRA — e o texto final nunca serve de
	// substituto. Entre as duas drenagens o nó deixou de servir os mesmos bytes designados.
	for nome, depois := range map[string][]byte{
		"so-o-texto-final":    []byte(`{"run_id":"` + aos501Run + `~read_notes","status":"completed","terminated":true,"final_text":"` + aos501TextoFinal + `","turns":2}`),
		"bytes-indisponiveis": aos501Concluido(aos501Ancora("binding", "designated", envelope), `"output_omitted":"unavailable"`, `"output_unavailable":true`),
		"outros-bytes":        aos501Concluido(aos501Ancora("binding", "designated", strings.Replace(envelope, "1250", "9999", 1)), aos501Servido(t, strings.Replace(envelope, "1250", "9999", 1))),
		"vinculo-so-medicao":  aos501Concluido(aos501Ancora("measure", "designated", envelope), aos501Servido(t, envelope)),
	} {
		t.Run("ReidratacaoQueNaoSeConfirma/"+nome, func(t *testing.T) {
			s, f := primeiraDrenagem(t)
			f.mu.Lock()
			f.respostas["read_notes"] = depois
			f.mu.Unlock()
			d2 := s.drenar(t, 2, "on", "30s")
			if d2.classe != "terminal" || d2.codigo != exitNosFalhados || !strings.HasSuffix(d2.detalhe, " causa="+causaEntradaPorCumprir+":1") {
				t.Fatalf("sem o payload reconstruido o consumidor fecha entrada_por_cumprir (13); saiu %s/%d %q\n%s", d2.classe, d2.codigo, d2.detalhe, d2.stdout)
			}
			if !strings.Contains(d2.stdout, "NAO rehidratado por referencia") || !strings.Contains(d2.stdout, "0 de 1 payload(s) reconstruido(s) do log") {
				t.Fatalf("o log diz que o payload nao voltou:\n%s", d2.stdout)
			}
			if f.corpo(aos501Run, "rever") != nil {
				t.Fatalf("o consumidor correu sem o payload confirmado: %v", f.corpo(aos501Run, "rever"))
			}
			aos501NuncaAparece(t, "o texto final do produtor", aos501TextoFinal, d2, f, aos499EventosDoPlano(t, s.wal, aos501Run))
			aos501NuncaAparece(t, "os bytes trocados", "9999", d2, f, aos499EventosDoPlano(t, s.wal, aos501Run))
		})
	}
}

// TestAOS501_ExtrairEntrega: a regra do que se entrega, sobre os envelopes que o CODIFICADOR REAL
// da sandbox escreve (`substrate/sandbox/testdata/aos499_envelope/`) e sobre resultados crus.
func TestAOS501_ExtrairEntrega(t *testing.T) {
	envelope := func(nome string) string {
		e, _ := aos499Envelope(t, nome)
		return e
	}
	_, documento := aos499Envelope(t, "notas")
	_, comArtefacto := aos499Envelope(t, "leitura-notas")
	for nome, c := range map[string]struct {
		servido, conteudo string
		forma             plannerevents.PayloadExtraction
		causa             string
	}{
		"envelope com o documento":               {envelope("notas"), documento, plannerevents.PayloadExtractionSandboxStdoutText, ""},
		"envelope com o documento e o artefacto": {envelope("leitura-notas"), comArtefacto, plannerevents.PayloadExtractionSandboxStdoutText, ""},
		"envelope de uma linha":                  {envelope("uma-linha"), "", plannerevents.PayloadExtractionSandboxStdoutText, ""},
		"envelope com exit_code 3":               {envelope("saida-3"), "", "", causaOrigemToolFalhou},
		"envelope com exit_code 1 sem stdout":    {envelope("leitura-em-falta"), "", "", causaOrigemToolFalhou},
		"envelope vazio":                         {envelope("vazio"), "", "", causaOrigemVazia},
		"envelope so com espacos":                {`{"stdout_text":" \n\t","exit_code":0}`, "", "", causaOrigemVazia},
		"envelope binario":                       {envelope("binario"), "", "", causaOrigemNaoTransportavel},
		"resultado cru":                          {"linha 1\nlinha 2\n", "linha 1\nlinha 2\n", plannerevents.PayloadExtractionRaw, ""},
		"JSON que nao e o envelope":              {`{"titulo":"acta","exit_code":0}`, `{"titulo":"acta","exit_code":0}`, plannerevents.PayloadExtractionRaw, ""},
		"cru vazio":                              {"", "", "", causaOrigemVazia},
		"cru so com espacos":                     {" \r\n\t", "", "", causaOrigemVazia},
		"texto acima do tecto":                   {strings.Repeat("a", maxPayloadBytes+1), "", "", causaOrigemNaoTransportavel},
		"texto no tecto exacto":                  {strings.Repeat("a", maxPayloadBytes), strings.Repeat("a", maxPayloadBytes), plannerevents.PayloadExtractionRaw, ""},
	} {
		conteudo, forma, causa := extrairEntrega(c.servido)
		if causa != c.causa || forma != c.forma {
			t.Errorf("%s: forma=%q causa=%q; quero forma=%q causa=%q", nome, forma, causa, c.forma, c.causa)
			continue
		}
		if c.causa != "" && conteudo != "" {
			t.Errorf("%s: com causa nao ha conteudo; veio %d bytes", nome, len(conteudo))
		}
		if c.causa == "" && c.conteudo != "" && conteudo != c.conteudo {
			t.Errorf("%s: conteudo entregue = %q; quero %q", nome, conteudo, c.conteudo)
		}
		if c.causa == "" && forma == plannerevents.PayloadExtractionSandboxStdoutText && (conteudo == c.servido || strings.Contains(conteudo, `"exit_code"`)) {
			t.Errorf("%s: de um envelope entrega-se so o texto, e veio o envelope", nome)
		}
	}
	// O envelope com o artefacto leva o documento DUAS vezes; o entregue leva-o uma.
	if e := envelope("leitura-notas"); len(e) < 2*len(comArtefacto) {
		t.Fatalf("pre-condicao: o envelope da leitura (%d bytes) leva o documento (%d bytes) em texto e outra vez em base64", len(e), len(comArtefacto))
	}
}

// aos501RunDoLeitor é o run filho do nó `ler` nos testes de unidade (o run do plano é `run-418`,
// o de [executorReidratado]).
const aos501RunDoLeitor = "run-418~ler"

// aos501NoLeitor é o nó `ler` do documento aprovado: uma tool, uma saída `conteudo`, com a origem
// dada (vazia ⇒ o contrato não declara a origem).
func aos501NoLeitor(origem string) plan.Node {
	return plan.Node{NodeID: "ler", Role: "reader", Objective: "ler o documento",
		Tools:   []plan.ToolRef{{Name: "doc_read", Version: "1", Digest: "sha256:a"}},
		Outputs: []plan.Output{{Name: "conteudo", Type: plan.PayloadRecord, FromTool: origem}}}
}

// aos501Facto é o facto `plan.output_source_declared` que o executor escreve para o nó `n`: a
// tool e o digest do contrato da saída com origem, com o vínculo dado.
func aos501Facto(n plan.Node, vinculo plannerevents.OutputSourceBinding) declaracaoDeOrigem {
	saida, _ := saidaComOrigem(n)
	return declaracaoDeOrigem{tool: saida.FromTool, vinculo: vinculo, digestDoContrato: plan.OutputDigest(n, saida)}
}

// TestAOS501_EntregaDoRun: a ordem das garantias — a resposta é sobre o run pedido, a âncora
// (vínculo, tool e passo), o estado, os bytes inteiros contra a âncora, e só então a extracção.
// Nunca devolve entrega E causa.
func TestAOS501_EntregaDoRun(t *testing.T) {
	envelope, documento := aos499Envelope(t, "notas")
	envelopeFalhou, _ := aos499Envelope(t, "saida-3")
	envelopeVazio, _ := aos499Envelope(t, "vazio")
	envelopeBinario, _ := aos499Envelope(t, "binario")
	ancora := func(mexer func(*agentruntime.OutputSource)) *agentruntime.OutputSource {
		a := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
			StepID: "step-000001-tool-1", Digest: digestDoConteudo(envelope), Bytes: len(envelope)}
		if mexer != nil {
			mexer(a)
		}
		return a
	}
	estado := func(a *agentruntime.OutputSource, servido *string, marca string) estadoDoRun {
		return estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: aos501TextoFinal, OutputSource: a, Output: servido, OutputOmitted: marca}
	}
	deOutroRun := func(id string) estadoDoRun {
		st := estado(ancora(nil), &envelope, "")
		st.RunID = id
		return st
	}
	outro := strings.Replace(envelope, "1250", "9999", 1)
	soEspacos := " \n\t "
	// O caso bom.
	ent, causa := entregaDoRun("doc_read", aos501RunDoLeitor, estado(ancora(nil), &envelope, ""))
	if causa != "" || ent.conteudo != documento || ent.extraccao != plannerevents.PayloadExtractionSandboxStdoutText ||
		ent.digestDaAncora != digestDoConteudo(envelope) || ent.bytesDaAncora != len(envelope) || ent.passo != "step-000001-tool-1" {
		t.Fatalf("a entrega de um envelope que confere e o documento, com a ancora do envelope: %+v (%q)", ent, causa)
	}
	if strings.Contains(ent.conteudo, aos501TextoFinal) {
		t.Fatal("o texto final entrou na entrega")
	}
	// PRÉ-CONDIÇÃO dos casos do passo (M3): a âncora ACEITA estes passos — o kernel não os limita
	// — e o construtor do evento de publicação recusa-os. É o desacordo que abortava o `serve`.
	passosQueOEventoRecusa := []string{strings.Repeat("a", 200) + "-tool-1", "passo com espaco-tool-1", "x\ny-tool-1"}
	for _, passo := range passosQueOEventoRecusa {
		a := ancora(func(a *agentruntime.OutputSource) { a.StepID = passo })
		if !a.BemFormada() || plannerevents.ValidSourceStepID(passo) {
			t.Fatalf("pre-condicao: o passo de %d bytes e aceite pela ancora e recusado pelo evento (BemFormada=%v, evento=%v)", len(passo), a.BemFormada(), plannerevents.ValidSourceStepID(passo))
		}
	}
	casos := map[string]struct {
		st    estadoDoRun
		causa string
	}{
		"sem ancora":               {estado(nil, &envelope, ""), causaOrigemSemVinculo},
		"vinculo so medicao":       {estado(ancora(func(a *agentruntime.OutputSource) { a.Binding = agentruntime.OutputSourceMeasure }), &envelope, ""), causaOrigemSemVinculo},
		"outra tool":               {estado(ancora(func(a *agentruntime.OutputSource) { a.Tool = "web_post" }), &envelope, ""), causaOrigemSemVinculo},
		"ancora mal formada":       {estado(ancora(func(a *agentruntime.OutputSource) { a.Digest = "nada" }), &envelope, ""), causaOrigemSemVinculo},
		"missing":                  {estado(&agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceMissing}, nil, ""), causaOrigemEmFalta},
		"ambiguous":                {estado(&agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceAmbiguous}, nil, ""), causaOrigemAmbigua},
		"inapplicable":             {estado(&agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceInapplicable}, nil, ""), causaOrigemInaplicavel},
		"missing com output":       {estado(&agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceMissing}, &envelope, ""), causaOrigemEmFalta},
		"sem bytes, too_large":     {estado(ancora(nil), nil, "too_large"), causaOrigemNaoTransportavel},
		"sem bytes, not_utf8":      {estado(ancora(nil), nil, "not_utf8"), causaOrigemNaoTransportavel},
		"sem bytes, unavailable":   {estado(ancora(nil), nil, "unavailable"), causaOrigemIndisponivel},
		"sem bytes, sem marca":     {estado(ancora(nil), nil, ""), causaOrigemIndisponivel},
		"sem bytes, marca forjada": {estado(ancora(nil), nil, "tudo bem"), causaOrigemIndisponivel},
		"outros bytes":             {estado(ancora(nil), &outro, ""), causaOrigemNaoConfere},
		"tamanho diferente":        {estado(ancora(func(a *agentruntime.OutputSource) { a.Bytes-- }), &envelope, ""), causaOrigemNaoConfere},
		"digest de outros bytes":   {estado(ancora(func(a *agentruntime.OutputSource) { a.Digest = digestDoConteudo(outro) }), &envelope, ""), causaOrigemNaoConfere},
		// CONFERIR ANTES DE EXTRAIR (revisão adversarial, M1). Os bytes servidos NÃO são os que o
		// kernel selou, e têm uma forma que a extracção recusaria por conta própria: um envelope
		// de uma execução falhada, um envelope vazio, um binário, só espaços. A causa é SEMPRE a
		// de não conferirem — extrair primeiro dava `origem_tool_falhou`, `origem_vazia` ou
		// `origem_nao_transportavel` a bytes que ninguém selou, e a série de `origem_nao_confere`,
		// a que tem de ser zero, subcontava.
		"nao conferem, e sao um envelope que falhou": {estado(ancora(nil), &envelopeFalhou, ""), causaOrigemNaoConfere},
		"nao conferem, e sao um envelope vazio":      {estado(ancora(nil), &envelopeVazio, ""), causaOrigemNaoConfere},
		"nao conferem, e sao um envelope binario":    {estado(ancora(nil), &envelopeBinario, ""), causaOrigemNaoConfere},
		"nao conferem, e sao so espacos":             {estado(ancora(nil), &soEspacos, ""), causaOrigemNaoConfere},
		// A RESPOSTA É SOBRE O RUN PEDIDO (M4). A âncora e os bytes conferem entre si — e são de
		// outro run, ou a resposta não diz de que run é.
		"resposta de outro run":           {deOutroRun("OUTRO-RUN~x"), causaOrigemSemVinculo},
		"resposta de outro no do plano":   {deOutroRun("run-418~resumir"), causaOrigemSemVinculo},
		"resposta sem run_id":             {deOutroRun(""), causaOrigemSemVinculo},
		"resposta com o run_id noutra cx": {deOutroRun(strings.ToUpper(aos501RunDoLeitor)), causaOrigemSemVinculo},
	}
	// O PASSO (M3): o que a âncora aceita e o evento recusa fecha com causa.
	for _, passo := range passosQueOEventoRecusa {
		casos[fmt.Sprintf("passo de %d bytes que o evento recusa", len(passo))] = struct {
			st    estadoDoRun
			causa string
		}{estado(ancora(func(a *agentruntime.OutputSource) { a.StepID = passo }), &envelope, ""), causaOrigemSemVinculo}
	}
	for nome, c := range casos {
		ent, causa := entregaDoRun("doc_read", aos501RunDoLeitor, c.st)
		if causa != c.causa {
			t.Errorf("%s: causa=%q; quero %q", nome, causa, c.causa)
		}
		if ent != (entregaPorReferencia{}) {
			t.Errorf("%s: com causa nao ha entrega; veio %+v", nome, ent)
		}
	}
	// NÃO-VACUIDADE do M1: cada um desses bytes, COM a âncora deles, dá a causa da extracção.
	for nome, c := range map[string]struct {
		servido string
		causa   string
	}{
		"envelope que falhou": {envelopeFalhou, causaOrigemToolFalhou},
		"envelope vazio":      {envelopeVazio, causaOrigemVazia},
		"envelope binario":    {envelopeBinario, causaOrigemNaoTransportavel},
		"so espacos":          {soEspacos, causaOrigemVazia},
	} {
		servido := c.servido
		a := ancora(func(a *agentruntime.OutputSource) { a.Digest, a.Bytes = digestDoConteudo(servido), len(servido) })
		if _, causa := entregaDoRun("doc_read", aos501RunDoLeitor, estado(a, &servido, "")); causa != c.causa {
			t.Errorf("%s, com a ancora dos proprios bytes: causa=%q; quero %q", nome, causa, c.causa)
		}
	}
	// As causas são do vocabulário fechado, e todas contam como «a conclusão não se cumpriu».
	for _, c := range causasDaOrigem {
		if !causaDaConclusao(c) || !plan.ValidIdentifier(c) {
			t.Errorf("a causa %q tem de ser um identificador e da classe da conclusao", c)
		}
	}
	if causaDoAviso(exitNosFalhados, map[string]int{causaOrigemVazia: 1}) != causaConclusaoNaoCumprida {
		t.Error("um no falhado por uma causa da origem leva o aviso da conclusao por cumprir")
	}
}

// aos501Executor compõe um executor só com o que [executorDeNos.resolverEntrega] lê: o run do
// plano, e os factos do log.
func aos501Executor(declaradas map[string]declaracaoDeOrigem) *executorDeNos {
	return &executorDeNos{runID: "run-418", declaradas: declaradas, medicao: &medicaoDoContrato{}}
}

// TestAOS501_SemOFactoVinculativoNaoSeEntrega: a prova do LOG. Um run cuja resposta é perfeita —
// âncora `binding`, designada, bytes que conferem — NÃO se entrega por referência se o log do
// plano não disser que foi pedido com o vínculo vinculativo para essa tool E PARA ESSE CONTRATO.
// É o que impede um `serve` em `on` de entregar um run que outro submeteu em «só medição».
func TestAOS501_SemOFactoVinculativoNaoSeEntrega(t *testing.T) {
	envelope, documento := aos499Envelope(t, "notas")
	a := &agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
		StepID: "step-000001-tool-1", Digest: digestDoConteudo(envelope), Bytes: len(envelope)}
	st := estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: aos501TextoFinal, OutputSource: a, Output: &envelope}
	leitor := aos501NoLeitor("doc_read")
	bom := aos501Facto(leitor, plannerevents.OutputSourceBindingBinds)
	// O mesmo nó com o contrato da saída mudado (outro tipo): o digest do contrato é outro.
	outroContrato := aos501NoLeitor("doc_read")
	outroContrato.Outputs[0].Type = plan.PayloadArtifact
	if plan.OutputDigest(outroContrato, outroContrato.Outputs[0]) == bom.digestDoContrato {
		t.Fatal("pre-condicao: mudar o tipo da saida muda o digest do contrato")
	}
	// Os dois nós que o validador recusa e que aqui não são entregáveis: duas saídas com origem,
	// e o NÓ MISTO (uma saída de texto ao lado da origem).
	duasOrigens := aos501NoLeitor("doc_read")
	duasOrigens.Outputs = append(duasOrigens.Outputs, plan.Output{Name: "copia", Type: plan.PayloadRecord, FromTool: "doc_read"})
	misto := aos501NoLeitor("doc_read")
	misto.Outputs = append(misto.Outputs, plan.Output{Name: "resumo", Type: plan.PayloadSummary})
	comMetricas := aos501NoLeitor("doc_read")
	comMetricas.Outputs = append(comMetricas.Outputs, plan.Output{Name: "medidas", Type: plan.PayloadMetrics})
	semDigest := bom
	semDigest.digestDoContrato = ""
	for nome, c := range map[string]struct {
		no         plan.Node
		declaradas map[string]declaracaoDeOrigem
		causa      string
	}{
		"facto binding":                     {leitor, map[string]declaracaoDeOrigem{"ler": bom}, ""},
		"facto binding, com metrics":        {comMetricas, map[string]declaracaoDeOrigem{"ler": aos501Facto(comMetricas, plannerevents.OutputSourceBindingBinds)}, ""},
		"facto measure":                     {leitor, map[string]declaracaoDeOrigem{"ler": aos501Facto(leitor, plannerevents.OutputSourceBindingMeasure)}, causaOrigemSemVinculo},
		"sem facto":                         {leitor, map[string]declaracaoDeOrigem{}, causaOrigemSemVinculo},
		"facto de outro no":                 {leitor, map[string]declaracaoDeOrigem{"outro": bom}, causaOrigemSemVinculo},
		"facto de outra tool":               {leitor, map[string]declaracaoDeOrigem{"ler": {tool: "web_post", vinculo: plannerevents.OutputSourceBindingBinds, digestDoContrato: bom.digestDoContrato}}, causaOrigemSemVinculo},
		"facto sem vinculo":                 {leitor, map[string]declaracaoDeOrigem{"ler": {tool: "doc_read", digestDoContrato: bom.digestDoContrato}}, causaOrigemSemVinculo},
		"facto sem o digest do contrato":    {leitor, map[string]declaracaoDeOrigem{"ler": semDigest}, causaOrigemSemVinculo},
		"facto com o digest de outro":       {leitor, map[string]declaracaoDeOrigem{"ler": aos501Facto(outroContrato, plannerevents.OutputSourceBindingBinds)}, causaOrigemSemVinculo},
		"duas saidas com origem":            {duasOrigens, map[string]declaracaoDeOrigem{"ler": bom}, causaOrigemSemVinculo},
		"no misto: origem e saida de texto": {misto, map[string]declaracaoDeOrigem{"ler": aos501Facto(misto, plannerevents.OutputSourceBindingBinds)}, causaOrigemSemVinculo},
	} {
		e := aos501Executor(c.declaradas)
		var entrega *entregaPorReferencia
		causa := e.resolverEntrega(c.no, st, &entrega)
		if causa != c.causa {
			t.Errorf("%s: causa=%q; quero %q", nome, causa, c.causa)
		}
		if (c.causa == "") != (entrega != nil) {
			t.Errorf("%s: entrega=%v com causa %q", nome, entrega != nil, causa)
		}
		if c.causa == "" && entrega.conteudo != documento {
			t.Errorf("%s: a entrega e o documento; veio %q", nome, entrega.conteudo)
		}
		// RESOLVER NÃO CONTA (M9): a métrica é do desfecho do nó, e só se escreve depois dele.
		if len(e.medicao.entregas) != 0 || len(e.medicao.extraccoes) != 0 {
			t.Errorf("%s: resolver a entrega nao conta nada; contou %v / %v", nome, e.medicao.entregas, e.medicao.extraccoes)
		}
	}
	// O MESMO RUN, PEDIDO POR OUTRO PLANO: a resposta é sobre o run de `run-418`, e este executor
	// é de outro run do plano — o `run_id` não é o que ele pediria.
	outroPlano := aos501Executor(map[string]declaracaoDeOrigem{"ler": bom})
	outroPlano.runID = "run-999"
	var entrega *entregaPorReferencia
	if causa := outroPlano.resolverEntrega(leitor, st, &entrega); causa != causaOrigemSemVinculo || entrega != nil {
		t.Errorf("a resposta de um run de outro plano nao se entrega; causa=%q entrega=%v", causa, entrega != nil)
	}
}

// TestAOS501_AMetricaContaODesfechoDoNo (revisão adversarial, M9): `entregue` só se conta num nó
// que fechou `complete` com a saída publicada. Uma entrega resolvida num nó que acaba `failed`
// conta a causa dele, e nunca `entregue`.
func TestAOS501_AMetricaContaODesfechoDoNo(t *testing.T) {
	entrega := &entregaPorReferencia{conteudo: "x", extraccao: plannerevents.PayloadExtractionRaw}
	for nome, c := range map[string]struct {
		destino   string
		causa     string
		entrega   *entregaPorReferencia
		entregas  map[string]int
		extraccao int
	}{
		"concluiu com entrega":                     {"complete", "", entrega, map[string]int{resultadoEntregue: 1}, 1},
		"falhou com a causa da origem":             {"failed", causaOrigemVazia, nil, map[string]int{causaOrigemVazia: 1}, 0},
		"entrega resolvida e o no fechou failed":   {"failed", causaSaidaVazia, entrega, map[string]int{causaSaidaVazia: 1}, 0},
		"entrega resolvida, failed e sem causa":    {"failed", "", entrega, map[string]int{}, 0},
		"concluiu sem entrega (nao e desta serie)": {"complete", "", nil, map[string]int{}, 0},
	} {
		e := aos501Executor(nil)
		e.contarEntrega(arstate.State(c.destino), c.causa, c.entrega)
		if !reflect.DeepEqual(e.medicao.entregas, c.entregas) && !(len(e.medicao.entregas) == 0 && len(c.entregas) == 0) {
			t.Errorf("%s: entregas=%v; quero %v", nome, e.medicao.entregas, c.entregas)
		}
		if e.medicao.entregas[resultadoEntregue] > 0 && c.destino != "complete" {
			t.Errorf("%s: contou `entregue` num no que nao concluiu", nome)
		}
		if got := e.medicao.extraccoes[string(plannerevents.PayloadExtractionRaw)]; got != c.extraccao {
			t.Errorf("%s: extraccoes=%d; quero %d", nome, got, c.extraccao)
		}
	}
	if arstate.Complete != "complete" || arstate.Failed != "failed" {
		t.Fatalf("pre-condicao: os estados terminais do no sao complete e failed (%q, %q)", arstate.Complete, arstate.Failed)
	}
	// Só as causas do vocabulário fechado chegam ao ficheiro de métricas: `saida_vazia` não é
	// desta série, e a contagem dela não vira uma série `entregue`.
	m := &metricasDoConsumo{series: map[string]float64{}}
	c := &medicaoDoContrato{}
	e := &executorDeNos{medicao: c}
	e.contarEntrega(arstate.Failed, causaSaidaVazia, entrega)
	m.registarEntrega(c)
	if texto := string(m.texto()); strings.Contains(texto, resultadoEntregue) {
		t.Fatalf("um no que fechou failed nao escreve a serie entregue:\n%s", texto)
	}
}

// aos501Cliente é o nó visto pela reidratação por referência: devolve o estado que lhe dermos.
type aos501Cliente struct {
	st        estadoDoRun
	existe    bool
	erro      error
	consultas int
}

func (c *aos501Cliente) Submit(context.Context, pedidoDeRun) error {
	panic("a reidratacao nao submete runs")
}

func (c *aos501Cliente) Status(context.Context, string) (estadoDoRun, bool, error) {
	c.consultas++
	return c.st, c.existe, c.erro
}

// aos501Reidratado compõe o executor pelo CONSTRUTOR — que é quem reidrata —, com o DOCUMENTO
// APROVADO dado: desde a revisão adversarial (I1) a reidratação consulta-o.
func aos501Reidratado(t *testing.T, cli nodeRunner, store runlifecycle.EventStore, nos ...plan.Node) *executorDeNos {
	t.Helper()
	e, err := novoExecutorDeNos(context.Background(), cli, nil, nil, "run-418", plan.PlanDocument{Nodes: nos}, nil, nil, store, "plan-418")
	if err != nil {
		t.Fatalf("novoExecutorDeNos: %v", err)
	}
	return e
}

// TestAOS501_Reidratacao: do log saem (a) os factos da declaração, incluindo o vínculo e o digest
// do contrato — o primeiro de cada nó é o que fica —, e (b) os payloads por referência, relidos
// do run filho e conferidos contra o DOCUMENTO, contra o facto e contra TUDO o que o evento
// registou.
func TestAOS501_Reidratacao(t *testing.T) {
	envelope, documento := aos499Envelope(t, "notas")
	leitor := aos501NoLeitor("doc_read")
	digestDoContrato := plan.OutputDigest(leitor, leitor.Outputs[0])
	a := agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
		StepID: "step-000001-tool-1", Digest: digestDoConteudo(envelope), Bytes: len(envelope)}
	publicado := plannerevents.PayloadPublishedPayload{
		NodeID: "ler", Output: "conteudo", Type: plan.PayloadRecord, Taint: plan.TaintUntrusted,
		Record: plannerevents.PayloadRecordRef{Store: plannerevents.PayloadStoreEventStore, Stream: aos501RunDoLeitor, Digest: digestDoConteudo(documento)},
		Source: &plannerevents.PayloadSource{Kind: plannerevents.PayloadSourceToolResult, Tool: "doc_read", StepID: a.StepID,
			AnchorDigest: a.Digest, AnchorBytes: a.Bytes, Extraction: plannerevents.PayloadExtractionSandboxStdoutText},
	}
	bom := func() estadoDoRun {
		copia := a
		return estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: aos501TextoFinal, OutputSource: &copia, Output: &envelope}
	}
	chave := chaveDePayload{no: "ler", output: "conteudo"}
	facto := func(no string, vinculo plannerevents.OutputSourceBinding) eventoDeTeste {
		return eventoDeTeste{tipo: plannerevents.EventOutputSourceDeclared, payload: plannerevents.OutputSourceDeclaredPayload{
			NodeID: no, Output: "conteudo", Tool: "doc_read", Binding: vinculo, ContractDigest: digestDoContrato}}
	}
	// O log de um plano que correu em `on`: o facto vinculativo, e depois a publicação.
	store := func(p plannerevents.PayloadPublishedPayload) *storeDePayloads {
		return &storeDePayloads{eventos: []eventoDeTeste{
			facto("ler", plannerevents.OutputSourceBindingBinds),
			facto("outro", plannerevents.OutputSourceBindingBinds),
			{tipo: plannerevents.EventPayloadPublished, payload: p},
		}}
	}

	// (a) Os factos: o PRIMEIRO de cada nó. Um facto `measure` no log não é apagado por um
	// `binding` posterior — e com ele o nó não se entrega por referência, nem o payload dele se
	// reconstrói.
	cliMeasure := &aos501Cliente{st: bom(), existe: true}
	eMeasure := aos501Reidratado(t, cliMeasure, &storeDePayloads{eventos: []eventoDeTeste{
		facto("ler", plannerevents.OutputSourceBindingMeasure),
		facto("ler", plannerevents.OutputSourceBindingBinds),
		facto("outro", plannerevents.OutputSourceBindingBinds),
		{tipo: plannerevents.EventPayloadPublished, payload: publicado},
	}}, leitor)
	if d := eMeasure.declaradas["ler"]; d.vinculo != plannerevents.OutputSourceBindingMeasure || d.tool != "doc_read" || d.digestDoContrato != digestDoContrato {
		t.Fatalf("o facto que vale e o primeiro do log (measure), com a tool e o digest do contrato; ficou %+v", d)
	}
	if d := eMeasure.declaradas["outro"]; !d.vinculativa("doc_read", digestDoContrato) {
		t.Fatalf("o facto binding de outro no le-se do log: %+v", d)
	}
	var entrega *entregaPorReferencia
	eMeasure.medicao = &medicaoDoContrato{}
	if causa := eMeasure.resolverEntrega(leitor, bom(), &entrega); causa != causaOrigemSemVinculo || entrega != nil {
		t.Fatalf("um run cujo facto no log diz measure NAO se entrega por um serve em on; causa=%q entrega=%v", causa, entrega != nil)
	}
	if _, ok := eMeasure.payloads[chave]; ok || eMeasure.rehidratados != 0 || cliMeasure.consultas != 0 {
		t.Fatalf("com o facto measure o payload por referencia NAO se reconstroi, e nem se chega a perguntar ao no (ok=%v, rehidratados=%d, consultas=%d)", ok, eMeasure.rehidratados, cliMeasure.consultas)
	}

	// (b) Com o facto vinculativo, o payload por referência confirma-se e entra — com o
	// documento, e não com o texto final.
	cli := &aos501Cliente{st: bom(), existe: true}
	e := aos501Reidratado(t, cli, store(publicado), leitor)
	if got, ok := e.payloads[chave]; !ok || got != documento || e.rehidratados != 1 || cli.consultas != 1 {
		t.Fatalf("o payload por referencia tinha de ser reconstruido com o documento (ok=%v, %d bytes, rehidratados=%d, consultas=%d)", ok, len(got), e.rehidratados, cli.consultas)
	}

	// O que não se confirma não entra. Cada caso muda UMA coisa.
	outro := strings.Replace(envelope, "1250", "9999", 1)
	for nome, c := range map[string]struct {
		cli *aos501Cliente
		pub func(plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload
	}{
		"o no responde 503":         {&aos501Cliente{erro: errors.New("estado no no: HTTP 503")}, nil},
		"o no ja nao conhece o run": {&aos501Cliente{existe: false}, nil},
		"so o texto final":          {&aos501Cliente{existe: true, st: estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: documento}}, nil},
		"run que nao concluiu":      {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.Status = "failed"; return s }()}, nil},
		"bytes que nao conferem":    {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.Output = &outro; return s }()}, nil},
		"ancora measure":            {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.OutputSource.Binding = agentruntime.OutputSourceMeasure; return s }()}, nil},
		"bytes indisponiveis":       {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.Output = nil; s.OutputOmitted = "unavailable"; return s }()}, nil},
		"resposta de outro run":     {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.RunID = "OUTRO-RUN~x"; return s }()}, nil},
		"outro passo no evento": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			s := *p.Source
			s.StepID = "step-000002-tool-1"
			p.Source = &s
			return p
		}},
		"outra ancora no evento": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			s := *p.Source
			s.AnchorDigest = digestDoConteudo(outro)
			p.Source = &s
			return p
		}},
		"outra extraccao no evento": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			s := *p.Source
			s.Extraction = plannerevents.PayloadExtractionRaw
			p.Source = &s
			return p
		}},
		"extraccao desconhecida no evento": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			s := *p.Source
			s.Extraction = "sandbox_artifact"
			p.Source = &s
			return p
		}},
		"outro digest entregue": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			p.Record.Digest = digestDoConteudo(envelope)
			return p
		}},
		"outra tool no evento": {&aos501Cliente{existe: true, st: bom()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			s := *p.Source
			s.Tool = "web_post"
			p.Source = &s
			return p
		}},
		"o evento refere o run de outro no": {&aos501Cliente{existe: true, st: func() estadoDoRun { s := bom(); s.RunID = "run-418~outro"; return s }()}, func(p plannerevents.PayloadPublishedPayload) plannerevents.PayloadPublishedPayload {
			p.Record.Stream = "run-418~outro"
			return p
		}},
	} {
		p := publicado
		if c.pub != nil {
			p = c.pub(p)
		}
		e := aos501Reidratado(t, c.cli, store(p), leitor)
		if got, ok := e.payloads[chave]; ok {
			t.Errorf("%s: o payload entrou sem se confirmar (%d bytes)", nome, len(got))
		}
		if e.rehidratados != 0 {
			t.Errorf("%s: contou %d payloads reconstruidos", nome, e.rehidratados)
		}
	}

	// UM `aos-orq` ANTERIOR, a ler o mesmo evento, usava a regra do texto final: comparava o
	// digest do `final_text` com o `record.digest`. Não batem — o digest publicado é o do
	// documento entregue —, pelo que o consumidor falhava fechado em vez de receber o texto.
	if digestDoConteudo(aos501TextoFinal) == publicado.Record.Digest {
		t.Fatal("o digest publicado bate com o do texto final: um leitor anterior entregava o texto do modelo")
	}
}

// TestAOS501_AReidratacaoConsultaODocumentoAprovado (revisão adversarial de 2026-10-06, I1): na
// reconstrução dos payloads a partir do log, o CONTRATO DA SAÍDA NO DOCUMENTO é a autoridade.
//
//   - uma saída cujo contrato DECLARA `from_tool` só entra de um `plan.payload_published` COM
//     `source`, com o facto `plan.output_source_declared` vinculativo no log para esse nó e essa
//     tool, e com o `contract_digest` do facto igual ao do contrato;
//   - uma saída cujo contrato NÃO declara a origem só entra de um evento SEM `source`.
//
// O construtor do evento não escreve nenhuma das combinações recusadas (a regra simétrica); o
// que aqui se prende é a LEITURA, contra um evento que tenha entrado no stream do plano por
// outra via. Era por aí — e só por aí — que o texto do modelo chegava a um consumidor sob um
// contrato com origem. O consumidor fecha `entrada_por_cumprir`, a regra do AOS-418.
func TestAOS501_AReidratacaoConsultaODocumentoAprovado(t *testing.T) {
	const textoDoModelo = "RESUMO-DO-MODELO sem o numero"
	envelope, documento := aos499Envelope(t, "notas")
	comOrigem, semOrigem := aos501NoLeitor("doc_read"), aos501NoLeitor("")
	consumidor := plan.Node{NodeID: "resumir", Role: "summarizer", Objective: "resumir", DependsOn: []string{"ler"},
		Consumes: []plan.PayloadEdge{{From: "ler", Output: "conteudo", Type: plan.PayloadRecord}}}
	chave := chaveDePayload{no: "ler", output: "conteudo"}
	a := agentruntime.OutputSource{Tool: "doc_read", Binding: agentruntime.OutputSourceBinds, State: agentruntime.OutputSourceDesignated,
		StepID: "step-000001-tool-1", Digest: digestDoConteudo(envelope), Bytes: len(envelope)}
	// O nó responde com TUDO: o texto final do modelo, a âncora designada e os bytes que conferem.
	// É a resposta com que seria mais fácil deixar entrar qualquer um dos dois.
	resposta := estadoDoRun{RunID: aos501RunDoLeitor, Status: "completed", Terminated: true, FinalText: textoDoModelo, OutputSource: &a, Output: &envelope}
	doTexto := plannerevents.PayloadPublishedPayload{NodeID: "ler", Output: "conteudo", Type: plan.PayloadRecord, Taint: plan.TaintUntrusted,
		Record: plannerevents.PayloadRecordRef{Store: plannerevents.PayloadStoreEventStore, Stream: aos501RunDoLeitor, Digest: digestDoConteudo(textoDoModelo)}}
	porReferencia := plannerevents.PayloadPublishedPayload{NodeID: "ler", Output: "conteudo", Type: plan.PayloadRecord, Taint: plan.TaintUntrusted,
		Record: plannerevents.PayloadRecordRef{Store: plannerevents.PayloadStoreEventStore, Stream: aos501RunDoLeitor, Digest: digestDoConteudo(documento)},
		Source: &plannerevents.PayloadSource{Kind: plannerevents.PayloadSourceToolResult, Tool: "doc_read", StepID: a.StepID,
			AnchorDigest: a.Digest, AnchorBytes: a.Bytes, Extraction: plannerevents.PayloadExtractionSandboxStdoutText}}
	facto := func(mexer func(*plannerevents.OutputSourceDeclaredPayload)) eventoDeTeste {
		d := plannerevents.OutputSourceDeclaredPayload{NodeID: "ler", Output: "conteudo", Tool: "doc_read",
			Binding: plannerevents.OutputSourceBindingBinds, ContractDigest: plan.OutputDigest(comOrigem, comOrigem.Outputs[0])}
		if mexer != nil {
			mexer(&d)
		}
		return eventoDeTeste{tipo: plannerevents.EventOutputSourceDeclared, payload: d}
	}
	publicacao := func(p plannerevents.PayloadPublishedPayload) eventoDeTeste {
		return eventoDeTeste{tipo: plannerevents.EventPayloadPublished, payload: p}
	}
	fechado := plannerevents.PayloadPublishedPayload{NodeID: "ler", Output: "conteudo",
		Closed: &plannerevents.ClosedPayload{Outcome: "pass", Reasons: []string{"documento_lido"}}}

	for nome, c := range map[string]struct {
		produtor plan.Node
		eventos  []eventoDeTeste
		entra    string // o conteúdo que tem de entrar; vazio ⇒ NÃO entra
	}{
		// OS DOIS CASOS DA REVISÃO (`TestREV_Reidratacao`): entravam os dois.
		"(a) contrato COM origem, evento SEM source: o texto do modelo NAO entra": {comOrigem,
			[]eventoDeTeste{facto(nil), publicacao(doTexto)}, ""},
		"(b) contrato SEM origem, evento COM source e sem facto: NAO entra": {semOrigem,
			[]eventoDeTeste{publicacao(porReferencia)}, ""},
		// E as variantes à volta deles.
		"(a') contrato COM origem, evento SEM source e sem facto": {comOrigem,
			[]eventoDeTeste{publicacao(doTexto)}, ""},
		"(b') contrato SEM origem, evento COM source e COM facto": {semOrigem,
			[]eventoDeTeste{facto(nil), publicacao(porReferencia)}, ""},
		"contrato COM origem, evento de forma fechada": {comOrigem,
			[]eventoDeTeste{facto(nil), publicacao(fechado)}, ""},
		"contrato COM origem, evento COM source, SEM facto": {comOrigem,
			[]eventoDeTeste{publicacao(porReferencia)}, ""},
		"contrato COM origem, facto measure": {comOrigem,
			[]eventoDeTeste{facto(func(d *plannerevents.OutputSourceDeclaredPayload) {
				d.Binding = plannerevents.OutputSourceBindingMeasure
			}), publicacao(porReferencia)}, ""},
		"contrato COM origem, facto de outra tool": {comOrigem,
			[]eventoDeTeste{facto(func(d *plannerevents.OutputSourceDeclaredPayload) { d.Tool = "web_fetch" }), publicacao(porReferencia)}, ""},
		"contrato COM origem, facto com outro contract_digest": {comOrigem,
			[]eventoDeTeste{facto(func(d *plannerevents.OutputSourceDeclaredPayload) {
				d.ContractDigest = plan.OutputDigest(semOrigem, semOrigem.Outputs[0])
			}), publicacao(porReferencia)}, ""},
		"contrato COM origem, facto sem contract_digest": {comOrigem,
			[]eventoDeTeste{facto(func(d *plannerevents.OutputSourceDeclaredPayload) { d.ContractDigest = "" }), publicacao(porReferencia)}, ""},
		"contrato COM origem, facto de outro no": {comOrigem,
			[]eventoDeTeste{facto(func(d *plannerevents.OutputSourceDeclaredPayload) { d.NodeID = "outro" }), publicacao(porReferencia)}, ""},
		"contrato COM origem, o facto vem DEPOIS da publicacao": {comOrigem,
			[]eventoDeTeste{publicacao(porReferencia), facto(nil)}, ""},
		// NÃO-VACUIDADE: as duas combinações certas entram, cada uma com o seu conteúdo.
		"contrato COM origem, evento COM source e facto vinculativo: entra o DOCUMENTO": {comOrigem,
			[]eventoDeTeste{facto(nil), publicacao(porReferencia)}, documento},
		"contrato SEM origem, evento SEM source: entra o texto final, como sempre": {semOrigem,
			[]eventoDeTeste{publicacao(doTexto)}, textoDoModelo},
	} {
		cli := &aos501Cliente{existe: true, st: resposta}
		e := aos501Reidratado(t, cli, &storeDePayloads{eventos: c.eventos}, c.produtor, consumidor)
		got, entrou := e.payloads[chave]
		if entrou != (c.entra != "") || got != c.entra {
			t.Errorf("%s: entrou=%v com %q; quero entrou=%v com %q", nome, entrou, got, c.entra != "", c.entra)
		}
		if quer := map[bool]int{true: 1, false: 0}[c.entra != ""]; e.rehidratados != quer {
			t.Errorf("%s: contou %d payloads reconstruidos; quero %d", nome, e.rehidratados, quer)
		}
		// O CONSUMIDOR: sem o payload fica com o contrato por cumprir — é o que o fecha
		// `entrada_por_cumprir` (AOS-418) em vez de correr com o que não se confirmou.
		aresta, porCumprir := e.contratoPorCumprir("resumir")
		if porCumprir != (c.entra == "") {
			t.Errorf("%s: o consumidor tem o contrato por cumprir = %v; quero %v", nome, porCumprir, c.entra == "")
		}
		if porCumprir && (aresta.From != "ler" || aresta.Output != "conteudo") {
			t.Errorf("%s: o contrato por cumprir e o de ler/conteudo; veio %+v", nome, aresta)
		}
		if _, err := e.entradasDe(consumidor); (err != nil) != (c.entra == "") || (err != nil && !errors.Is(err, ErrPayloadPerdido)) {
			t.Errorf("%s: montar as entradas do consumidor devolveu %v", nome, err)
		}
		// Em NENHUM caso recusado o texto do modelo, nem o documento, ficam em memória.
		if c.entra == "" && len(e.payloads) != 0 {
			t.Errorf("%s: ficou conteudo em memoria sem se confirmar: %d payload(s)", nome, len(e.payloads))
		}
	}
}

// aos501Modelo é um [decompose.Model] que guarda o prompt de sistema que recebeu.
type aos501Modelo struct{ sistema string }

func (m *aos501Modelo) Complete(_ context.Context, system, _ string) (string, error) {
	m.sistema = system
	return "", errors.New("o modelo de teste so captura o prompt")
}

// TestAOS501_OPromptSegueAPostura: SÓ com a entrega activa o planeador recebe o prompt 1.5.0, que
// nomeia `from_tool`. Com o interruptor em `off`/`observe`, ou em `on` contra um nó que não
// anuncia o vínculo vinculativo, recebe o corrente (1.4.0), que não pede o campo.
func TestAOS501_OPromptSegueAPostura(t *testing.T) {
	snap := planvalidate.Snapshot{Hash: "sha256:snap"}
	for _, c := range []struct {
		modo    string
		anuncio anuncioDoNo
		postura posturaDaEntrega
		prompt  plannerprompt.Prompt
	}{
		{"", anuncioDoNo{origem: true, vinculativa: true}, entregaDesligada, plannerprompt.Current},
		{"off", anuncioDoNo{origem: true, vinculativa: true}, entregaDesligada, plannerprompt.Current},
		{"observe", anuncioDoNo{origem: true, vinculativa: true}, entregaDesligada, plannerprompt.Current},
		{"on", anuncioDoNo{}, entregaSemNo, plannerprompt.Current},
		{"on", anuncioDoNo{origem: true}, entregaSemNo, plannerprompt.Current},
		{"on", anuncioDoNo{origem: true, vinculativa: true}, entregaActiva, plannerprompt.WithOutputSource},
	} {
		modo := c.modo
		if modo == "" {
			modo = saidaPorReferenciaOff
		}
		postura := posturaDe(modo, c.anuncio)
		if postura != c.postura {
			t.Fatalf("modo %q, anuncio %+v: postura=%d; quero %d", c.modo, c.anuncio, postura, c.postura)
		}
		if got := promptDoPlaneador(postura); got.CacheKey() != c.prompt.CacheKey() {
			t.Fatalf("modo %q, anuncio %+v: prompt %s; quero %s", c.modo, c.anuncio, got.MetaPromptVersion(), c.prompt.MetaPromptVersion())
		}
		// E é esse o prompt que CHEGA ao modelo, pelo decompositor que o `serve` compõe.
		modelo := &aos501Modelo{}
		dec, err := novoDecompositor(modelo, snap, postura)
		if err != nil {
			t.Fatalf("novoDecompositor: %v", err)
		}
		_, _ = dec.Decompose(context.Background(), planner.DecomposeInput{Context: planner.PlanningContext{Goal: "ler", CapabilitiesHash: snap.Hash}})
		if !strings.Contains(modelo.sistema, c.prompt.Template) {
			t.Fatalf("modo %q, anuncio %+v: o prompt de sistema nao e o %s", c.modo, c.anuncio, c.prompt.MetaPromptVersion())
		}
		if nomeia := strings.Contains(modelo.sistema, "from_tool"); nomeia != (c.postura == entregaActiva) {
			t.Fatalf("modo %q, anuncio %+v: o prompt nomeia from_tool = %v", c.modo, c.anuncio, nomeia)
		}
	}
	// A omissão de quem não escolhe é a 1.4.0.
	if plannerprompt.Current.MetaPromptVersion() != "1.4.0" || plannerprompt.WithOutputSource.MetaPromptVersion() != "1.5.0" {
		t.Fatalf("as duas versoes do prompt sao 1.4.0 (corrente) e 1.5.0 (com a origem); sao %s e %s",
			plannerprompt.Current.MetaPromptVersion(), plannerprompt.WithOutputSource.MetaPromptVersion())
	}
	var _ decompose.Model = (*aos501Modelo)(nil)
}

// TestAOS501_AGuardaSegueAPostura: a guarda da linha 1.3.0, postura a postura.
func TestAOS501_AGuardaSegueAPostura(t *testing.T) {
	comOrigem := aos500Decode(t, aos500PlanoComOrigem)
	soCarimbo := aos500Decode(t, aos500PlanoSemOrigem(t))
	deHoje := aos500Decode(t, aos500PlanoDeHoje(t))

	// Desligada: a guarda do AOS-500, tal como era — o campo e o carimbo.
	for nome, doc := range map[string]plan.PlanDocument{"com origem": comOrigem, "so o carimbo": soCarimbo} {
		if err := entregaDesligada.recusar(doc); !errors.Is(err, errOrigemSemEntrega) {
			t.Errorf("desligada, %s: tinha de recusar com errOrigemSemEntrega; veio %v", nome, err)
		}
		if r := entregaDesligada.recusaDoLaco(doc); r == nil || r.Reason != string(planvalidate.ReasonVersionAheadOfReader) {
			t.Errorf("desligada, %s: no laco e uma tentativa recusada; veio %+v", nome, r)
		}
	}
	// Sem nó: só o que DECLARA a origem é recusado, com a causa do nó.
	if err := entregaSemNo.recusar(comOrigem); !errors.Is(err, errNoSemEntregaPorReferencia) || errors.Is(err, errOrigemSemEntrega) {
		t.Errorf("sem no, com origem: tinha de recusar com errNoSemEntregaPorReferencia; veio %v", err)
	}
	if r := entregaSemNo.recusaDoLaco(comOrigem); r == nil {
		t.Error("sem no, com origem: no laco e uma tentativa recusada")
	}
	if err, r := entregaSemNo.recusar(soCarimbo), entregaSemNo.recusaDoLaco(soCarimbo); err != nil || r != nil {
		t.Errorf("sem no, so o carimbo: nao pede entrega nenhuma e segue para o validador; veio %v / %+v", err, r)
	}
	// Activa: nada é recusado aqui.
	for nome, doc := range map[string]plan.PlanDocument{"com origem": comOrigem, "so o carimbo": soCarimbo} {
		if err, r := entregaActiva.recusar(doc), entregaActiva.recusaDoLaco(doc); err != nil || r != nil {
			t.Errorf("activa, %s: a postura nao recusa; veio %v / %+v", nome, err, r)
		}
	}
	// Um plano de hoje não é de ninguém, em nenhuma postura.
	for _, p := range []posturaDaEntrega{entregaDesligada, entregaSemNo, entregaActiva} {
		if err, r := p.recusar(deHoje), p.recusaDoLaco(deHoje); err != nil || r != nil {
			t.Errorf("postura %d: um plano sem a linha 1.3.0 nao e recusado; veio %v / %+v", p, err, r)
		}
	}
	// A postura que não se diz é a desligada: o contexto sem ela dá o comportamento anterior.
	if posturaDoContexto(context.Background()) != entregaDesligada || posturaDoContexto(comPostura(context.Background(), entregaActiva)) != entregaActiva {
		t.Error("o contexto sem postura e a entrega desligada; com ela, e a que se pos")
	}
	// Os leitores SEM nó (o `decide`, a re-verificação do pendente) seguem o interruptor.
	for modo, recusa := range map[string]bool{"": true, "off": true, "observe": true, "on": false, "On": true, "lixo": true} {
		t.Setenv("AOS_ORQ_SAIDA_POR_REFERENCIA", modo)
		if err := recusarSemEntrega(comOrigem); (err != nil) != recusa {
			t.Errorf("AOS_ORQ_SAIDA_POR_REFERENCIA=%q: recusarSemEntrega = %v; quero recusa=%v", modo, err, recusa)
		}
	}
}

// TestAOS501_Submit_OVinculoVaiNoCorpoENaoTemOmissao: o corpo do `POST /runs` leva o vínculo que
// o executor escolheu, e uma origem SEM vínculo não se submete — não há valor por omissão.
func TestAOS501_Submit_OVinculoVaiNoCorpoENaoTemOmissao(t *testing.T) {
	f := &aos495No{}
	srv := f.servidor(t)
	cred := filepath.Join(t.TempDir(), "nhi.jwt")
	escrever(t, cred, "nhi")
	t.Setenv("AOS_ORQ_NODE_URL", srv.URL)
	t.Setenv("AOS_ORQ_NODE_CREDENTIAL_FILE", cred)
	t.Setenv("AOS_MODE", "")
	cli, err := nodeClientDoAmbiente()
	if err != nil {
		t.Fatalf("nodeClientDoAmbiente: %v", err)
	}
	ctx := context.Background()
	for _, v := range []agentruntime.OutputSourceBinding{agentruntime.OutputSourceBinds, agentruntime.OutputSourceMeasure} {
		id := "run~" + string(v)
		if err := cli.Submit(ctx, pedidoDeRun{RunID: id, Objective: "o", Tools: []string{"doc_read"}, OutputFromTool: "doc_read", OutputBinding: v}); err != nil {
			t.Fatalf("Submit com o vinculo %q: %v", v, err)
		}
		if tool, vinculo, _ := origemEnviada(f.corpos[id]); tool != "doc_read" || vinculo != string(v) {
			t.Fatalf("o corpo leva a origem e o vinculo %q; levou %q/%q", v, tool, vinculo)
		}
	}
	antes := f.submissoes
	for _, v := range []agentruntime.OutputSourceBinding{"", "enforce"} {
		if err := cli.Submit(ctx, pedidoDeRun{RunID: "run~sem", Objective: "o", Tools: []string{"doc_read"}, OutputFromTool: "doc_read", OutputBinding: v}); err == nil {
			t.Fatalf("uma origem com o vinculo %q tinha de recusar a submissao", v)
		}
	}
	if f.submissoes != antes {
		t.Fatal("a origem sem vinculo chegou ao no")
	}
}

// TestAOS501_Anuncio_OVinculoVinculativo: o `GET /tools` do nó real (ficheiro de fio) anuncia os
// dois vínculos; um nó com o veredicto desligado, ou anterior, não anuncia nenhum.
func TestAOS501_Anuncio_OVinculoVinculativo(t *testing.T) {
	for nome, quer := range map[string]anuncioDoNo{
		"tools-enforce": {aceita: true, modo: "enforce", origem: true, vinculativa: true},
		"tools-observe": {aceita: true, modo: "observe", origem: true, vinculativa: true},
	} {
		f := &aos495No{catalogo: bytes.TrimSpace(aos495Fio(t, nome))}
		srv := f.servidor(t)
		cli := &nodeClient{base: srv.URL, http: srv.Client()}
		if got, err := cli.ContratoDeConclusao(context.Background()); err != nil || got != quer {
			t.Fatalf("%s: anuncio = %+v, %v; quero %+v", nome, got, err, quer)
		}
	}
	for nome, catalogo := range map[string]string{
		"sem output_source":       `{"tools":[],"completion_contract":{"mode":"enforce"}}`,
		"so measure":              `{"tools":[],"completion_contract":{"mode":"enforce"},"output_source":{"bindings":["measure"]}}`,
		"vinculo com outra caixa": `{"tools":[],"completion_contract":{"mode":"enforce"},"output_source":{"bindings":["measure","Binding"]}}`,
	} {
		f := &aos495No{catalogo: []byte(catalogo)}
		srv := f.servidor(t)
		cli := &nodeClient{base: srv.URL, http: srv.Client()}
		if got, err := cli.ContratoDeConclusao(context.Background()); err != nil || got.vinculativa {
			t.Fatalf("%s: o anuncio nao diz o vinculo binding; veio %+v, %v", nome, got, err)
		}
	}
}

// TestAOS501_Metricas_SoVocabularioFechado: as séries da entrega levam só rótulos deste binário,
// e só existem com valor — com o interruptor fora de `on` o ficheiro de métricas é o de antes.
func TestAOS501_Metricas_SoVocabularioFechado(t *testing.T) {
	vazio := &metricasDoConsumo{series: map[string]float64{}}
	vazio.registarContrato(&medicaoDoContrato{})
	if len(vazio.series) != 0 {
		t.Fatalf("uma medicao sem nada nao escreve series: %v", vazio.series)
	}
	c := &medicaoDoContrato{}
	c.entregaResolvida(resultadoEntregue)
	c.entregaResolvida(causaOrigemVazia)
	c.entregaResolvida("texto vindo do no\n")
	c.extraccaoFeita(string(plannerevents.PayloadExtractionRaw))
	c.extraccaoFeita("qualquer coisa")
	c.candidatoSemOrigem()
	c.recusaDaOrigemNoValidador(string(planvalidate.ReasonFromToolWithConsumes))
	c.recusaDaOrigemNoValidador("razao inventada")
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarContrato(c)
	texto := string(m.texto())
	for _, quer := range []string{
		serie(metricaEntregaPorReferencia, "resultado", resultadoEntregue) + " 1\n",
		serie(metricaEntregaPorReferencia, "resultado", causaOrigemVazia) + " 1\n",
		serie(metricaEntregaExtraccao, "extraccao", "raw") + " 1\n",
		metricaCandidatosSemOrigem + " 1\n",
		serie(metricaOrigemRecusasDoValidador, "razao", "from_tool_with_consumes") + " 1\n",
	} {
		if !strings.Contains(texto, quer) {
			t.Fatalf("faltou a serie %q:\n%s", quer, texto)
		}
	}
	for _, proibido := range []string{"vindo do no", "qualquer coisa", "inventada"} {
		if strings.Contains(texto, proibido) {
			t.Fatalf("as metricas levam um rotulo fora do vocabulario fechado (%q):\n%s", proibido, texto)
		}
	}
	// As razões do validador que se contam são as que o `planvalidate` tem para a origem.
	for _, r := range []planvalidate.Reason{planvalidate.ReasonFromToolOnVerifier, planvalidate.ReasonFromToolWithConsumes, planvalidate.ReasonFromToolMultiple,
		planvalidate.ReasonFromToolOutputType, planvalidate.ReasonFromToolUnknownTool, planvalidate.ReasonFromToolAmbiguousTool,
		planvalidate.ReasonFromToolWithTextOutput} {
		tem := false
		for _, s := range razoesDaOrigemNoValidador {
			tem = tem || s == string(r)
		}
		if !tem {
			t.Errorf("a razao %q do validador nao se conta", r)
		}
	}
	if len(razoesDaOrigemNoValidador) != 7 {
		t.Errorf("as razoes da origem contadas sao sete; sao %d", len(razoesDaOrigemNoValidador))
	}
}

// TestAOS501_OConsumidorPrivilegiadoNaoConsomeUmaSaidaPorReferencia: uma saída por referência é
// SEMPRE untrusted — o advisory do documento não a desclassifica —, e um nó com uma tool de
// efeito continua a não a poder consumir (a regra `consumes_taint_authority` do validador).
func TestAOS501_OConsumidorPrivilegiadoNaoConsomeUmaSaidaPorReferencia(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "snap.json")
	escrever(t, caminho, aos408SnapshotComPerigo)
	snap, err := carregarSnapshot(caminho)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	plano := strings.Replace(aos500PlanoComOrigem, `"tools":[],`, `"tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],`, 1)
	plano = strings.Replace(plano, `{"name":"notas","type":"record","from_tool":"fs.read"}`, `{"name":"notas","type":"record","taint":"trusted","from_tool":"fs.read"}`, 1)
	doc := aos500Decode(t, plano)
	if got := doc.Nodes[0].EffectiveOutputTaint(doc.Nodes[0].Outputs[0]); got != plan.TaintUntrusted {
		t.Fatalf("o taint efectivo de uma saida por referencia e untrusted, mesmo com o advisory trusted; veio %q", got)
	}
	v := planvalidate.Validate(doc, snap, planvalidate.Ceilings{MaxNodes: planvalidate.DefaultMaxNodes})
	if !v.Rejected() || v.Reason != planvalidate.ReasonConsumesTaintAuthority {
		t.Fatalf("um no com tool de efeito a consumir uma saida por referencia tinha de ser recusado com %s; veio %+v", planvalidate.ReasonConsumesTaintAuthority, v)
	}
}
