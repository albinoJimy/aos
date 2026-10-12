package main

// AOS-521 — O DISJUNTOR NO LIVRO DO CANARY (ADR-041 §2.9), sem o nó: o livro sobre um Event Store em
// memória, com o candidato dos testes do AOS-520.
//
// O que se prende: o estado é o que o livro tem (um «reinício» é um livro novo sobre o mesmo store);
// cada modo faz o que diz; aberto, os planos novos são do titular e os do candidato são recusados; a
// abertura fica gravada UMA vez, com duas réplicas a correr; um registo novo começa fechado e o mesmo
// registo reposto continua aberto; e um leitor da versão anterior não cai.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"

	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// aos521Livro abre o canary do candidato e liga o disjuntor no modo dado (vazio ⇒ desligado).
func aos521Livro(t *testing.T, es eventstore.EventStore, c candidatoEmVigor, modo string) *livroDoCanary {
	t.Helper()
	l := aos520Livro(t, es, c)
	if modo != "" {
		l.disjuntor = novoDisjuntorDoModelo(modo, c.entryDigest)
	}
	return l
}

// aos521Desfecho leva ao livro o desfecho de um run do candidato, na sua própria unidade.
func aos521Desfecho(t *testing.T, l *livroDoCanary, c candidatoEmVigor, run, sinal string) {
	t.Helper()
	if err := l.observarNoDisjuntor(context.Background(), c.registo(), observacaoDoDisjuntor{run: run, unidade: run, sinal: sinal}); err != nil {
		t.Fatalf("observar(%s, %s): %v", run, sinal, err)
	}
}

// aos521Marcas devolve as marcas do tipo dado que o livro tem.
func aos521Marcas(t *testing.T, es eventstore.EventStore, tipo string) []disjuntorPayload {
	t.Helper()
	evs, err := es.Read(context.Background(), canaryStream, 1)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []disjuntorPayload
	for _, ev := range evs {
		if ev.Type != tipo {
			continue
		}
		var p disjuntorPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if ev.Producer.NHIID != disjuntorNHI {
			t.Fatalf("evento %s com o emissor %q", tipo, ev.Producer.NHIID)
		}
		out = append(out, p)
	}
	return out
}

// aos521TresFalhas grava três falhas de sinais diferentes: a regra dispara à terceira.
func aos521TresFalhas(t *testing.T, l *livroDoCanary, c candidatoEmVigor) {
	t.Helper()
	aos521Desfecho(t, l, c, "run-f1", sinalNaoCumprido)
	aos521Desfecho(t, l, c, "run-ok", sinalCumprido)
	aos521Desfecho(t, l, c, "run-f3", sinalRespostaRejeitada)
	aos521Desfecho(t, l, c, "run-f4", sinalErroDoProvider)
}

// EM `enforce`: a terceira falha abre, a abertura fica gravada com o registo, a regra, os cinco
// contadores e a janela; e a partir daí nenhum plano novo vai para o candidato — zero em 200
// identificadores que a função lhe daria (a fatia é 100%).
func TestAOS521_Livro_Enforce_ATerceiraFalhaAbreECorta(t *testing.T) {
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	if d := aos520Decidir(t, l, aos520Cand, "plano-antes", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("antes de abrir o plano e do candidato: %+v", d)
	}
	aos521Desfecho(t, l, aos520Cand, "run-f1", sinalNaoCumprido)
	aos521Desfecho(t, l, aos520Cand, "run-ok", sinalCumprido)
	aos521Desfecho(t, l, aos520Cand, "run-f3", sinalRespostaRejeitada)
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 0 {
		t.Fatalf("duas falhas nao abrem: %d marca(s)", n)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-com-duas-falhas", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("com duas falhas o candidato continua a receber: %+v", d)
	}
	aos521Desfecho(t, l, aos520Cand, "run-f4", sinalErroDoProvider)

	marcas := aos521Marcas(t, es, EventTypeModelBreakerOpened)
	quer := disjuntorPayload{
		V: "1.0", EntryDigest: aos520Cand.entryDigest, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest,
		Rule:     regraFalhasNaJanela,
		Counters: map[string]int{sinalNaoCumprido: 1, sinalRespostaVazia: 0, sinalRespostaRejeitada: 1, sinalErroDoProvider: 1, sinalModeloDiferente: 0, sinalNaoConvergiu: 0, sinalFalhaNaoClassificada: 0},
		Window:   []itemDaJanela{{"run-f1", sinalNaoCumprido}, {"run-ok", sinalCumprido}, {"run-f3", sinalRespostaRejeitada}, {"run-f4", sinalErroDoProvider}},
	}
	if len(marcas) != 1 || !reflect.DeepEqual(marcas[0], quer) {
		t.Fatalf("marca de abertura:\n veio:  %+v\n quero: %+v", marcas, quer)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerWouldOpen)); n != 0 {
		t.Fatalf("em enforce nao se grava «teria aberto»: %d", n)
	}
	antes := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned))
	for i := 0; i < 200; i++ {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-depois-%03d", i), 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
			t.Fatalf("com o disjuntor aberto o plano novo %d foi para %+v", i, d)
		}
	}
	if depois := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); depois != antes {
		t.Fatalf("com o disjuntor aberto foram atribuidos %d plano(s) ao candidato", depois-antes)
	}
	if v := l.disjuntor; v.abertoVisto.Load() != 1 || v.disparouVisto.Load() != 1 || v.falhasVistas.Load() != 3 || v.unidadesVistas.Load() != 4 || v.sinaisVistos[sinalErroDoProvider].Load() != 1 {
		t.Fatalf("o que o /metrics le: aberto=%d disparou=%d falhas=%d unidades=%d", v.abertoVisto.Load(), v.disparouVisto.Load(), v.falhasVistas.Load(), v.unidadesVistas.Load())
	}
}

// O PLANO A MEIO É RECUSADO, E NÃO PASSA PARA O TITULAR. Um plano já atribuído ao candidato, com o
// disjuntor aberto: o run novo desse plano dá o erro próprio — nem candidato, nem titular.
func TestAOS521_Livro_Enforce_OPlanoJaAtribuidoERecusado(t *testing.T) {
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	aos520Decidir(t, l, aos520Cand, "plano-a-meio", 0)
	aos521TresFalhas(t, l, aos520Cand)
	d, err := l.decidir(context.Background(), aos520Cand, chaveDeRun("plano-a-meio"), 0)
	if !errors.Is(err, ErrCandidateBreakerOpen) || d.papel != "" {
		t.Fatalf("o run novo de um plano do candidato, com o disjuntor aberto: %+v, %v; quer ErrCandidateBreakerOpen", d, err)
	}
}

