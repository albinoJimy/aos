package main

// AOS-443 — O CAMINHO DO PLANO OBSERVA-SE: MÉTRICAS DA DRENAGEM, E UM RESUMO NO DESFECHO TAMBÉM EM
// SUCESSO.
//
// Medido em produção: para encontrar o AOS-438 foi preciso segurar o lock da drenagem e correr o
// `consume` à mão, porque o stdout ia para o journal do sistema, o `aos-orq` não tinha métricas, e
// o `GET /plans/{id}` só trazia `detail` com erro — «terminado com sucesso» e «terminado» diziam o
// mesmo.
//
// Os testes unitários fixam a forma (o resumo, a acumulação, a escrita atómica); o do binário real
// corre o `consume` contra o nó falso do AOS-442 e lê o que chega ao nó e o que fica no disco.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	planner "github.com/aos-ref/control-plane/orchestrator/planner"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/substrate/eventstore"
)

// ── o resumo do desfecho ─────────────────────────────────────────────────────────────────────

// O RESUMO EXISTE EM SUCESSO, e tem a forma exacta que o nó grava e o `GET /plans/{id}` serve.
func TestAOS443ResumoEmSucessoTemAFormaDeclarada(t *testing.T) {
	r := resumoDoPedido{origem: origemDocumento, geracao: 2, nos: 3, duracao: 1500 * time.Millisecond}
	if got, quer := detalheDoDesfecho(r), "resumo: origem=documento geracao=2 nos=3 duracao_s=1.500"; got != quer {
		t.Fatalf("detail em sucesso:\n  veio  %q\n  quer  %q", got, quer)
	}
	r.nos, r.erro = -1, tipoDoErro(errors.New("rede em baixo"))
	if got, quer := detalheDoDesfecho(r), "resumo: origem=documento geracao=2 nos=- duracao_s=1.500 erro=generico"; got != quer {
		t.Fatalf("detail com erro:\n  veio  %q\n  quer  %q", got, quer)
	}
}

// O TEXTO DO ERRO NÃO VAI PARA O NÓ (revisão do AOS-443, M4). Um erro do `serve` pode citar conteúdo
// escrito pelo modelo — o `plan.Decode` cita `node_id`s e campos com `%q` —, e o nó grava o `detail`
// em claro no stream da fila, fora do alcance do `/dsar/erase`. Só vai o tipo, e cada sentinela que
// o [codigoDe] conhece tem um tipo seu (nenhum cai no `generico`).
func TestAOS443DetalheNaoLevaOTextoDoErro(t *testing.T) {
	const pessoal = "Maria-da-Silva-NIF-123456789"
	sentinelas := map[error]string{
		eventstore.ErrWALHeld:                  "wal_detido",
		durable.ErrLeaseHeld:                   "posse_negada",
		durable.ErrStaleFencingToken:           "posse_superada",
		durable.ErrLeaseSuperseded:             "posse_superada",
		durable.ErrLeaseExpired:                "posse_superada",
		errPlanoPendente:                       "plano_pendente",
		errDecisaoRecusada:                     "decisao_recusada",
		errNosEmVoo:                            "nos_em_voo",
		planner.ErrPlanRejected:                "plano_recusado_pelo_planeador",
		errDocumentoDoPlanoRecusado:            "documento_recusado",
		ErrSnapshotNaoCorresponde:              "snapshot_nao_corresponde",
		ErrSnapshotDiferenteDoSelado:           "snapshot_diferente_do_selado",
		errors.New("sem sentinela " + pessoal): "generico",
	}
	for s, quer := range sentinelas {
		err := fmt.Errorf("plan: node_id %q invalido: %w", pessoal, s)
		codigo, _, tipo := desfechoDoServe(err)
		if tipo != quer {
			t.Errorf("%v: tipo %q, quer %q", s, tipo, quer)
		}
		if quer != "generico" && codigo == exitErro {
			t.Errorf("%v: o tipo conhece-o e o codigoDe não — as duas tabelas divergiram", s)
		}
		d := detalheDoDesfecho(resumoDoPedido{origem: origemDecomposicao, geracao: 1, nos: -1, erro: tipo})
		if strings.Contains(d, "Maria") || strings.Contains(d, "node_id") || len(d) > 512 {
			t.Errorf("o detail leva o texto do erro (ou passa dos 512 bytes do nó): %q", d)
		}
	}
}

