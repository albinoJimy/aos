package main

// AOS-520 (PR 1) — O LIVRO DO CANARY, sobre o Event Store em memória (ADR-041 §2.6).
//
// O que se prova: a abertura é idempotente e a contagem sobrevive a um reinício; a primeira escrita
// de um plano ganha; dois escritores concorrentes não dão ao candidato mais planos do que o limite;
// os dois erros que o perdedor do CAS pode receber são os dois tratados; o 40.º plano fecha o
// canary, uma vez, e sela-o; o plano anterior à abertura é do titular; reabrir é um registo novo; e
// um livro que não se sabe ler recusa.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

var aos520Cand = candidatoEmVigor{
	alias:         "gpt-4o-mini",
	profileDigest: "sha256:" + strings.Repeat("a", 64),
	entryDigest:   "sha256:" + strings.Repeat("b", 64),
	fatia:         100,
}

func aos520Store(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// aos520Marca devolve uma marca de abertura fixa e conta quantas vezes foi pedida.
func aos520Marca(m uint64, chamadas *int) func(context.Context) (uint64, error) {
	return func(context.Context) (uint64, error) {
		if chamadas != nil {
			*chamadas++
		}
		return m, nil
	}
}

func aos520Livro(t *testing.T, es eventstore.EventStore, c candidatoEmVigor) *livroDoCanary {
	t.Helper()
	l := &livroDoCanary{es: es}
	if err := l.abrir(context.Background(), c, aos520Marca(0, nil)); err != nil {
		t.Fatalf("abrir: %v", err)
	}
	return l
}

func aos520EventosDoLivro(t *testing.T, es eventstore.EventStore, tipo string) []canaryPayload {
	t.Helper()
	evs, err := es.Read(context.Background(), canaryStream, 1)
	if err != nil {
		t.Fatal(err)
	}
	var out []canaryPayload
	for _, ev := range evs {
		if ev.Type != tipo {
			continue
		}
		var p canaryPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// aos520Chave é a chave de um plano destes testes: de pedido quando traz o seq do pedido na fila,
// de run avulso quando não traz — como o `POST /runs` a compõe.
func aos520Chave(id string, seq uint64) chaveCanary {
	if seq != 0 {
		return chaveDePedido(id)
	}
	return chaveDeRun(id)
}

func aos520Decidir(t *testing.T, l *livroDoCanary, c candidatoEmVigor, chave string, seq uint64) decisaoDoCanary {
	t.Helper()
	d, err := l.decidir(context.Background(), c, aos520Chave(chave, seq), seq)
	if err != nil {
		t.Fatalf("decidir(%q): %v", chave, err)
	}
	return d
}

// O nome do stream é representável em todos os backends (sem pontos — ADR-029), e está no espaço
// reservado que o `POST /runs` recusa como `run_id`.
func TestAOS520_Livro_ONomeDoStreamEValido(t *testing.T) {
	if err := eventstore.ValidarStreamID(canaryStream); err != nil {
		t.Fatal(err)
	}
	if !runIDReservado(canaryStream) {
		t.Fatalf("%q tem de estar no espaco reservado", canaryStream)
	}
}

// A ABERTURA É IDEMPOTENTE, E UM REINÍCIO NÃO REPÕE A CONTAGEM. Abrir duas vezes — a segunda por
// outra instância, como depois de um reinício — deixa UMA abertura, com a marca da primeira; a
// marca não volta a ser lida; e os planos atribuídos antes continuam contados.
func TestAOS520_Livro_AbrirEIdempotenteEOReinicioNaoRepoeAContagem(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	chamadas := 0
	l1 := &livroDoCanary{es: es}
	if err := l1.abrir(ctx, aos520Cand, aos520Marca(7, &chamadas)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if d := aos520Decidir(t, l1, aos520Cand, fmt.Sprintf("plano-%d", i), 0); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
			t.Fatalf("plano %d a 100%%: %+v", i, d)
		}
	}
	// «Reinício»: outra instância, o mesmo store, outra marca disponível.
	l2 := &livroDoCanary{es: es}
	if err := l2.abrir(ctx, aos520Cand, aos520Marca(99, &chamadas)); err != nil {
		t.Fatal(err)
	}
	abertas := aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)
	if len(abertas) != 1 || abertas[0].PlanRequestMark != 7 || abertas[0].Limit != 40 || abertas[0].SharePercent != 100 || abertas[0].EntryDigest != aos520Cand.entryDigest {
		t.Fatalf("aberturas = %+v; quer UMA, com a marca 7 e o limite 40", abertas)
	}
	if chamadas != 1 {
		t.Fatalf("a marca foi lida %d vezes; so se le quando a abertura tem de ser escrita", chamadas)
	}
	if got := l2.atribuidosVistos.Load(); got != 3 {
		t.Fatalf("depois do reinicio a contagem e %d; quer 3", got)
	}
	// E o plano atribuído antes do reinício continua a ser do candidato.
	if d := aos520Decidir(t, l2, aos520Cand, "plano-1", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("plano ja atribuido, depois do reinicio: %+v", d)
	}
	// Um candidato sem os campos todos não abre nada.
	for _, mau := range []candidatoEmVigor{{}, {alias: "a", profileDigest: "p", entryDigest: "e", fatia: 0}, {alias: "a", profileDigest: "p", entryDigest: "e", fatia: 101}} {
		if err := (&livroDoCanary{es: aos520Store(t)}).abrir(ctx, mau, aos520Marca(0, nil)); !errors.Is(err, ErrCanaryInconsistent) {
			t.Fatalf("abrir(%+v) = %v", mau, err)
		}
	}
}

// A FUNÇÃO DECIDE, COM A FATIA GRAVADA. A 10%, o plano "0" (balde 168) é do candidato e fica
// gravado; o plano "a" (balde 8230) é do titular e NÃO se grava nada.
func TestAOS520_Livro_AFuncaoDecideESoOCandidatoSeGrava(t *testing.T) {
	es := aos520Store(t)
	c := aos520Cand
	c.fatia = 10
	l := aos520Livro(t, es, c)
	if d := aos520Decidir(t, l, c, "0", 0); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
		t.Fatalf("plano \"0\" a 10%%: %+v", d)
	}
	if d := aos520Decidir(t, l, c, "a", 0); d.papel != canary.RoleIncumbent || d.razao != razaoFuncao {
		t.Fatalf("plano \"a\" a 10%%: %+v", d)
	}
	atribuidos := aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)
	if len(atribuidos) != 1 || atribuidos[0].PlanKey != "0" || atribuidos[0].Alias != c.alias || atribuidos[0].ProfileDigest != c.profileDigest {
		t.Fatalf("atribuicoes = %+v; quer so a do plano \"0\"", atribuidos)
	}
	if _, err := l.decidir(context.Background(), c, chaveDeRun(""), 0); !errors.Is(err, ErrCanaryUnreadable) {
		t.Fatalf("um run sem chave de plano tem de ser recusado: %v", err)
	}
}

