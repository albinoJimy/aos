package eventstore

// migracao_test.go — O SENSOR DA CÓPIA VIVE ONDE A CÓPIA VIVE.
//
// # PORQUE É QUE ESTE FICHEIRO EXISTE
//
// O `CopiarStream` nasceu da migração da fila de aprovações, escrita à mão, que tinha um defeito
// CRÍTICO: tolerava `ErrStreamNotFound` e não `ErrConfig`, e sobre JetStream o nó deixava de
// arrancar. A lição foi movida para cá — mas o TESTE dela ficou no chamador.
//
// Uma revisão adversarial mediu a consequência: desligando o ramo do `ErrConfig`, as suites
// deste pacote e a da memória ficavam VERDES; só o pacote `integration` falhava. E esse é
// precisamente o pacote cuja constante legada está marcada para desaparecer — no dia em que
// desaparecer, leva o único sensor da lição consigo, e a terceira migração repete o defeito.
//
// Mudar o código sem mudar o teste é mudar metade.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// storeFalso imita o que interessa de um backend: um mapa de streams, e a possibilidade de
// recusar um nome LEXICALMENTE, como o `subjectDe` do JetStream faz.
type storeFalso struct {
	streams       map[string][]Event
	recusaPonto   bool   // imita o subjectDe: recusa nomes com `.`
	recusaEscrita string // um destino cujo Append devolve ErrConfig
	leuOrigem     bool
}

func novoStoreFalso() *storeFalso { return &storeFalso{streams: map[string][]Event{}} }

func (s *storeFalso) Read(_ context.Context, streamID string, _ uint64) ([]Event, error) {
	if s.recusaPonto && strings.Contains(streamID, ".") {
		s.leuOrigem = true
		return nil, fmt.Errorf("%w: stream_id %q nao representavel", ErrConfig, streamID)
	}
	evs, ok := s.streams[streamID]
	if !ok {
		return nil, ErrStreamNotFound
	}
	return evs, nil
}

func (s *storeFalso) Append(_ context.Context, streamID string, in EventInput, _ ...AppendOption) (AppendResult, error) {
	if s.recusaEscrita != "" && streamID == s.recusaEscrita {
		return AppendResult{}, fmt.Errorf("%w: stream_id %q nao representavel", ErrConfig, streamID)
	}
	for _, ev := range s.streams[streamID] {
		if ev.RunID == in.RunID && ev.StepID == in.StepID {
			return AppendResult{Status: StatusDuplicate, Event: ev}, nil
		}
	}
	ev := Event{
		Type: in.Type, Payload: in.Payload, SchemaVersion: in.SchemaVersion,
		RunID: in.RunID, StepID: in.StepID, ParentStepID: in.ParentStepID,
		Producer: in.Producer, Seq: uint64(len(s.streams[streamID]) + 1),
	}
	s.streams[streamID] = append(s.streams[streamID], ev)
	return AppendResult{Status: StatusCommitted, Event: ev, Seq: ev.Seq}, nil
}

// UM BACKEND QUE NÃO CONSEGUE NOMEAR A ORIGEM NÃO TEM NADA PARA MIGRAR.
//
// É o defeito CRÍTICO, e agora tem sensor no pacote onde a decisão vive.
func TestCopiarStreamToleraOrigemNaoNomeavel(t *testing.T) {
	st := novoStoreFalso()
	st.recusaPonto = true

	copiados, err := CopiarStream(context.Background(), st, "gov.approvals", "aos-internal/gov/approvals")
	if err != nil {
		t.Fatalf("um backend que recusa o NOME da origem nao pode fazer a copia falhar (%v):\n"+
			"o Append passa pelo MESMO subjectDe, logo um stream com ponto nunca pode ter recebido\n"+
			"uma escrita ali — nao ha nada para migrar. Abortar aqui impede o no de arrancar no\n"+
			"unico substrato que arbitra entre processos.", err)
	}
	if copiados != 0 {
		t.Fatalf("nao havia nada para copiar, copiou %d", copiados)
	}
	if !st.leuOrigem {
		t.Fatal("o teste nao exercitou a leitura da origem: nao esta a medir nada")
	}
}

// O `ErrConfig` NA ESCRITA DO DESTINO TEM DE PROPAGAR.
//
// É a outra metade, e não tinha sensor em lado nenhum: um destino que o backend recusa é um
// nome EM USO inválido — um defeito —, e não um facto sobre o mundo.
func TestCopiarStreamPropagaErrConfigNaEscrita(t *testing.T) {
	st := novoStoreFalso()
	const destino = "destino-invalido"
	st.recusaEscrita = destino
	st.streams["origem"] = []Event{{Type: "t", RunID: "r", StepID: "s", Payload: []byte(`{}`)}}

	if _, err := CopiarStream(context.Background(), st, "origem", destino); err == nil {
		t.Fatal("um ErrConfig na ESCRITA do destino tem de propagar: e um nome em uso que o " +
			"backend recusa, o que e um defeito — nao uma verdade sobre a origem")
	} else if !errors.Is(err, ErrConfig) {
		t.Fatalf("o erro devia embrulhar ErrConfig, veio %v", err)
	}
}

