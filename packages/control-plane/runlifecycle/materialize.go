package runlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// materialize.go — O ORÁCULO DE EFEITO REAL, LIGADO (DEF-273, segunda metade).
//
// # O que estava em falta, e porque era um eixo de segurança
//
// `planmaterialize` clampa DUAS vezes a autoridade de um nó com o papel reservado
// `verifier`: primeiro às tools do papel, depois RETIRANDO as tools de efeito. O
// predicado «esta tool tem efeito?» não vive lá — é `planvalidate.IsEffectTool`,
// derivado dos eixos de risco PINADOS, e chega por `WithEffectOracle` ligado pelo
// composition root. O próprio `doc.go` do materializador o diz.
//
// Não havia composition root. Sem a opção ligada, o materializador usa o
// [planmaterialize.DefaultEffectOracle], que devolve `true` para TUDO — e o efeito
// medido é o que o DEF-273 regista: um verificador materializa com `Authority[]`
// VAZIA e sem tool call ([Materializer.primaryTool] devolve false, porque nenhuma
// tool sobrevive ao clamp). O AC1 de AOS-271 «cumpre-se» por o verificador não
// conseguir fazer nada.
//
// É fail-closed, não um buraco — e é por isso que a dívida era de UTILIDADE. Mas um
// verificador que não pode verificar torna o ramo de qualidade a jusante INDECIDO
// para sempre, que é metade do organigrama aprovado a não correr.
//
// # A forma da solução: o oráculo não é uma opção
//
// [Tenure.Materializer] DERIVA o oráculo do snapshot pinado e não aceita que o
// chamador o substitua. É a mesma disciplina do grafo re-hidratado: em vez de
// verificar que alguém ligou a coisa certa, torna-se impossível construir o objecto
// sem ela. O guarda [TestGuard_OraculoDeEfeitoNaoEOpcional] falsifica-o sobre o
// source.

// ErrSemSnapshot — pediu-se um materializador sem snapshot pinado de capabilities.
// Fail-closed: sem snapshot não há oráculo de efeito real, e sem oráculo real o
// clamp do verificador degrada para «tudo é efeito» — que é a dívida que este
// caminho existe para pagar.
var ErrSemSnapshot = errors.New("runlifecycle: snapshot de capabilities vazio — sem ele não há oráculo de efeito real")

// Materializer compõe o materializador de AOS-237 SOB A POSSE deste run, com o
// ORÁCULO DE EFEITO REAL derivado do snapshot pinado.
//
// O que esta via liga por si (e o chamador não pode enganar):
//
//   - o ORÁCULO DE EFEITO é `snapshot.EffectOracle()`, sempre. Não há parâmetro que
//     o substitua e este pacote não expõe `planmaterialize.WithEffectOracle`;
//   - o LeafAdmitter é o `GraphBuilder` RE-HIDRATADO desta posse, pelo que os
//     `task.node.created` da materialização são escritos por quem detém o lease e
//     passam pelo `FencedAppender` (ADR-023 §2.3/§2.4);
//   - o MaterializeRecorder é o [PlanRecorder] desta posse, pelo que o
//     `plan.materialized` é igualmente fenced.
//
// O que o chamador fornece, e porquê: `admission` e `spawner` são portas de OUTROS
// domínios — admissão global (AOS-027/028) e delegação de sub-agentes (AOS-026). Não
// são autoridade de ciclo de vida e o materializador declara-as como portas
// precisamente porque o wiring delas varia com o deployment. Para a admissão sobre o
// orçamento da árvore — o caso normal — há [NewBudgetAdmission] neste pacote.
//
// `snapshot` é OBRIGATÓRIO e não-vazio ([ErrSemSnapshot]): um snapshot vazio faria o
// oráculo devolver «efeito» para tudo (nada resolve), reproduzindo exactamente o
// comportamento do default que esta via existe para eliminar. Recusa-se em vez de o
// reproduzir em silêncio.
func (t *Tenure) Materializer(
	ctx context.Context,
	snapshot planvalidate.Snapshot,
	rec *PlanRecorder,
	admission planmaterialize.Admission,
	spawner planmaterialize.Spawner,
	opts ...planmaterialize.Option,
) (*planmaterialize.Materializer, error) {
	if rec == nil {
		return nil, fmt.Errorf("%w: emissor do domínio do plano nil", ErrDeps)
	}
	if admission == nil || spawner == nil {
		return nil, fmt.Errorf("%w: admissão/spawner nil", ErrDeps)
	}
	if len(snapshot.Tools) == 0 {
		return nil, ErrSemSnapshot
	}
	g, err := t.Graph(ctx, rec.producer())
	if err != nil {
		return nil, err
	}
	// O oráculo REAL entra DEPOIS das opções do chamador. A ordem não é estilo: é o
	// que garante que um `WithEffectOracle` que chegasse por `opts` — de uma versão
	// futura deste pacote, de um teste, de um wiring distraído — NÃO possa baixar o
	// clamp do verificador. A última opção vence, e a última é sempre esta.
	todas := append(append([]planmaterialize.Option(nil), opts...),
		planmaterialize.WithEffectOracle(snapshot.EffectOracle()))

	return planmaterialize.NewMaterializer(
		admission,
		planmaterialize.NewGraphLeafAdmitter(g),
		spawner,
		rec.recorder,
		todas...,
	)
}