// EM `observe`: conta, grava «teria aberto» UMA vez com as mesmas contagens, e NÃO corta — os planos
// novos continuam a ir para o candidato, e o plano a meio continua nele.
func TestAOS521_Livro_Observe_RegistaENaoCorta(t *testing.T) {
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerObserve)
	aos520Decidir(t, l, aos520Cand, "plano-a-meio", 0)
	aos521TresFalhas(t, l, aos520Cand)
	teria := aos521Marcas(t, es, EventTypeModelBreakerWouldOpen)
	if len(teria) != 1 || teria[0].Rule != regraFalhasNaJanela || teria[0].EntryDigest != aos520Cand.entryDigest || len(teria[0].Window) != 4 || teria[0].Counters[sinalErroDoProvider] != 1 {
		t.Fatalf("«teria aberto»: %+v", teria)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 0 {
		t.Fatalf("em observe nao se grava a abertura: %d", n)
	}
	for i := 0; i < 30; i++ {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-novo-%02d", i), 0); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
			t.Fatalf("em observe o plano novo %d foi para %+v", i, d)
		}
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-a-meio", 0); d.papel != canary.RoleCandidate || d.razao != razaoPlanoJaAtribuido {
		t.Fatalf("em observe o plano a meio continua no candidato: %+v", d)
	}
	if v := l.disjuntor; v.abertoVisto.Load() != 0 || v.disparouVisto.Load() != 1 {
		t.Fatalf("em observe: aberto=%d (quer 0) teria aberto=%d (quer 1)", v.abertoVisto.Load(), v.disparouVisto.Load())
	}
	// EM `observe`, DEPOIS DO «TERIA ABERTO», CONTINUA A MEDIR: o desfecho seguinte grava-se, o
	// `/metrics` mostra a janela de agora, e a marca — o estado do primeiro disparo — não muda.
	antes := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome))
	aos521Desfecho(t, l, aos520Cand, "run-depois", sinalNaoCumprido)
	aos521Desfecho(t, l, aos520Cand, "run-depois-bom", sinalCumprido)
	if depois := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); depois != antes+2 {
		t.Fatalf("em observe, depois do disparo, gravaram-se %d desfecho(s); quer 2", depois-antes)
	}
	if v := l.disjuntor; v.falhasVistas.Load() != 4 || v.unidadesVistas.Load() != 6 || v.sinaisVistos[sinalNaoCumprido].Load() != 2 {
		t.Fatalf("a medicao depois do disparo: falhas=%d unidades=%d nao_cumprido=%d; quer 4, 6 e 2", v.falhasVistas.Load(), v.unidadesVistas.Load(), v.sinaisVistos[sinalNaoCumprido].Load())
	}
	if agora := aos521Marcas(t, es, EventTypeModelBreakerWouldOpen); len(agora) != 1 || !reflect.DeepEqual(agora[0], teria[0]) {
		t.Fatalf("a marca mudou com as observacoes seguintes: %+v", agora)
	}
}

// O TECTO DO LIVRO. Em observação as observações continuam depois do disparo, e o livro lê-se em cada
// run novo: por registo guardam-se no máximo [disjuntorMaxObservacoes]. Atingido, não se grava mais
// nada — sem erro, e sem contar como observação perdida.
func TestAOS521_Livro_OTectoDeObservacoesPorRegisto(t *testing.T) {
	if disjuntorMaxObservacoes != 400 {
		t.Fatalf("o tecto e %d; o ADR-041 2.9 escreve 400", disjuntorMaxObservacoes)
	}
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerObserve)
	for i := 0; i < disjuntorMaxObservacoes+7; i++ {
		aos521Desfecho(t, l, aos520Cand, fmt.Sprintf("run-%04d", i), sinalNaoCumprido)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); n != disjuntorMaxObservacoes {
		t.Fatalf("observacoes no livro = %d; quer o tecto, %d", n, disjuntorMaxObservacoes)
	}
	if l.disjuntor.naoGravadas.Load() != 0 {
		t.Fatal("uma observacao acima do tecto nao e uma observacao perdida")
	}
	// O tecto é POR REGISTO: outro registo grava.
	outro := aos520Cand
	outro.entryDigest = "sha256:" + strings.Repeat("e", 64)
	lo := aos521Livro(t, es, outro, modelBreakerObserve)
	aos521Desfecho(t, lo, outro, "run-do-outro", sinalCumprido)
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); n != disjuntorMaxObservacoes+1 {
		t.Fatalf("o tecto de um registo calou outro: %d", n)
	}
}

// A PRIMEIRA ESCRITA GANHA, na leitura. Dois desfechos do mesmo run no livro (que a chave de
// idempotência não deixa este código escrever) contam UM; e dois turnos com o modelo trocado do mesmo
// run contam um.
func TestAOS521_Livro_APrimeiraEscritaGanhaNaLeitura(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerObserve)
	gravar := func(tipo, passo string, p disjuntorPayload) {
		t.Helper()
		p.V, p.EntryDigest, p.Alias, p.ProfileDigest = "1.0", aos520Cand.entryDigest, aos520Cand.alias, aos520Cand.profileDigest
		payload, _ := json.Marshal(p)
		if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: tipo, Payload: payload, RunID: canaryRunID, StepID: passo, Producer: eventstore.Producer{NHIID: disjuntorNHI}}); err != nil {
			t.Fatal(err)
		}
	}
	gravar(EventTypeModelBreakerRunOutcome, "a", disjuntorPayload{RunID: "run-1", Unit: "run-1", Signal: sinalNaoCumprido})
	gravar(EventTypeModelBreakerRunOutcome, "b", disjuntorPayload{RunID: "run-1", Unit: "outra-unidade", Signal: sinalErroDoProvider})
	gravar(EventTypeModelBreakerRunOutcome, "c", disjuntorPayload{RunID: "run-1", Unit: "terceira-unidade", Signal: sinalRespostaVazia})
	st, err := l.ler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	regra := avaliarDisjuntor(st.disjuntorDe(aos520Cand.entryDigest).obs)
	if regra.disparou || len(regra.janela) != 1 || regra.janela[0] != (itemDaJanela{"run-1", sinalNaoCumprido}) || regra.contadores[sinalNaoCumprido] != 1 || len(regra.contadores) != 1 {
		t.Fatalf("tres desfechos do mesmo run contaram mais de um: %+v", regra)
	}
	gravar(EventTypeModelBreakerRouteMismatch, "d", disjuntorPayload{RunID: "run-2"})
	gravar(EventTypeModelBreakerRouteMismatch, "e", disjuntorPayload{RunID: "run-2"})
	st, _ = l.ler(ctx)
	if regra := avaliarDisjuntor(st.disjuntorDe(aos520Cand.entryDigest).obs); regra.contadoresAgora[sinalModeloDiferente] != 1 {
		t.Fatalf("dois turnos trocados do mesmo run contaram %d", regra.contadoresAgora[sinalModeloDiferente])
	}
}

// A MARCA QUE PERDE O CAS GRAVA-SE SEM ELE — e por isso `off` não devolve o que o `enforce` cortou.
// Uma réplica com a vista do stream atrasada perde TODAS as escritas com CAS; as outras passam. As
// três falhas em `enforce` cortam, a marca de aberto FICA no livro, e um nó depois posto em `off`,
// ou em `observe`, continua a não dar planos ao candidato.
func TestAOS521_Livro_AMarcaQuePerdeOCASGravaSeSemEle_EOffNaoReabre(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	for i, sinal := range []string{sinalNaoCumprido, sinalRespostaVazia, sinalErroDoProvider} {
		run := fmt.Sprintf("run-%d", i)
		payload, _ := json.Marshal(disjuntorPayload{V: "1.0", EntryDigest: aos520Cand.entryDigest, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest, RunID: run, Unit: run, Signal: sinal})
		if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelBreakerRunOutcome, Payload: payload, RunID: canaryRunID, StepID: passoDoDisjuntor("breaker-run-", run), Producer: eventstore.Producer{NHIID: disjuntorNHI}}); err != nil {
			t.Fatal(err)
		}
	}
	atrasado := &aos521StoreSemMarca{Store: es, conflito: true, soComCAS: true}
	l := &livroDoCanary{es: atrasado, disjuntor: novoDisjuntorDoModelo(modelBreakerEnforce, aos520Cand.entryDigest)}
	if aberto, _, err := l.conferirDisjuntor(ctx, aos520Cand); err != nil || !aberto {
		t.Fatalf("em enforce a regra corta: aberto=%v err=%v", aberto, err)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 1 || atrasado.recusadas == 0 {
		t.Fatalf("a marca que perdeu o CAS tinha de ficar gravada sem ele: marcas=%d, escritas com CAS recusadas=%d", n, atrasado.recusadas)
	}
	for _, modo := range []string{"", modelBreakerObserve} {
		depois := aos521Livro(t, es, aos520Cand, modo)
		for i := 0; i < 20; i++ {
			if d := aos520Decidir(t, depois, aos520Cand, fmt.Sprintf("plano-%q-%02d", modo, i), 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
				t.Fatalf("modo %q depois do corte em enforce: o plano %d foi para %+v — a fatia voltou ao candidato sem registo novo", modo, i, d)
			}
		}
	}
}

