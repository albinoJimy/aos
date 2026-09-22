package integration

// approval_stream_migracao_test.go — O USO-ÚNICO TEM DE ATRAVESSAR A MIGRAÇÃO.
//
// É o único teste desta série que interessa de verdade. Um grant consumido ANTES da migração
// não pode voltar a ser consumível DEPOIS — se puder, a cerimónia de quatro olhos deixa de dar
// a propriedade que existe para dar, e deixa de a dar exactamente no momento em que ninguém
// está a olhar (um arranque).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// migTestStore devolve um Event Store de referência para estes testes.
func migTestStore(t *testing.T) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// escreverNoLegado apende um facto ao stream ANTIGO, como o binário anterior o faria.
func escreverNoLegado(t *testing.T, st *eventstore.Store, tipo, stepID string, payload []byte) {
	t.Helper()
	if _, err := st.Append(context.Background(), approvalStreamLegado, eventstore.EventInput{
		Type:     tipo,
		Payload:  payload,
		RunID:    approvalRunID,
		StepID:   stepID,
		Producer: eventstore.Producer{NHIID: "nhi:binario-antigo"},
	}); err != nil {
		t.Fatalf("Append no stream legado (%s/%s): %v", tipo, stepID, err)
	}
}

// UM GRANT CONSUMIDO ANTES DA MIGRAÇÃO CONTINUA CONSUMIDO DEPOIS.
//
// É a asserção central. O `used-<id>` copiado com a MESMA `(RunID, StepID)` faz o dedup do
// Event Store recusar, no stream novo, a reclamação de um grant que já tinha sido reclamado no
// antigo.
func TestAOS424UsoUnicoAtravessaAMigracao(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	// (1) O mundo ANTES: um grant emitido e JÁ CONSUMIDO, no nome antigo.
	const grantID = "grant-ja-consumido"
	escreverNoLegado(t, st, approvalGrantedEventType, "grant-"+grantID, []byte(`{"id":"`+grantID+`"}`))
	escreverNoLegado(t, st, approvalConsumedEventType, "used-"+grantID, []byte(`{"id":"`+grantID+`"}`))

	// (2) A migração.
	copiados, err := MigrarAprovacoes(ctx, st)
	if err != nil {
		t.Fatalf("MigrarAprovacoes: %v", err)
	}
	if copiados != 2 {
		t.Fatalf("deviam ter sido copiados 2 factos, foram %d", copiados)
	}

	// (3) O mundo DEPOIS: tentar reclamar o grant outra vez tem de vir DUPLICADO.
	res, err := st.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalConsumedEventType,
		Payload: []byte(`{"id":"` + grantID + `"}`),
		RunID:   approvalRunID,
		StepID:  "used-" + grantID,
	})
	if err != nil {
		t.Fatalf("Append da segunda reclamacao: %v", err)
	}
	if res.Status != eventstore.StatusDuplicate {
		t.Fatalf("o grant %q voltou a ser reclamavel depois da migracao (status=%q):\n"+
			"o uso-unico NAO atravessou, e a cerimonia four-eyes deixa de garantir que uma "+
			"aprovacao destrava no maximo UMA execucao.", grantID, res.Status)
	}
}

// CONTROLO DE NÃO-VACUIDADE: um grant NÃO consumido continua consumível depois da migração.
//
// Sem isto, uma migração que copiasse um `used-` para TODOS os grants passaria no teste acima
// e partiria o produto: nenhuma aprovação destravaria nada.
func TestAOS424GrantPorConsumirContinuaConsumivel(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	const grantID = "grant-por-consumir"
	escreverNoLegado(t, st, approvalGrantedEventType, "grant-"+grantID, []byte(`{"id":"`+grantID+`"}`))

	if _, err := MigrarAprovacoes(ctx, st); err != nil {
		t.Fatalf("MigrarAprovacoes: %v", err)
	}

	res, err := st.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalConsumedEventType,
		Payload: []byte(`{"id":"` + grantID + `"}`),
		RunID:   approvalRunID,
		StepID:  "used-" + grantID,
	})
	if err != nil {
		t.Fatalf("Append da reclamacao: %v", err)
	}
	if res.Status == eventstore.StatusDuplicate {
		t.Fatalf("o grant %q deixou de ser reclamavel depois da migracao: nenhuma aprovacao "+
			"destravaria nada", grantID)
	}
}