func TestAOS443OrigemDoResumo(t *testing.T) {
	casos := []struct {
		nome   string
		o      origemDoPlano
		correu bool
		classe string
		quer   string
	}{
		{"decompoe", origemDoPlano{}, true, "terminal", origemDecomposicao},
		{"retoma pelo documento", origemDoPlano{porDocumento: true, jaValidado: true}, true, "terminal", origemDocumento},
		{"re-verificacao sem serve", origemDoPlano{jaValidado: true}, false, "aguarda_humano", origemReverificacao},
		{"recusa antes do serve", origemDoPlano{jaValidado: true}, false, "terminal", origemSemServe},
		{"documento ilegivel antes do serve", origemDoPlano{}, false, "terminal", origemSemServe},
	}
	for _, c := range casos {
		if got := origemDoResumo(c.o, c.correu, c.classe); got != c.quer {
			t.Errorf("%s: origem %q, quer %q", c.nome, got, c.quer)
		}
	}
}

// UM DOCUMENTO QUE JÁ ESTAVA NO DISCO E QUE O LOG NÃO ANCORA NÃO CONTA — é o documento plantado que
// o AOS-442 recusa usar; contá-lo daria o número de nós de um plano que não correu.
func TestAOS443NosDoDocumentoSoContaODestePedido(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.plan.json")
	escrever(t, doc, planoFixtureDuasFolhasComSnapshotAOS408)
	antigo := time.Now().Add(-time.Hour)
	if err := os.Chtimes(doc, antigo, antigo); err != nil {
		t.Fatal(err)
	}
	agora := time.Now()
	if n := nosDoDocumento(doc, false, agora); n != -1 {
		t.Fatalf("documento anterior ao pedido e sem ancora contou %d nos", n)
	}
	if n := nosDoDocumento(doc, true, agora); n != 2 {
		t.Fatalf("documento ancorado no log: %d nos, quer 2", n)
	}
	if n := nosDoDocumento(doc, false, antigo.Add(-time.Minute)); n != 2 {
		t.Fatalf("documento escrito durante o pedido: %d nos, quer 2", n)
	}
	if n := nosDoDocumento(filepath.Join(dir, "nao-existe"), true, agora); n != -1 {
		t.Fatalf("sem documento: %d, quer -1", n)
	}
	escrever(t, doc, `{"truncado`)
	if n := nosDoDocumento(doc, true, agora); n != -1 {
		t.Fatalf("documento que não descodifica: %d, quer -1", n)
	}
}

// ── as métricas ──────────────────────────────────────────────────────────────────────────────

// A REGRA DAS FALHAS SEGUIDAS (revisão do AOS-443, M2). O 8 é o caminho feliz de um plano mais
// longo do que o prazo — a drenagem seguinte retoma-o —, e três drenagens dele não podem disparar o
// aviso; três genéricos seguidos têm de o disparar.
func TestAOS443FalhasSeguidasNaoContamONosEmVoo(t *testing.T) {
	r := resumoDoPedido{origem: origemDocumento, geracao: 1, nos: 2}
	correr := func(desfechos ...[2]any) *metricasDoConsumo {
		m := &metricasDoConsumo{series: map[string]float64{}}
		for _, d := range desfechos {
			m.registarDesfecho(r, d[0].(string), d[1].(int), true)
		}
		m.registarDrenagem("ok", len(desfechos), time.Unix(1, 0))
		return m
	}
	oito := [2]any{"transitorio", exitNosEmVoo}
	exigirSerie(t, correr(oito, oito, oito), metricaFalhasConsecutivas, 0)
	um := [2]any{"transitorio", exitErro}
	exigirSerie(t, correr(um, oito, um, oito, um), metricaFalhasConsecutivas, 3) // o 8 não zera
	exigirSerie(t, correr(um, um, oito, [2]any{"terminal", exitOK}), metricaFalhasConsecutivas, 0)
	exigirSerie(t, correr(um, um, um), metricaFalhasConsecutivas, 3)
	sete := [2]any{"terminal", exitDecisaoRecusada}
	exigirSerie(t, correr(sete), metricaFalhasConsecutivas, 1) // o ambíguo conta (ver efeitoNasFalhas)
}