// A PRIMEIRA ESCRITA GANHA. Decidir duas vezes o mesmo plano grava UMA atribuição e conta um plano;
// e uma segunda escrita directa do mesmo plano, com outro conteúdo, é absorvida pelo Event Store.
func TestAOS520_Livro_APrimeiraEscritaGanha(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := aos520Livro(t, es, aos520Cand)
	aos520Decidir(t, l, aos520Cand, "plano-x", 0)
	if d := aos520Decidir(t, l, aos520Cand, "plano-x", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("segunda decisao do mesmo plano: %+v", d)
	}
	outro, _ := json.Marshal(canaryPayload{V: canaryVersaoDoLivro, EntryDigest: aos520Cand.entryDigest, Alias: "outro-alias", ProfileDigest: aos520Cand.profileDigest, PlanKey: "plano-x", PlanKeyKind: tipoDeRun})
	res, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelCanaryPlanAssigned, Payload: outro, RunID: canaryRunID, StepID: passoDoPlano(chaveDeRun("plano-x"))})
	if err != nil || res.Status != eventstore.StatusDuplicate {
		t.Fatalf("a segunda escrita do mesmo plano tinha de ser um duplicado: status=%q err=%v", res.Status, err)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 1 {
		t.Fatalf("atribuicoes do plano = %d; quer 1", n)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-x", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("depois da segunda escrita: %+v", d)
	}
}

// aos520StoreComIntruso é um Event Store que, antes do N-ésimo Append de uma atribuição, deixa
// correr `antes` — a outra réplica a escrever no livro entre a leitura e a escrita desta — ou
// devolve um erro em vez de escrever.
type aos520StoreComIntruso struct {
	eventstore.EventStore
	mu       sync.Mutex
	antes    func()
	erros    []error
	appends  int
	sempre   error
	soNoTipo string
}

func (s *aos520StoreComIntruso) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if streamID == canaryStream && in.Type == s.soNoTipo {
		s.mu.Lock()
		s.appends++
		antes, sempre := s.antes, s.sempre
		s.antes = nil
		var erro error
		if len(s.erros) > 0 {
			erro, s.erros = s.erros[0], s.erros[1:]
		}
		s.mu.Unlock()
		if sempre != nil {
			return eventstore.AppendResult{}, sempre
		}
		if erro != nil {
			return eventstore.AppendResult{}, erro
		}
		if antes != nil {
			antes()
		}
	}
	return s.EventStore.Append(ctx, streamID, in, opts...)
}

// DOIS ESCRITORES CONCORRENTES NO 40.º. Com 39 planos atribuídos, duas réplicas lêem o livro ao
// mesmo tempo e as duas decidem «candidato» para planos diferentes. A que escreve primeiro fica com
// o 40.º; a outra perde o CAS, relê, vê o canary fechado e dá o titular. O candidato fica com 40
// planos, e não com 41. (Sem o CAS, a segunda escrita entrava e eram 41.)
func TestAOS520_Livro_DoisEscritoresConcorrentesNoQuadragesimo(t *testing.T) {
	es := aos520Store(t)
	rapida := aos520Livro(t, es, aos520Cand)
	for i := 0; i < 39; i++ {
		aos520Decidir(t, rapida, aos520Cand, fmt.Sprintf("plano-%02d", i), 0)
	}
	intruso := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryPlanAssigned}
	lenta := &livroDoCanary{es: intruso}
	// Entre a leitura e a escrita da réplica lenta, a rápida atribui o 40.º.
	intruso.antes = func() {
		if d := aos520Decidir(t, rapida, aos520Cand, "plano-da-rapida", 0); d.papel != canary.RoleCandidate {
			t.Errorf("a replica rapida tinha de ficar com o 40.o: %+v", d)
		}
	}
	d := aos520Decidir(t, lenta, aos520Cand, "plano-da-lenta", 0)
	if d.papel != canary.RoleIncumbent || d.razao != razaoCanaryFechado {
		t.Fatalf("a replica que perdeu o CAS no 40.o tinha de dar o titular por canary fechado; deu %+v", d)
	}
	atribuidos := aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)
	if len(atribuidos) != 40 {
		t.Fatalf("o candidato ficou com %d planos; o limite e 40", len(atribuidos))
	}
	for _, a := range atribuidos {
		if a.PlanKey == "plano-da-lenta" {
			t.Fatal("o plano da replica que perdeu o CAS ficou atribuido ao candidato")
		}
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 1 {
		t.Fatalf("marcas de fecho = %d; quer 1", n)
	}
}

