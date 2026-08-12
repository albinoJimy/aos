package integration

// AOS-263 (PARTE 1) — O REGISTO DE PENDENTES PASSA A TER TIPO.
//
// A maquinaria de pendentes era mono-tipo: uma aprovação de tool call amarrada ao digest da
// PREVIEW. O prompt de exaustão de orçamento é uma segunda decisão humana com a MESMA
// mecânica de suspensão, mas SEM preview. Estes testes trancam as duas propriedades que a
// generalização não pode perder:
//
//   - COMPATIBILIDADE: os registos JÁ SELADOS no WORM não têm campo `kind` e não podem ser
//     reescritos. Têm de continuar a ler-se — e a expirar-se — como aprovações. E os
//     escritos HOJE, sendo do tipo default, têm de sair byte-idênticos aos de ontem;
//   - DISTINÇÃO: os dois tipos coexistem no MESMO run e no MESMO passo sem se deduplicarem,
//     sem se expirarem um ao outro e sem se confundirem na listagem.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// exaustaoRec constrói um pendente de EXAUSTÃO: sem preview, amarrado a (run, limiar,
// montante consumido) — os valores que o aviso de burn-down de AOS-262 produz.
func exaustaoRec(runID, stepID string, criadoEm time.Time) PendingRecord {
	rec := PendingRecord{
		Kind:           PendingKindExhaustion,
		RunID:          runID,
		StepID:         stepID,
		Turn:           7,
		Threshold:      0.8,
		ConsumedTokens: 8_200,
		LimitTokens:    10_000,
	}
	if !criadoEm.IsZero() {
		rec.CreatedAt = criadoEm.Format(time.RFC3339Nano)
	}
	return rec
}

// leEventos devolve o stream de aprovações em bruto (é onde se verifica a FORMA gravada, não
// só o que a API devolve — a compatibilidade é uma propriedade dos bytes). Um stream que
// NUNCA existiu é o vazio legítimo: é o que se observa quando toda a escrita foi recusada.
func leEventos(t *testing.T, es *eventstore.Store) []eventstore.Event {
	t.Helper()
	evs, err := es.Read(context.Background(), approvalStream, 0)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("Read(%s): %v", approvalStream, err)
	}
	return evs
}

// TestPendingKind_RegistoSeladoSemTipoLeComoAprovacao — A PROVA DE NÃO-VACUOSIDADE da
// compatibilidade de LEITURA. O evento é escrito À MÃO exactamente na forma de antes de
// AOS-263 (payload SEM `kind`, chave de idempotência `pending-<run>-<step>`), como se
// estivesse selado no WORM antes desta entrega. Tem de continuar a resolver como aprovação —
// na listagem, no varrimento e na expiração pela assinatura antiga de `Expire`.
func TestPendingKind_RegistoSeladoSemTipoLeComoAprovacao(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()
	criado := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	// A forma EXACTA de antes de AOS-263 — nenhum campo de tipo.
	antigo := `{"run_id":"run-selado","step_id":"s1","turn":3,"tool_id":"doc_write",` +
		`"capability":"cap:fs.write","preview":"cHJldmlldy1hbnRpZ2E=",` +
		`"created_at":"` + criado.Format(time.RFC3339Nano) + `"}`
	if _, err := es.Append(ctx, approvalStream, eventstore.EventInput{
		Type:    approvalPendingEventType,
		Payload: json.RawMessage(antigo),
		RunID:   approvalRunID,
		StepID:  "pending-run-selado-s1", // a chave ANTIGA, sem âmbito de tipo
	}); err != nil {
		t.Fatalf("append do registo antigo: %v", err)
	}

	got, err := p.ListForRun(ctx, "run-selado")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("o registo selado antes de AOS-263 tem de continuar a aparecer; deu %+v", got)
	}
	if got[0].Kind != PendingKindApproval {
		t.Fatalf("um registo SEM campo de tipo é uma APROVAÇÃO; deu kind=%q", got[0].Kind)
	}
	if len(got[0].Preview) == 0 || got[0].Turn != 3 {
		t.Fatalf("o resto do registo antigo tem de sobreviver à desserialização: %+v", got[0])
	}

	// O varrimento também o alcança...
	exp, err := p.ListExpirable(ctx, criado.Add(time.Hour), 15*time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(exp) != 1 || exp[0].Kind != PendingKindApproval {
		t.Fatalf("o registo antigo tem de ser expirável como aprovação; deu %+v", exp)
	}

	// ...e a assinatura ANTIGA de Expire continua a resolvê-lo (a chave não mudou para o
	// tipo default; se tivesse mudado, este pendente ficaria eterno).
	if err := p.Expire(ctx, "run-selado", "s1"); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if got, _ := p.ListForRun(ctx, "run-selado"); len(got) != 0 {
		t.Fatalf("o registo antigo expirado devia sair da lista; ficou %+v", got)
	}
}