// OS CONTADORES ACUMULAM-SE ENTRE DRENAGENS e a série que o sensor lê segue a regra declarada:
// sobe com transitórios e terminais ≠ 0, não se mexe com `aguarda_humano`, volta a 0 com
// terminal/0.
func TestAOS443MetricasAcumulamEntreDrenagens(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), nomeDoFicheiroDeMetricas)
	fim := time.Unix(1790000000, 0)
	drenar := func(f func(m *metricasDoConsumo), tratados int) *metricasDoConsumo {
		t.Helper()
		m, err := lerMetricas(caminho)
		if err != nil {
			t.Fatal(err)
		}
		f(m)
		m.registarDrenagem("ok", tratados, fim)
		if err := escreverMetricas(caminho, m.texto()); err != nil {
			t.Fatal(err)
		}
		return m
	}
	r := resumoDoPedido{origem: origemDecomposicao, geracao: 1, nos: 2, duracao: 2 * time.Second}

	m := drenar(func(m *metricasDoConsumo) {
		m.registarReclamacao(1)
		m.registarDesfecho(r, "transitorio", exitPosseNegada, true)
		m.registarReclamacao(1)
		m.registarDesfecho(r, "terminal", exitDecisaoRecusada, true)
	}, 2)
	exigirSerie(t, m, metricaFalhasConsecutivas, 2)

	r.origem, r.geracao = origemReverificacao, 2
	m = drenar(func(m *metricasDoConsumo) {
		m.registarReclamacao(2)
		m.registarDesfecho(r, "aguarda_humano", exitPendenteDeAprovacao, false)
	}, 1)
	exigirSerie(t, m, metricaFalhasConsecutivas, 2) // esperar por um humano não é falha nem sucesso
	exigirSerie(t, m, metricaNaoReportados, 1)

	r.origem, r.geracao = origemDocumento, 3
	m = drenar(func(m *metricasDoConsumo) {
		m.registarReclamacao(3)
		m.registarDesfecho(r, "terminal", exitOK, true)
	}, 1)
	exigirSerie(t, m, metricaFalhasConsecutivas, 0)
	exigirSerie(t, m, serie(metricaDrenagens, "resultado", "ok"), 3)
	exigirSerie(t, m, metricaReclamados, 4)
	exigirSerie(t, m, metricaRetomas, 2)
	exigirSerie(t, m, serie(metricaOrigem, "origem", origemDecomposicao), 2)
	exigirSerie(t, m, serie(metricaOrigem, "origem", origemReverificacao), 1)
	exigirSerie(t, m, serie(metricaOrigem, "origem", origemDocumento), 1)
	exigirSerie(t, m, serie(metricaDesfechos, "classe", "terminal", "codigo", "0"), 1)
	exigirSerie(t, m, serie(metricaDesfechos, "classe", "terminal", "codigo", "7"), 1)
	exigirSerie(t, m, serie(metricaDuracao+"_count", "classe", "terminal"), 2)
	exigirSerie(t, m, serie(metricaDuracao+"_sum", "classe", "terminal"), 4)
	exigirSerie(t, m, metricaUltimaDrenagem, 1790000000)
	exigirSerie(t, m, metricaUltimaPedidos, 1)

	// O ficheiro relido dá os mesmos bytes: a forma é estável, e o leitor não inventa nada.
	raw, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	relido, err := lerMetricas(caminho)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(relido.texto(), raw) {
		t.Fatalf("reler e reescrever mudou o ficheiro:\n--- no disco\n%s\n--- reescrito\n%s", raw, relido.texto())
	}
	for _, quer := range []string{
		"# TYPE aos_orq_consume_desfechos_total counter\n",
		`aos_orq_consume_desfechos_total{classe="terminal",codigo="0"} 1` + "\n",
		"# TYPE aos_orq_consume_plano_duracao_segundos summary\n",
		"# TYPE aos_orq_consume_falhas_consecutivas gauge\n",
		"aos_orq_consume_falhas_consecutivas 0\n",
	} {
		if !strings.Contains(string(raw), quer) {
			t.Errorf("o ficheiro não tem %q:\n%s", quer, raw)
		}
	}
}