// DOIS ESCRITORES, LONGE DO LIMITE: quem perde o CAS relê e fica também com o seu plano — os dois
// são atribuídos, cada um uma vez.
func TestAOS520_Livro_DoisEscritoresConcorrentes_AmbosAtribuidos(t *testing.T) {
	es := aos520Store(t)
	rapida := aos520Livro(t, es, aos520Cand)
	intruso := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryPlanAssigned}
	lenta := &livroDoCanary{es: intruso}
	intruso.antes = func() { aos520Decidir(t, rapida, aos520Cand, "plano-da-rapida", 0) }
	if d := aos520Decidir(t, lenta, aos520Cand, "plano-da-lenta", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("a replica lenta, depois de reler: %+v", d)
	}
	if intruso.appends != 2 {
		t.Fatalf("a replica lenta escreveu %d vezes; quer 2 (a que perdeu o CAS e a que entrou)", intruso.appends)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 2 {
		t.Fatalf("atribuicoes = %d; quer 2", n)
	}
	// E o mesmo plano decidido pelas duas ao mesmo tempo fica atribuído UMA vez.
	intruso.antes = func() { aos520Decidir(t, rapida, aos520Cand, "plano-comum", 0) }
	if d := aos520Decidir(t, lenta, aos520Cand, "plano-comum", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("o plano que a outra replica atribuiu entretanto: %+v", d)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 3 {
		t.Fatalf("atribuicoes = %d; quer 3", n)
	}
}

// OS DOIS ERROS DO PERDEDOR DO CAS. O Event Store pode devolver ao perdedor o conflito de seq OU a
// violação append-only; os dois são «relê e decide de novo». Um conflito que nunca passa esgota o
// tecto de tentativas e a decisão FALHA — não cai no titular. Outro erro falha logo.
func TestAOS520_Livro_OsDoisErrosDoPerdedorDoCAS(t *testing.T) {
	ctx := context.Background()
	for _, erro := range []error{eventstore.ErrSeqConflict, eventstore.ErrAppendOnlyViolation} {
		es := aos520Store(t)
		aos520Livro(t, es, aos520Cand)
		intruso := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryPlanAssigned, erros: []error{erro}}
		l := &livroDoCanary{es: intruso}
		if d := aos520Decidir(t, l, aos520Cand, "plano", 0); d.papel != canary.RoleCandidate {
			t.Fatalf("depois de um %v: %+v", erro, d)
		}
		if intruso.appends != 2 {
			t.Fatalf("depois de um %v houve %d escritas; quer 2", erro, intruso.appends)
		}

		sempre := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryPlanAssigned, sempre: erro}
		_, err := (&livroDoCanary{es: sempre}).decidir(ctx, aos520Cand, chaveDeRun("outro-plano"), 0)
		if !errors.Is(err, ErrCanaryContention) {
			t.Fatalf("com %v em todas as escritas a decisao tinha de falhar por contencao; veio %v", erro, err)
		}
		if sempre.appends != canaryMaxTentativas {
			t.Fatalf("tentativas = %d; o tecto e %d", sempre.appends, canaryMaxTentativas)
		}
	}
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	avaria := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryPlanAssigned, sempre: errors.New("disco cheio")}
	if _, err := (&livroDoCanary{es: avaria}).decidir(ctx, aos520Cand, chaveDeRun("plano"), 0); !errors.Is(err, ErrCanaryUnreadable) || avaria.appends != 1 {
		t.Fatalf("uma escrita que falha por outra razao recusa logo: err=%v escritas=%d", err, avaria.appends)
	}
	// A abertura tem o mesmo ciclo.
	abre := &aos520StoreComIntruso{EventStore: aos520Store(t), soNoTipo: EventTypeModelCanaryOpened, sempre: eventstore.ErrSeqConflict}
	if err := (&livroDoCanary{es: abre}).abrir(ctx, aos520Cand, aos520Marca(0, nil)); !errors.Is(err, ErrCanaryContention) {
		t.Fatalf("abrir sob conflito permanente: %v", err)
	}
}

// O 40.º PLANO. Os primeiros 40 planos são do candidato; o 40.º fecha o canary — UMA marca de
// fecho, com as contagens, selada uma vez e dita no log. O 41.º é do titular. Um plano já
// atribuído continua a ser do candidato depois do fecho. E um reinício não reabre nada.
func TestAOS520_Livro_OQuadragesimoPlanoFechaOCanary(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	var selos []fimDoCanary
	var linhas []string
	l := &livroDoCanary{es: es,
		selar: func(_ context.Context, f fimDoCanary) error { selos = append(selos, f); return nil },
		log:   func(f string, a ...any) { linhas = append(linhas, fmt.Sprintf(f, a...)) },
	}
	if err := l.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 40; i++ {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-%02d", i), 0); d.papel != canary.RoleCandidate {
			t.Fatalf("o plano %d tinha de ser do candidato: %+v", i, d)
		}
		fechos := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed))
		if (i < 40 && fechos != 0) || (i == 40 && fechos != 1) {
			t.Fatalf("depois do plano %d ha %d marcas de fecho", i, fechos)
		}
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-41", 0); d.papel != canary.RoleIncumbent || d.razao != razaoCanaryFechado {
		t.Fatalf("o 41.o plano tinha de ser do titular por canary fechado: %+v", d)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-07", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("um plano ja atribuido, depois do fecho: %+v", d)
	}
	fechos := aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)
	if len(fechos) != 1 || fechos[0].Assigned != 40 || fechos[0].Limit != 40 || fechos[0].EntryDigest != aos520Cand.entryDigest {
		t.Fatalf("marca de fecho = %+v", fechos)
	}
	if len(selos) != 1 || selos[0].atribuidos != 40 || selos[0].limite != 40 || selos[0].alias != aos520Cand.alias {
		t.Fatalf("selos do fim = %+v; quer um, com as contagens", selos)
	}
	if l.atribuidosVistos.Load() != 40 || l.fechadoVisto.Load() != 1 {
		t.Fatalf("o que o /metrics publica: atribuidos=%d fechado=%d", l.atribuidosVistos.Load(), l.fechadoVisto.Load())
	}
	ditos := 0
	for _, linha := range linhas {
		if strings.Contains(linha, "CANARY DO MODELO CANDIDATO TERMINADO") {
			ditos++
		}
	}
	if ditos != 1 {
		t.Fatalf("o fim do canary foi dito %d vezes no log; quer 1", ditos)
	}
	// REINÍCIO depois do fecho: nada reabre. O selo é pedido outra vez — o processo novo não sabe
	// se ficou selado, e pergunta: quem responde é o audit (ver o teste do selo reconciliado).
	l2 := &livroDoCanary{es: es, selar: l.selar}
	if err := l2.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	if l2.fechadoVisto.Load() != 1 || l2.atribuidosVistos.Load() != 40 {
		t.Fatalf("depois do reinicio: atribuidos=%d fechado=%d", l2.atribuidosVistos.Load(), l2.fechadoVisto.Load())
	}
	if d := aos520Decidir(t, l2, aos520Cand, "plano-42", 0); d.papel != canary.RoleIncumbent || d.razao != razaoCanaryFechado {
		t.Fatalf("depois do reinicio, um plano novo: %+v", d)
	}
	if len(selos) != 2 || len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)) != 40 {
		t.Fatalf("depois do reinicio: pedidos de selo=%d (um por processo) atribuicoes=%d", len(selos), len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)))
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 1 {
		t.Fatalf("depois do reinicio ha %d marcas de fecho", n)
	}
}