// O RESÍDUO, AFIRMADO: se o Event Store recusa a escrita da marca de TODAS as maneiras, o `enforce`
// corta na mesma (decide a regra), cada tentativa falhada conta em `aos_model_breaker_unrecorded_total`
// — e é só por isso que se vê: sem a marca no livro, um nó posto depois em `off` volta a dar planos
// ao candidato. ADR-041 §5 e o runbook dizem como o confirmar antes de mudar de modo.
func TestAOS521_Livro_Residuo_SemMarcaNoLivroOffDevolveAFatia_EConta(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	avariado := &aos521StoreSemMarca{Store: es}
	l := &livroDoCanary{es: avariado, disjuntor: novoDisjuntorDoModelo(modelBreakerEnforce, aos520Cand.entryDigest)}
	aos521TresFalhas(t, l, aos520Cand)
	for i := 0; i < 5; i++ {
		if d, err := l.decidir(ctx, aos520Cand, chaveDeRun(fmt.Sprintf("plano-%d", i)), 0); err != nil || d.razao != razaoDisjuntorAberto {
			t.Fatalf("em enforce, sem conseguir gravar a marca, corta na mesma: %+v %v", d, err)
		}
	}
	// EM `enforce`, DEPOIS DO DISPARO, NÃO SE GRAVA MAIS NADA — mesmo sem a marca no livro: o
	// conjunto das observações de um registo que disparou em `enforce` fica estável.
	observadas := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome))
	aos521Desfecho(t, l, aos520Cand, "run-depois-do-disparo", sinalNaoCumprido)
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); n != observadas {
		t.Fatalf("em enforce, depois do disparo e sem marca, gravaram-se %d observacao(oes)", n-observadas)
	}
	// (a) A MARCA É RECONCILIADA: tentada em cada decisão, e cada falha conta.
	if avariado.recusadas < 6 || l.disjuntor.naoGravadas.Load() != int64(avariado.recusadas) {
		t.Fatalf("tentativas de gravar a marca = %d, contadas = %d; quer uma por decisao, todas contadas", avariado.recusadas, l.disjuntor.naoGravadas.Load())
	}
	// (b) O RESÍDUO: sem a marca, `off` não tem como saber.
	if d := aos520Decidir(t, aos521Livro(t, es, aos520Cand, ""), aos520Cand, "plano-com-off", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("residuo conhecido (ADR-041 5): sem a marca no livro, off devolve a fatia; veio %+v — se isto mudou, actualiza o ADR e o runbook", d)
	}
	// E reparado o Event Store, a decisão seguinte em `enforce` grava a marca, e `off` passa a respeitá-la.
	aos520Decidir(t, aos521Livro(t, es, aos520Cand, modelBreakerEnforce), aos520Cand, "plano-reparado", 0)
	if d := aos520Decidir(t, aos521Livro(t, es, aos520Cand, ""), aos520Cand, "plano-com-off-2", 0); d.razao != razaoDisjuntorAberto {
		t.Fatalf("com a marca reposta, off respeita-a: %+v", d)
	}
}

// A MARCA DE UM REGISTO FORA DE VIGOR DI-LO. Um run antigo, fixado sob o registo A, acaba num nó cujo
// registo em vigor é B (o mesmo alias): a regra dispara para A, a marca grava-se para A com
// `out_of_force`, o log diz o digest e «fora de vigor» — e NÃO diz que o candidato deixa de receber —,
// e o `/metrics` e as decisões de B não mudam.
func TestAOS521_Livro_AMarcaDeUmRegistoForaDeVigorDiLo(t *testing.T) {
	es := aos520Store(t)
	a := aos520Cand
	b := aos520Cand
	b.entryDigest = "sha256:" + strings.Repeat("c", 64)
	aos520Livro(t, es, a)
	l := aos521Livro(t, es, b, modelBreakerEnforce)
	var dito []string
	l.log = func(f string, args ...any) { dito = append(dito, fmt.Sprintf(f, args...)) }
	aos521TresFalhas(t, l, a)
	marcas := aos521Marcas(t, es, EventTypeModelBreakerOpened)
	if len(marcas) != 1 || marcas[0].EntryDigest != a.entryDigest || !marcas[0].OutOfForce {
		t.Fatalf("a marca do registo fora de vigor: %+v", marcas)
	}
	log := strings.Join(dito, "\n")
	if !strings.Contains(log, "REGISTO FORA DE VIGOR") || !strings.Contains(log, a.entryDigest) || !strings.Contains(log, b.entryDigest) || strings.Contains(log, "DEIXA DE RECEBER RUNS NOVOS") {
		t.Fatalf("o log da marca de um registo fora de vigor:\n%s", log)
	}
	if v := l.disjuntor; v.abertoVisto.Load() != 0 || v.disparouVisto.Load() != 0 || v.falhasVistas.Load() != 0 {
		t.Fatalf("o /metrics do registo em vigor mudou por causa de outro registo: aberto=%d disparou=%d falhas=%d", v.abertoVisto.Load(), v.disparouVisto.Load(), v.falhasVistas.Load())
	}
	if d := aos520Decidir(t, l, b, "plano-de-b", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("o registo em vigor continua a receber: %+v", d)
	}
	// E a marca do registo EM VIGOR não leva o campo.
	for i := 0; i < 3; i++ {
		aos521Desfecho(t, l, b, fmt.Sprintf("run-de-b-%d", i), sinalErroDoProvider)
	}
	for _, m := range aos521Marcas(t, es, EventTypeModelBreakerOpened) {
		if m.EntryDigest == b.entryDigest && m.OutOfForce {
			t.Fatalf("a marca do registo em vigor diz fora de vigor: %+v", m)
		}
	}
	if !strings.Contains(strings.Join(dito, "\n"), "DEIXA DE RECEBER RUNS NOVOS") {
		t.Fatal("a abertura do registo em vigor tem de dizer que o candidato deixa de receber")
	}
}

// aos521StoreQuePendura é um Event Store cujas leituras do livro ficam penduradas até o contexto
// acabar — um Event Store avariado que não responde.
type aos521StoreQuePendura struct {
	*eventstore.Store
	pendurado  atomic.Bool
	lidasDoRun atomic.Int64
}

func (s *aos521StoreQuePendura) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	if streamID != canaryStream {
		s.lidasDoRun.Add(1)
	} else if s.pendurado.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.Store.Read(ctx, streamID, fromSeq)
}

// aos521RunDoCandidato grava no stream de um run o `run.model_pinned` que o nó escreve num run do
// candidato dado.
func aos521RunDoCandidato(t *testing.T, es eventstore.EventStore, runID string, c candidatoEmVigor) {
	t.Helper()
	p, _ := json.Marshal(modeloFixadoPayload{V: "1.0", Role: "candidato", Alias: c.alias, ProfileDigest: c.profileDigest, EntryDigest: c.entryDigest, SharePercent: c.fatia, Function: versaoDaEscolha, Reason: razaoFuncao, PlanKey: runID, PlanKeyKind: tipoDeRun})
	if _, err := es.Append(context.Background(), runID, eventstore.EventInput{Type: EventTypeRunModelPinned, Payload: p, RunID: runID, StepID: passoDoModeloFixado, Producer: eventstore.Producer{NHIID: modeloFixadoNHI}}); err != nil {
		t.Fatal(err)
	}
}

