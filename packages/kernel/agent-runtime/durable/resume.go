package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ResumePoint é o CURSOR DE RETOMA que o [Resumer] devolve: onde o Agent Runtime
// deve recomeçar após um crash, sem re-executar passos já confirmados nem perder
// activities pendentes (resume-from-step, não resume-from-task).
//
// # Semântica (ADR-001)
//
// O checkpoint dá EFICIÊNCIA (salta o trabalho já confirmado); o step-ledger de
// AOS-014 é a REDE DE SEGURANÇA de idempotência (se um passo for na mesma
// re-tentado, o downstream deduplica pela chave determinística). Por isso o resume
// é uma optimização segura: apontar o RT ao próximo passo não confirmado nunca
// compromete a correcção — no pior caso re-executa e o ledger dedup.
//
// # Consumibilidade: MARCADOR de progresso, não estado de execução (AOS-015 vs AOS-016)
//
// O ResumePoint é AUTO-SUFICIENTE em FRONTEIRAS DE TURNO: nessas, retomar significa
// (re)entrar o turno NextTurn e voltar a chamar o modelo — todo o não-determinismo
// nasce da resposta do modelo, e a não-repetição de efeitos é garantida pelo ledger.
//
// NÃO é auto-suficiente MID-DISPATCH. PendingActivities identifica QUAIS activities
// faltam e ONDE recomeçar, mas NÃO transporta a invocação (ToolID/Capability/Input)
// necessária para RE-DESPACHAR essas activities. Essa invocação não vive em lado
// nenhum do log durável de AOS-015: o turn.recorded grava só a CONTAGEM de tool
// calls (não os inputs); o evento de mediação do RM grava o ToolID mas NÃO o Input;
// e o cursor REFERENCIA por posição, não copia (ver [Cursor]). No pior caso — crash
// depois de despachar a activity k mas antes de a activity k+1 ser sequer mediada —
// não existe nenhum registo da invocação de k+1.
//
// Consequência honesta: reconstruir as PendingActivities para RE-DESPACHAR sem
// re-chamar o modelo é RESPONSABILIDADE DE AOS-016 (replay determinístico), que tem
// de persistir/reconstruir a resposta do modelo do turno e, a partir dela, re-derivar
// as tool calls (mesma ordem, mesmos inputs). Enquanto AOS-016 não fornecer essa
// fonte, um consumidor de AOS-015 que queira progredir mid-dispatch tem de re-chamar
// o modelo (o ledger dedup os efeitos já aplicados) — o cursor sozinho não retoma o
// despacho. Ver a nota "Contrato de consumo" em [Resumer].
type ResumePoint struct {
	// RunID é a trajectória a retomar.
	RunID string
	// FromScratch indica que não há checkpoints para o run: retoma do início
	// (turno 1). NextTurn é 1 e NextStepID é o step_id do turno 1.
	FromScratch bool
	// LastConfirmed é o cursor do ÚLTIMO checkpoint confirmado (a fronteira de
	// progresso). Zero-value quando FromScratch.
	LastConfirmed Cursor
	// NextTurn é o turno (1-based) em que o RT deve retomar.
	NextTurn int
	// NextStepID é o step_id do PRÓXIMO passo NÃO confirmado — o ponto de retoma.
	// Quando há activities pendentes é o step_id da primeira; caso contrário é o
	// step_id do turno a (re)entrar.
	NextStepID string
	// PendingActivities são os step_ids (IDENTIDADE/POSIÇÃO) das activities que
	// faltava despachar na iteração corrente quando o crash ocorreu (vazio se a
	// fronteira for uma fronteira de turno). Marcam ONDE recomeçar e QUAIS as já
	// confirmadas a NÃO repetir — mas NÃO carregam a invocação (ToolID/Capability/
	// Input) precisa para as re-despachar. Reconstruir essa invocação é de AOS-016
	// (ver a secção "Consumibilidade" acima): com só AOS-015, retomar mid-dispatch
	// implica re-chamar o modelo, ficando o ledger como rede de dedup dos efeitos já
	// aplicados. Não interpretar esta lista como "pronta a re-despachar".
	PendingActivities []string
}