// A MIGRAÇÃO É IDEMPOTENTE. É o que a torna segura no arranque e retomável se falhar a meio.
func TestAOS424MigracaoEIdempotente(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	for i, step := range []string{"grant-a", "used-a", "pending-b", "resume-run-1"} {
		escreverNoLegado(t, st, approvalGrantedEventType, step, []byte(`{"i":`+string(rune('0'+i))+`}`))
	}

	primeira, err := MigrarAprovacoes(ctx, st)
	if err != nil {
		t.Fatalf("1.a passagem: %v", err)
	}
	if primeira != 4 {
		t.Fatalf("a 1.a passagem devia copiar 4, copiou %d", primeira)
	}

	segunda, err := MigrarAprovacoes(ctx, st)
	if err != nil {
		t.Fatalf("2.a passagem: %v", err)
	}
	if segunda != 0 {
		t.Fatalf("a 2.a passagem devia copiar 0 (tudo duplicado), copiou %d — a migracao nao e "+
			"idempotente e corre-la no arranque duplicaria factos de governacao", segunda)
	}

	// E o stream novo tem EXACTAMENTE os quatro, não oito.
	evs, err := st.Read(ctx, approvalStream, 0)
	if err != nil {
		t.Fatalf("Read do stream novo: %v", err)
	}
	if len(evs) != 4 {
		t.Fatalf("o stream novo devia ter 4 factos, tem %d", len(evs))
	}
}

