package adapters

// migracao_test.go — O QUE FOI APAGADO NÃO PODE VOLTAR.
//
// A migração das aprovações tinha o marcador `used-` como o facto que não podia ficar para
// trás. Aqui é o TOMBSTONE: um registo apagado volta a estar lá, sem erro nenhum, porque o
// `rebuild` reconstrói o estado por replay do log.
//
// CALIBRAÇÃO: hoje **não há tombstones em produção** — o `Delete` da MemoryPort não tem
// chamadores fora de testes e o `/dsar/erase` não alcança estes streams (ver adapters/migracao.go).
// O invariante é o que torna o apagamento possível quando alguém o compuser, e é por isso que
// se preserva agora: depois de haver tombstones, migrar já não é seguro sem ele.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/substrate/eventstore"
)

func migStore(t *testing.T) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// escreverNoLegado apende ao stream ANTIGO de uma classe, como o binário anterior o faria.
func escreverNoLegado(t *testing.T, st *eventstore.Store, class domain.MemoryClass, tipo, stepID, runID string, payload []byte) {
	t.Helper()
	if _, err := st.Append(context.Background(), streamLegadoDe(class), eventstore.EventInput{
		Type:     tipo,
		Payload:  payload,
		RunID:    runID,
		StepID:   stepID,
		Producer: eventstore.Producer{NHIID: "nhi:binario-antigo"},
	}); err != nil {
		t.Fatalf("Append no legado (%s/%s): %v", class, stepID, err)
	}
}

// UM REGISTO APAGADO ANTES DA MIGRAÇÃO CONTINUA APAGADO DEPOIS.
//
// É a asserção central, e mede-se pelo caminho REAL: o `rebuild` do adaptador em vigor, que é
// o que a MemoryPort usa para responder a qualquer leitura.
func TestAOS424TombstoneAtravessaAMigracao(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()
	const class = domain.ClassSemantic

	// (1) O mundo ANTES, no nome antigo: um registo escrito e DEPOIS apagado.
	rec := domain.Record{
		ID:    "memoria-apagada",
		Class: class,
		Body:  domain.SemanticBody{Subject: "titular", Predicate: "mandou-apagar", Object: "este facto", Confidence: 1},
	}
	rec.Metadata.RunID = "run-1"
	rec.Metadata.AgentID = "agt-1"
	rec.Metadata.Provenance = domain.ProvenanceTrusted
	corpo, err := domain.MarshalRecord(rec)
	if err != nil {
		t.Fatalf("MarshalRecord: %v", err)
	}
	escreverNoLegado(t, st, class, EventTypeWritten, string(class)+":put:"+rec.ID, "run-1", corpo)

	tomb, err := marshalTombstone(rec.ID, "agt-1", "run-1", domain.ProvenanceTrusted)
	if err != nil {
		t.Fatalf("marshalTombstone: %v", err)
	}
	escreverNoLegado(t, st, class, EventTypeDeleted, string(class)+":del:"+rec.ID+":1", "run-1", tomb)

	// (2) A migração.
	copiados, err := MigrarStreamsDeMemoria(ctx, st)
	if err != nil {
		t.Fatalf("MigrarStreamsDeMemoria: %v", err)
	}
	if copiados != 2 {
		t.Fatalf("deviam ter sido copiados 2 factos, foram %d", copiados)
	}

	// (3) O mundo DEPOIS, pelo caminho real: o adaptador em vigor não pode ver o registo.
	ad := NewEventStoreAdapter(st)
	_, gerr := ad.Get(ctx, class, rec.ID)
	if gerr == nil {
		t.Fatal("A MEMORIA APAGADA RESSUSCITOU: o tombstone nao atravessou a migracao.\n" +
			"Um registo apagado voltou a estar legivel, sem erro nenhum, porque o rebuild\n" +
			"reconstroi o estado por replay do log.")
	}
	if !errors.Is(gerr, domain.ErrNotFound) {
		t.Fatalf("Get devia dar ErrNotFound depois do tombstone, veio %v", gerr)
	}
}

