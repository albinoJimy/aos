package referencemonitor

import (
	"context"
	"sync"
	"testing"
)

// aos311_registo_pos_decisao_test.go — ACHADO DE REVISÃO ADVERSARIAL SOBRE AOS-311 no
// Reference Monitor: separar «fail-closed do efeito» de «durabilidade da prova».
//
// O RM tem os DOIS caminhos no mesmo ficheiro e eles não podem ser tratados da mesma
// maneira:
//
//   - PERMIT (audit-BEFORE-effect, monitor.go:348-354): o registo decide se a tool corre.
//     Um sink que falhe — por prazo esgotado ou por qualquer outra razão — TEM de degradar
//     para deny. Herda o ctx do chamador, e é assim que fica.
//   - DENY/ESCALATE ([Monitor.fail]): a decisão JÁ está tomada e o efeito JÁ está
//     bloqueado. O que se escreve é a PROVA de um facto consumado. Desde que o AOS-311 pôs
//     o `audit.Store.Append` a respeitar o ctx, um `Mediate` com o ctx do run já morto
//     bloqueava a acção e não deixava rasto — o `_` do erro tornava a perda silenciosa, e
//     um deny sem registo é indistinguível de uma chamada que nunca aconteceu.
//
// O `fakeSink` da suite ignora o ctx de propósito (mede o fail-closed por ERRO). Aqui
// usa-se um sink que o HONRA, para medir o fail-closed por PRAZO — que é a propriedade
// que o AOS-311 introduziu e que estes testes têm de manter separada.

// sinkQueHonraOContexto modela o sink real do nó (o `audit.RMAdapter` sobre um
// `audit.Store` pós-AOS-311): recusa escrever sob contexto morto.
type sinkQueHonraOContexto struct {
	mu      sync.Mutex
	records []MediationRecord
	seq     uint64
}

func (s *sinkQueHonraOContexto) RecordMediation(ctx context.Context, rec MediationRecord) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.records = append(s.records, rec)
	return s.seq, nil
}

func (s *sinkQueHonraOContexto) all() []MediationRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MediationRecord, len(s.records))
	copy(out, s.records)
	return out
}

// TestDenyContinuaRegistadoComOContextoDoRunMorto — a metade da DURABILIDADE DA PROVA.
//
// O ctx morre DURANTE a cadeia de mediação (um hook cancela-o e nega a seguir). É o
// cenário real: um deadline que expira a meio da avaliação, ou um run abortado enquanto o
// PDP responde. A negação acontece, o efeito fica bloqueado, e o registo TEM de sobreviver.
//
// Não se usa um ctx morto À ENTRADA porque esse caminho tem um retorno próprio, anterior a
// toda a cadeia ([Monitor.evaluate], monitor.go:269), que é deliberadamente não-auditado —
// ver a nota no final deste ficheiro.
func TestDenyContinuaRegistadoComOContextoDoRunMorto(t *testing.T) {
	t.Parallel()
	sink := &sinkQueHonraOContexto{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// O hook mata o contexto e nega: quando [Monitor.fail] escrever, o ctx já morreu.
	hook := &spyHook{
		name:   "policy",
		mutate: func(*Call) { cancel() },
		result: HookResult{Decision: HookDeny, Reason: "negado com o prazo a expirar"},
	}
	m := New(WithHooks(hook), WithEventSink(sink))
	var executou bool
	if err := m.Register("tool.echo", toolSpy(&executou, []byte("efeito"))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectDeny {
		t.Fatalf("Effect=%q, quero deny", d.Effect)
	}
	if executou {
		t.Fatal("FAIL-CLOSED violado: a tool correu apesar da negacao")
	}
	if ctx.Err() == nil {
		t.Fatal("o hook nao chegou a matar o contexto — o teste nao mede o que promete")
	}

	recs := sink.all()
	if len(recs) != 1 {
		t.Fatalf("registos = %d, quero 1 — uma negacao sem registo e indistinguivel de uma chamada que nunca aconteceu", len(recs))
	}
	if recs[0].Effect != EffectDeny {
		t.Fatalf("Effect registado = %q, quero deny", recs[0].Effect)
	}
	if recs[0].DeniedBy != "policy" {
		t.Fatalf("o registo nao diz quem negou: DeniedBy=%q", recs[0].DeniedBy)
	}
	if d.MediationSeq == 0 {
		t.Fatal("MediationSeq = 0 — a decisao nao ficou ancorada no trilho")
	}
}

// TestPermitContinuaFailClosedPorPrazo — O CONTROLO, e a metade que NÃO pode mudar. Se
// este teste falhasse, a correcção acima teria sido feita desligando o audit-before-effect
// do permit, e o RM passaria a despachar tools sobre prazos esgotados.
//
// A tool está registada, todos os hooks permitem, e o único obstáculo é o prazo — que
// morre a meio da cadeia, exactamente como no teste anterior. O registo pré-efeito falha
// por `ctx.Err()`, e a decisão TEM de degradar para deny sem despachar nada.
func TestPermitContinuaFailClosedPorPrazo(t *testing.T) {
	t.Parallel()
	sink := &sinkQueHonraOContexto{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// O hook PERMITE e mata o contexto: só o audit-before-effect se pode opor.
	hook := &spyHook{name: "policy", mutate: func(*Call) { cancel() }, result: HookResult{Decision: HookAllow}}
	var executou bool
	m := New(WithHooks(hook), WithEventSink(sink))
	if err := m.Register("tool.echo", toolSpy(&executou, []byte("efeito"))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := m.Mediate(ctx, baseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectDeny {
		t.Fatalf("Effect=%q, quero deny — um efeito nao-auditavel nao e permitido (ADR-002/010)", d.Effect)
	}
	if d.DeniedBy != "audit-sink" {
		t.Fatalf("DeniedBy=%q, quero audit-sink — o deny tem de vir do audit-before-effect", d.DeniedBy)
	}
	if executou {
		t.Fatal("FAIL-CLOSED violado: a tool correu com o prazo esgotado")
	}
	// E o deny resultante ficou registado — as duas metades valem ao mesmo tempo. O
	// registo pré-efeito (permit) falhou pelo prazo; o pós-decisão (deny) passou.
	recs := sink.all()
	if len(recs) != 1 || recs[0].Effect != EffectDeny {
		t.Fatalf("registos = %+v, quero exactamente um deny", recs)
	}
}

// NOTA SOBRE O QUE ESTE CHANGESET NÃO FECHA: um ctx morto À ENTRADA de [Monitor.Mediate]
// resolve em [Monitor.evaluate] (monitor.go:269) num deny DELIBERADAMENTE não-auditado,
// anterior a toda a cadeia. O comentário lá justifica-o com «gravar exigiria o mesmo
// contexto e falharia de qualquer forma» — justificação que este changeset torna obsoleta.
// Não se mexeu nele por ser uma escolha de política e não um defeito silencioso: registar
// ali faria uma run abortada produzir uma entrada WORM por cada tool call restante. Fica
// dito em vez de fingido.