// O EVENT STORE PENDURADO NÃO PRENDE O TURNO, E O MODELO TROCADO NÃO PASSA EM SILÊNCIO. Um turno do
// candidato com o modelo servido diferente, com o livro a não responder: a chamada volta dentro do
// prazo de I/O, a observação conta como não gravada — e, em `enforce`, esta réplica passa a tratar o
// disjuntor como aberto (os planos novos vão para o titular). Em `observe` não corta. Reposto o Event
// Store, o turno seguinte com o modelo trocado grava-se; e o mesmo run não volta a ler o seu log.
func TestAOS521_OEventStorePenduradoNaoPrendeOTurno(t *testing.T) {
	for _, modo := range []string{modelBreakerEnforce, modelBreakerObserve} {
		t.Run(modo, func(t *testing.T) {
			es := &aos521StoreQuePendura{Store: aos520Store(t)}
			l := aos521Livro(t, es, aos520Cand, modo)
			l.disjuntor.prazoDeIO = 200 * time.Millisecond
			c := &canaryDoNo{livro: l, servir: true, candidato: aos520Cand}
			aos521RunDoCandidato(t, es, "run-trocado", aos520Cand)
			trocado := agentruntime.ModelResponse{RouteCheck: agentruntime.RouteDifferent}
			ctx := agentruntime.ContextWithModelCall(context.Background(), "run-trocado", "passo-1")

			es.pendurado.Store(true)
			feito := make(chan struct{})
			go func() { defer close(feito); c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, trocado, nil) }()
			select {
			case <-feito:
			case <-time.After(10 * time.Second):
				t.Fatal("o Event Store pendurado prendeu o turno")
			}
			if l.disjuntor.naoGravadas.Load() != 1 {
				t.Fatalf("o turno que nao se gravou tem de contar: %d", l.disjuntor.naoGravadas.Load())
			}
			es.pendurado.Store(false)
			d := aos520Decidir(t, l, aos520Cand, "plano-novo", 0)
			if modo == modelBreakerEnforce && (d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto) {
				t.Fatalf("em enforce, o modelo trocado que nao se gravou tem de cortar nesta replica: %+v", d)
			}
			if modo == modelBreakerObserve && d.papel != canary.RoleCandidate {
				t.Fatalf("em observe nao corta: %+v", d)
			}
			// EM `observe` NADA CORTA, nem que a réplica tivesse o sinal de pé: quem o lê é o `enforce`.
			if modo == modelBreakerObserve {
				l.disjuntor.turnoPorGravar.Store(true)
				if d := aos520Decidir(t, l, aos520Cand, "plano-novo-2", 0); d.papel != canary.RoleCandidate {
					t.Fatalf("em observe, o turno trocado por gravar cortou: %+v", d)
				}
				l.disjuntor.turnoPorGravar.Store(false)
			} else if !l.disjuntor.turnoPorGravar.Load() {
				t.Fatal("em enforce, o turno trocado que nao se gravou tem de deixar o sinal de pe")
			}
			// Um turno do TITULAR, ou sem o modelo trocado, não faz nada.
			antes := es.lidasDoRun.Load()
			c.observarTurnoNoDisjuntor(ctx, "gpt-4o", trocado, nil)
			c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, agentruntime.ModelResponse{RouteCheck: agentruntime.RouteEqual}, nil)
			c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, agentruntime.ModelResponse{RouteCheck: agentruntime.RouteUnreported}, nil)
			if es.lidasDoRun.Load() != antes {
				t.Fatal("um turno que nao e do candidato com o modelo trocado leu o log do run")
			}
			// Reposto o Event Store, o turno seguinte grava-se — e depois disso o run não volta a ler.
			c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, trocado, nil)
			if n := len(aos521Marcas(t, es.Store, EventTypeModelBreakerRouteMismatch)); n != 1 {
				t.Fatalf("o turno trocado, com o Event Store reposto, tinha de ficar no livro: %d", n)
			}
			lidas := es.lidasDoRun.Load()
			c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, trocado, nil)
			c.observarTurnoNoDisjuntor(ctx, aos520Cand.alias, trocado, nil)
			if es.lidasDoRun.Load() != lidas {
				t.Fatalf("o run ja tratado voltou a ler o seu log %d vez(es)", es.lidasDoRun.Load()-lidas)
			}
		})
	}
}

// O DESFECHO QUE NÃO SE GRAVA CONTA, E NÃO PRENDE A SAÍDA DA HOSPEDAGEM.
func TestAOS521_ODesfechoQueNaoSeGravaConta(t *testing.T) {
	es := &aos521StoreQuePendura{Store: aos520Store(t)}
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	l.disjuntor.prazoDeIO = 200 * time.Millisecond
	var dito []string
	l.log = func(f string, args ...any) { dito = append(dito, fmt.Sprintf(f, args...)) }
	aos521RunDoCandidato(t, es, "run-1", aos520Cand)
	svc := &NodeService{node: &Node{EventStore: es, canary: &canaryDoNo{livro: l, servir: true, candidato: aos520Cand}}, logw: io.Discard}
	goal := agentruntime.Goal{RunID: "run-1", ModelPinnedBy: versaoDaEscolha}
	goal.Model.ModelID = aos520Cand.alias
	es.pendurado.Store(true)
	feito := make(chan struct{})
	go func() {
		defer close(feito)
		svc.observarDesfechoNoDisjuntor("run-1", goal, state.Failed, agentruntime.Result{Unfulfilled: true, Verdict: aos521Verdicto(agentruntime.OutcomeContractNoCall)}, nil, false)
	}()
	select {
	case <-feito:
	case <-time.After(10 * time.Second):
		t.Fatal("o Event Store pendurado prendeu a saida da hospedagem")
	}
	if l.disjuntor.naoGravadas.Load() != 1 {
		t.Fatalf("o desfecho que nao se gravou tem de contar: %d", l.disjuntor.naoGravadas.Load())
	}
	es.pendurado.Store(false)
	svc.observarDesfechoNoDisjuntor("run-1", goal, state.Failed, agentruntime.Result{Unfulfilled: true, Verdict: aos521Verdicto(agentruntime.OutcomeContractNoCall)}, nil, false)
	if d := aos521Marcas(t, es.Store, EventTypeModelBreakerRunOutcome); len(d) != 1 || d[0].Signal != sinalNaoCumprido || l.disjuntor.naoGravadas.Load() != 1 {
		t.Fatalf("reposto o Event Store, o desfecho grava-se: %+v", d)
	}
}