// A ORDEM RELATIVA PRESERVA-SE. O `lookup` varre do fim para o início e o `geracaoDe` conta
// ocorrências: os dois dependem da ordem, e nenhum do valor do `seq`.
func TestAOS424MigracaoPreservaAOrdem(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	ordem := []string{"grant-1", "pending-1", "used-1", "decided-1"}
	for _, step := range ordem {
		escreverNoLegado(t, st, approvalGrantedEventType, step, []byte(`{}`))
	}
	if _, err := MigrarAprovacoes(ctx, st); err != nil {
		t.Fatalf("MigrarAprovacoes: %v", err)
	}

	evs, err := st.Read(ctx, approvalStream, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != len(ordem) {
		t.Fatalf("esperava %d factos, vieram %d", len(ordem), len(evs))
	}
	for i, ev := range evs {
		if ev.StepID != ordem[i] {
			t.Fatalf("a ordem mudou na posicao %d: esperava %q, veio %q", i, ordem[i], ev.StepID)
		}
	}
}

// UM NÓ FRESCO — sem stream legado — migra sem erro e sem copiar nada. É o caso da esmagadora
// maioria dos arranques depois da primeira passagem, e falhar aqui seria impedir o nó de
// arrancar por não haver nada para migrar.
func TestAOS424MigracaoSemLegadoNaoEErro(t *testing.T) {
	st := migTestStore(t)
	copiados, err := MigrarAprovacoes(context.Background(), st)
	if err != nil {
		t.Fatalf("um no sem stream legado nao pode falhar a migracao: %v", err)
	}
	if copiados != 0 {
		t.Fatalf("nao havia nada para copiar, mas copiou %d", copiados)
	}
}

// O FACTO COPIADO CONTINUA A DIZER QUEM O EMITIU. Uma cópia que reescrevesse o produtor seria
// uma falsificação com melhor intenção — e este material é de governação.
func TestAOS424MigracaoPreservaOProdutor(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	escreverNoLegado(t, st, approvalGrantedEventType, "grant-proveniencia", []byte(`{}`))
	if _, err := MigrarAprovacoes(ctx, st); err != nil {
		t.Fatalf("MigrarAprovacoes: %v", err)
	}

	evs, err := st.Read(ctx, approvalStream, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Producer.NHIID != "nhi:binario-antigo" {
		t.Errorf("o produtor do facto copiado devia ser preservado, veio %q", evs[0].Producer.NHIID)
	}
	if evs[0].Type != approvalGrantedEventType {
		t.Errorf("o tipo do facto copiado devia ser preservado, veio %q", evs[0].Type)
	}
}

// O NOME NOVO NÃO PODE VOLTAR A TER UM PONTO, nem perder a barra.
//
// Duas propriedades distintas, e cada uma fecha um defeito medido: o ponto tornava o stream
// inutilizável sobre JetStream (AOS-424); a ausência de barra tornava-o legível por
// `GET /runs/{id}/trajectory` (AOS-426).
func TestAOS424NomeNovoDoStreamDeAprovacoes(t *testing.T) {
	for _, c := range []struct {
		nome     string
		proibido rune
	}{
		{"ponto", '.'}, {"asterisco", '*'}, {"maior", '>'}, {"espaco", ' '},
	} {
		for _, r := range approvalStream {
			if r == c.proibido {
				t.Errorf("o stream de aprovacoes (%q) contem %q (%s): um subject NATS nao o "+
					"representa e o Append recusa com E_CONFIG", approvalStream, c.proibido, c.nome)
				break
			}
		}
	}
	temBarra := false
	for _, r := range approvalStream {
		if r == '/' {
			temBarra = true
			break
		}
	}
	if !temBarra {
		t.Errorf("o stream de aprovacoes (%q) perdeu a barra: sem ela volta a ser um segmento "+
			"de caminho e `GET /runs/<stream>/trajectory` alcanca os grants", approvalStream)
	}
	// E o legado continua a ser o nome ANTIGO — se alguem o «corrigir», a migracao passa a
	// copiar o stream errado e os factos reais ficam orfaos, em silencio.
	if approvalStreamLegado != "gov.approvals" {
		t.Errorf("o nome LEGADO mudou (%q): a migracao deixa de encontrar os factos que existem",
			approvalStreamLegado)
	}
}

// O GRANT MIGRADO CONTINUA UTILIZÁVEL — pelo caminho REAL, e não só pela chave.
//
// # PORQUE É QUE ESTE TESTE FALTAVA, E PORQUE É O PIOR DOS BURACOS
//
// Os outros testes verificam a chave de idempotência, a ordem, o tipo e o produtor. **Nenhum
// olhava para o `Payload`.** Uma revisão adversarial mutou a cópia para `Payload: nil` e a
// suite INTEIRA do pacote passou.
//
// É o sobrevivente mais perigoso possível, porque a propriedade de SEGURANÇA continua a valer
// — os marcadores `used-` continuam a bloquear — e os DADOS desaparecem todos: grants
// irresolvíveis, pendentes ilegíveis, registos de retoma sem o `Goal`. E pior: o `Consume`
// reclama ANTES de ler (ordem deliberada), pelo que cada grant seria QUEIMADO e só depois se
// descobriria que é ilegível — a cerimónia de quatro olhos teria de ser repetida, grant a
// grant.
//
// O teste usa o caminho de produção nas duas pontas: escreve com `Put` e lê com `Consume`.
func TestAOS424GrantMigradoContinuaUtilizavelPeloCaminhoReal(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	// (1) O mundo ANTES: um grant escrito no LEGADO pelo mesmo código que o escreveria em
	// produção. Escreve-se via uma store apontada ao nome antigo — aqui, à mão, com o MESMO
	// payload que o `Put` produz, porque a store em vigor já escreve no nome novo.
	const grantID = "grant-utilizavel"
	original := ApprovalGrant{
		ID:          grantID,
		Preview:     []byte("preview-canonica"),
		Approvers:   []string{"humano:ana", "humano:bruno"},
		DualControl: true,
		ExpiresAt:   time.Unix(1_800_000_000, 0).UTC(),
	}
	// O MESMO wire que o `Put` produz — e não a struct crua. Escrever aqui um formato
	// inventado faria o teste medir a minha serialização em vez da do produto.
	corpo, err := json.Marshal(grantPayload{
		ID:          original.ID,
		Preview:     original.Preview,
		Approvers:   original.Approvers,
		DualControl: original.DualControl,
		ExpiresAt:   original.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal do grant: %v", err)
	}
	escreverNoLegado(t, st, approvalGrantedEventType, "grant-"+grantID, corpo)

	// (2) A migração.
	if _, merr := MigrarAprovacoes(ctx, st); merr != nil {
		t.Fatalf("MigrarAprovacoes: %v", merr)
	}

	// (3) O mundo DEPOIS: a store EM VIGOR consome o grant migrado e tem de o devolver
	// INTEIRO. É aqui que um `Payload` perdido aparece.
	store, err := NewEventStoreApprovalStore(st)
	if err != nil {
		t.Fatalf("NewEventStoreApprovalStore: %v", err)
	}
	g, ok, err := store.Consume(ctx, grantID)
	if err != nil {
		t.Fatalf("Consume depois da migracao devolveu erro (%v):\n"+
			"o grant foi QUEIMADO pela reclamacao e o conteudo nao se le \u2014 a cerimonia "+
			"four-eyes tem de ser repetida para este grant.", err)
	}
	if !ok {
		t.Fatal("o grant migrado nao foi encontrado pela store em vigor")
	}
	if g.ID != original.ID {
		t.Errorf("o ID nao atravessou: %q != %q", g.ID, original.ID)
	}
	if string(g.Preview) != string(original.Preview) {
		t.Errorf("a PREVIEW nao atravessou (%q != %q): e a amarra do grant \u00e0 accao aprovada, "+
			"e sem ela o grant deixa de servir a call que foi aprovada",
			g.Preview, original.Preview)
	}
	if len(g.Approvers) != len(original.Approvers) {
		t.Errorf("os aprovadores nao atravessaram: %v != %v", g.Approvers, original.Approvers)
	}
	if !g.DualControl {
		t.Error("o DualControl nao atravessou: uma accao irreversivel passaria a parecer ter " +
			"tido uma so aprovacao")
	}
	if !g.ExpiresAt.Equal(original.ExpiresAt) {
		t.Errorf("a validade nao atravessou: %v != %v", g.ExpiresAt, original.ExpiresAt)
	}
}

// A FIDELIDADE DA CÓPIA, campo a campo. Os mutantes `SchemaVersion` e `ParentStepID`
// sobreviviam — o comentário da migração promete preservá-los, e uma promessa sem sensor é
// uma promessa até alguem a partir.
func TestAOS424MigracaoPreservaOEnvelopeInteiro(t *testing.T) {
	st := migTestStore(t)
	ctx := context.Background()

	if _, err := st.Append(ctx, approvalStreamLegado, eventstore.EventInput{
		Type:    approvalGrantedEventType,
		Payload: []byte(`{"campo":"valor"}`),
		// UMA VERSÃO QUE NÃO É O DEFAULT. Com "1.0" este teste passava pela razão errada: o
		// store preenche a versão CORRENTE quando o campo vem vazio, pelo que apagar a cópia
		// era indistinguível de a preservar. Medido por mutação — o mutante sobreviveu.
		SchemaVersion: "1.1",
		RunID:         approvalRunID,
		StepID:        "grant-envelope",
		ParentStepID:  "pai-do-passo",
		Producer:      eventstore.Producer{NHIID: "nhi:emissor", Scope: []string{"approve"}},
	}); err != nil {
		t.Fatalf("Append no legado: %v", err)
	}
	if _, err := MigrarAprovacoes(ctx, st); err != nil {
		t.Fatalf("MigrarAprovacoes: %v", err)
	}

	evs, err := st.Read(ctx, approvalStream, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	ev := evs[0]
	if string(ev.Payload) != `{"campo":"valor"}` {
		t.Errorf("o PAYLOAD nao atravessou: %q", ev.Payload)
	}
	if ev.SchemaVersion != "1.1" {
		t.Errorf("o SchemaVersion nao atravessou: %q \u2014 o facto copiado deixa de dizer sob que "+
			"schema foi escrito", ev.SchemaVersion)
	}
	if ev.ParentStepID != "pai-do-passo" {
		t.Errorf("o ParentStepID nao atravessou: %q \u2014 a cadeia de causalidade parte-se",
			ev.ParentStepID)
	}
	if len(ev.Producer.Scope) != 1 || ev.Producer.Scope[0] != "approve" {
		t.Errorf("o Scope do produtor nao atravessou: %v", ev.Producer.Scope)
	}
}

// UM BACKEND QUE NÃO CONSEGUE NOMEAR O STREAM LEGADO NÃO TEM NADA PARA MIGRAR.
//
// Este é o teste do defeito CRÍTICO que uma revisão adversarial encontrou: sobre JetStream o
// `Read` do nome legado devolve `ErrConfig` — o `subjectDe` recusa o ponto LEXICALMENTE, antes
// de tocar na rede — e a primeira versão propagava-o. O `Bootstrap` abortava, e **um nó
// JetStream com four-eyes deixava de arrancar, sempre**, tivesse ou não factos legados.
//
// Era o inverso exacto do propósito da migração. E o smoke não o via porque corre sobre WAL.
func TestAOS424BackendQueRecusaONomeLegadoNaoTemNadaParaMigrar(t *testing.T) {
	falso := &storeQueRecusaONomeLegado{}
	copiados, err := MigrarAprovacoes(context.Background(), falso)
	if err != nil {
		t.Fatalf("um backend que recusa o NOME legado nao pode impedir a migracao (%v):\n"+
			"sobre JetStream o Append passa pelo MESMO subjectDe, logo um stream com ponto nunca "+
			"pode ter recebido uma escrita la \u2014 nao ha nada para migrar, e abortar o arranque "+
			"impede o four-eyes de correr no unico substrato que arbitra entre processos.", err)
	}
	if copiados != 0 {
		t.Fatalf("nao havia nada para copiar, copiou %d", copiados)
	}
	// CONTROLO: um `ErrConfig` na ESCRITA continua a abortar — esse e um nome EM USO que o
	// backend recusa, que e um defeito e nao um facto.
	if !falso.leuOLegado {
		t.Error("o teste nao exercitou a leitura do nome legado: nao esta a medir nada")
	}
}

// storeQueRecusaONomeLegado imita a semantica do `jetstream.Store`: recusa LEXICALMENTE
// qualquer stream_id com um ponto, no Read e no Append.
type storeQueRecusaONomeLegado struct{ leuOLegado bool }

func (s *storeQueRecusaONomeLegado) Read(_ context.Context, streamID string, _ uint64) ([]eventstore.Event, error) {
	if strings.Contains(streamID, ".") {
		s.leuOLegado = true
		return nil, fmt.Errorf("%w: stream_id %q contem um caracter nao representavel", eventstore.ErrConfig, streamID)
	}
	return nil, eventstore.ErrStreamNotFound
}

func (s *storeQueRecusaONomeLegado) Append(_ context.Context, streamID string, _ eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if strings.Contains(streamID, ".") {
		return eventstore.AppendResult{}, fmt.Errorf("%w: stream_id %q", eventstore.ErrConfig, streamID)
	}
	return eventstore.AppendResult{Status: eventstore.StatusCommitted}, nil
}