// A MARCA DE FECHO QUE FALTA É REPOSTA. O nó que escreveu a 40.ª atribuição morreu antes de gravar o
// fecho: a contagem já decide (o plano seguinte é do titular), e a decisão seguinte grava a marca.
func TestAOS520_Livro_AMarcaDeFechoEmFaltaERepostaNaDecisaoSeguinte(t *testing.T) {
	es := aos520Store(t)
	l := aos520Livro(t, es, aos520Cand)
	semFecho := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryClosed, erros: []error{errors.New("o no morreu aqui")}}
	morre := &livroDoCanary{es: semFecho}
	for i := 1; i <= 40; i++ {
		quem := l
		if i == 40 {
			quem = morre
		}
		aos520Decidir(t, quem, aos520Cand, fmt.Sprintf("plano-%02d", i), 0)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 0 {
		t.Fatalf("o fecho nao devia ter ficado gravado: %d", n)
	}
	selos := 0
	l.selar = func(context.Context, fimDoCanary) error { selos++; return nil }
	if d := aos520Decidir(t, l, aos520Cand, "plano-41", 0); d.papel != canary.RoleIncumbent || d.razao != razaoCanaryFechado {
		t.Fatalf("sem a marca de fecho, a contagem decide: %+v", d)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 1 || selos != 1 {
		t.Fatalf("a marca de fecho tinha de ser reposta e selada: fechos=%d selos=%d", n, selos)
	}
}

// O PLANO ANTERIOR À ABERTURA É DO TITULAR. Com a marca em 50: o pedido de seq 50 e os anteriores
// são do titular, e nada se grava; o de seq 51 e um run sem pedido (seq 0) vão à função.
func TestAOS520_Livro_OPlanoAnteriorAAberturaEDoTitular(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := &livroDoCanary{es: es}
	if err := l.abrir(ctx, aos520Cand, aos520Marca(50, nil)); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint64{1, 49, 50} {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("antigo-%d", seq), seq); d.papel != canary.RoleIncumbent || d.razao != razaoPlanoAnteriorAAbertura {
			t.Fatalf("pedido de seq %d com a marca em 50: %+v", seq, d)
		}
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 0 {
		t.Fatalf("um plano anterior a abertura ficou atribuido: %d", n)
	}
	for _, seq := range []uint64{51, 0} {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("novo-%d", seq), seq); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
			t.Fatalf("pedido de seq %d com a marca em 50: %+v", seq, d)
		}
	}
}