// CONTROLO DE NÃO-VACUIDADE: um registo NÃO apagado continua legível, e com o conteúdo certo.
//
// Sem isto, uma migração que copiasse um tombstone para tudo — ou que não copiasse nada —
// passaria no teste acima e apagaria a memória inteira do nó.
func TestAOS424RegistoNaoApagadoAtravessaComConteudo(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()
	const class = domain.ClassSemantic

	rec := domain.Record{
		ID:    "memoria-viva",
		Class: class,
		Body:  domain.SemanticBody{Subject: "memoria", Predicate: "tem-de", Object: "sobreviver", Confidence: 1},
	}
	rec.Metadata.RunID = "run-2"
	rec.Metadata.AgentID = "agt-1"
	rec.Metadata.Provenance = domain.ProvenanceTrusted
	corpo, err := domain.MarshalRecord(rec)
	if err != nil {
		t.Fatalf("MarshalRecord: %v", err)
	}
	escreverNoLegado(t, st, class, EventTypeWritten, string(class)+":put:"+rec.ID, "run-2", corpo)

	if _, err := MigrarStreamsDeMemoria(ctx, st); err != nil {
		t.Fatalf("MigrarStreamsDeMemoria: %v", err)
	}

	ad := NewEventStoreAdapter(st)
	got, err := ad.Get(ctx, class, rec.ID)
	if err != nil {
		t.Fatalf("o registo NAO apagado desapareceu na migracao (%v): a memoria do no foi perdida", err)
	}
	corpoGot, okTipo := got.Body.(domain.SemanticBody)
	if !okTipo {
		t.Fatalf("o corpo mudou de tipo na migracao: %T", got.Body)
	}
	if corpoGot.Object != "sobreviver" {
		t.Errorf("o CONTEUDO nao atravessou: %+v", corpoGot)
	}
	if got.Metadata.RunID != "run-2" || got.Metadata.Provenance != domain.ProvenanceTrusted {
		t.Errorf("a proveniencia nao atravessou: run=%q prov=%q",
			got.Metadata.RunID, got.Metadata.Provenance)
	}
}

// AS QUATRO CLASSES MIGRAM, e não só aquela em que alguém pensou.
func TestAOS424MigracaoCobreAsQuatroClasses(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()

	classes := domain.AllClasses()
	if len(classes) != 4 {
		t.Fatalf("esperava 4 classes canonicas, ha %d — actualize este teste", len(classes))
	}
	for _, class := range classes {
		escreverNoLegado(t, st, class, EventTypeWritten,
			string(class)+":put:x", "run-3", []byte(`{"nao-importa":true}`))
	}

	copiados, err := MigrarStreamsDeMemoria(ctx, st)
	if err != nil {
		t.Fatalf("MigrarStreamsDeMemoria: %v", err)
	}
	if copiados != len(classes) {
		t.Fatalf("deviam ter sido copiados %d factos (um por classe), foram %d",
			len(classes), copiados)
	}
	for _, class := range classes {
		evs, rerr := st.Read(ctx, streamFor(class), 0)
		if rerr != nil || len(evs) != 1 {
			t.Errorf("a classe %q nao migrou: err=%v n=%d", class, rerr, len(evs))
		}
	}
}

// A MIGRAÇÃO É IDEMPOTENTE. É o que a torna segura no arranque.
func TestAOS424MigracaoDeMemoriaEIdempotente(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()
	const class = domain.ClassWorking

	escreverNoLegado(t, st, class, EventTypeWritten, string(class)+":put:a", "run-4", []byte(`{}`))

	primeira, err := MigrarStreamsDeMemoria(ctx, st)
	if err != nil || primeira != 1 {
		t.Fatalf("1.a passagem: n=%d err=%v", primeira, err)
	}
	segunda, err := MigrarStreamsDeMemoria(ctx, st)
	if err != nil {
		t.Fatalf("2.a passagem: %v", err)
	}
	if segunda != 0 {
		t.Fatalf("a 2.a passagem devia copiar 0, copiou %d — correr no arranque duplicaria "+
			"registos de memoria", segunda)
	}
}

// A ORDEM PRESERVA-SE, e aqui ela DECIDE: o `rebuild` é last-write-wins e o «deleted» só
// apaga o que vem antes dele. Uma cópia que invertesse a ordem faria um registo apagado
// reaparecer ou um registo vivo desaparecer.
func TestAOS424MigracaoDeMemoriaPreservaAOrdem(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()
	const class = domain.ClassEpisodic

	// escrito → apagado → RE-escrito: o estado final é VIVO.
	rec := domain.Record{ID: "ressuscitado", Class: class,
		Body: domain.EpisodicBody{Goal: "segunda vida", Outcome: "success", Summary: "resumo"}}
	rec.Metadata.RunID = "run-5"
	rec.Metadata.AgentID = "agt-1"
	rec.Metadata.Provenance = domain.ProvenanceTrusted
	corpo, err := domain.MarshalRecord(rec)
	if err != nil {
		t.Fatalf("MarshalRecord: %v", err)
	}
	tomb, err := marshalTombstone(rec.ID, "agt-1", "run-5", domain.ProvenanceTrusted)
	if err != nil {
		t.Fatalf("marshalTombstone: %v", err)
	}
	escreverNoLegado(t, st, class, EventTypeWritten, string(class)+":put:"+rec.ID, "run-5", corpo)
	escreverNoLegado(t, st, class, EventTypeDeleted, string(class)+":del:"+rec.ID+":1", "run-5", tomb)
	escreverNoLegado(t, st, class, EventTypeWritten, string(class)+":put2:"+rec.ID, "run-5", corpo)

	if _, err := MigrarStreamsDeMemoria(ctx, st); err != nil {
		t.Fatalf("MigrarStreamsDeMemoria: %v", err)
	}

	ad := NewEventStoreAdapter(st)
	if _, err := ad.Get(ctx, class, rec.ID); err != nil {
		t.Fatalf("o registo RE-escrito depois do tombstone nao esta vivo (%v): a ordem nao "+
			"atravessou a migracao, e o rebuild e last-write-wins", err)
	}
}