// `observe` E `off` REPARTEM IGUAL. A mesma sequência de identificadores e de desfechos, num livro
// com o disjuntor desligado e noutro em observação: as mesmas decisões, uma a uma.
func TestAOS521_Livro_ObserveReparteComoOff(t *testing.T) {
	decisoes := func(modo string) []decisaoDoCanary {
		es := aos520Store(t)
		c := aos520Cand
		c.fatia = 25
		l := aos521Livro(t, es, c, modo)
		var out []decisaoDoCanary
		for i := 0; i < 120; i++ {
			d := aos520Decidir(t, l, c, fmt.Sprintf("plano-%04d", i), 0)
			out = append(out, d)
			if d.papel == canary.RoleCandidate {
				sinal := sinalCumprido
				if i%3 == 0 {
					sinal = sinalNaoCumprido
				}
				if err := l.observarNoDisjuntor(context.Background(), c.registo(), observacaoDoDisjuntor{run: fmt.Sprintf("plano-%04d", i), unidade: fmt.Sprintf("plano-%04d", i), sinal: sinal}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if modo == "" {
			if n := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); n != 0 {
				t.Fatalf("desligado, o disjuntor gravou %d desfecho(s)", n)
			}
		} else if len(aos521Marcas(t, es, EventTypeModelBreakerWouldOpen)) != 1 {
			t.Fatal("o cenario tinha de fazer a regra disparar em observe")
		}
		return out
	}
	off, observe := decisoes(""), decisoes(modelBreakerObserve)
	if !reflect.DeepEqual(off, observe) {
		t.Fatalf("observe repartiu de outra maneira:\n off:     %+v\n observe: %+v", off, observe)
	}
	doCandidato := 0
	for _, d := range off {
		if d.papel == canary.RoleCandidate {
			doCandidato++
		}
	}
	if doCandidato < 10 {
		t.Fatalf("o cenario nao da planos ao candidato (%d): nao prova nada", doCandidato)
	}
}

// O TURNO COM O MODELO SERVIDO DIFERENTE ABRE AO PRIMEIRO, com a janela vazia.
func TestAOS521_Livro_OTurnoComOModeloDiferenteAbreAoPrimeiro(t *testing.T) {
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	if err := l.observarNoDisjuntor(context.Background(), aos520Cand.registo(), observacaoDoDisjuntor{turno: true, run: "run-trocado"}); err != nil {
		t.Fatal(err)
	}
	marcas := aos521Marcas(t, es, EventTypeModelBreakerOpened)
	if len(marcas) != 1 || marcas[0].Rule != regraModeloDiferente || len(marcas[0].Window) != 0 || marcas[0].Counters[sinalModeloDiferente] != 1 {
		t.Fatalf("a abertura pelo modelo diferente: %+v", marcas)
	}
	if turnos := aos521Marcas(t, es, EventTypeModelBreakerRouteMismatch); len(turnos) != 1 || turnos[0].RunID != "run-trocado" {
		t.Fatalf("o turno gravado: %+v", turnos)
	}
	if d := aos520Decidir(t, l, aos520Cand, "plano-novo", 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
		t.Fatalf("depois do turno trocado: %+v", d)
	}
}

// O REINÍCIO NÃO PERDE O ESTADO. Um livro novo sobre o mesmo store — sem nada em memória — vê o
// disjuntor aberto, com as mesmas contagens; e a marca não se grava outra vez.
func TestAOS521_Livro_OReinicioNaoPerdeOEstado(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	aos521Desfecho(t, l, aos520Cand, "run-f1", sinalNaoCumprido)
	aos521Desfecho(t, l, aos520Cand, "run-f2", sinalRespostaVazia)

	// Reinício a meio da janela: as duas falhas estão lá, e a terceira abre.
	l2 := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	if aberto, regra, err := l2.conferirDisjuntor(ctx, aos520Cand); err != nil || aberto || regra.falhasNaJanela() != 2 {
		t.Fatalf("depois do reinicio: aberto=%v falhas=%d err=%v; quer fechado com 2 falhas", aberto, regra.falhasNaJanela(), err)
	}
	aos521Desfecho(t, l2, aos520Cand, "run-f3", sinalRespostaRejeitada)

	l3 := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	aberto, regra, err := l3.conferirDisjuntor(ctx, aos520Cand)
	if err != nil || !aberto || regra.regra != regraFalhasNaJanela || regra.falhasNaJanela() != 3 || len(regra.janela) != 3 {
		t.Fatalf("depois do segundo reinicio: aberto=%v regra=%+v err=%v", aberto, regra, err)
	}
	if d := aos520Decidir(t, l3, aos520Cand, "plano-novo", 0); d.razao != razaoDisjuntorAberto {
		t.Fatalf("depois do reinicio o disjuntor continua aberto: %+v", d)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("marcas de abertura depois de dois reinicios = %d; quer 1", n)
	}
}

// O REINÍCIO SOBRE FICHEIRO. O mesmo, com o Event Store em disco: o ficheiro fecha-se e abre-se outra
// vez, como num reinício do nó. O disjuntor continua aberto, a marca é a mesma, byte a byte, e as
// decisões são as de antes.
func TestAOS521_Livro_OReinicioSobreFicheiro(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "events.wal")
	es, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatal(err)
	}
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	aos520Decidir(t, l, aos520Cand, "plano-a-meio", 0)
	aos521TresFalhas(t, l, aos520Cand)
	antes := aos521Marcas(t, es, EventTypeModelBreakerOpened)
	if len(antes) != 1 {
		t.Fatalf("marcas antes do reinicio = %d", len(antes))
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}

	es2, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es2.Close() }()
	l2 := aos521Livro(t, es2, aos520Cand, modelBreakerEnforce)
	aberto, regra, err := l2.conferirDisjuntor(ctx, aos520Cand)
	if err != nil || !aberto || regra.regra != regraFalhasNaJanela || regra.falhasNaJanela() != 3 || len(regra.janela) != 4 || regra.contadores[sinalErroDoProvider] != 1 {
		t.Fatalf("depois de reabrir o ficheiro: aberto=%v regra=%+v err=%v", aberto, regra, err)
	}
	if depois := aos521Marcas(t, es2, EventTypeModelBreakerOpened); !reflect.DeepEqual(antes, depois) {
		t.Fatalf("a marca mudou com o reinicio:\n antes:  %+v\n depois: %+v", antes, depois)
	}
	if d := aos520Decidir(t, l2, aos520Cand, "plano-novo", 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
		t.Fatalf("depois do reinicio o plano novo foi para %+v", d)
	}
	if _, err := l2.decidir(ctx, aos520Cand, chaveDeRun("plano-a-meio"), 0); !errors.Is(err, ErrCandidateBreakerOpen) {
		t.Fatalf("depois do reinicio o plano a meio: %v", err)
	}
}

// NÃO FECHA SOZINHO. Aberto, continua aberto: com o disjuntor posto em `off` (a marca vale na
// mesma), de volta a `enforce`, em `observe`, e depois de 200 decisões. Não há caminho que o feche.
func TestAOS521_Livro_NaoFechaSozinho_NemComOff(t *testing.T) {
	es := aos520Store(t)
	aos521TresFalhas(t, aos521Livro(t, es, aos520Cand, modelBreakerEnforce), aos520Cand)
	for _, modo := range []string{"", modelBreakerObserve, modelBreakerEnforce, ""} {
		l := aos521Livro(t, es, aos520Cand, modo)
		for i := 0; i < 50; i++ {
			if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-%q-%02d", modo, i), 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
				t.Fatalf("modo %q: o disjuntor aberto deixou passar o plano %d: %+v", modo, i, d)
			}
		}
		if !l.disjuntorAbertoVisto.Load() {
			t.Fatalf("modo %q: a leitura nao viu o disjuntor aberto", modo)
		}
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("marcas de abertura = %d; quer 1", n)
	}
	if n := len(aos520EventosDoLivro(t, es, EventTypeModelCanaryPlanAssigned)); n != 0 {
		t.Fatalf("planos atribuidos ao candidato com o disjuntor aberto: %d", n)
	}
}