// A ORIGEM INEXISTENTE NÃO É ERRO. É o caso da maioria dos arranques depois da 1.ª passagem.
func TestCopiarStreamComOrigemInexistente(t *testing.T) {
	copiados, err := CopiarStream(context.Background(), novoStoreFalso(), "nao-existe", "destino")
	if err != nil {
		t.Fatalf("origem inexistente nao pode ser erro: %v", err)
	}
	if copiados != 0 {
		t.Fatalf("copiou %d de uma origem inexistente", copiados)
	}
}

// COPIAR PARA SI MESMO É RECUSADO. Um no-op silencioso esconderia a perda da migração.
func TestCopiarStreamRecusaOrigemIgualAoDestino(t *testing.T) {
	if _, err := CopiarStream(context.Background(), novoStoreFalso(), "x", "x"); err == nil {
		t.Fatal("copiar um stream para si mesmo tem de ser recusado: seria um no-op silencioso")
	}
}

// O ENVELOPE INTEIRO ATRAVESSA, e a idempotência vale à segunda passagem.
func TestCopiarStreamPreservaOEnvelopeEEIdempotente(t *testing.T) {
	st := novoStoreFalso()
	st.streams["origem"] = []Event{{
		Type: "facto.x", Payload: []byte(`{"a":1}`), SchemaVersion: "1.1",
		RunID: "run-1", StepID: "passo-1", ParentStepID: "pai",
		Producer: Producer{NHIID: "nhi:emissor", Scope: []string{"escrever"}},
	}}

	n, err := CopiarStream(context.Background(), st, "origem", "destino")
	if err != nil || n != 1 {
		t.Fatalf("1.a passagem: n=%d err=%v", n, err)
	}
	got := st.streams["destino"][0]
	switch {
	case got.Type != "facto.x":
		t.Errorf("o Type nao atravessou: %q", got.Type)
	case string(got.Payload) != `{"a":1}`:
		t.Errorf("o Payload nao atravessou: %q", got.Payload)
	case got.SchemaVersion != "1.1":
		t.Errorf("o SchemaVersion nao atravessou: %q", got.SchemaVersion)
	case got.RunID != "run-1" || got.StepID != "passo-1":
		t.Errorf("a idempotency-key nao atravessou: %q/%q", got.RunID, got.StepID)
	case got.ParentStepID != "pai":
		t.Errorf("o ParentStepID nao atravessou: %q", got.ParentStepID)
	case got.Producer.NHIID != "nhi:emissor" || len(got.Producer.Scope) != 1:
		t.Errorf("o Producer nao atravessou: %+v", got.Producer)
	}

	n2, err := CopiarStream(context.Background(), st, "origem", "destino")
	if err != nil {
		t.Fatalf("2.a passagem: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("a 2.a passagem devia copiar 0, copiou %d — correr no arranque duplicaria factos", n2)
	}
}

// UM DUPLICADO COM CONTEÚDO DIFERENTE É UMA DIVERGÊNCIA, E NÃO «JÁ MIGRADO».
//
// Sem esta verificação o facto do legado desaparecia em silêncio: sem erro, sem contagem, sem
// linha de log, com a migração a declarar-se bem-sucedida.
func TestCopiarStreamRecusaDuplicadoDivergente(t *testing.T) {
	st := novoStoreFalso()
	st.streams["origem"] = []Event{{Type: "t", RunID: "r", StepID: "s", Payload: []byte(`{"v":"legado"}`)}}
	st.streams["destino"] = []Event{{Type: "t", RunID: "r", StepID: "s", Payload: []byte(`{"v":"destino"}`)}}

	_, err := CopiarStream(context.Background(), st, "origem", "destino")
	if err == nil {
		t.Fatal("dois factos DIFERENTES sob a mesma idempotency-key tem de ser um erro:\n" +
			"tratar o duplicado como «ja migrado» descarta o facto do legado em silencio")
	}
	if !strings.Contains(err.Error(), "DIVERGENCIA") {
		t.Errorf("o erro devia nomear a divergencia, veio: %v", err)
	}
}

// CONTROLO DE NÃO-VACUIDADE do teste acima: um duplicado IGUAL não dispara. Sem isto, a
// verificação de divergência partiria a idempotência, que é o que torna a cópia segura no
// arranque.
func TestCopiarStreamAceitaDuplicadoIdentico(t *testing.T) {
	st := novoStoreFalso()
	mesmo := []byte(`{"v":"igual"}`)
	st.streams["origem"] = []Event{{Type: "t", RunID: "r", StepID: "s", Payload: mesmo}}
	st.streams["destino"] = []Event{{Type: "t", RunID: "r", StepID: "s", Payload: mesmo}}

	n, err := CopiarStream(context.Background(), st, "origem", "destino")
	if err != nil {
		t.Fatalf("um duplicado IDENTICO nao pode ser erro (%v): a idempotencia e o que torna a "+
			"copia segura de correr no arranque", err)
	}
	if n != 0 {
		t.Fatalf("um duplicado identico nao se conta como copiado, contou %d", n)
	}
}
