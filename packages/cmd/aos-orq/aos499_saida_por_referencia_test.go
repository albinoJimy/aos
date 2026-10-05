package main

// AOS-499 — O `aos-orq` MEDE A SAÍDA POR REFERÊNCIA SEM MUDAR A ENTREGA.
//
// Os testes de processo correm o `aos-orq consume` REAL contra o nó `aos` falso do AOS-495. O que
// esse nó responde não é inventado aqui: o anúncio do `GET /tools` e as respostas do
// `GET /runs/{id}` são os ficheiros `packages/cmd/aos/testdata/` que o teste do NÓ exige que o nó
// real responda, byte a byte — as respostas da forma de produção, ao corpo que ESTE binário
// enviou (`TestAOS499_Fio_PostRunsDoAosOrq`, do lado do nó).
//
// O corpo do `POST /runs` com a declaração de origem é GERADO aqui
// (`testdata/aos499_fio/post-runs-read_notes.json`) e consumido pelo teste do nó. Regenera-se com
// `AOS494_ACTUALIZAR_FIO=1`, pela ordem do AOS-494: o nó (o anúncio), este pacote (o corpo), e o
// nó outra vez (as respostas a esse corpo).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// aos499FioDoNo lê uma resposta do nó real, gravada pelos testes do AOS-498/AOS-499 do nó.
func aos499FioDoNo(t *testing.T, nome string) []byte {
	t.Helper()
	cru, err := os.ReadFile(filepath.Join("..", "aos", "testdata", "aos498_fio", nome+".json"))
	if err != nil && os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
		t.Skipf("a resposta do no ainda nao foi gerada (%v): corre o teste do no com AOS494_ACTUALIZAR_FIO=1, e este outra vez", err)
	}
	if err != nil || len(bytes.TrimSpace(cru)) == 0 {
		t.Fatalf("ficheiro do fio %s: err=%v bytes=%d — gera-o no no com AOS494_ACTUALIZAR_FIO=1", nome, err, len(cru))
	}
	return cru
}