// UM FICHEIRO ANTERIOR QUE NÃO SE ENTENDE RECOMEÇA DO ZERO e diz porquê; uma série que não é deste
// catálogo não é transportada.
func TestAOS443MetricasIlegiveisRecomecamEEstranhasSaem(t *testing.T) {
	dir := t.TempDir()
	mau := filepath.Join(dir, "mau.prom")
	escrever(t, mau, "aos_orq_consume_pedidos_reclamados_total muitos\n")
	m, err := lerMetricas(mau)
	if err == nil {
		t.Fatal("um valor ilegível tinha de dar erro, para quem chama o dizer")
	}
	if len(m.series) != 0 {
		t.Fatalf("recomeçar é do zero, veio %v", m.series)
	}

	estranho := filepath.Join(dir, "estranho.prom")
	escrever(t, estranho, "# HELP x y\nnode_cpu_seconds_total 5\naos_orq_consume_pedidos_reclamados_total 3\n")
	m, err = lerMetricas(estranho)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(m.texto()), "node_cpu") {
		t.Fatalf("uma série de fora do catálogo foi transportada:\n%s", m.texto())
	}
	exigirSerie(t, m, metricaReclamados, 3)

	vazio, err := lerMetricas(filepath.Join(dir, "nao-existe.prom"))
	if err != nil || len(vazio.series) != 0 {
		t.Fatalf("sem ficheiro anterior é o início da história, sem erro: %v %v", vazio.series, err)
	}
}

// A ESCRITA É ATÓMICA: não fica temporário nenhum na pasta, e o ficheiro é legível por outro uid
// (é copiado para fora do volume pelo drenar-planos.sh).
func TestAOS443EscritaAtomicaNaoDeixaTemporarios(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, nomeDoFicheiroDeMetricas)
	for i := 0; i < 3; i++ {
		if err := escreverMetricas(caminho, []byte("aos_orq_consume_pedidos_reclamados_total 1\n")); err != nil {
			t.Fatal(err)
		}
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 1 || entradas[0].Name() != nomeDoFicheiroDeMetricas {
		var nomes []string
		for _, e := range entradas {
			nomes = append(nomes, e.Name())
		}
		t.Fatalf("a pasta devia ter só o ficheiro de métricas, tem %v", nomes)
	}
}

func TestAOS443CaminhoDasMetricas(t *testing.T) {
	if got := caminhoDasMetricas("/x/m.prom", substrato{wal: "/var/lib/aos-orq/consume.wal"}); got != "/x/m.prom" {
		t.Errorf("o explícito ganha: %q", got)
	}
	if got, quer := caminhoDasMetricas("", substrato{wal: filepath.Join("var", "consume.wal")}), filepath.Join("var", nomeDoFicheiroDeMetricas); got != quer {
		t.Errorf("por omissão ao lado do WAL: %q, quer %q", got, quer)
	}
	if got := caminhoDasMetricas("", substrato{nats: "nats://x:4222"}); got != "" {
		t.Errorf("sobre --nats sem --metrics-file não se escreve: %q", got)
	}
}