// TestPendingKind_AprovacaoEscritaHojeFicaComAFormaAntiga — a compatibilidade de ESCRITA.
// O tipo default define-se pela AUSÊNCIA do discriminador: um pendente de aprovação gravado
// depois de AOS-263 tem de ter a MESMA chave de idempotência e um payload SEM `kind`. Sem
// isto, um nó actualizado escreveria registos que um nó anterior (rollback, réplica a meio de
// um upgrade) deduplicaria noutro âmbito — e o operador veria a mesma decisão duas vezes.
func TestPendingKind_AprovacaoEscritaHojeFicaComAFormaAntiga(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()

	// Escrito com o tipo EXPLÍCITO — o resultado tem de ser o mesmo que com ele omitido.
	rec := pendingRec("run-forma", "s1", time.Now())
	rec.Kind = PendingKindApproval
	if err := p.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	evs := leEventos(t, es)
	if len(evs) != 1 {
		t.Fatalf("esperava 1 evento; deu %d", len(evs))
	}
	if evs[0].StepID != "pending-run-forma-s1" {
		t.Fatalf("a chave do pendente de APROVAÇÃO não pode mudar; deu %q", evs[0].StepID)
	}
	var bruto map[string]any
	if err := json.Unmarshal(evs[0].Payload, &bruto); err != nil {
		t.Fatalf("payload ilegível: %v", err)
	}
	if _, tem := bruto["kind"]; tem {
		t.Fatalf("o tipo default NÃO se escreve (é a ausência que o define); payload=%v", bruto)
	}
	for _, campo := range []string{"threshold", "consumed_tokens", "limit_tokens"} {
		if _, tem := bruto[campo]; tem {
			t.Fatalf("campo %q do tipo exaustão não pode aparecer num pendente de aprovação", campo)
		}
	}
}

// TestPendingKind_TiposCoexistemNoMesmoPasso — a distinção. Os dois pendentes partilham run
// E step (o pior caso: o prompt de exaustão nasce no mesmo turno em que uma tool call
// escalou). Se o tipo não entrasse na chave de idempotência, o segundo Put viria DUPLICADO e
// a decisão desaparecia em silêncio.
func TestPendingKind_TiposCoexistemNoMesmoPasso(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()

	aprov := pendingRec("run-dois", "s1", agora)
	exaust := exaustaoRec("run-dois", "s1", agora)
	if err := p.Put(ctx, aprov); err != nil {
		t.Fatalf("Put(aprovação): %v", err)
	}
	if err := p.Put(ctx, exaust); err != nil {
		t.Fatalf("Put(exaustão): %v", err)
	}

	got, err := p.ListForRun(ctx, "run-dois")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("os dois tipos são duas decisões distintas e têm de coexistir; deu %+v", got)
	}
	porTipo := make(map[PendingKind]PendingRecord, 2)
	for _, r := range got {
		porTipo[r.Kind] = r
	}
	a, temA := porTipo[PendingKindApproval]
	e, temE := porTipo[PendingKindExhaustion]
	if !temA || !temE {
		t.Fatalf("cada tipo tem de ser DISTINGUÍVEL na listagem; deu %+v", got)
	}
	if len(a.Preview) == 0 {
		t.Fatalf("a aprovação continua amarrada à preview: %+v", a)
	}
	if len(e.Preview) != 0 {
		t.Fatalf("o pendente de exaustão NÃO tem preview: %+v", e)
	}
	if e.Threshold != 0.8 || e.ConsumedTokens != 8_200 || e.LimitTokens != 10_000 || e.Turn != 7 {
		t.Fatalf("a amarra do pendente de exaustão (limiar, consumo, tecto) tem de sobreviver: %+v", e)
	}
}