// A marca lê-se da fila de pedidos de plano: o último seq; zero se a fila não existe.
func TestAOS520_Livro_AMarcaEOUltimoSeqDaFilaDePedidos(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	if m, err := marcaDaFilaDePedidos(es)(ctx); err != nil || m != 0 {
		t.Fatalf("fila inexistente: marca=%d err=%v", m, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := es.Append(ctx, planRequestStream, eventstore.EventInput{Type: EventTypePlanRequestSubmitted, Payload: []byte(`{}`), RunID: planRequestRunID, StepID: fmt.Sprintf("p-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := marcaDaFilaDePedidos(es)(ctx); err != nil || m != 3 {
		t.Fatalf("fila com 3 eventos: marca=%d err=%v", m, err)
	}
}

// REABRIR É UM REGISTO NOVO. Outro digest de registo: outra abertura, a contagem a zero. Um plano
// atribuído na abertura anterior continua a ser do candidato enquanto o alias e o perfil forem os
// mesmos; com outro perfil, ou outro alias, o run é RECUSADO — não passa para o titular.
func TestAOS520_Livro_ReabrirEUmRegistoNovo(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := aos520Livro(t, es, aos520Cand)
	for i := 1; i <= 40; i++ {
		aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-%02d", i), 0)
	}
	novo := aos520Cand
	novo.entryDigest = "sha256:" + strings.Repeat("c", 64)
	novo.fatia = 50
	l2 := &livroDoCanary{es: es}
	if err := l2.abrir(ctx, novo, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	if l2.atribuidosVistos.Load() != 0 || l2.fechadoVisto.Load() != 0 {
		t.Fatalf("a abertura nova comeca a zero: atribuidos=%d fechado=%d", l2.atribuidosVistos.Load(), l2.fechadoVisto.Load())
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)); n != 2 {
		t.Fatalf("aberturas = %d; quer 2", n)
	}
	if d := aos520Decidir(t, l2, novo, "plano-07", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("plano da abertura anterior, com o mesmo alias e perfil: %+v", d)
	}
	// Um plano novo vai à função com a fatia da abertura nova: "0" (balde 168) é do candidato a 50%.
	if d := aos520Decidir(t, l2, novo, "0", 0); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
		t.Fatalf("plano novo na abertura nova: %+v", d)
	}
	for nome, outro := range map[string]candidatoEmVigor{
		"outro perfil": {alias: novo.alias, profileDigest: "sha256:" + strings.Repeat("d", 64), entryDigest: "sha256:" + strings.Repeat("e", 64), fatia: 10},
		"outro alias":  {alias: "k3", profileDigest: novo.profileDigest, entryDigest: "sha256:" + strings.Repeat("f", 64), fatia: 10},
	} {
		l3 := &livroDoCanary{es: es}
		if err := l3.abrir(ctx, outro, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := l3.decidir(ctx, outro, chaveDeRun("plano-07"), 0); !errors.Is(err, ErrPlanAssignedToAbsentCandidate) {
			t.Fatalf("%s: um plano atribuido a outro candidato tinha de ser recusado; veio %v", nome, err)
		}
	}
	// O que um nó sem candidato a servir lê no arranque: os planos atribuídos, por chave.
	atribuidos, err := l.planosAtribuidos(ctx)
	if err != nil || len(atribuidos) != 41 || !atribuidos[chaveDeRun("plano-07").indice()] || !atribuidos[chaveDeRun("0").indice()] || atribuidos[chaveDeRun("plano-99").indice()] || atribuidos[chaveDePedido("plano-07").indice()] {
		t.Fatalf("planos atribuidos = %d err=%v", len(atribuidos), err)
	}
}

// UM LIVRO QUE NÃO SE SABE LER RECUSA. Um tipo de evento desconhecido, um corpo que não é o de um
// evento do livro, uma abertura para o registo em vigor com outra fatia, e um livro sem abertura:
// em todos a decisão falha — nunca dá o titular «na dúvida».
func TestAOS520_Livro_IlegivelRecusa(t *testing.T) {
	ctx := context.Background()
	semear := func(t *testing.T, tipo, corpo string) *livroDoCanary {
		es := aos520Store(t)
		aos520Livro(t, es, aos520Cand)
		if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: tipo, Payload: []byte(corpo), RunID: canaryRunID, StepID: "lixo"}); err != nil {
			t.Fatal(err)
		}
		return &livroDoCanary{es: es}
	}
	valido := `{"v":"1.0","entry_digest":"e","alias":"a","profile_digest":"p"}`
	for nome, l := range map[string]*livroDoCanary{
		"tipo desconhecido":     semear(t, EventTypeQuotaReserved, valido),
		"corpo ilegivel":        semear(t, EventTypeModelCanaryPlanAssigned, `{"v":`),
		"atribuicao sem plano":  semear(t, EventTypeModelCanaryPlanAssigned, valido),
		"abertura sem limite":   semear(t, EventTypeModelCanaryOpened, valido),
		"evento sem o registo":  semear(t, EventTypeModelCanaryClosed, `{"v":"1.0","alias":"a","profile_digest":"p"}`),
		"abertura fatia a mais": semear(t, EventTypeModelCanaryOpened, `{"v":"1.0","entry_digest":"e","alias":"a","profile_digest":"p","limit":40,"share_percent":101}`),
	} {
		if _, err := l.decidir(ctx, aos520Cand, chaveDeRun("plano"), 0); !errors.Is(err, ErrCanaryUnreadable) {
			t.Errorf("%s: decidir = %v", nome, err)
		}
		if err := l.abrir(ctx, aos520Cand, aos520Marca(0, nil)); !errors.Is(err, ErrCanaryUnreadable) {
			t.Errorf("%s: abrir = %v", nome, err)
		}
		if _, err := l.planosAtribuidos(ctx); !errors.Is(err, ErrCanaryUnreadable) {
			t.Errorf("%s: planosAtribuidos = %v", nome, err)
		}
	}
	// A abertura gravada para este digest de registo tem outra fatia: não se reinterpreta.
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	outraFatia := aos520Cand
	outraFatia.fatia = 10
	l := &livroDoCanary{es: es}
	if err := l.abrir(ctx, outraFatia, aos520Marca(0, nil)); !errors.Is(err, ErrCanaryInconsistent) {
		t.Fatalf("abrir com outra fatia para o mesmo registo: %v", err)
	}
	if _, err := l.decidir(ctx, outraFatia, chaveDeRun("plano"), 0); !errors.Is(err, ErrCanaryInconsistent) {
		t.Fatalf("decidir com outra fatia para o mesmo registo: %v", err)
	}
	// Um livro sem a abertura do registo em vigor (ninguém abriu): recusa.
	if _, err := (&livroDoCanary{es: aos520Store(t)}).decidir(ctx, aos520Cand, chaveDeRun("plano"), 0); !errors.Is(err, ErrCanaryUnreadable) {
		t.Fatalf("decidir sem abertura: %v", err)
	}
	// Um livro vazio não tem planos atribuídos.
	if m, err := (&livroDoCanary{es: aos520Store(t)}).planosAtribuidos(ctx); err != nil || len(m) != 0 {
		t.Fatalf("livro vazio: %v %v", m, err)
	}
}

// aos520EventoFuturo escreve no livro um evento de um tipo que este binário não conhece, da família
// do livro — o que uma versão mais recente do nó (o disjuntor do AOS-521) lá poria.
func aos520EventoFuturo(t *testing.T, es eventstore.EventStore, passo string) {
	t.Helper()
	if _, err := es.Append(context.Background(), canaryStream, eventstore.EventInput{
		Type: "model.breaker.opened", Payload: []byte(`{"v":"9.0","coisa_que_nao_conheco":true}`), RunID: canaryRunID, StepID: passo,
	}); err != nil {
		t.Fatal(err)
	}
}

// O LIVRO EVOLUI POR TIPOS NOVOS. Um evento de um tipo desconhecido, da família do livro, não torna
// o livro ilegível: é ignorado na contagem e fica registado. Com ele visto, o canary não é decidível
// por esta versão — um plano novo é do titular (com a razão própria) e NADA se atribui; um plano já
// atribuído ao candidato é recusado (não se serve, e não passa para o titular); e a abertura de um
// registo novo não se escreve. Um tipo de FORA da família, e um evento malformado de um tipo
// conhecido, continuam a ser ilegíveis.
func TestAOS520_Livro_EventoDeVersaoMaisRecente_NaoDecidivel(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := aos520Livro(t, es, aos520Cand)
	aos520Decidir(t, l, aos520Cand, "plano-antes", 0)
	aos520EventoFuturo(t, es, "futuro-1")

	st, err := l.ler(ctx)
	if err != nil || st.futuros != 1 || st.porAbertura[aos520Cand.entryDigest] != 1 {
		t.Fatalf("o evento futuro tinha de ser ignorado na contagem e registado: futuros=%d atribuidos=%d err=%v", st.futuros, st.porAbertura[aos520Cand.entryDigest], err)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-depois", 0); d.papel != canary.RoleIncumbent || d.razao != razaoLivroMaisRecente {
		t.Fatalf("com um evento futuro, um plano novo a 100%%: %+v; quer o titular", d)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 1 {
		t.Fatalf("com um evento futuro atribuiu-se um plano: %d atribuicoes", n)
	}
	if _, err := l.decidir(ctx, aos520Cand, chaveDeRun("plano-antes"), 0); !errors.Is(err, ErrCanaryNewerVersion) {
		t.Fatalf("um plano ja atribuido, com um evento futuro: %v; quer a recusa", err)
	}
	if l.futurosVistos.Load() != 1 {
		t.Fatalf("o que o /metrics publica: %d", l.futurosVistos.Load())
	}
	// A abertura de OUTRO registo não se escreve; e o arranque não falha.
	novo := aos520Cand
	novo.entryDigest = "sha256:" + strings.Repeat("9", 64)
	l2 := &livroDoCanary{es: es}
	if err := l2.abrir(ctx, novo, aos520Marca(0, nil)); err != nil {
		t.Fatalf("abrir com um evento futuro: %v", err)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)); n != 1 {
		t.Fatalf("com um evento futuro abriu-se um canary: %d aberturas", n)
	}
	if d, err := l2.decidir(ctx, novo, chaveDeRun("plano-do-registo-novo"), 0); err != nil || d.papel != canary.RoleIncumbent || d.razao != razaoLivroMaisRecente {
		t.Fatalf("registo sem abertura, com um evento futuro: %+v %v", d, err)
	}
	// O nó sem candidato a servir lê os planos atribuídos na mesma.
	if m, err := l.planosAtribuidos(ctx); err != nil || !m[chaveDeRun("plano-antes").indice()] {
		t.Fatalf("planosAtribuidos com um evento futuro: %v %v", m, err)
	}
	// FORA da família: ilegível.
	if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: "modelo.canary.opened", Payload: []byte(`{}`), RunID: canaryRunID, StepID: "fora"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ler(ctx); !errors.Is(err, ErrCanaryUnreadable) {
		t.Fatalf("um tipo de fora da familia do livro: %v", err)
	}
	// Um evento MALFORMADO de um tipo conhecido: ilegível, mesmo com eventos futuros ao lado.
	es2 := aos520Store(t)
	aos520Livro(t, es2, aos520Cand)
	aos520EventoFuturo(t, es2, "futuro-1")
	if _, err := es2.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelCanaryPlanAssigned, Payload: []byte(`{"v":`), RunID: canaryRunID, StepID: "lixo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&livroDoCanary{es: es2}).decidir(ctx, aos520Cand, chaveDeRun("p"), 0); !errors.Is(err, ErrCanaryUnreadable) {
		t.Fatalf("um evento malformado de um tipo conhecido: %v", err)
	}
}

// REPOR UM REGISTO ANTIGO NÃO PARTE UM PLANO EM DOIS MODELOS (a sequência da revisão). O registo A
// abre o canary (marca 5) e atribui três planos. O dono troca-o pelo registo B (marca 10), e um
// plano é pedido sob B (seq 12): o seu primeiro nó vai à função de B, que o dá ao titular. O dono
// REPÕE o registo A: a abertura de A ganha uma ÉPOCA com a marca actual da fila (20). O segundo nó
// desse plano, sob A reposto, é do titular — o plano é anterior à época em vigor —, embora a fatia
// de A seja 100%. A contagem de A não volta a zero, e um reinício com o mesmo registo não abre
// outra época.
func TestAOS520_Livro_ReporUmRegistoAntigoAbreUmaEpocaComAMarcaActual(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	registoA := aos520Cand // fatia 100
	registoB := aos520Cand
	registoB.entryDigest, registoB.fatia = "sha256:"+strings.Repeat("c", 64), 10

	a1 := &livroDoCanary{es: es}
	if err := a1.abrir(ctx, registoA, aos520Marca(5, nil)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		aos520Decidir(t, a1, registoA, fmt.Sprintf("plano-de-a-%d", i), 0)
	}
	b := &livroDoCanary{es: es}
	if err := b.abrir(ctx, registoB, aos520Marca(10, nil)); err != nil {
		t.Fatal(err)
	}
	// O plano "a" (balde 8230), pedido sob B com o seq 12: a 10% a função dá-o ao titular.
	if d := aos520Decidir(t, b, registoB, "a", 12); d.papel != canary.RoleIncumbent || d.razao != razaoFuncao {
		t.Fatalf("o no 1 do plano, sob o registo B: %+v", d)
	}
	// O registo A volta.
	a2 := &livroDoCanary{es: es}
	if err := a2.abrir(ctx, registoA, aos520Marca(20, nil)); err != nil {
		t.Fatal(err)
	}
	if d := aos520Decidir(t, a2, registoA, "a", 12); d.papel != canary.RoleIncumbent || d.razao != razaoPlanoAnteriorAAbertura {
		t.Fatalf("o no 2 do plano, sob o registo A reposto: %+v — o plano comecou no titular e tem de la ficar", d)
	}
	abertas := aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)
	if len(abertas) != 3 || abertas[2].EntryDigest != registoA.entryDigest || abertas[2].PlanRequestMark != 20 || abertas[2].Limit != 40 || abertas[2].SharePercent != 100 {
		t.Fatalf("aberturas = %+v; quer A, B e uma epoca nova de A com a marca 20", abertas)
	}
	if a2.atribuidosVistos.Load() != 3 {
		t.Fatalf("a contagem do registo A voltou a %d; nao volta a zero ao repor", a2.atribuidosVistos.Load())
	}
	// Um plano pedido DEPOIS da época nova vai à função de A; um plano de A de antes continua dele.
	if d := aos520Decidir(t, a2, registoA, "plano-novo", 21); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
		t.Fatalf("plano pedido depois da epoca nova: %+v", d)
	}
	if d := aos520Decidir(t, a2, registoA, "plano-de-a-1", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("plano ja atribuido ao registo A: %+v", d)
	}
	// Um reinício com o MESMO registo em vigor não abre uma época.
	if err := (&livroDoCanary{es: es}).abrir(ctx, registoA, aos520Marca(99, nil)); err != nil {
		t.Fatal(err)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)); n != 3 {
		t.Fatalf("um reinicio abriu uma epoca: %d aberturas", n)
	}
	// Duas aberturas do mesmo registo que se contradizem: ilegível.
	mau, _ := json.Marshal(canaryPayload{V: canaryVersaoDoLivro, EntryDigest: registoA.entryDigest, Alias: registoA.alias, ProfileDigest: registoA.profileDigest, SharePercent: 50, Limit: 40})
	if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelCanaryOpened, Payload: mau, RunID: canaryRunID, StepID: "contradiz"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.ler(ctx); !errors.Is(err, ErrCanaryUnreadable) {
		t.Fatalf("duas aberturas do mesmo registo com fatias diferentes: %v", err)
	}
}