// aos499FioDoPost compara o corpo do `POST /runs` com o ficheiro que o teste do NÓ consome.
func aos499FioDoPost(t *testing.T, nome string, cru []byte) {
	t.Helper()
	caminho := filepath.Join("testdata", "aos499_fio", nome+".json")
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

// aos499Drenagem é o que uma drenagem deixou, com o WAL para se lerem os eventos do plano.
type aos499Drenagem struct {
	aos495Desfecho
	wal string
}

// aos499Consumir é o [aos495ConsumirCom] com o interruptor da saída por referência no ambiente
// (vazio ⇒ a variável NÃO é definida) e com o caminho do WAL de volta.
func aos499Consumir(t *testing.T, bin string, f *aos495No, run, plano, snapshot, modo string) aos499Drenagem {
	t.Helper()
	srv := f.servidor(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, snapshot)
	fix := filepath.Join(dir, "plano.json")
	escrever(t, fix, plano)
	f.mu.Lock()
	f.ofertas = append(f.ofertas, pedidoReclamado{RunID: run, Objective: "ler o documento notes e resumi-lo", Geracao: 1})
	f.mu.Unlock()

	env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE=", "AOS_ORQ_SAIDA_POR_REFERENCIA=" + modo}
	wal := filepath.Join(dir, "consume.wal")
	r := correrComEnv(t, env, bin, "consume", "--wal", wal, "--snapshot", snap,
		"--decompose-fixture", fix, "--poll-interval", "20ms", "--max", "1", "--plan-timeout", "30s")
	if r.code != exitOK {
		t.Fatalf("o consume em si tinha de sair 0 (o desfecho vai para o no), saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	metricas, err := os.ReadFile(filepath.Join(dir, nomeDoFicheiroDeMetricas))
	if err != nil {
		t.Fatalf("a drenagem tinha de escrever o ficheiro de metricas: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.desfechos) != 1 {
		t.Fatalf("queria um desfecho reportado ao no, vieram %d\n%s\n%s", len(f.desfechos), r.stdout, r.stderr)
	}
	d := f.desfechos[0]
	codigo, _ := d["codigo_saida"].(float64)
	classe, _ := d["classe"].(string)
	detalhe, _ := d["detalhe"].(string)
	return aos499Drenagem{wal: wal, aos495Desfecho: aos495Desfecho{codigo: int(codigo), classe: classe, detalhe: detalhe,
		stdout: r.stdout, stderr: r.stderr, metricas: string(metricas)}}
}

// aos499Evento é um evento do log do plano: o stream, o tipo, o passo e o payload. Fica de fora o
// envelope do Event Store (o instante da gravação e a cadeia de hashes).
type aos499Evento struct {
	Stream, Tipo, Passo, Payload string
}

// aos499EventosDoPlano lê do WAL os eventos do run do plano e do stream do planeador, por ordem.
func aos499EventosDoPlano(t *testing.T, wal, run string) []aos499Evento {
	t.Helper()
	store, err := eventstore.OpenReadOnly(wal)
	if err != nil {
		t.Fatalf("abrir o WAL: %v", err)
	}
	defer func() { _ = store.Close() }()
	var eventos []aos499Evento
	for _, stream := range []string{run, run + "-plan"} {
		evs, err := store.Read(context.Background(), stream, 1)
		if err != nil {
			if errors.Is(err, eventstore.ErrStreamNotFound) {
				continue
			}
			t.Fatalf("ler %s: %v", stream, err)
		}
		for _, e := range evs {
			eventos = append(eventos, aos499Evento{Stream: stream, Tipo: e.Type, Passo: e.StepID, Payload: string(e.Payload)})
		}
	}
	if len(eventos) == 0 {
		t.Fatal("o WAL nao tem eventos do plano: o teste nao comparava nada")
	}
	return eventos
}

// origemEnviada lê do corpo de um `POST /runs` a declaração de origem.
func origemEnviada(corpo map[string]any) (tool, vinculo string, presente bool) {
	t, temTool := corpo["output_from_tool"].(string)
	v, temVinculo := corpo["output_source_binding"].(string)
	return t, v, temTool || temVinculo
}

const (
	// O que a `doc_read` do nó de teste devolve, e o que o modelo falso escreve depois: são os
	// bytes do fio do nó (`producao-designada-*.json`).
	aos499Documento = "conteudo do documento"
	aos499Texto     = "feito"
)

// TestAOS499ComOBinarioReal corre os cenários de processo sobre UM só binário.
func TestAOS499ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	const run = "plan-aos499-fio"

	// (1) A FORMA DE PRODUÇÃO, EM `observe`: o nó `read_notes` (uma tool, uma saída `record`,
	// sem `consumes`) é candidato e declara a origem com o vínculo «só medição»; o `summarize` não.
	// O corpo do `POST` é o ficheiro que o teste do nó consome.
	t.Run("Observe_OCorpoLevaAOrigemEOVinculoMeasure", func(t *testing.T) {
		p := aos495FormaDeProducao(t, "enforce")
		f := &aos495No{catalogo: p.catalogo}
		d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
		aos499FioDoPost(t, "post-runs-read_notes", f.crus[run+"~read_notes"])

		corpo := f.corpo(run, "read_notes")
		tool, vinculo, presente := origemEnviada(corpo)
		if !presente || tool != "doc_read" || vinculo != "measure" {
			t.Fatalf("o candidato tinha de declarar a origem doc_read com o vinculo measure; levou %q/%q (presente=%v)", tool, vinculo, presente)
		}
		tools, _ := corpo["tools"].([]any)
		if c, ok := contratoEnviado(corpo); len(tools) != 1 || tools[0] != tool || !ok || len(c) != 1 || c[0] != tool {
			t.Fatalf("a tool declarada tem de ser a da lista-branca e a do contrato do MESMO pedido: tools=%v contrato=%v", tools, c)
		}
		if _, _, presente := origemEnviada(f.corpo(run, "summarize")); presente || f.corpo(run, "summarize") == nil {
			t.Fatalf("o no com consumes e sem tools nao e candidato e nao declara a origem: %v", f.corpo(run, "summarize"))
		}
		for _, quer := range []string{
			"saida por referencia (AOS-499): modo observe — cada no candidato por estrutura",
			"execucao: no read_notes saida por referencia: classe=candidato — declara a origem (a sua unica tool) com o vinculo measure\n",
			"execucao: no summarize saida por referencia: classe=nao_candidato — NAO declara a origem\n",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
		}
	})

	// (2) A RESPOSTA DO NÓ REAL A ESSE CORPO: âncora `designated`, e os bytes da tool diferentes do
	// texto final. O `aos-orq` regista a medição e PUBLICA E ENTREGA O TEXTO FINAL, como hoje.
	for _, modoDoNo := range []string{"enforce", "observe"} {
		t.Run("Observe_Designada_MedeEPublicaOTextoFinal/no-em-"+modoDoNo, func(t *testing.T) {
			fio := aos499FioDoNo(t, "producao-designada-"+modoDoNo)
			var st estadoDoRun
			if err := json.Unmarshal(fio, &st); err != nil || st.OutputSource == nil || st.OutputSource.State != agentruntime.OutputSourceDesignated ||
				st.Output == nil || *st.Output != aos499Documento || st.FinalText != aos499Texto || !st.concluiu() {
				t.Fatalf("pre-condicao: o fio traz o run concluido, a ancora designada, os bytes da tool e o texto final (%+v, %v)", st, err)
			}
			p := aos495FormaDeProducao(t, modoDoNo)
			f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": fio}}
			d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
			if d.classe != "terminal" || d.codigo != exitOK {
				t.Fatalf("em observe o plano sai como hoje, terminal/0; saiu %s/%d %q\n%s", d.classe, d.codigo, d.detalhe, d.stdout)
			}
			// A ENTREGA É A DE HOJE: o consumidor recebe o TEXTO FINAL, com o digest do texto final.
			entradas, _ := f.corpo(run, "summarize")["inputs"].([]any)
			if len(entradas) != 1 {
				t.Fatalf("o consumidor tinha de receber uma entrada: %v", entradas)
			}
			entrada := entradas[0].(map[string]any)
			if entrada["content"] != aos499Texto || entrada["digest"] != digestDoConteudo(aos499Texto) {
				t.Fatalf("o consumidor recebe o texto final e o seu digest, e nao os bytes designados: %v", entrada)
			}
			if entrada["digest"] == st.OutputSource.Digest {
				t.Fatal("o digest entregue e o da ancora: o aos-orq publicou o resultado designado")
			}
			for _, quer := range []string{
				// A `doc_read` deste nó de teste devolve bytes CRUS (não corre na sandbox): a forma
				// é `cru`, e o texto final (`feito`) não tem a única linha do documento.
				"execucao: no read_notes ORIGEM MEDIDA estado=designated bytes_da_origem=21 (ate_1k) transporte=servido_confere forma=cru bytes_do_texto=5 texto_final=linhas_abaixo_de_0_5 numeros=sem_numeros razao=abaixo_de_0_5 — so medicao",
				"execucao: payload read_notes/conteudo publicado (record)",
				"execucao: read_notes=complete summarize=complete",
			} {
				if !strings.Contains(d.stdout, quer) {
					t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
				}
			}
			for chave, valor := range map[string]int{
				serie(metricaNosPorEstrutura, "classe", classeCandidato):                    1,
				serie(metricaNosPorEstrutura, "classe", classeNaoCandidato):                 1,
				serie(metricaOrigemDesignacao, "estado", "designated"):                      1,
				serie(metricaOrigemTamanho, "classe", tamanhoAte1K):                         1,
				serie(metricaOrigemRazao, "classe", razaoAbaixoDe05):                        1,
				serie(metricaOrigemTextoFinal, "comparacao", comparacaoLinhasAbaixo05):      1,
				serie(metricaOrigemNumeros, "resultado", numerosNenhum):                     1,
				serie(metricaOrigemForma, "forma", formaCru):                                1,
				serie(metricaOrigemTransporte, "resultado", transporteConfere):              1,
				serie(metricaNosPorContrato, "classe", classeComContratoSaidaAberta):        1,
				serie(metricaDesfechos, "classe", "terminal", "codigo", fmt.Sprint(exitOK)): 1,
			} {
				if !temSerie(d.metricas, chave, valor) {
					t.Fatalf("faltou nas metricas %s %d:\n%s", chave, valor, d.metricas)
				}
			}
			// SEM CONTEÚDO: nem o log, nem as métricas, nem o desfecho levam os bytes da tool. (O
			// texto final também não: `feito` só aparece no log como parte de outras palavras, e
			// por isso a prova faz-se com o documento.)
			for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": d.metricas, "detalhe": d.detalhe} {
				if strings.Contains(texto, aos499Documento) {
					t.Fatalf("o %s leva conteudo do titular (%q):\n%s", onde, aos499Documento, texto)
				}
			}
			// E nem a âncora vinda do nó: o digest e o passo não são repetidos.
			if strings.Contains(d.stdout, st.OutputSource.Digest) || strings.Contains(d.metricas, st.OutputSource.Digest) {
				t.Fatalf("o digest da ancora veio do no e nao se repete no log nem nas metricas:\n%s", d.stdout)
			}
		})
	}

	// (3) NENHUM DESFECHO MUDA — COM RESPOSTAS DIFERENTES POR MODO (revisão de 2026-10-05, I3).
	//
	// A primeira versão dava ao `off` e ao `observe` a MESMA resposta do nó, já com a âncora e os
	// bytes. Mas é exactamente isso que os distingue: em `off` não há declaração e o nó responde
	// SEM os três campos novos; em `observe` responde COM eles. Um `aos-orq` que lesse um dos
	// campos novos para decidir (a mutação Q9: `output_omitted` lido como `output_unavailable`)
	// passava no teste antigo, porque a leitura errada acontecia nos dois lados.
	//
	// Aqui cada caso é a resposta completa do nó em `observe`; em `off` (e sem a variável) o nó
	// falso responde a mesma coisa SEM `output_source`, `output` e `output_omitted`. Exige-se o
	// mesmo desfecho do plano, os mesmos estados de nó, os mesmos eventos e o mesmo `POST` do
	// consumidor — e que nada do que o nó mandou nos campos novos chegue ao log, às métricas ou ao
	// `detail`.
	semOrigem := func(t *testing.T, cru []byte) []byte {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(cru, &m); err != nil {
			t.Fatalf("resposta ilegivel: %v", err)
		}
		delete(m, "output_source")
		delete(m, "output")
		delete(m, "output_omitted")
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	const idDoNo = run + "~read_notes"
	ancora := func(estado, resto string) string {
		a := `"output_source":{"tool":"doc_read","binding":"measure","state":"` + estado + `"`
		if estado == "designated" {
			a += `,"step_id":"step-000001-tool-1","digest":"` + digestDoConteudo(aos499Documento) + `","bytes":21`
		}
		return a + "}" + resto
	}
	concluido := `{"run_id":"` + idDoNo + `","status":"completed","terminated":true,"final_text":"feito","turns":2,`
	type casoDeModo struct {
		nome, modoDoNo string
		completa       []byte
		semVariavel    bool // corre também sem a variável definida
	}
	casos := []casoDeModo{
		{nome: "fio-designada", modoDoNo: "enforce", completa: aos499FioDoNo(t, "producao-designada-enforce"), semVariavel: true},
		{nome: "fio-em-falta", modoDoNo: "observe", completa: aos499FioDoNo(t, "producao-em-falta-observe"), semVariavel: true},
	}
	for nome, completa := range map[string]string{
		"servido":            concluido + ancora("designated", `,"output":"`+aos499Documento+`"`) + "}",
		"servido-nao-bate":   concluido + ancora("designated", `,"output":"OUTRO conteudo qualquer"`) + "}",
		"unavailable_now":    concluido + ancora("designated", `,"output_omitted":"unavailable_now"`) + "}",
		"unavailable":        concluido + ancora("designated", `,"output_omitted":"unavailable"`) + "}",
		"too_large":          concluido + ancora("designated", `,"output_omitted":"too_large"`) + "}",
		"not_utf8":           concluido + ancora("designated", `,"output_omitted":"not_utf8"`) + "}",
		"missing":            concluido + ancora("missing", "") + "}",
		"ambiguous":          concluido + ancora("ambiguous", "") + "}",
		"inapplicable":       concluido + ancora("inapplicable", "") + "}",
		"marca-desconhecida": concluido + ancora("designated", `,"output_omitted":"qualquer coisa\nforjada"`) + "}",
		"ancora-mal-formada": concluido + `"output_source":{"tool":"x y","binding":"binding","state":"Designated","step_id":"zzz","digest":"nada","bytes":-4},"output":"lixo"}`,
		"failed-com-ancora":  `{"run_id":"` + idDoNo + `","status":"failed","outcome_reason":"contract_unmet_no_call","error":"contrato","turns":1,` + ancora("missing", "") + "}",
		"texto-vazio-com-output": `{"run_id":"` + idDoNo + `","status":"completed","terminated":true,"final_text":"","turns":2,` +
			ancora("designated", `,"output":"`+aos499Documento+`"`) + "}",
		"saida-indisponivel-e-ancora": `{"run_id":"` + idDoNo + `","status":"completed","terminated":true,"output_unavailable":true,"turns":2,` +
			ancora("designated", `,"output_omitted":"unavailable"`) + "}",
	} {
		casos = append(casos, casoDeModo{nome: nome, modoDoNo: "enforce", completa: []byte(completa)})
	}
	estados := func(stdout string) string {
		for _, linha := range strings.Split(stdout, "\n") {
			if strings.Contains(linha, "execucao: read_notes=") {
				return strings.TrimSpace(linha)
			}
		}
		return ""
	}
	for _, c := range casos {
		t.Run("NenhumDesfechoMuda/"+c.nome, func(t *testing.T) {
			p := aos495FormaDeProducao(t, c.modoDoNo)
			semCampos := semOrigem(t, c.completa)
			if bytes.Equal(bytes.TrimSpace(semCampos), bytes.TrimSpace(c.completa)) {
				t.Fatal("pre-condicao: a resposta em observe tem de diferir da resposta em off — senao o teste nao distingue os modos")
			}
			correr := func(modo string, resposta []byte) (aos499Drenagem, []aos499Evento, *aos495No) {
				f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": resposta}}
				d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, modo)
				return d, aos499EventosDoPlano(t, d.wal, run), f
			}
			base, eventosBase, fBase := correr("off", semCampos)
			if strings.Contains(base.stdout, "ORIGEM MEDIDA") || strings.Contains(base.stdout, "saida por referencia: classe=") ||
				strings.Contains(base.metricas, metricaNosPorEstrutura+"{") || strings.Contains(base.metricas, metricaOrigemDesignacao+"{") {
				t.Fatalf("em off nada e declarado nem medido:\n%s\n%s", base.stdout, base.metricas)
			}
			if _, _, presente := origemEnviada(fBase.corpo(run, "read_notes")); presente {
				t.Fatal("em off o corpo nao leva a declaracao de origem")
			}
			if !strings.Contains(base.stdout, "saida por referencia (AOS-499): modo off") {
				t.Fatalf("o banner do serve diz o modo, tambem em off:\n%s", base.stdout)
			}
			if estados(base.stdout) == "" {
				t.Fatalf("pre-condicao: o log tem a linha dos estados dos nos:\n%s", base.stdout)
			}
			type corrida struct {
				modo     string
				resposta []byte
			}
			corridas := []corrida{{"observe", c.completa}}
			if c.semVariavel {
				corridas = append(corridas, corrida{"", semCampos})
			}
			for _, k := range corridas {
				d, eventos, f := correr(k.modo, k.resposta)
				if d.codigo != base.codigo || d.classe != base.classe {
					t.Fatalf("modo %q: o desfecho do plano MUDOU: %s/%d %q, e em off era %s/%d %q", k.modo, d.classe, d.codigo, d.detalhe, base.classe, base.codigo, base.detalhe)
				}
				if estados(d.stdout) != estados(base.stdout) {
					t.Fatalf("modo %q: os estados dos nos mudaram: %q, e em off eram %q", k.modo, estados(d.stdout), estados(base.stdout))
				}
				// Stream, tipo, passo e payload de cada evento, pela ordem do log: byte a byte.
				if !reflect.DeepEqual(eventos, eventosBase) {
					t.Fatalf("modo %q: os eventos do plano mudaram:\n  agora: %v\n  em off: %v", k.modo, eventos, eventosBase)
				}
				// O `POST` do consumidor é o mesmo, byte a byte — ou nenhum, nos dois.
				if !reflect.DeepEqual(f.corpo(run, "summarize"), fBase.corpo(run, "summarize")) {
					t.Fatalf("modo %q: o consumidor recebeu outra coisa: %v, e em off era %v", k.modo, f.corpo(run, "summarize"), fBase.corpo(run, "summarize"))
				}
				_, _, presente := origemEnviada(f.corpo(run, "read_notes"))
				if presente != (k.modo == "observe") {
					t.Fatalf("modo %q: declaracao de origem no corpo = %v", k.modo, presente)
				}
				if k.modo != "observe" {
					continue
				}
				if !strings.Contains(d.stdout, "ORIGEM MEDIDA") {
					t.Fatalf("em observe o candidato tem a sua linha de medicao:\n%s", d.stdout)
				}
				// NADA do que o nó mandou nos campos novos se repete fora do vocabulário fechado.
				for onde, texto := range map[string]string{"stdout": d.stdout, "stderr": d.stderr, "metricas": d.metricas, "detalhe": d.detalhe} {
					for _, proibido := range []string{aos499Documento, "OUTRO conteudo", "forjada", "lixo", "x y", "zzz", digestDoConteudo(aos499Documento)} {
						if strings.Contains(texto, proibido) {
							t.Fatalf("o %s leva %q, que veio do no:\n%s", onde, proibido, texto)
						}
					}
				}
			}
		})
	}

	// (4) A ORIGEM EM FALTA (a resposta de produção de 2026-10-04: a tool call escrita como texto).
	// A medição diz `missing`; o plano sai como sai hoje.
	t.Run("Observe_EmFalta_MedeMissing", func(t *testing.T) {
		p := aos495FormaDeProducao(t, "observe")
		f := &aos495No{catalogo: p.catalogo, respostas: map[string][]byte{"read_notes": aos499FioDoNo(t, "producao-em-falta-observe")}}
		d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("com o no em observe o plano sai terminal/0, como hoje; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
		}
		if !strings.Contains(d.stdout, "execucao: no read_notes ORIGEM MEDIDA estado=missing — so medicao") ||
			!temSerie(d.metricas, serie(metricaOrigemDesignacao, "estado", "missing"), 1) ||
			strings.Contains(d.metricas, metricaOrigemTamanho+"{") {
			t.Fatalf("a origem em falta mede-se como missing, sem tamanho:\n%s\n%s", d.stdout, d.metricas)
		}
	})

	// (5) NÓ `aos` ANTERIOR: não anuncia, e o decoder dele recusa campos que não conhece. Em
	// `observe` o `aos-orq` submete como hoje, sem os campos, e regista que não mediu.
	t.Run("Observe_NoAnterior_NaoEnviaENaoMede", func(t *testing.T) {
		p := aos495FormaDeProducao(t, "enforce")
		// O catálogo do nó real SEM os dois anúncios: o `GET /tools` de um nó anterior ao AOS-494.
		var catalogo map[string]json.RawMessage
		if err := json.Unmarshal(p.catalogo, &catalogo); err != nil {
			t.Fatal(err)
		}
		if _, tem := catalogo["output_source"]; !tem {
			t.Fatal("pre-condicao: o anuncio do fio do no real traz output_source")
		}
		delete(catalogo, "output_source")
		delete(catalogo, "completion_contract")
		anterior, _ := json.Marshal(catalogo)
		f := &aos495No{catalogo: anterior, estrito: true}
		d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
		if f.recusados != 0 {
			t.Fatalf("o no anterior recusou %d submissao(oes) com 400: o aos-orq enviou um campo que ele nao conhece\n%s", f.recusados, d.stdout)
		}
		if d.classe != "terminal" || d.codigo != exitOK {
			t.Fatalf("contra um no anterior o plano corre como hoje, terminal/0; saiu %s/%d\n%s", d.classe, d.codigo, d.stdout)
		}
		if _, _, presente := origemEnviada(f.corpo(run, "read_notes")); presente {
			t.Fatal("a um no que nao anuncia, a declaracao de origem nao vai")
		}
		for _, quer := range []string{
			"saida por referencia (AOS-499): modo observe, NAO MEDIDO — o no nao anuncia o suporte",
			"execucao: no read_notes saida por referencia: classe=candidato — NAO MEDIDO: o no aos nao anuncia o suporte, e a declaracao nao vai\n",
			"execucao: no read_notes ORIGEM MEDIDA estado=nao_medido — so medicao",
		} {
			if !strings.Contains(d.stdout, quer) {
				t.Fatalf("faltou no log da drenagem %q:\n%s", quer, d.stdout)
			}
		}
		if !temSerie(d.metricas, serie(metricaOrigemDesignacao, "estado", estadoNaoMedido), 1) ||
			!temSerie(d.metricas, serie(metricaNosPorEstrutura, "classe", classeCandidato), 1) {
			t.Fatalf("o candidato nao medido conta-se:\n%s", d.metricas)
		}
	})

	// (5b) O NÓ ANUNCIA O CONTRATO E NÃO A ORIGEM (um nó do AOS-494, anterior ao AOS-498): leva o
	// contrato, como hoje, e não leva a declaração de origem.
	t.Run("Observe_NoDoAOS494_LevaContratoENaoAOrigem", func(t *testing.T) {
		p := aos495FormaDeProducao(t, "enforce")
		var catalogo map[string]json.RawMessage
		if err := json.Unmarshal(p.catalogo, &catalogo); err != nil {
			t.Fatal(err)
		}
		delete(catalogo, "output_source")
		semOrigem, _ := json.Marshal(catalogo)
		f := &aos495No{catalogo: semOrigem}
		d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
		corpo := f.corpo(run, "read_notes")
		if _, ok := contratoEnviado(corpo); !ok {
			t.Fatalf("o no anuncia o contrato: o corpo leva-o, como hoje: %v", corpo)
		}
		if _, _, presente := origemEnviada(corpo); presente {
			t.Fatalf("o no nao anuncia a origem: o corpo nao a leva: %v", corpo)
		}
		if d.codigo != exitOK {
			t.Fatalf("o plano sai como hoje; saiu %d\n%s", d.codigo, d.stdout)
		}
	})

	// (6) O ANÚNCIO NÃO SE LEU: pára, e o pedido volta à fila — a regra do AOS-495, que vale
	// para os dois anúncios porque se lêem na mesma resposta.
	t.Run("Observe_AnuncioIlegivel_PedidoVoltaAFila", func(t *testing.T) {
		p := aos495FormaDeProducao(t, "enforce")
		f := &aos495No{catalogo: p.catalogo, toolsNaLeitura: func(n int) (int, []byte) {
			if n >= 3 {
				return http.StatusServiceUnavailable, []byte(`{"error":"indisponivel"}`)
			}
			return 0, nil
		}}
		d := aos499Consumir(t, bin, f, run, p.plano, p.snapshot, "observe")
		if d.classe != "transitorio" || d.codigo != exitErro || !strings.HasSuffix(d.detalhe, " erro=anuncio_ilegivel") || f.submissoes != 0 {
			t.Fatalf("um anuncio que nao se leu da transitorio/1 e nenhuma submissao; veio %s/%d %q, %d submissao(oes)\n%s", d.classe, d.codigo, d.detalhe, f.submissoes, d.stdout)
		}
		if strings.Contains(d.stdout, "saida por referencia (AOS-499)") {
			t.Fatalf("com o anuncio por ler nao ha banner da saida por referencia:\n%s", d.stdout)
		}
	})

	// (7) O INTERRUPTOR RECUSA O QUE NÃO CONHECE, E `on`: antes de reclamar o pedido.
	for _, valor := range []string{"on", "enforce", "Observe", "1"} {
		t.Run("ValorRecusado/"+valor, func(t *testing.T) {
			p := aos495FormaDeProducao(t, "enforce")
			f := &aos495No{catalogo: p.catalogo}
			srv := f.servidor(t)
			dir := t.TempDir()
			cred := filepath.Join(dir, "nhi.jwt")
			escrever(t, cred, "nhi-do-operador")
			snap := filepath.Join(dir, "snap.json")
			escrever(t, snap, p.snapshot)
			fix := filepath.Join(dir, "plano.json")
			escrever(t, fix, p.plano)
			f.mu.Lock()
			f.ofertas = append(f.ofertas, pedidoReclamado{RunID: run, Objective: "ler", Geracao: 1})
			f.mu.Unlock()
			env := []string{"AOS_ORQ_NODE_URL=" + srv.URL, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE=", "AOS_ORQ_SAIDA_POR_REFERENCIA=" + valor}
			r := correrComEnv(t, env, bin, "consume", "--wal", filepath.Join(dir, "consume.wal"), "--snapshot", snap,
				"--decompose-fixture", fix, "--poll-interval", "20ms", "--max", "1", "--plan-timeout", "30s")
			if r.code == exitOK || !strings.Contains(r.stderr, "AOS_ORQ_SAIDA_POR_REFERENCIA invalido") {
				t.Fatalf("o consume tinha de recusar o arranque com %q; saiu %d\n%s\n%s", valor, r.code, r.stdout, r.stderr)
			}
			f.mu.Lock()
			pendentes, desfechos, submissoes := len(f.ofertas), len(f.desfechos), f.submissoes
			f.mu.Unlock()
			if pendentes != 1 || desfechos != 0 || submissoes != 0 {
				t.Fatalf("a recusa e de configuracao: nada se reclama nem se submete (ofertas=%d desfechos=%d submissoes=%d)", pendentes, desfechos, submissoes)
			}
			// O `serve` manual recusa o mesmo, antes da posse.
			s := correrComEnv(t, env, bin, "serve", "--wal", filepath.Join(dir, "serve.wal"), "--run", "plan-aos499-serve",
				"--goal", "ler", "--snapshot", snap, "--decompose-fixture", fix)
			if s.code == exitOK || !strings.Contains(s.stderr, "AOS_ORQ_SAIDA_POR_REFERENCIA invalido") || strings.Contains(s.stdout, "posse: run=") {
				t.Fatalf("o serve tinha de recusar o arranque com %q, antes da posse; saiu %d\n%s\n%s", valor, s.code, s.stdout, s.stderr)
			}
		})
	}
}

// TestAOS499_Interruptor: a omissão é `off`; `observe` mede; `on` e tudo o resto recusam, sem
// repetir o valor recusado.
func TestAOS499_Interruptor(t *testing.T) {
	for bruto, quer := range map[string]string{"": saidaPorReferenciaOff, "off": saidaPorReferenciaOff, "observe": saidaPorReferenciaObserve} {
		if got, err := modoDaSaidaPorReferencia(bruto); err != nil || got != quer {
			t.Errorf("modoDaSaidaPorReferencia(%q) = %q, %v; quero %q", bruto, got, err, quer)
		}
	}
	for _, bruto := range []string{"on", "ON", "Observe", "OFF", "enforce", "measure", "binding", "true", "1", "observe "} {
		got, err := modoDaSaidaPorReferencia(bruto)
		if !errors.Is(err, ErrSaidaPorReferencia) || got != "" {
			t.Errorf("modoDaSaidaPorReferencia(%q) = %q, %v; quero a recusa", bruto, got, err)
			continue
		}
		if bruto != "on" && strings.Contains(err.Error(), "`"+bruto+"`") {
			t.Errorf("a recusa de %q repete o valor: %v", bruto, err)
		}
	}
	if _, err := modoDaSaidaPorReferencia("on"); err == nil || !strings.Contains(err.Error(), "AOS-501") {
		t.Errorf("a recusa de `on` diz de quem e: %v", err)
	}
}

// TestAOS499_Interruptor_DoAmbiente fixa o que a LEITURA da variável faz com os espaços (mutação
// Q3b da revisão: nada dizia se se aparavam). Aparam-se nas pontas — um `.env` com um espaço ou
// uma quebra de linha a mais não é um valor desconhecido —, e só nas pontas; as maiúsculas não se
// dobram. A função que valida o valor já lido continua exacta ([TestAOS499_Interruptor]).
func TestAOS499_Interruptor_DoAmbiente(t *testing.T) {
	for bruto, quer := range map[string]string{
		"": saidaPorReferenciaOff, "   ": saidaPorReferenciaOff, "off": saidaPorReferenciaOff, " off\n": saidaPorReferenciaOff,
		"observe": saidaPorReferenciaObserve, " observe ": saidaPorReferenciaObserve, "\tobserve\r\n": saidaPorReferenciaObserve,
	} {
		t.Setenv("AOS_ORQ_SAIDA_POR_REFERENCIA", bruto)
		if got, err := modoDaSaidaPorReferenciaDoAmbiente(); err != nil || got != quer {
			t.Errorf("AOS_ORQ_SAIDA_POR_REFERENCIA=%q: modo = %q, %v; quero %q", bruto, got, err, quer)
		}
	}
	for _, bruto := range []string{" Observe ", "obs erve", " on ", "observe,off"} {
		t.Setenv("AOS_ORQ_SAIDA_POR_REFERENCIA", bruto)
		if got, err := modoDaSaidaPorReferenciaDoAmbiente(); !errors.Is(err, ErrSaidaPorReferencia) || got != "" {
			t.Errorf("AOS_ORQ_SAIDA_POR_REFERENCIA=%q: modo = %q, %v; quero a recusa", bruto, got, err)
		}
	}
}

// TestAOS499_CandidatoEstrutural: a regra, caso a caso.
func TestAOS499_CandidatoEstrutural(t *testing.T) {
	aberta := plan.Output{Name: "conteudo", Type: plan.PayloadRecord}
	outraAberta := plan.Output{Name: "resumo", Type: plan.PayloadSummary}
	fechada := plan.Output{Name: "veredicto", Type: plan.PayloadVerdict}
	consome := []plan.PayloadEdge{{From: "outro", Output: "conteudo", Type: plan.PayloadRecord}}
	for _, c := range []struct {
		nome  string
		no    plan.Node
		tools []string
		quer  string
	}{
		{"uma tool, uma saida aberta, sem consumes", plan.Node{Outputs: []plan.Output{aberta}}, []string{"doc_read"}, classeCandidato},
		{"uma saida aberta e uma fechada", plan.Node{Outputs: []plan.Output{aberta, fechada}}, []string{"doc_read"}, classeCandidato},
		{"verificador", plan.Node{Role: plan.RoleVerifier, Outputs: []plan.Output{aberta}}, []string{"doc_read"}, classeNaoCandidato},
		{"sem tools", plan.Node{Outputs: []plan.Output{aberta}}, nil, classeNaoCandidato},
		{"duas tools", plan.Node{Outputs: []plan.Output{aberta}}, []string{"doc_read", "doc_write"}, classeNaoCandidato},
		{"sem saidas", plan.Node{}, []string{"doc_read"}, classeNaoCandidato},
		{"so uma saida fechada", plan.Node{Outputs: []plan.Output{fechada}}, []string{"doc_read"}, classeNaoCandidato},
		{"duas saidas abertas", plan.Node{Outputs: []plan.Output{aberta, outraAberta}}, []string{"doc_read"}, classeNaoCandidato},
		{"com consumes", plan.Node{Outputs: []plan.Output{aberta}, Consumes: consome}, []string{"doc_read"}, classeNaoCandidato},
	} {
		if got := classeEstrutural(c.no, c.tools); got != c.quer {
			t.Errorf("%s: classe = %q, quero %q", c.nome, got, c.quer)
		}
	}
}

// TestAOS499_OrigemDeclaravel: a declaração só vai quando não acrescenta uma razão para o run
// não arrancar — a tool está no contrato do mesmo pedido, e o nome tem a forma que a âncora admite.
func TestAOS499_OrigemDeclaravel(t *testing.T) {
	for _, c := range []struct {
		tool     string
		contrato []string
		quer     bool
	}{
		{"doc_read", []string{"doc_read"}, true},
		{"doc_read", nil, false},                   // o nó não anuncia o contrato
		{"doc_read", []string{"doc_write"}, false}, // outro contrato
		{"ler documento", []string{"ler documento"}, false},
		{strings.Repeat("a", 129), []string{strings.Repeat("a", 129)}, false},
		{strings.Repeat("a", 128), []string{strings.Repeat("a", 128)}, true},
	} {
		if got := origemDeclaravel(c.tool, c.contrato); got != c.quer {
			t.Errorf("origemDeclaravel(%.20q, %d no contrato) = %v, quero %v", c.tool, len(c.contrato), got, c.quer)
		}
	}
}

// TestAOS499_ClassesDeTamanhoERazao: as fronteiras das classes.
func TestAOS499_ClassesDeTamanhoERazao(t *testing.T) {
	for n, quer := range map[int]string{0: tamanhoVazio, 1: tamanhoAte1K, 1024: tamanhoAte1K, 1025: tamanhoAte16K, 16 << 10: tamanhoAte16K,
		16<<10 + 1: tamanhoAte128K, maxPayloadBytes: tamanhoAte128K, maxPayloadBytes + 1: tamanhoAcima128K} {
		if got := classeDeTamanho(n); got != quer {
			t.Errorf("classeDeTamanho(%d) = %q, quero %q", n, got, quer)
		}
	}
	for _, c := range []struct {
		texto, origem int
		quer          string
	}{
		{10, 0, razaoOrigemVazia}, {0, 10, razaoTextoVazio}, {49, 100, razaoAbaixoDe05}, {50, 100, razaoDe05A09}, {89, 100, razaoDe05A09},
		{90, 100, razaoDe09A11}, {100, 100, razaoDe09A11}, {110, 100, razaoDe09A11}, {111, 100, razaoDe11A2}, {199, 100, razaoDe11A2},
		{200, 100, razaoAcimaDe2}, {5000, 100, razaoAcimaDe2},
	} {
		if got := classeDeRazao(c.texto, c.origem); got != c.quer {
			t.Errorf("classeDeRazao(%d, %d) = %q, quero %q", c.texto, c.origem, got, c.quer)
		}
	}
}

// TestAOS499_Anuncio_OClienteLeOFioDoNo: o cliente lê o anúncio da origem dos bytes que o NÓ REAL
// responde, e não o inventa onde ele não está.
func TestAOS499_Anuncio_OClienteLeOFioDoNo(t *testing.T) {
	ler := func(t *testing.T, corpo []byte) (anuncioDoNo, error) {
		t.Helper()
		f := &aos495No{catalogo: corpo}
		srv := f.servidor(t)
		return (&nodeClient{base: srv.URL, http: srv.Client()}).ContratoDeConclusao(context.Background())
	}
	for _, modo := range []string{"enforce", "observe"} {
		a, err := ler(t, aos495Fio(t, "tools-"+modo))
		if err != nil || !a.aceita || a.modo != modo || !a.origem {
			t.Fatalf("o anuncio do no real (%s) traz o contrato e a origem; veio %+v, %v", modo, a, err)
		}
	}
	// O NÓ REAL COM O VEREDICTO DESLIGADO (`off`): anuncia o contrato com o modo `off` e NÃO anuncia
	// a origem — em `off` o kernel não lê a declaração. É o ficheiro que o nó gera
	// (`aos498_fio/tools-off.json`); antes este teste apagava a chave à mão a um anúncio de outro modo.
	if a, err := ler(t, aos499FioDoNo(t, "tools-off")); err != nil || !a.aceita || a.modo != "off" || a.origem {
		t.Fatalf("o anuncio do no real em off traz o contrato (modo off) e NAO a origem; veio %+v, %v", a, err)
	}
	for nome, c := range map[string]struct {
		corpo  string
		aceita bool
	}{
		"no do AOS-494, sem a chave": {`{"tools":[],"completion_contract":{"mode":"enforce"}}`, true},
		"no anterior ao AOS-494":     {`{"tools":[]}`, false},
		"chave presente sem measure": {`{"tools":[],"completion_contract":{"mode":"enforce"},"output_source":{"bindings":["binding"]}}`, true},
		"chave presente e vazia":     {`{"tools":[],"completion_contract":{"mode":"enforce"},"output_source":{}}`, true},
	} {
		a, err := ler(t, []byte(c.corpo))
		if err != nil || a.origem || a.aceita != c.aceita {
			t.Errorf("%s: anuncio = %+v, %v; quero origem=false aceita=%v", nome, a, err, c.aceita)
		}
	}
	// A chave com a forma errada é um anúncio ILEGÍVEL, e não um «não»: o `serve` pára.
	if _, err := ler(t, []byte(`{"tools":[],"completion_contract":{"mode":"enforce"},"output_source":"measure"}`)); err == nil {
		t.Error("um output_source com a forma errada tinha de ser um anuncio ilegivel")
	}
}