// TestPendingKind_ExpirarUmTipoNaoExpiraOOutro — expirar é POR TIPO. Duas decisões distintas
// sobre o mesmo passo não se apagam uma à outra: expirar a aprovação por TTL não pode fazer
// desaparecer, sem resposta, o prompt de exaustão que ainda espera pelo operador.
func TestPendingKind_ExpirarUmTipoNaoExpiraOOutro(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()

	if err := p.Put(ctx, pendingRec("run-exp2", "s1", agora)); err != nil {
		t.Fatalf("Put(aprovação): %v", err)
	}
	if err := p.Put(ctx, exaustaoRec("run-exp2", "s1", agora)); err != nil {
		t.Fatalf("Put(exaustão): %v", err)
	}

	if err := p.Expire(ctx, "run-exp2", "s1"); err != nil { // tipo default
		t.Fatalf("Expire: %v", err)
	}
	got, err := p.ListForRun(ctx, "run-exp2")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 1 || got[0].Kind != PendingKindExhaustion {
		t.Fatalf("expirar a APROVAÇÃO não pode expirar o prompt de exaustão; ficou %+v", got)
	}

	if err := p.ExpireKind(ctx, PendingKindExhaustion, "run-exp2", "s1"); err != nil {
		t.Fatalf("ExpireKind: %v", err)
	}
	if got, _ := p.ListForRun(ctx, "run-exp2"); len(got) != 0 {
		t.Fatalf("o pendente de exaustão expirado devia sair da lista; ficou %+v", got)
	}
}

// TestPendingKind_ListExpirableCobreAmbosOsTipos — o varrimento não pode ter um ponto cego.
// Um pendente de exaustão esquecido pelo operador tem de envelhecer e expirar como qualquer
// outro, senão fica eterno na lista e o run eternamente marcado como à espera.
func TestPendingKind_ListExpirableCobreAmbosOsTipos(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	const ttl = 15 * time.Minute

	if err := p.Put(ctx, pendingRec("run-varre", "s1", agora.Add(-20*time.Minute))); err != nil {
		t.Fatalf("Put(aprovação): %v", err)
	}
	if err := p.Put(ctx, exaustaoRec("run-varre", "s2", agora.Add(-20*time.Minute))); err != nil {
		t.Fatalf("Put(exaustão): %v", err)
	}

	got, err := p.ListExpirable(ctx, agora, ttl)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ambos os tipos têm de ser expiráveis; deu %+v", got)
	}
	// E o varrimento, expirando COM O TIPO de cada registo, esvazia mesmo a lista — e não
	// devolve o mesmo trabalho no tick seguinte.
	for _, r := range got {
		if err := p.ExpireKind(ctx, r.Kind, r.RunID, r.StepID); err != nil {
			t.Fatalf("ExpireKind(%s): %v", r.Kind, err)
		}
	}
	again, err := p.ListExpirable(ctx, agora, ttl)
	if err != nil {
		t.Fatalf("ListExpirable (2ª volta): %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("um pendente expirado com o SEU tipo não pode voltar a aparecer ao varrimento: %+v", again)
	}
}

// TestPendingKind_PutRecusaExaustaoComPreview — fail-closed contra CONFUSÃO DE TIPO. Um
// pendente de exaustão que transportasse preview podia ser dado por "decidido" por um grant
// emitido para outra acção (o filtro de decisão é por preview), ou ser renderizado como uma
// aprovação. Recusa-se na escrita, que é onde ainda não fez mal.
func TestPendingKind_PutRecusaExaustaoComPreview(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()

	mau := exaustaoRec("run-mau", "s1", time.Now())
	mau.Preview = []byte("preview-que-nao-devia-existir")
	if err := p.Put(ctx, mau); !errors.Is(err, ErrExhaustionWithPreview) {
		t.Fatalf("exaustão com preview tem de ser recusada com ErrExhaustionWithPreview; deu %v", err)
	}
	if evs := leEventos(t, es); len(evs) != 0 {
		t.Fatalf("uma recusa NÃO pode deixar rasto no log: %+v", evs)
	}
}

// TestPendingKind_PutRecusaTipoDesconhecido — o mesmo fail-closed para um tipo que este
// binário não sabe apresentar nem consumir. Um registo assim seria uma decisão humana que
// ninguém consegue tomar.
func TestPendingKind_PutRecusaTipoDesconhecido(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()

	rec := pendingRec("run-desconhecido", "s1", time.Now())
	rec.Kind = PendingKind("inventado")
	if err := p.Put(ctx, rec); !errors.Is(err, ErrUnknownPendingKind) {
		t.Fatalf("tipo desconhecido tem de ser recusado com ErrUnknownPendingKind; deu %v", err)
	}
	if evs := leEventos(t, es); len(evs) != 0 {
		t.Fatalf("uma recusa NÃO pode deixar rasto no log: %+v", evs)
	}
}

// ---------------------------------------------------------------------------
// AOS-263 (PARTE 3) — a pergunta RESPONDIDA sai da lista por DECISÃO
// ---------------------------------------------------------------------------