// O SELO DO FIM É RECONCILIADO, E QUEM DIZ SE ESTÁ FEITO É O AUDIT. Com o selador real sobre um
// audit em memória (o mesmo de [seladorDoFimDoCanary], que o nó usa):
//
//  1. o selo FALHA no 40.º plano: o fim está no livro, o audit não tem selo. A decisão seguinte
//     volta a tentar, e sela — uma vez;
//  2. depois de selado, mais decisões e um REINÍCIO (processo novo, o mesmo audit) não acrescentam
//     selos: o audit tem um;
//  3. o processo que fechou MORRE antes de selar (o selo nem chega a ser pedido): o processo
//     seguinte, no arranque, encontra o fim no livro sem selo no audit e sela-o.
func TestAOS520_Livro_OSeloDoFimEReconciliadoPeloAudit(t *testing.T) {
	ctx := context.Background()
	encher := func(t *testing.T, l *livroDoCanary) {
		t.Helper()
		for i := 1; i <= 40; i++ {
			aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-%02d", i), 0)
		}
	}
	t.Run("o selo falha no 40.o e repete-se", func(t *testing.T) {
		es, store := aos520Store(t), audit.NewMemStore()
		garantir, contar := seladorDoFimDoCanary(store)
		falhas := 1
		l := &livroDoCanary{es: es, selar: func(ctx context.Context, f fimDoCanary) error {
			if falhas > 0 {
				falhas--
				return errors.New("audit indisponivel")
			}
			return garantir(ctx, f)
		}}
		if err := l.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		encher(t, l)
		if n, _ := contar(ctx, aos520Cand.entryDigest); n != 0 {
			t.Fatalf("o selo falhou e o audit tem %d selos", n)
		}
		if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 1 {
			t.Fatalf("o fim tinha de estar no livro: %d", n)
		}
		aos520Decidir(t, l, aos520Cand, "plano-41", 0)
		if n, err := contar(ctx, aos520Cand.entryDigest); err != nil || n != 1 {
			t.Fatalf("a decisao seguinte tinha de selar o fim: selos=%d err=%v", n, err)
		}
		// Selado: mais decisões, e um reinício, não acrescentam selos.
		aos520Decidir(t, l, aos520Cand, "plano-42", 0)
		aos520Decidir(t, l, aos520Cand, "plano-07", 0)
		reinicio := &livroDoCanary{es: es, selar: garantir}
		if err := reinicio.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		aos520Decidir(t, reinicio, aos520Cand, "plano-43", 0)
		if n, _ := contar(ctx, aos520Cand.entryDigest); n != 1 {
			t.Fatalf("o audit tem %d selos do fim; quer exactamente 1", n)
		}
		// O que ficou selado, lido do audit.
		recs, err := store.Read(ctx, "modelgw-gov:route-profiles", 1, 1)
		if err != nil || len(recs) != 1 {
			t.Fatalf("ler o audit: %d %v", len(recs), err)
		}
		p := recs[0].Obligations[0].Params
		if p["model_canary"] != "closed" || p["entry_digest"] != aos520Cand.entryDigest || p["requested"] != aos520Cand.alias || p["plans_assigned"] != "40" || p["plan_limit"] != "40" {
			t.Fatalf("o selo do fim, lido do audit: %+v", p)
		}
	})
	t.Run("o processo morre antes de selar", func(t *testing.T) {
		es, store := aos520Store(t), audit.NewMemStore()
		garantir, contar := seladorDoFimDoCanary(store)
		morre := &livroDoCanary{es: es} // sem onde selar: o processo morreu entre o fim e o selo
		if err := morre.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		encher(t, morre)
		if n, _ := contar(ctx, aos520Cand.entryDigest); n != 0 {
			t.Fatalf("selos antes do reinicio: %d", n)
		}
		// O arranque seguinte, sem mais nenhum run.
		if err := (&livroDoCanary{es: es, selar: garantir}).abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		if n, _ := contar(ctx, aos520Cand.entryDigest); n != 1 {
			t.Fatalf("o arranque tinha de selar o fim que encontrou no livro sem selo: selos=%d", n)
		}
	})
	t.Run("a marca de fecho falha no 40.o e nao vem mais nenhum run", func(t *testing.T) {
		es, store := aos520Store(t), audit.NewMemStore()
		garantir, contar := seladorDoFimDoCanary(store)
		aos520Livro(t, es, aos520Cand)
		semFecho := &aos520StoreComIntruso{EventStore: es, soNoTipo: EventTypeModelCanaryClosed, sempre: errors.New("o fecho nao se grava")}
		encher(t, &livroDoCanary{es: semFecho, selar: garantir})
		if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 0 {
			t.Fatalf("o fecho nao devia ter ficado gravado: %d", n)
		}
		if n, _ := contar(ctx, aos520Cand.entryDigest); n != 0 {
			t.Fatalf("sem a marca no livro nao se sela: selos=%d", n)
		}
		// O arranque seguinte grava a marca e sela — sem esperar por um run.
		reinicio := &livroDoCanary{es: es, selar: garantir}
		if err := reinicio.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
			t.Fatal(err)
		}
		fechos := aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)
		if n, _ := contar(ctx, aos520Cand.entryDigest); len(fechos) != 1 || fechos[0].Assigned != 40 || n != 1 {
			t.Fatalf("depois do arranque: fechos=%+v selos=%d", fechos, n)
		}
	})
}