// Resumer reconstrói o [ResumePoint] de um run a partir dos checkpoints
// persistidos no Event Store (AOS-015). É a peça de retoma resume-from-step: relê
// o stream do run, encontra a fronteira de progresso (o último checkpoint) e
// devolve o próximo passo a executar.
//
// # Reconstrutibilidade e failover
//
// Como os checkpoints vivem no Event Store replicado, o Resumer recupera o cursor
// mesmo depois de o worker original morrer (failover): um worker novo constrói um
// Resumer sobre o mesmo cluster e chama [Resumer.Resume]. É o análogo de leitura
// do [StepLedger.Rebuild].
//
// # Contrato de consumo (AOS-015 vs AOS-016)
//
// O [ResumePoint] devolvido é um MARCADOR de progresso, auto-suficiente em
// fronteiras de turno mas NÃO mid-dispatch — reconstruir as invocações pendentes
// para re-despachar é de AOS-016 (ver [ResumePoint]). Além disso, os ramos que
// RE-ENTRAM o turno re-chamam o modelo: uma retoma FIEL exige reconstruir o tail
// append-only do turno (para o prompt_hash bater com o manifesto persistido em
// turn.recorded), o que AOS-015 não faz — é também pré-condição de AOS-016. Com só
// AOS-015 a correcção assenta no ledger (dedup dos efeitos), não na fidelidade do
// prompt re-montado.
//
// Sem estado mutável ⇒ seguro para uso concorrente e -race limpo.
type Resumer struct {
	store        EventStore
	stepIdentity agentruntime.StepIdentity
}

// ResumerOption configura o [Resumer].
type ResumerOption func(*Resumer)

// WithStepIdentity injecta o derivador de step_id usado para nomear o próximo
// passo numa fronteira de turno (default: um [StepSequencer] com o formato
// canónico). DEVE ser o MESMO derivador que o loop usa (AOS-014), para que o
// NextStepID coincida com o step_id que o RT vai atribuir ao turno retomado.
//
// Este acoplamento é agora VERIFICADO, não só documentado: [Resumer.Resume]
// confirma que o derivador injectado reproduz o step_id realmente gravado no
// checkpoint e fail-closed com [ErrStepIdentityMismatch] se divergir (p.ex.
// prefixo/largura custom no loop e default no Resumer). Assim, um formato
// incompatível é detectado no acto da retoma em vez de produzir silenciosamente um
// NextStepID que o loop nunca atribui.
func WithStepIdentity(s agentruntime.StepIdentity) ResumerOption {
	return func(r *Resumer) { r.stepIdentity = s }
}

// NewResumer constrói um Resumer sobre o Event Store dado. store é obrigatório
// (não-nil). Por omissão usa um [StepSequencer] com o formato canónico de step_id.
func NewResumer(store EventStore, opts ...ResumerOption) (*Resumer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	r := &Resumer{store: store, stepIdentity: NewStepSequencer()}
	for _, o := range opts {
		o(r)
	}
	if r.stepIdentity == nil {
		r.stepIdentity = NewStepSequencer()
	}
	return r, nil
}

// Resume lê os checkpoints do run e devolve o cursor de retoma. Um stream sem
// eventos (ou sem checkpoints) retoma do início (FromScratch, turno 1).
//
// A fronteira de progresso é o ÚLTIMO evento de checkpoint por seq — os
// checkpoints são escritos na ordem de execução (assembled → model_called →
// turn_recorded → dispatched* → verified, turno a turno), pelo que o seq mais alto
// é o ponto mais avançado alcançado antes do crash.
func (r *Resumer) Resume(ctx context.Context, runID string) (ResumePoint, error) {
	if runID == "" {
		return ResumePoint{}, ErrEmptyRunID
	}
	events, err := r.store.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return r.fromScratch(runID), nil
		}
		return ResumePoint{}, err
	}

	// Fronteira = último checkpoint por seq (Read devolve por seq ascendente).
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != EventTypeCheckpoint {
			continue
		}
		var cur Cursor
		if err := json.Unmarshal(events[i].Payload, &cur); err != nil {
			return ResumePoint{}, err
		}
		return r.next(runID, cur)
	}
	// Stream existe mas ainda sem checkpoints (p.ex. só turn.recorded/ledger).
	return r.fromScratch(runID), nil
}