// TestPendingDecide_RespondidoSaiDaListaENaoVoltaAoVarrimento tranca a razão de [Decide]
// existir: sem ele, uma pergunta já respondida continuaria a aparecer ao operador como
// trabalho por fazer e, passado o TTL, o varrimento acabaria por a "expirar" — escrevendo no
// log append-only que ninguém decidiu uma decisão que foi tomada e selada.
//
// As duas metades:
//
//   - a decisão RETIRA o pendente das DUAS listagens (a do operador e a do varrimento). Se
//     [Decide] escrevesse um facto que só uma delas conhecesse, o varrimento devolveria para
//     sempre um registo que o operador já não vê;
//   - a decisão é IDEMPOTENTE (a chave é por (tipo, run, passo)) e o pendente NÃO volta.
func TestPendingDecide_RespondidoSaiDaListaENaoVoltaAoVarrimento(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	const ttl = 15 * time.Minute

	if err := p.Put(ctx, exaustaoRec("run-dec", "exhaustion@chat#4", agora.Add(-20*time.Minute))); err != nil {
		t.Fatalf("Put(exaustão): %v", err)
	}
	// NÃO-VACUOSIDADE: antes da decisão, a pergunta está nas duas listagens.
	if got, _ := p.ListForRun(ctx, "run-dec"); len(got) != 1 {
		t.Fatalf("antes da decisão a pergunta tem de estar por responder; deu %+v", got)
	}
	if got, _ := p.ListExpirable(ctx, agora, ttl); len(got) != 1 {
		t.Fatalf("antes da decisão a pergunta é expirável (envelheceu); deu %+v", got)
	}

	if err := p.Decide(ctx, PendingKindExhaustion, "run-dec", "exhaustion@chat#4", "abort", "human:alice"); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if got, err := p.ListForRun(ctx, "run-dec"); err != nil || len(got) != 0 {
		t.Fatalf("uma pergunta respondida sai da lista do operador; deu %+v (err=%v)", got, err)
	}
	if got, err := p.ListExpirable(ctx, agora, ttl); err != nil || len(got) != 0 {
		t.Fatalf("o varrimento NÃO pode expirar o que já foi decidido; deu %+v (err=%v)", got, err)
	}
	// Idempotente: repetir a decisão não ressuscita nada nem falha.
	if err := p.Decide(ctx, PendingKindExhaustion, "run-dec", "exhaustion@chat#4", "abort", "human:alice"); err != nil {
		t.Fatalf("Decide (repetido): %v", err)
	}
	if got, _ := p.ListForRun(ctx, "run-dec"); len(got) != 0 {
		t.Fatalf("a repetição não pode devolver o pendente à lista; deu %+v", got)
	}
}