// OS SCRIPTS DO SERVIDOR LÊEM O FICHEIRO SEM PARSER: `awk '$1 == "<nome>" { print $2 }'`, e o bash
// só compara inteiros. As duas séries que eles lêem têm por isso de sair SEM rótulos e como
// inteiro, e os nomes e caminhos que os scripts usam têm de ser os que este código escreve. Se um
// dos lados mudar sozinho, o sensor deixa de ver as falhas — em silêncio, porque um valor ilegível
// não dispara. Este teste é o que o impede (o molde do TestAOS437OExpDeTopoESeguidoDoJti).
func TestAOS443ContratoComOsScriptsDoServidor(t *testing.T) {
	m := &metricasDoConsumo{series: map[string]float64{}}
	m.registarDesfecho(resumoDoPedido{origem: origemDecomposicao, geracao: 1, duracao: 1234 * time.Millisecond}, "transitorio", exitErro, true)
	m.registarDrenagem("ok", 1, time.Unix(1790000123, 0))
	texto := string(m.texto())
	for _, quer := range []string{
		"\n" + metricaFalhasConsecutivas + " 1\n",
		"\n" + metricaUltimaDrenagem + " 1790000123\n",
	} {
		if !strings.Contains(texto, quer) {
			t.Errorf("o ficheiro não tem a linha %q, que os scripts lêem com awk:\n%s", strings.TrimSpace(quer), texto)
		}
	}

	drenar := lerDoRepo(t, "deploy", "server", "drenar-planos.sh")
	for _, quer := range []string{
		`METRICAS_NO_VOLUME="` + nomeDoFicheiroDeMetricas + `"`,
		`--wal /var/lib/aos-orq/consume.wal`,
		`$1 == "` + metricaUltimaDrenagem + `"`,
		`METRICAS="${LOG_DIR}/` + nomeDoFicheiroDeMetricas + `"`,
	} {
		if !strings.Contains(drenar, quer) {
			t.Errorf("drenar-planos.sh já não tem %q", quer)
		}
	}
	// O script NÃO passa `--metrics-file` (revisão do AOS-443, M1): o deploy sincroniza os scripts
	// antes de trocar a imagem, e o rollback repõe a imagem sem repor os scripts — um script novo
	// com um binário antigo recusaria a flag e parava a fila. O caminho vem do `--wal`, e tem de ser
	// o que o script copia do volume.
	if strings.Contains(drenar, "--metrics-file") {
		t.Error("drenar-planos.sh passa --metrics-file: um rollback da imagem pararia a fila (flag desconhecida)")
	}
	if got := caminhoDasMetricas("", substrato{wal: "/var/lib/aos-orq/consume.wal"}); filepath.ToSlash(got) != "/var/lib/aos-orq/"+nomeDoFicheiroDeMetricas {
		t.Errorf("o caminho por omissão (%s) não é o que o script copia do volume", got)
	}
	alerta := lerDoRepo(t, "deploy", "server", "alerta-nhi.sh")
	for _, quer := range []string{
		`${AOS_DIR}/logs/` + nomeDoFicheiroDeMetricas,
		`$1 == "` + metricaFalhasConsecutivas + `"`,
	} {
		if !strings.Contains(alerta, quer) {
			t.Errorf("alerta-nhi.sh já não tem %q", quer)
		}
	}
	// O log da drenagem não pode voltar a levar o objectivo: a linha do `consume` que o imprimia é
	// a única fonte, e foi tirada na origem.
	consumir := lerDoRepo(t, "packages", "cmd", "aos-orq", "consumir.go")
	if strings.Contains(consumir, "objectivo=%q") {
		t.Error("o consume voltou a imprimir o objectivo do pedido no stdout (journal e log da drenagem)")
	}
}

func exigirSerie(t *testing.T, m *metricasDoConsumo, chave string, quer float64) {
	t.Helper()
	if got, ok := m.series[chave]; !ok || got != quer {
		t.Fatalf("%s = %v (presente=%v), quer %v\n%s", chave, got, ok, quer, m.texto())
	}
}

// ── o binário real ───────────────────────────────────────────────────────────────────────────

