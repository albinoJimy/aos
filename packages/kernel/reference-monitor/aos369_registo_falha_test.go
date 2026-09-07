package referencemonitor

import (
	"context"
	"errors"
	"testing"
)

// AOS-369 — A PERDA DE UM REGISTO DE MEDIAÇÃO DEIXA DE SER SILENCIOSA.
//
// O sítio pós-decisão [Monitor.fail] gravava a PROVA de um deny/escalate com o erro
// descartado (`seq, _ :=`). Um WORM em baixo negava 100% das tool calls e não deixava
// rasto nenhum — indistinguível de um nó parado. Agora a perda é CONTADA
// ([Metrics.RecordFailures]) e o ÚLTIMO desfecho é ESTADO ([Metrics.RecordingHealthy]),
// que o /readyz lê como dependência crítica. Este teste prova, ao nível do RM:
//
//   - uma call NEGADA cujo registo pós-decisão falha ⇒ RecordFailures sobe e o RM
//     declara-se a-registar-mal (RecordingHealthy=false), SEM alterar a decisão (deny);
//   - um registo BEM-SUCEDIDO posterior RECUPERA a saúde (last-outcome, não «alguma vez
//     falhou»), e o contador NÃO recua — o incidente aconteceu.
//
// É a âncora de mutação (ii): reverter a instrumentação de :478 para `seq, _ :=` deixa
// RecordFailures a 0 e RecordingHealthy sempre true, e as duas asserções avermelham.
func TestAOS369_RegistoDeMediacaoFalhado_ContaERecupera(t *testing.T) {
	t.Parallel()
	errDisco := errors.New("no space left on device")
	sink := &fakeSink{fail: errDisco} // o WORM recusa TODAS as escritas
	m := New(WithEventSink(sink))

	// CONTROLO ANTES: zero-value ⇒ saudável e sem falhas. Um nó que nunca mediou é pronto.
	if !m.Metrics().RecordingHealthy() {
		t.Fatal("o valor-zero devia declarar-se saudavel (recordingFailing=false)")
	}
	if got := m.Metrics().RecordFailures(); got != 0 {
		t.Fatalf("RecordFailures devia comecar a 0, veio %d", got)
	}

	// FASE 1 — DENY com o sink em baixo. A tool NÃO está registada ⇒ default-deny; o registo
	// pós-decisão em [Monitor.fail] tenta gravar a PROVA da negação e falha. A decisão MANTÉM-SE
	// deny (a falha do registo pós-decisão não a altera), mas a perda tem de ser contada.
	dec, err := m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate (deny) erro inesperado: %v", err)
	}
	if dec.Effect != EffectDeny {
		t.Fatalf("a call de tool nao-registada devia ser NEGADA (default-deny), veio %q", dec.Effect)
	}
	if got := m.Metrics().RecordFailures(); got != 1 {
		t.Fatalf("a perda do registo pos-decisao NAO foi contada (%d) — um deny sem prova e "+
			"indistinguivel de uma chamada que nunca aconteceu", got)
	}
	if m.Metrics().RecordingHealthy() {
		t.Fatal("com o ultimo registo a falhar, RecordingHealthy devia ser false (o /readyz le isto)")
	}

	// FASE 2 — o disco esvazia e uma call PERMITIDA grava com sucesso. O último desfecho passa a
	// saudável (auto-curativo), SEM que o contador cumulativo recue.
	sink.fail = nil
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dec, err = m.Mediate(context.Background(), baseCall())
	if err != nil {
		t.Fatalf("Mediate (permit) erro inesperado: %v", err)
	}
	if dec.Effect != EffectPermit {
		t.Fatalf("com o sink saudavel a call devia ser PERMITIDA, veio %q", dec.Effect)
	}
	if !m.Metrics().RecordingHealthy() {
		t.Fatal("um registo BEM-SUCEDIDO nao limpou a saude — um solucco de disco tiraria o no de " +
			"rotacao ate reinicio")
	}
	if got := m.Metrics().RecordFailures(); got != 1 {
		t.Fatalf("o contador recuou apos a recuperacao (%d) — um contador que esquece esconde o "+
			"incidente de quem o investiga depois", got)
	}
}