// «TERIA ABERTO» NÃO É «ABERTO» — mas as observações são as mesmas. Um nó em `off` que leia um
// «teria aberto» não corta (a marca do `observe` não vale como abertura). Um nó em `enforce` sobre o
// MESMO livro abre na primeira leitura: a regra dispara sobre as observações que lá estão, e a
// abertura fica gravada com a janela do disparo.
func TestAOS521_Livro_DeObserveParaEnforce_AbreNaPrimeiraLeitura(t *testing.T) {
	es := aos520Store(t)
	aos521TresFalhas(t, aos521Livro(t, es, aos520Cand, modelBreakerObserve), aos520Cand)
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 0 {
		t.Fatalf("observe nao abre: %d", n)
	}
	desligado := aos521Livro(t, es, aos520Cand, "")
	if d := aos520Decidir(t, desligado, aos520Cand, "plano-com-off", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("com off, um «teria aberto» nao corta: %+v", d)
	}
	imposto := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	if d := aos520Decidir(t, imposto, aos520Cand, "plano-com-enforce", 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
		t.Fatalf("em enforce, o registo cujo observe teria aberto abre logo: %+v", d)
	}
	abertas, teria := aos521Marcas(t, es, EventTypeModelBreakerOpened), aos521Marcas(t, es, EventTypeModelBreakerWouldOpen)
	if len(abertas) != 1 || len(teria) != 1 || !reflect.DeepEqual(abertas[0], teria[0]) {
		t.Fatalf("a abertura tem de trazer a regra, os contadores e a janela do disparo:\n aberta: %+v\n teria:  %+v", abertas, teria)
	}
	// E a marca de aberto passa a valer para o nó em `off`.
	if d := aos520Decidir(t, aos521Livro(t, es, aos520Cand, ""), aos520Cand, "plano-com-off-depois", 0); d.razao != razaoDisjuntorAberto {
		t.Fatalf("com off, a marca de aberto vale: %+v", d)
	}
}

// A MARCA EM FALTA É REPOSTA NA DECISÃO. O nó que gravou a terceira falha morreu antes de gravar a
// abertura: a decisão seguinte, em `enforce`, corta na mesma (decide a regra) e grava a marca.
func TestAOS521_Livro_AMarcaEmFaltaEReposta_EADecisaoNaoDependeDela(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	aos520Livro(t, es, aos520Cand)
	for i, sinal := range []string{sinalNaoCumprido, sinalRespostaVazia, sinalErroDoProvider} {
		run := fmt.Sprintf("run-%d", i)
		payload, _ := json.Marshal(disjuntorPayload{V: "1.0", EntryDigest: aos520Cand.entryDigest, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest, RunID: run, Unit: run, Signal: sinal})
		if _, err := es.Append(ctx, canaryStream, eventstore.EventInput{Type: EventTypeModelBreakerRunOutcome, Payload: payload, RunID: canaryRunID, StepID: passoDoDisjuntor("breaker-run-", run), Producer: eventstore.Producer{NHIID: disjuntorNHI}}); err != nil {
			t.Fatal(err)
		}
	}
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	if d := aos520Decidir(t, l, aos520Cand, "plano-novo", 0); d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto {
		t.Fatalf("tres falhas no livro e sem marca: a regra decide, e corta: %+v", d)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("a marca em falta nao foi reposta: %d", n)
	}
	// A DECISÃO NÃO DEPENDE DA MARCA: com um store que recusa a gravação dela, corta na mesma.
	es2 := aos520Store(t)
	aos520Livro(t, es2, aos520Cand)
	evs, _ := es.Read(ctx, canaryStream, 1)
	for _, ev := range evs {
		if ev.Type == EventTypeModelBreakerRunOutcome {
			if _, err := es2.Append(ctx, canaryStream, eventstore.EventInput{Type: ev.Type, Payload: ev.Payload, RunID: ev.RunID, StepID: ev.StepID, Producer: ev.Producer}); err != nil {
				t.Fatal(err)
			}
		}
	}
	semMarca := &aos521StoreSemMarca{Store: es2}
	l2 := &livroDoCanary{es: semMarca, disjuntor: novoDisjuntorDoModelo(modelBreakerEnforce, aos520Cand.entryDigest)}
	if d, err := l2.decidir(ctx, aos520Cand, chaveDeRun("plano-novo"), 0); err != nil || d.razao != razaoDisjuntorAberto {
		t.Fatalf("sem conseguir gravar a marca, a decisao tem de cortar na mesma: %+v, %v", d, err)
	}
	if semMarca.recusadas == 0 {
		t.Fatal("o teste nao chegou a tentar gravar a marca")
	}
}

// A MARCA QUE PERDE SEMPRE O CAS NÃO RECUSA RUNS NEM IMPEDE O ARRANQUE. É o que acontece a uma réplica
// com a vista do stream atrasada (o defeito do JetStream, AOS-524): a marca nunca fica gravada. Em
// `observe` as decisões são as de `off`; em `enforce` a regra corta na mesma; o arranque diz o estado
// sem falhar; e uma observação que já está no livro não é dada por perdida.
func TestAOS521_Livro_AMarcaQuePerdeOCASNaoRecusaRuns(t *testing.T) {
	ctx := context.Background()
	for _, modo := range []string{modelBreakerObserve, modelBreakerEnforce} {
		t.Run(modo, func(t *testing.T) {
			es := aos520Store(t)
			aos520Livro(t, es, aos520Cand)
			atrasado := &aos521StoreSemMarca{Store: es, conflito: true}
			l := &livroDoCanary{es: atrasado, disjuntor: novoDisjuntorDoModelo(modo, aos520Cand.entryDigest)}
			for i, sinal := range []string{sinalNaoCumprido, sinalRespostaVazia, sinalErroDoProvider} {
				run := fmt.Sprintf("run-%d", i)
				if err := l.observarNoDisjuntor(ctx, aos520Cand.registo(), observacaoDoDisjuntor{run: run, unidade: run, sinal: sinal}); err != nil {
					t.Fatalf("a observacao %d ficou no livro e foi dada por perdida: %v", i, err)
				}
			}
			if n := len(aos521Marcas(t, es, EventTypeModelBreakerRunOutcome)); n != 3 {
				t.Fatalf("desfechos no livro = %d; quer 3", n)
			}
			aberto, regra, err := l.conferirDisjuntor(ctx, aos520Cand)
			if err != nil || !regra.disparou || aberto != (modo == modelBreakerEnforce) {
				t.Fatalf("o arranque com a marca por gravar: aberto=%v disparou=%v err=%v", aberto, regra.disparou, err)
			}
			for i := 0; i < 10; i++ {
				d, err := l.decidir(ctx, aos520Cand, chaveDeRun(fmt.Sprintf("plano-%d", i)), 0)
				if err != nil {
					t.Fatalf("a marca por gravar recusou o run %d: %v", i, err)
				}
				if modo == modelBreakerObserve && (d.papel != canary.RoleCandidate || d.razao != razaoFuncao) {
					t.Fatalf("em observe, com a marca por gravar, o plano %d foi para %+v", i, d)
				}
				if modo == modelBreakerEnforce && (d.papel != canary.RoleIncumbent || d.razao != razaoDisjuntorAberto) {
					t.Fatalf("em enforce, com a marca por gravar, o plano %d foi para %+v", i, d)
				}
			}
			if atrasado.recusadas < 10 {
				t.Fatalf("o teste nao chegou a tentar gravar a marca em cada decisao: %d", atrasado.recusadas)
			}
			// CADA TENTATIVA FALHADA CONTA: é por aqui que se vê uma marca por gravar.
			if l.disjuntor.naoGravadas.Load() == 0 {
				t.Fatal("as tentativas falhadas de gravar a marca nao contaram em aos_model_breaker_unrecorded_total")
			}
			if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)) + len(aos521Marcas(t, es, EventTypeModelBreakerWouldOpen)); n != 0 {
				t.Fatalf("o store do teste deixou gravar %d marca(s)", n)
			}
		})
	}
}

// aos521StoreSemMarca recusa a gravação das marcas do disjuntor.
type aos521StoreSemMarca struct {
	*eventstore.Store
	recusadas int
	// conflito faz a recusa ser a perda do CAS, e não um erro de escrita.
	conflito bool
	// soComCAS recusa só as escritas feitas COM concorrência optimista: as outras passam.
	soComCAS bool
}