// aos443Espiao fica à frente do nó falso do AOS-442 e guarda o CORPO de cada desfecho — o nó
// falso só guarda classe e código, e o que este ticket muda é o `detalhe`.
type aos443Espiao struct {
	mu        sync.Mutex
	desfechos []map[string]any
}

func (e *aos443Espiao) servidor(t *testing.T, destino string) *httptest.Server {
	t.Helper()
	alvo, err := url.Parse(destino)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(alvo)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/plans/outcome" {
			corpo, _ := io.ReadAll(r.Body)
			var d map[string]any
			_ = json.Unmarshal(corpo, &d)
			e.mu.Lock()
			e.desfechos = append(e.desfechos, d)
			e.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(corpo))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (e *aos443Espiao) detalhes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ds []string
	for _, d := range e.desfechos {
		s, _ := d["detalhe"].(string)
		ds = append(ds, s)
	}
	return ds
}

// TestAOS443ComOBinarioReal — uma retoma completa (transitório na 1.ª geração, terminal/0 pela
// retoma na 2.ª), vista pelos três olhos do ticket: o `detail` que chega ao nó, o ficheiro de
// métricas, e o stdout da drenagem, que não pode levar o objectivo.
func TestAOS443ComOBinarioReal(t *testing.T) {
	bin := construir(t)
	no := &aos442No{nuncaAcaba: true}
	a := novoAmbiente442(t, bin, no)
	espiao := &aos443Espiao{}
	frente := espiao.servidor(t, strings.TrimPrefix(a.env[0], "AOS_ORQ_NODE_URL="))
	a.env[0] = "AOS_ORQ_NODE_URL=" + frente.URL
	metricas := filepath.Join(a.dir, nomeDoFicheiroDeMetricas) // ao lado do WAL, por omissão

	const run = "plan-aos443-retoma"
	const objectivo = "OBJECTIVO-DO-TITULAR-443 com o nome da Maria"
	nos := aos443NosDoFixture(t)

	no.oferecer(pedidoReclamado{RunID: run, Objective: objectivo, Geracao: 1})
	r1 := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "300ms")

	m1, err := lerMetricas(metricas)
	if err != nil {
		t.Fatal(err)
	}
	exigirSerie(t, m1, metricaFalhasConsecutivas, 0) // o 8 é o caminho feliz da retoma: neutro
	exigirSerie(t, m1, serie(metricaDesfechos, "classe", "transitorio", "codigo", "8"), 1)

	no.mu.Lock()
	no.nuncaAcaba = false
	no.mu.Unlock()
	no.oferecer(pedidoReclamado{RunID: run, Objective: objectivo, Geracao: 2})
	r2 := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "30s")

	// (1) O `detail` — com erro na 1.ª, e TAMBÉM em sucesso na 2.ª.
	ds := espiao.detalhes()
	if len(ds) != 2 {
		t.Fatalf("esperava 2 desfechos, vieram %d: %q", len(ds), ds)
	}
	if p := "resumo: origem=decomposicao geracao=1 nos=" + nos + " duracao_s="; !strings.HasPrefix(ds[0], p) || !strings.HasSuffix(ds[0], " erro=nos_em_voo") {
		t.Errorf("detail da 1.ª geração (transitória):\n  veio %q\n  quer prefixo %q e o TIPO do erro no fim", ds[0], p)
	}
	if p := "resumo: origem=documento geracao=2 nos=" + nos + " duracao_s="; !strings.HasPrefix(ds[1], p) || strings.Contains(ds[1], "erro") {
		t.Errorf("detail da 2.ª geração (SUCESSO) tinha de levar o resumo e nenhum erro:\n  veio %q\n  quer prefixo %q", ds[1], p)
	}

	// (2) As métricas, acumuladas pelas duas drenagens.
	m, err := lerMetricas(metricas)
	if err != nil {
		t.Fatal(err)
	}
	exigirSerie(t, m, serie(metricaDrenagens, "resultado", "ok"), 2)
	exigirSerie(t, m, metricaReclamados, 2)
	exigirSerie(t, m, metricaRetomas, 1)
	exigirSerie(t, m, serie(metricaOrigem, "origem", origemDecomposicao), 1)
	exigirSerie(t, m, serie(metricaOrigem, "origem", origemDocumento), 1)
	exigirSerie(t, m, serie(metricaDesfechos, "classe", "terminal", "codigo", "0"), 1)
	exigirSerie(t, m, metricaFalhasConsecutivas, 0)
	exigirSerie(t, m, serie(metricaDuracao+"_count", "classe", "terminal"), 1)

	// (3) Nem o objectivo nem o `run_id` saem para fora do nó por estes caminhos: o ficheiro de
	// métricas não leva identificador nenhum, e o stdout/stderr (journal e log da drenagem) não
	// leva o objectivo.
	raw, err := os.ReadFile(metricas)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), run) || strings.Contains(string(raw), "Maria") {
		t.Errorf("o ficheiro de métricas leva um identificador:\n%s", raw)
	}
	for _, r := range []resultado{r1, r2} {
		if strings.Contains(r.stdout+r.stderr, "Maria") || strings.Contains(r.stdout+r.stderr, "OBJECTIVO-DO-TITULAR") {
			t.Errorf("o output da drenagem leva o objectivo do titular:\n%s\n%s", r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, "objectivo_bytes=") {
			t.Errorf("a linha `reclamado:` perdeu o tamanho do objectivo:\n%s", r.stdout)
		}
		if !strings.Contains(r.stdout, "metricas: "+metricas+" (drenagem ok)") {
			t.Errorf("a drenagem não disse onde escreveu as métricas:\n%s", r.stdout)
		}
	}
	if !strings.Contains(r1.stdout, "desfecho: run="+run+" codigo=8 classe=transitorio origem=decomposicao") {
		t.Errorf("a linha `desfecho:` da 1.ª geração:\n%s", r1.stdout)
	}
	if !strings.Contains(r2.stdout, "desfecho: run="+run+" codigo=0 classe=terminal origem=documento geracao=2") {
		t.Errorf("a linha `desfecho:` do stdout tinha de levar o resumo:\n%s", r2.stdout)
	}
}