// O LIMITE QUE DECIDE É O GRAVADO NA ABERTURA, e não a constante do binário. Uma abertura gravada
// com o limite 2 (por um binário com outra constante): o canary fecha ao segundo plano, e a marca
// de fecho e a época seguinte levam esse limite.
func TestAOS520_Livro_OLimiteGravadoNaAberturaDecide(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	aberta, _ := json.Marshal(canaryPayload{V: canaryVersaoDoLivro, EntryDigest: aos520Cand.entryDigest, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest, SharePercent: aos520Cand.fatia, Limit: 2})
	if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelCanaryOpened, Payload: aberta, RunID: canaryRunID, StepID: "opened-" + aos520Cand.entryDigest + "-1"}); err != nil {
		t.Fatal(err)
	}
	l := &livroDoCanary{es: es}
	if err := l.abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	for i, quer := range []canary.Role{canary.RoleCandidate, canary.RoleCandidate, canary.RoleIncumbent} {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-%d", i), 0); d.papel != quer {
			t.Fatalf("plano %d com o limite gravado 2: %+v; quer %s", i, d, quer)
		}
	}
	fechos := aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)
	if len(fechos) != 1 || fechos[0].Limit != 2 || fechos[0].Assigned != 2 {
		t.Fatalf("o fecho leva o limite gravado: %+v", fechos)
	}
	// SEM A MARCA DE FECHO (a escrita falha sempre): quem fecha é a CONTAGEM contra o limite GRAVADO.
	es2 := aos520Store(t)
	if _, err := es2.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelCanaryOpened, Payload: aberta, RunID: canaryRunID, StepID: "opened-" + aos520Cand.entryDigest + "-1"}); err != nil {
		t.Fatal(err)
	}
	semFecho := &livroDoCanary{es: &aos520StoreComIntruso{EventStore: es2, soNoTipo: EventTypeModelCanaryClosed, sempre: errors.New("o fecho nao se grava")}}
	for i, quer := range []canary.Role{canary.RoleCandidate, canary.RoleCandidate, canary.RoleIncumbent, canary.RoleIncumbent} {
		if d := aos520Decidir(t, semFecho, aos520Cand, fmt.Sprintf("plano-%d", i), 0); d.papel != quer {
			t.Fatalf("sem a marca de fecho, plano %d com o limite gravado 2: %+v; quer %s", i, d, quer)
		}
	}
	if n := len(aos520EventosDoLivro(t, es2, EventTypeModelCanaryPlanAssigned)); n != 2 {
		t.Fatalf("sem a marca de fecho o candidato ficou com %d planos; o limite gravado e 2", n)
	}
	// Outro registo entra e este volta: a época nova conserva o limite gravado.
	outro := aos520Cand
	outro.entryDigest = "sha256:" + strings.Repeat("c", 64)
	if err := (&livroDoCanary{es: es}).abrir(ctx, outro, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	if err := (&livroDoCanary{es: es}).abrir(ctx, aos520Cand, aos520Marca(0, nil)); err != nil {
		t.Fatal(err)
	}
	abertas := aos520EventosDoLivro(t, es, EventTypeModelCanaryOpened)
	if len(abertas) != 3 || abertas[1].Limit != 40 || abertas[2].Limit != 2 {
		t.Fatalf("aberturas = %+v; a epoca nova do registo conserva o limite 2, e o registo novo abre com 40", abertas)
	}
}