// ---------------------------------------------------------------------------
// Admissão global sobre o orçamento da árvore (ADR-008).
// ---------------------------------------------------------------------------

// BudgetAdmission liga [planmaterialize.Admission] ao orçamento hierárquico de
// ADR-008: admitir um nó do plano É reservar a sua estimativa (tokens/$) contra a
// árvore, atomicamente e em toda a ancestralidade.
//
// # Porque o orçamento e não o rate-limit do provider
//
// `planmaterialize.AdmitRequest` traz `NodeID` + `Tokens` + `CostMicroUSD` — a forma
// exacta de uma reserva de [budget.Reserver]. O admission control do escalonador
// (`scheduler.Admission`) tem outra chave (`provider:model:region` + tenant) e outra
// pergunta: cabe no TPM/RPM do provider. São admissões diferentes e complementares;
// a que o materializador faz por-nó é a da ÁRVORE.
//
// # Reserva agora, decide depois — e é o materializador que impõe a ordem
//
// A materialização é em DUAS FASES por desenho: admite TODOS os nós primeiro e só
// então materializa, para que uma única negação aborte antes de qualquer spawn (zero
// efeitos parciais). Isso significa que, entre o `Admit` e o desfecho, existe uma
// reserva PENDENTE por nó — e alguém tem de a saldar. Este adaptador guarda-as por
// `(plan_id, node_id)` e expõe [BudgetAdmission.Commit] / [BudgetAdmission.Release],
// que o chamador invoca conforme a materialização tenha ou não corrido.
//
// É a MESMA disciplina Reserve→Commit/Release que o `planbudget.TreeBudgetMeter` já
// usa para as avaliações de condição, e a chave é a mesma por a mesma razão: a
// admissão de um nó de um plano é única.
//
// FAIL-CLOSED: um erro de reserva (tipicamente falta de headroom em qualquer nível da
// ancestralidade) devolve `Admitted=false` com a razão — nunca um `true` optimista.
// Seguro para uso concorrente.
type BudgetAdmission struct {
	reserver budget.Reserver
	// node é o nó de orçamento contra o qual as reservas são feitas — tipicamente o
	// nó RAIZ do run. O aninhamento papel-sob-papel é declaradamente achatado pelo
	// materializador (ver o §5 do doc.go dele), pelo que reservar na raiz é coerente
	// com o que ele próprio faz aos spawns.
	node string

	mu        sync.Mutex
	pendentes map[string]budget.Reservation
}

// Compile-time: o adaptador honra a porta do materializador.
var _ planmaterialize.Admission = (*BudgetAdmission)(nil)