// next traduz a fronteira de progresso no próximo passo a executar. A tradução é
// PURA e determinística — depende apenas da fase e das activities pendentes da
// fronteira. Antes de nomear o próximo passo, VERIFICA que o [agentruntime.StepIdentity]
// injectado reproduz o step_id que o loop gravou no cursor (fail-closed com
// [ErrStepIdentityMismatch] se o formato divergir — ver [WithStepIdentity]).
func (r *Resumer) next(runID string, f Cursor) (ResumePoint, error) {
	if err := r.verifyStepIdentity(runID, f); err != nil {
		return ResumePoint{}, err
	}
	rp := ResumePoint{RunID: runID, LastConfirmed: f}
	switch agentruntime.CheckpointPhase(f.Phase) {
	case agentruntime.PhaseVerified:
		// O turno f.Turn ficou verificado (completo): retoma no turno seguinte. O
		// step_id do turno f.Turn+1 usa a MESMA derivação já validada para f.Turn
		// (função pura da posição), pelo que coincidirá com o que o loop atribuirá.
		rp.NextTurn = f.Turn + 1
		rp.NextStepID = r.stepIdentity.StepID(runID, f.Turn+1)

	case agentruntime.PhaseDispatched:
		if len(f.PendingActivities) > 0 {
			// Ainda há activities por despachar: retoma na primeira pendente, dentro do
			// mesmo turno, sem repetir as já confirmadas. NOTA (AOS-016): estes step_ids
			// marcam a posição, não a invocação — re-despachar sem re-chamar o modelo
			// exige a reconstrução de AOS-016 (ver [ResumePoint]).
			rp.NextTurn = f.Turn
			rp.NextStepID = f.PendingActivities[0]
			rp.PendingActivities = append([]string(nil), f.PendingActivities...)
		} else {
			// Todas as activities do turno despachadas, mas a verificação ainda não
			// ficou confirmada: re-entra o turno f.Turn. As activities re-executadas (se
			// alguma) são deduplicadas pelo ledger de AOS-014 (rede de segurança). A
			// re-entrada re-chama o modelo; a fidelidade do prompt re-montado depende da
			// reconstrução do tail (pré-condição de AOS-016 — ver [Resumer]).
			rp.NextTurn = f.Turn
			rp.NextStepID = r.stepIdentity.StepID(runID, f.Turn)
		}

	default:
		// assembled / model_called / turn_recorded: fronteira ANTES do despacho. Nada
		// de externo foi confirmado neste turno para além do registo do turno (que o
		// ES deduplica ao re-gravar). Re-entra o turno f.Turn; o modelo é re-chamado e
		// as suas activities (ainda por despachar) seguem o caminho normal. Como acima,
		// a re-montagem fiel do prompt é responsabilidade de AOS-016 (reconstrução do
		// tail); AOS-015 apoia-se no ledger para a correcção dos efeitos.
		rp.NextTurn = f.Turn
		rp.NextStepID = r.stepIdentity.StepID(runID, f.Turn)
	}
	return rp, nil
}

// verifyStepIdentity fecha o acoplamento de formato entre o Resumer e o loop
// (finding config-coupling AOS-015): confirma que o [agentruntime.StepIdentity]
// injectado deriva EXACTAMENTE o step_id do turno que o loop persistiu no cursor.
// O step_id do turno lê-se do cursor — directamente em fronteiras de turno
// (ConfirmedStepID == step_id do turno) ou removendo o sufixo "-tool-N" numa
// fronteira de despacho. Divergência ⇒ [ErrStepIdentityMismatch] (fail-closed),
// em vez de devolver um NextStepID que o loop nunca atribuiria.
func (r *Resumer) verifyStepIdentity(runID string, f Cursor) error {
	confirmed := f.ConfirmedStepID
	if confirmed == "" {
		// Sem step_id persistido não há o que verificar (cursor legado/incompleto);
		// não bloqueia a retoma.
		return nil
	}
	turnStep := confirmed
	if i := strings.LastIndex(confirmed, "-tool-"); i >= 0 {
		turnStep = confirmed[:i]
	}
	if r.stepIdentity.StepID(runID, f.Turn) != turnStep {
		return ErrStepIdentityMismatch
	}
	return nil
}

// fromScratch é o cursor de retoma quando não há checkpoints: turno 1 do início.
func (r *Resumer) fromScratch(runID string) ResumePoint {
	return ResumePoint{
		RunID:       runID,
		FromScratch: true,
		NextTurn:    1,
		NextStepID:  r.stepIdentity.StepID(runID, 1),
	}
}