// CONCORRÊNCIA REAL (com `-race`). Quatro réplicas — quatro livros sobre o MESMO Event Store em
// memória — e trinta goroutines em cada uma, todas a decidir planos diferentes a 100%. Os testes
// acima são intercalações deterministas num só fio; este não controla a ordem de nada, e afirma só
// os invariantes: ao candidato são atribuídos EXACTAMENTE 40 planos, cada um uma vez; há UMA marca
// de fecho; quem recebeu «candidato» está no livro; e ninguém recebeu um erro que não seja a
// contenção (que recusa o run — nunca o entrega ao titular «na dúvida»).
func TestAOS520_Livro_ConcorrenciaReal_NuncaMaisDeQuarenta(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	const replicas, porReplica = 4, 30
	livros := make([]*livroDoCanary, replicas)
	for i := range livros {
		livros[i] = aos520Livro(t, es, aos520Cand)
	}
	var (
		wg                           sync.WaitGroup
		candidato, titular, contidos atomic.Int64
		doCandidato                  sync.Map
	)
	for r := 0; r < replicas; r++ {
		for k := 0; k < porReplica; k++ {
			wg.Add(1)
			go func(r, k int) {
				defer wg.Done()
				chave := fmt.Sprintf("plano-%d-%02d", r, k)
				d, err := livros[r].decidir(ctx, aos520Cand, chaveDeRun(chave), 0)
				switch {
				case errors.Is(err, ErrCanaryContention):
					contidos.Add(1)
				case err != nil:
					t.Errorf("decidir(%s): %v", chave, err)
				case d.papel == canary.RoleCandidate:
					candidato.Add(1)
					doCandidato.Store(chave, true)
				default:
					titular.Add(1)
				}
			}(r, k)
		}
	}
	wg.Wait()
	atribuidos := aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)
	vistos := map[string]bool{}
	for _, a := range atribuidos {
		if vistos[a.PlanKey] {
			t.Fatalf("o plano %q foi atribuido duas vezes", a.PlanKey)
		}
		vistos[a.PlanKey] = true
	}
	if len(atribuidos) != 40 {
		t.Fatalf("planos atribuidos ao candidato = %d; quer exactamente 40 (candidato=%d titular=%d contidos=%d)", len(atribuidos), candidato.Load(), titular.Load(), contidos.Load())
	}
	// Quem recebeu «candidato» está no livro. O contrário pode não valer: uma goroutine cuja escrita
	// entrou e cuja resposta se perdeu não existe aqui, mas o livro é que conta.
	doCandidato.Range(func(k, _ any) bool {
		if !vistos[k.(string)] {
			t.Errorf("o plano %q recebeu «candidato» e nao esta no livro", k)
		}
		return true
	})
	if candidato.Load() != 40 {
		t.Fatalf("decisoes «candidato» = %d; quer 40", candidato.Load())
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryClosed)); n != 1 {
		t.Fatalf("marcas de fecho = %d; quer 1", n)
	}
	if candidato.Load()+titular.Load()+contidos.Load() != replicas*porReplica {
		t.Fatalf("decisoes: candidato=%d titular=%d contidos=%d", candidato.Load(), titular.Load(), contidos.Load())
	}
}

// A ESPERA PELA VEZ NO LIVRO RESPEITA O CONTEXTO. Com uma decisão presa a ler o livro, a seguinte
// espera; se o contexto dela acaba, DESISTE com a causa de contenção — não fica pendurada, e não
// decide. Largada a primeira, o livro continua a servir.
func TestAOS520_Livro_AEsperaPelaVezRespeitaOContexto(t *testing.T) {
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	preso, largar := make(chan struct{}), make(chan struct{})
	lento := &aos520StoreLento{EventStore: es, preso: preso, largar: largar}
	l := &livroDoCanary{es: lento}
	primeira := make(chan error, 1)
	go func() {
		_, err := l.decidir(context.Background(), aos520Cand, chaveDeRun("plano-lento"), 0)
		primeira <- err
	}()
	<-preso // a primeira decisão tem a vez e está a ler
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// A decisão à espera corre numa goroutine: se a espera NÃO ouvir o contexto, o teste falha por
	// asserção ao fim de cinco segundos (e larga a primeira, para não deixar nada pendurado), em vez
	// de ficar à espera até ao tecto de tempo da suite.
	segunda := make(chan error, 1)
	go func() {
		_, err := l.decidir(ctx, aos520Cand, chaveDeRun("plano-a-espera"), 0)
		segunda <- err
	}()
	select {
	case err := <-segunda:
		if !errors.Is(err, ErrCanaryContention) || !errors.Is(err, context.DeadlineExceeded) {
			close(largar)
			t.Fatalf("a decisao a espera tinha de desistir com a causa de contencao quando o contexto acabou; veio %v", err)
		}
	case <-time.After(5 * time.Second):
		close(largar)
		t.Fatal("a decisao a espera pela vez no livro NAO ouviu o contexto: cinco segundos depois de ele acabar ainda estava a espera")
	}
	if err := (&livroDoCanary{es: lento}).abrir(ctx, aos520Cand, aos520Marca(0, nil)); err == nil {
		t.Fatal("com o contexto acabado, abrir tinha de falhar")
	}
	close(largar)
	if err := <-primeira; err != nil {
		t.Fatalf("a primeira decisao: %v", err)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-a-espera", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("largada a vez, o livro continua a servir: %+v", d)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 2 {
		t.Fatalf("atribuicoes = %d; quer 2 (a que desistiu nao escreveu nada)", n)
	}
}

// aos520StoreLento prende a PRIMEIRA leitura do livro até o teste a largar.
type aos520StoreLento struct {
	eventstore.EventStore
	uma           sync.Once
	preso, largar chan struct{}
}

func (s *aos520StoreLento) Read(ctx context.Context, streamID string, from uint64) ([]eventstore.Event, error) {
	if streamID == canaryStream {
		s.uma.Do(func() {
			close(s.preso)
			<-s.largar
		})
	}
	return s.EventStore.Read(ctx, streamID, from)
}