// TestPendingDecide_DecidirUmTipoNaoDecideOOutro — a decisão é POR TIPO, como a expiração.
// Responder ao prompt de exaustão de um passo não pode dar por decidida a APROVAÇÃO do mesmo
// passo: são duas decisões humanas distintas, com amarras e rotas distintas, e colapsá-las
// autorizaria em silêncio uma acção que ninguém aprovou.
func TestPendingDecide_DecidirUmTipoNaoDecideOOutro(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()

	if err := p.Put(ctx, pendingRec("run-dec2", "s1", agora)); err != nil {
		t.Fatalf("Put(aprovação): %v", err)
	}
	if err := p.Put(ctx, exaustaoRec("run-dec2", "s1", agora)); err != nil {
		t.Fatalf("Put(exaustão): %v", err)
	}

	if err := p.Decide(ctx, PendingKindExhaustion, "run-dec2", "s1", "abort", "human:alice"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got, err := p.ListForRun(ctx, "run-dec2")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	if len(got) != 1 || got[0].Kind != PendingKindApproval {
		t.Fatalf("decidir o prompt de exaustão não pode decidir a APROVAÇÃO do mesmo passo; ficou %+v", got)
	}
}

// ---------------------------------------------------------------------------
// AOS-263 (REMEDIAÇÃO) — a chave que o varrimento usa é INJECTIVA
// ---------------------------------------------------------------------------

// TestPendingListExpirable_ChavesAmbiguasNaoSeAnulam sela o defeito de injectividade que a
// generalização introduziu na LISTAGEM: `ListExpirable` percorre TODOS os runs e passou a
// colapsar registos por `pendingKey("", …)` — que, no tipo default, é a concatenação AMBÍGUA
// `<run>-<step>`. Dois pendentes distintos cujas concatenações coincidem (`run="run-263"` +
// `step="chat#1"` e `run="run"` + `step="263-chat#1"`) deduplicavam-se um ao outro na
// listagem: um deles nunca chegava ao varrimento e, por isso, NUNCA expirava — ficava para
// sempre na lista do operador. Antes de AOS-263 a chave desta listagem era `run + "|" + step`,
// inequívoca; a regressão foi gratuita.
//
// A chave DURÁVEL não se pode reescrever (é ela que mantém legíveis e expiráveis os registos
// já selados), pelo que os dois eventos são escritos com chaves de idempotência próprias — é
// assim que o LOG pode conter os dois. O que o teste exige é que a LISTAGEM não os funda.
func TestPendingListExpirable_ChavesAmbiguasNaoSeAnulam(t *testing.T) {
	p, es := pendingFixture(t)
	ctx := context.Background()
	velho := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)

	// As duas concatenações coincidem: "run-263" + "-" + "chat#1" == "run" + "-" + "263-chat#1".
	for i, rec := range []PendingRecord{
		{RunID: "run-263", StepID: "chat#1", Turn: 1, ToolID: "doc_write", Capability: "cap:fs.write", Preview: []byte("p-a"), CreatedAt: velho},
		{RunID: "run", StepID: "263-chat#1", Turn: 1, ToolID: "doc_write", Capability: "cap:fs.write", Preview: []byte("p-b"), CreatedAt: velho},
	} {
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if _, err := es.Append(ctx, approvalStream, eventstore.EventInput{
			Type:    approvalPendingEventType,
			Payload: body,
			RunID:   approvalRunID,
			// Chave própria por evento: o objecto do teste é a LISTAGEM, não a deduplicação
			// durável (que é ambígua por compatibilidade e assim tem de ficar).
			StepID: "pending-distinto-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	expiraveis, err := p.ListExpirable(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("ListExpirable: %v", err)
	}
	if len(expiraveis) != 2 {
		t.Fatalf("dois pendentes DISTINTOS tem de chegar ambos ao varrimento (senao um nunca expira); vieram %d: %+v", len(expiraveis), expiraveis)
	}
	vistos := map[string]bool{}
	for _, e := range expiraveis {
		vistos[e.RunID+"|"+e.StepID] = true
	}
	if !vistos["run-263|chat#1"] || !vistos["run|263-chat#1"] {
		t.Fatalf("os dois registos tem de vir INTEIROS e distintos: %+v", expiraveis)
	}
}

// TestPendingPut_ExaustaoDuplicadaERecusada — o duplicado SILENCIOSO era o pior dos mundos.
//
// O Event Store deduplica por chave de idempotência e devolve StatusDuplicate SEM erro: para
// uma aprovação re-escalada é o comportamento certo (o pendente já lá está e é o mesmo); para
// um prompt de exaustão é fatal, porque a pergunta anterior JÁ SAIU da lista (expirou ou foi
// decidida) e a nova nunca aparece — o run fica suspenso à espera de uma resposta a uma
// pergunta que ninguém vê nem consegue decidir. Fail-closed: erro NOMEADO.
func TestPendingPut_ExaustaoDuplicadaERecusada(t *testing.T) {
	p, _ := pendingFixture(t)
	ctx := context.Background()
	agora := time.Now()

	if err := p.Put(ctx, exaustaoRec("run-dup", "chat#1", agora)); err != nil {
		t.Fatalf("primeiro Put: %v", err)
	}
	if err := p.Put(ctx, exaustaoRec("run-dup", "chat#1", agora)); !errors.Is(err, ErrExhaustionPromptColide) {
		t.Fatalf("um segundo prompt com a MESMA chave tem de ser recusado; deu %v", err)
	}
	// E a APROVAÇÃO continua idempotente — re-escalar a mesma tool call é normal (AOS-021) e
	// transformá-lo em erro partiria o bridge de aprovação humana.
	if err := p.Put(ctx, pendingRec("run-dup", "chat#1", agora)); err != nil {
		t.Fatalf("Put(aprovacao): %v", err)
	}
	if err := p.Put(ctx, pendingRec("run-dup", "chat#1", agora)); err != nil {
		t.Fatalf("re-escalar a MESMA aprovacao continua a ser idempotente: %v", err)
	}
	// E a pergunta continua UMA (a recusa não duplicou nada no log).
	got, err := p.ListForRun(ctx, "run-dup")
	if err != nil {
		t.Fatalf("ListForRun: %v", err)
	}
	var exaustoes int
	for _, r := range got {
		if r.Kind.Resolved() == PendingKindExhaustion {
			exaustoes++
		}
	}
	if exaustoes != 1 {
		t.Fatalf("tinha de ficar exactamente 1 prompt de exaustao; ficaram %d", exaustoes)
	}
}