// UMA DRENAGEM QUE NÃO CONSEGUE ESCREVER AS MÉTRICAS FALHA — o sensor leria um ficheiro parado e
// diria «tudo bem» sobre uma fila que ninguém vê.
//
// In-process e com a fila vazia: o que se prova é o `defer` do `consume`, não o pedido.
func TestAOS443SemMetricasADrenagemFalha(t *testing.T) {
	a := novoAmbiente442(t, "", &aos442No{})
	for _, kv := range a.env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	impossivel := filepath.Join(a.dir, "nao-existe", "sub", nomeDoFicheiroDeMetricas)
	err := cmdConsume([]string{"--wal", a.wal, "--snapshot", a.snap, "--max", "1", "--metrics-file", impossivel})
	if err == nil || !strings.Contains(err.Error(), "metricas da drenagem NAO escritas") {
		t.Fatalf("sem métricas escritas a drenagem tinha de falhar e dizê-lo; veio %v", err)
	}

	// E com o ficheiro no sítio, a mesma drenagem vazia sai 0 e conta-se.
	caminho := filepath.Join(a.dir, nomeDoFicheiroDeMetricas)
	if err := cmdConsume([]string{"--wal", a.wal, "--snapshot", a.snap, "--max", "1"}); err != nil {
		t.Fatalf("drenagem vazia: %v", err)
	}
	m, err := lerMetricas(caminho)
	if err != nil {
		t.Fatal(err)
	}
	exigirSerie(t, m, serie(metricaDrenagens, "resultado", "ok"), 1)
	exigirSerie(t, m, metricaUltimaPedidos, 0)
	exigirSerie(t, m, metricaFalhasConsecutivas, 0)
}

func aos443NosDoFixture(t *testing.T) string {
	t.Helper()
	d, err := plan.Decode([]byte(planoFixtureDuasFolhasComSnapshotAOS408))
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(len(d.Nodes))
}