// NewBudgetAdmission constrói a admissão sobre o orçamento da árvore. `reserver` e
// `budgetNode` são obrigatórios.
func NewBudgetAdmission(reserver budget.Reserver, budgetNode string) (*BudgetAdmission, error) {
	if reserver == nil {
		return nil, fmt.Errorf("%w: reserver de orçamento nil", ErrDeps)
	}
	if budgetNode == "" {
		return nil, fmt.Errorf("%w: nó de orçamento vazio", ErrDeps)
	}
	return &BudgetAdmission{reserver: reserver, node: budgetNode, pendentes: map[string]budget.Reservation{}}, nil
}

// admitKey identifica a reserva pendente de um nó de um plano.
func admitKey(planID, nodeID string) string { return planID + "\x00" + nodeID }

// Admit reserva a estimativa do nó contra a árvore. Uma reserva JÁ pendente para o
// mesmo `(plan_id, node_id)` é admitida sem reservar de novo — a materialização é
// re-invocável e um retry não pode drenar a árvore, que é a mesma razão pela qual o
// débito das condições acompanha a DECISÃO e não a tentativa.
func (a *BudgetAdmission) Admit(ctx context.Context, req planmaterialize.AdmitRequest) (planmaterialize.AdmitVerdict, error) {
	k := admitKey(req.PlanID, req.NodeID)
	a.mu.Lock()
	_, pendente := a.pendentes[k]
	a.mu.Unlock()
	if pendente {
		return planmaterialize.AdmitVerdict{Admitted: true}, nil
	}

	amt := budget.Amount{Tokens: req.Tokens, CostMicroUSD: req.CostMicroUSD}
	// Uma estimativa nula não é motivo para NÃO admitir — é um nó sem custo previsto.
	// Reservar zero seria recusado por [budget.Amount.validReserve], pelo que se
	// admite sem reserva: não há nada a saldar depois.
	if amt.Tokens <= 0 && amt.CostMicroUSD <= 0 {
		return planmaterialize.AdmitVerdict{Admitted: true}, nil
	}

	res, err := a.reserver.Reserve(ctx, a.node, amt)
	if err != nil {
		// Sem headroom (ou nó desconhecido) é uma NEGAÇÃO declarada, não um erro de
		// infra: o materializador aborta o plano inteiro antes de qualquer efeito, que
		// é a direcção segura. Devolver o erro faria a mesma coisa, mas perderia a
		// razão legível no `plan.materialized`.
		return planmaterialize.AdmitVerdict{Admitted: false, Reason: err.Error()}, nil
	}
	a.mu.Lock()
	a.pendentes[k] = res
	a.mu.Unlock()
	return planmaterialize.AdmitVerdict{Admitted: true}, nil
}

// Commit CONFIRMA todas as reservas pendentes — a materialização correu e o custo é
// devido. Idempotente: uma segunda chamada não tem nada a confirmar.
func (a *BudgetAdmission) Commit(ctx context.Context) error { return a.saldar(ctx, true) }

// Release DEVOLVE todas as reservas pendentes — a materialização abortou e não há
// nada a pagar. Idempotente.
//
// É o par que impede o vazamento: sem ele, um plano recusado a meio da fase de
// admissão deixaria reservadas as estimativas dos nós que JÁ tinham sido admitidos, e
// a árvore encolheria em cada tentativa falhada até negar tudo.
func (a *BudgetAdmission) Release(ctx context.Context) error { return a.saldar(ctx, false) }

// saldar confirma ou devolve TODAS as reservas pendentes, esvaziando o registo.
// Acumula os erros em vez de parar no primeiro: uma reserva que não salda não pode
// deixar as seguintes por saldar — seria trocar um vazamento por vários.
func (a *BudgetAdmission) saldar(ctx context.Context, confirmar bool) error {
	a.mu.Lock()
	pend := a.pendentes
	a.pendentes = map[string]budget.Reservation{}
	a.mu.Unlock()

	var errs []error
	for _, r := range pend {
		var err error
		if confirmar {
			err = a.reserver.Commit(ctx, r)
		} else {
			err = a.reserver.Release(ctx, r)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Pendentes devolve quantas reservas estão por saldar. Só observabilidade e testes.
func (a *BudgetAdmission) Pendentes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pendentes)
}