// UM NÓ FRESCO migra sem erro e sem copiar nada.
func TestAOS424MigracaoDeMemoriaSemLegadoNaoEErro(t *testing.T) {
	copiados, err := MigrarStreamsDeMemoria(context.Background(), migStore(t))
	if err != nil {
		t.Fatalf("um no sem streams legados nao pode falhar a migracao: %v", err)
	}
	if copiados != 0 {
		t.Fatalf("nao havia nada para copiar, copiou %d", copiados)
	}
}

// O PREFIXO NOVO NÃO PODE VOLTAR A TER UM PONTO, NEM PERDER A BARRA.
func TestAOS424PrefixoNovoDosStreamsDeMemoria(t *testing.T) {
	for _, proibido := range []string{".", "*", ">", " "} {
		for _, class := range domain.AllClasses() {
			nome := streamFor(class)
			if containsStr(nome, proibido) {
				t.Errorf("o stream da classe %q (%q) contem %q: um subject NATS nao o representa "+
					"e o Append recusa com E_CONFIG", class, nome, proibido)
			}
		}
	}
	if !containsStr(streamPrefix, "/") {
		t.Errorf("o prefixo (%q) perdeu a barra: sem ela os streams de memoria voltam a ser um "+
			"segmento de caminho e `GET /runs/<stream>/trajectory` alcanca-os", streamPrefix)
	}
	if streamPrefixLegado != "memory." {
		t.Errorf("o prefixo LEGADO mudou (%q): a migracao deixa de encontrar os factos que existem",
			streamPrefixLegado)
	}
}

// O ENVELOPE ATRAVESSA INTEIRO — e o `RunID` não é decorativo.
//
// Uma revisão adversarial mutou a cópia para `RunID: ""` e para `Producer{}`, e os sete testes
// desta suite ficaram VERDES (só o pacote `integration` apanhava). O `RunID` do envelope é o
// que a trava do AOS-426 lê para decidir se um stream é de um run: perdê-lo aqui mudaria, em
// silêncio, a classificação de um stream inteiro.
func TestAOS424MigracaoDeMemoriaPreservaOEnvelope(t *testing.T) {
	st := migStore(t)
	ctx := context.Background()
	const class = domain.ClassProcedural

	if _, err := st.Append(ctx, streamLegadoDe(class), eventstore.EventInput{
		Type:          EventTypeWritten,
		Payload:       []byte(`{"corpo":"x"}`),
		SchemaVersion: "1.1",
		RunID:         "run-envelope",
		StepID:        string(class) + ":put:env",
		ParentStepID:  "pai-do-passo",
		Producer:      eventstore.Producer{NHIID: "nhi:emissor", Scope: []string{"escrever"}},
	}); err != nil {
		t.Fatalf("Append no legado: %v", err)
	}
	if _, err := MigrarStreamsDeMemoria(ctx, st); err != nil {
		t.Fatalf("MigrarStreamsDeMemoria: %v", err)
	}

	evs, err := st.Read(ctx, streamFor(class), 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	ev := evs[0]
	if ev.RunID != "run-envelope" {
		t.Errorf("o RunID nao atravessou (%q): e o que a trava do AOS-426 le para decidir se um "+
			"stream e de um run", ev.RunID)
	}
	if ev.SchemaVersion != "1.1" {
		t.Errorf("o SchemaVersion nao atravessou: %q", ev.SchemaVersion)
	}
	if ev.ParentStepID != "pai-do-passo" {
		t.Errorf("o ParentStepID nao atravessou: %q", ev.ParentStepID)
	}
	if ev.Producer.NHIID != "nhi:emissor" || len(ev.Producer.Scope) != 1 {
		t.Errorf("o Producer nao atravessou: %+v", ev.Producer)
	}
	if string(ev.Payload) != `{"corpo":"x"}` {
		t.Errorf("o Payload nao atravessou: %q", ev.Payload)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