func (s *aos521StoreSemMarca) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if (in.Type == EventTypeModelBreakerOpened || in.Type == EventTypeModelBreakerWouldOpen) && (!s.soComCAS || len(opts) > 0) {
		s.recusadas++
		if s.conflito {
			return eventstore.AppendResult{}, eventstore.ErrSeqConflict
		}
		return eventstore.AppendResult{}, errors.New("disco cheio")
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// DUAS RÉPLICAS ABREM UMA VEZ. Dois livros sobre o mesmo store — cada um com o seu estado em memória,
// como dois processos —, goroutines reais a gravar desfechos e a decidir ao mesmo tempo: fica UMA
// marca de abertura, cada run tem um desfecho, e depois da abertura nenhuma réplica dá um plano ao
// candidato.
func TestAOS521_Livro_DuasReplicasAbremUmaVez(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	const replicas, porReplica = 2, 12
	livros := make([]*livroDoCanary, replicas)
	for i := range livros {
		livros[i] = aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	}
	var wg sync.WaitGroup
	for r := 0; r < replicas; r++ {
		for k := 0; k < porReplica; k++ {
			wg.Add(1)
			go func(r, k int) {
				defer wg.Done()
				run := fmt.Sprintf("run-%d-%02d", r, k)
				// O MESMO run observado pelas duas réplicas (uma re-hospedagem): conta uma vez.
				for _, l := range livros {
					if err := l.observarNoDisjuntor(ctx, aos520Cand.registo(), observacaoDoDisjuntor{run: run, unidade: run, sinal: sinalErroDoProvider}); err != nil && !errors.Is(err, ErrCanaryContention) {
						t.Errorf("observar(%s): %v", run, err)
					}
				}
				if _, err := livros[r].decidir(ctx, aos520Cand, chaveDeRun("plano-"+run), 0); err != nil && !errors.Is(err, ErrCanaryContention) {
					t.Errorf("decidir(%s): %v", run, err)
				}
			}(r, k)
		}
	}
	wg.Wait()
	abertas := aos521Marcas(t, es, EventTypeModelBreakerOpened)
	if len(abertas) != 1 || abertas[0].Rule != regraFalhasNaJanela || len(abertas[0].Window) != 3 || abertas[0].Counters[sinalErroDoProvider] != 3 {
		t.Fatalf("marcas de abertura com duas replicas: %+v; quer UMA, com a janela das tres primeiras falhas", abertas)
	}
	vistos := map[string]bool{}
	for _, d := range aos521Marcas(t, es, EventTypeModelBreakerRunOutcome) {
		if vistos[d.RunID] {
			t.Fatalf("o run %q tem dois desfechos no livro", d.RunID)
		}
		vistos[d.RunID] = true
	}
	// Depois do disparo não se grava mais nada: os três da janela, e nem mais um.
	if len(vistos) != 3 {
		t.Fatalf("desfechos gravados = %d; quer os 3 que dispararam a regra", len(vistos))
	}
	for r, l := range livros {
		if d := aos520Decidir(t, l, aos520Cand, fmt.Sprintf("plano-depois-%d", r), 0); d.razao != razaoDisjuntorAberto {
			t.Fatalf("replica %d: depois da abertura o plano novo foi para %+v", r, d)
		}
	}
}

// UM REGISTO NOVO COMEÇA FECHADO, E O MESMO REGISTO REPOSTO CONTINUA ABERTO. O estado é por digest do
// registo: A abre; B (assinado de novo) não tem observações nem marca e recebe planos; A reposto
// depois de B continua aberto. Repor o mesmo registo não reabre.
func TestAOS521_Livro_RegistoNovoFechado_OMesmoRepostoAberto(t *testing.T) {
	ctx := context.Background()
	es := aos520Store(t)
	a := aos520Cand
	b := aos520Cand
	b.entryDigest = "sha256:" + strings.Repeat("c", 64)
	la := aos521Livro(t, es, a, modelBreakerEnforce)
	aos520Decidir(t, la, a, "plano-de-a", 0)
	aos521TresFalhas(t, la, a)

	lb := aos521Livro(t, es, b, modelBreakerEnforce)
	if aberto, regra, err := lb.conferirDisjuntor(ctx, b); err != nil || aberto || regra.disparou || len(regra.janela) != 0 {
		t.Fatalf("o registo novo comeca fechado, com a janela vazia: aberto=%v regra=%+v err=%v", aberto, regra, err)
	}
	if d := aos520Decidir(t, lb, b, "plano-de-b", 0); d.papel != canary.RoleCandidate || d.razao != razaoFuncao {
		t.Fatalf("o registo novo recebe planos: %+v", d)
	}
	// As falhas de A não contam para B: duas falhas em B não abrem.
	aos521Desfecho(t, lb, b, "run-b1", sinalNaoCumprido)
	aos521Desfecho(t, lb, b, "run-b2", sinalNaoCumprido)
	if d := aos520Decidir(t, lb, b, "plano-de-b-2", 0); d.papel != canary.RoleCandidate {
		t.Fatalf("as falhas do registo anterior contaram para o novo: %+v", d)
	}
	// A reposto depois de B: continua aberto, e o plano que era dele é recusado.
	la2 := aos521Livro(t, es, a, modelBreakerEnforce)
	if d := aos520Decidir(t, la2, a, "plano-novo-sob-a", 0); d.razao != razaoDisjuntorAberto {
		t.Fatalf("o mesmo registo reposto reabriu: %+v", d)
	}
	if _, err := la2.decidir(ctx, a, chaveDeRun("plano-de-a"), 0); !errors.Is(err, ErrCandidateBreakerOpen) {
		t.Fatalf("o plano do registo reposto, com o disjuntor aberto: %v", err)
	}
	if n := len(aos521Marcas(t, es, EventTypeModelBreakerOpened)); n != 1 {
		t.Fatalf("marcas de abertura = %d; quer 1 (a de A)", n)
	}
}

// aos521LerComoOPR1 é o leitor do livro TAL COMO ESTAVA no PR 1 do AOS-520 (be8b169d), reduzido ao
// que decide: os três tipos que essa versão conhece, e o contador dos que não conhece.
func aos521LerComoOPR1(t *testing.T, es eventstore.EventStore) (futuros int, err error) {
	t.Helper()
	evs, rerr := es.Read(context.Background(), canaryStream, 1)
	if rerr != nil {
		return 0, rerr
	}
	for _, ev := range evs {
		switch ev.Type {
		case "model.canary.opened", "model.canary.plan_assigned", "model.canary.closed":
			var p canaryPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil || p.EntryDigest == "" || p.Alias == "" || p.ProfileDigest == "" {
				return futuros, fmt.Errorf("%w: evento %d", ErrCanaryUnreadable, ev.Seq)
			}
		default:
			if !strings.HasPrefix(ev.Type, "model.") {
				return futuros, fmt.Errorf("%w: tipo %q no evento %d", ErrCanaryUnreadable, ev.Type, ev.Seq)
			}
			futuros++
		}
	}
	return futuros, nil
}

// O NÓ DE VERSÃO ANTERIOR NÃO CAI. Os quatro tipos do disjuntor são da família do livro: o leitor do
// PR 1 lê o livro sem erro, conta-os como eventos de uma versão mais recente — e por isso deixa de
// atribuir planos ao candidato, que é o lado fechado. E esta versão conhece-os: não os conta.
func TestAOS521_Livro_ONoDeVersaoAnteriorFicaNaoDecidivelSemCair(t *testing.T) {
	es := aos520Store(t)
	l := aos521Livro(t, es, aos520Cand, modelBreakerEnforce)
	aos520Decidir(t, l, aos520Cand, "plano-1", 0)
	aos521Desfecho(t, l, aos520Cand, "run-ok", sinalCumprido)
	if err := l.observarNoDisjuntor(context.Background(), aos520Cand.registo(), observacaoDoDisjuntor{turno: true, run: "run-trocado"}); err != nil {
		t.Fatal(err)
	}
	// Um «teria aberto», de um nó em observação sobre outro registo, para os quatro tipos lá estarem.
	outro := aos520Cand
	outro.entryDigest = "sha256:" + strings.Repeat("d", 64)
	lo := aos521Livro(t, es, outro, modelBreakerObserve)
	if err := lo.observarNoDisjuntor(context.Background(), outro.registo(), observacaoDoDisjuntor{turno: true, run: "run-do-outro"}); err != nil {
		t.Fatal(err)
	}
	tipos := map[string]int{}
	evs, _ := es.Read(context.Background(), canaryStream, 1)
	for _, ev := range evs {
		if tipoDoDisjuntor(ev.Type) {
			tipos[ev.Type]++
			if !strings.HasPrefix(ev.Type, familiaDoLivro) {
				t.Fatalf("o tipo %q nao e da familia do livro: um no anterior nao arrancava", ev.Type)
			}
		}
	}
	if len(tipos) != 4 {
		t.Fatalf("o cenario tinha de gravar os quatro tipos do disjuntor: %v", tipos)
	}
	futuros, err := aos521LerComoOPR1(t, es)
	if err != nil {
		t.Fatalf("o leitor do PR 1 nao leu o livro com os eventos do disjuntor: %v", err)
	}
	if quer := tipos[EventTypeModelBreakerRunOutcome] + tipos[EventTypeModelBreakerRouteMismatch] + tipos[EventTypeModelBreakerOpened] + tipos[EventTypeModelBreakerWouldOpen]; futuros != quer || futuros == 0 {
		t.Fatalf("o leitor do PR 1 contou %d evento(s) de versao mais recente; quer %d", futuros, quer)
	}
	st, err := l.ler(context.Background())
	if err != nil || st.futuros != 0 {
		t.Fatalf("esta versao conhece os tipos do disjuntor: futuros=%d err=%v", st.futuros, err)
	}
}

// UM EVENTO DO DISJUNTOR QUE NÃO SE SABE LER É ILEGÍVEL — fail-closed, como os do canary: a decisão
// falha, em qualquer modo. Um caso por campo obrigatório.
func TestAOS521_Livro_EventoDoDisjuntorMalformadoRecusa(t *testing.T) {
	base := disjuntorPayload{V: "1.0", EntryDigest: aos520Cand.entryDigest, Alias: aos520Cand.alias, ProfileDigest: aos520Cand.profileDigest}
	com := func(f func(*disjuntorPayload)) disjuntorPayload {
		p := base
		f(&p)
		return p
	}
	for nome, c := range map[string]struct {
		tipo string
		p    any
	}{
		"desfecho sem run":            {EventTypeModelBreakerRunOutcome, com(func(p *disjuntorPayload) { p.Unit, p.Signal = "u", sinalCumprido })},
		"desfecho sem unidade":        {EventTypeModelBreakerRunOutcome, com(func(p *disjuntorPayload) { p.RunID, p.Signal = "r", sinalCumprido })},
		"desfecho com sinal de fora":  {EventTypeModelBreakerRunOutcome, com(func(p *disjuntorPayload) { p.RunID, p.Unit, p.Signal = "r", "u", "limite_de_ritmo" })},
		"turno sem run":               {EventTypeModelBreakerRouteMismatch, base},
		"abertura sem regra":          {EventTypeModelBreakerOpened, base},
		"abertura com regra de fora":  {EventTypeModelBreakerOpened, com(func(p *disjuntorPayload) { p.Rule = "porque_sim" })},
		"teria aberto sem regra":      {EventTypeModelBreakerWouldOpen, base},
		"sem o digest do registo":     {EventTypeModelBreakerOpened, com(func(p *disjuntorPayload) { p.EntryDigest, p.Rule = "", regraFalhasNaJanela })},
		"payload que nao e um objeto": {EventTypeModelBreakerOpened, "abre"},
	} {
		t.Run(nome, func(t *testing.T) {
			es := aos520Store(t)
			aos520Decidir(t, aos520Livro(t, es, aos520Cand), aos520Cand, "plano-ja-atribuido", 0)
			payload, _ := json.Marshal(c.p)
			if _, err := es.Append(context.Background(), canaryStream, eventstore.EventInput{Type: c.tipo, Payload: payload, RunID: canaryRunID, StepID: "x", Producer: eventstore.Producer{NHIID: disjuntorNHI}}); err != nil {
				t.Fatal(err)
			}
			for _, modo := range []string{modelBreakerEnforce, modelBreakerObserve} {
				l := &livroDoCanary{es: es, disjuntor: novoDisjuntorDoModelo(modo, aos520Cand.entryDigest)}
				if _, err := l.decidir(context.Background(), aos520Cand, chaveDeRun("plano"), 0); !errors.Is(err, ErrCanaryUnreadable) {
					t.Fatalf("modo %q: decidir sobre um livro com um evento do disjuntor malformado: %v; quer ilegivel", modo, err)
				}
			}
			// COM O DISJUNTOR DESLIGADO o nó é o do AOS-520, para quem este evento era de uma versão
			// mais recente: não cai, serve o titular, e não dá planos novos ao candidato.
			desligado := &livroDoCanary{es: es}
			if err := desligado.abrir(context.Background(), aos520Cand, aos520Marca(0, nil)); err != nil {
				t.Fatalf("desligado, um evento do disjuntor malformado impediu o arranque: %v", err)
			}
			d, err := desligado.decidir(context.Background(), aos520Cand, chaveDeRun("plano"), 0)
			if err != nil || d.papel != canary.RoleIncumbent || d.razao != razaoLivroMaisRecente {
				t.Fatalf("desligado: o plano novo tinha de ser do titular, sem erro: %+v %v", d, err)
			}
			if _, err := desligado.decidir(context.Background(), aos520Cand, chaveDeRun("plano-ja-atribuido"), 0); !errors.Is(err, ErrCanaryNewerVersion) {
				t.Fatalf("desligado: o plano ja atribuido ao candidato e recusado, nao passa para o titular: %v", err)
			}
			if desligado.disjuntorIlegiveisVistos.Load() != 1 || desligado.futurosVistos.Load() != 1 {
				t.Fatalf("desligado: o evento ilegivel tem de ficar contado: %d, %d", desligado.disjuntorIlegiveisVistos.Load(), desligado.futurosVistos.Load())
			}
		})
	}
}

// DESLIGADO, NÃO GRAVA NEM LÊ. Com o disjuntor a `off`, levar uma observação ao livro não faz nada —
// nem uma leitura; e a decisão faz as mesmas leituras e escritas que fazia.
func TestAOS521_Livro_DesligadoNaoLeNemGrava(t *testing.T) {
	es := aos520Store(t)
	conta := &aos520StoreQueConta{Store: es}
	l := aos520Livro(t, conta, aos520Cand)
	antes := conta.lidas(canaryStream)
	if err := l.observarNoDisjuntor(context.Background(), aos520Cand.registo(), observacaoDoDisjuntor{run: "r", unidade: "r", sinal: sinalNaoCumprido}); err != nil {
		t.Fatal(err)
	}
	if conta.lidas(canaryStream) != antes {
		t.Fatal("desligado, o disjuntor leu o livro")
	}
	aos520Decidir(t, l, aos520Cand, "plano", 0)
	if lidas := conta.lidas(canaryStream) - antes; lidas != 1 {
		t.Fatalf("a decisao leu o livro %d vez(es); quer 1, como antes do disjuntor", lidas)
	}
	evs, _ := es.Read(context.Background(), canaryStream, 1)
	for _, ev := range evs {
		if tipoDoDisjuntor(ev.Type) {
			t.Fatalf("desligado, o disjuntor gravou %s", ev.Type)
		}
	}
}
