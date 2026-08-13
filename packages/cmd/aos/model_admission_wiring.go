package main

// AOS-260 — A ADMISSÃO DO TURNO DE MODELO NO NÓ, E A SUA DEGRADAÇÃO DECLARADA.
//
// O adaptador de `integration` (AOS-260, `model_admission.go`) sabe reservar e saldar contra
// o orçamento por-run. O que ele NÃO sabe — nem deve — é o que fazer quando nega: isso
// depende da maquinaria que ESTE nó tem composta. Este ficheiro é essa metade, e não inventa
// caminho nenhum: usa os dois que já existem.
//
//	admissao NEGA (headroom esgotado)
//	  ├─ prompt de exaustao ARMADO (AOS-263) → PENDENTE DURÁVEL + running→waiting_on_human
//	  │    → o run e SUSPENSO e RETOMÁVEL; o operador decide `continue` ou `abort` na rota
//	  │      assinada, exactamente como no aviso de burn-down. É o MESMO exhaustionPrompt.
//	  └─ prompt DESARMADO → o run PARA com razão própria (Result.BudgetExhausted), o nó
//	       escreve a linha no log e o selo terminal materializa `timed_out` com o rótulo
//	       `budget_exhausted` — ao lado de max_turns_exhausted e do wall-clock, que são os
//	       outros dois TECTOS DEFENSIVOS. Nunca `failed`: um tecto atingido não é uma avaria
//	       recuperável por saga (AOS-254), é um limite a ser levantado por quem o pôs.
//
// PORQUE NÃO UM CAMINHO NOVO: um «deny + retry» dentro do loop queimaria wall-clock sem
// progresso e o run morreria pelo disjuntor com a causa ERRADA no log; e um mecanismo de
// decisão próprio daria uma decisão humana MAIS FRACA do que a que AOS-263 já entregou
// (pendente durável, TTL varrido, rota assinada com operador pinado e selo WORM). A decisão
// do dono sobre AOS-263 — «nada aqui é mecanismo novo» — vale igual aqui.
//
// O DETECTOR DE REPLAY é a outra metade node-local: quem sabe se um turno vai ser
// REPRODUZIDO é o nó, porque é o nó que monta o plano de replay da retoma no ctx
// (`resume_model.go`). É o MESMO plano que faz o [agentruntime.ModelClient] devolver a
// captura em vez de chamar o provider — pelo que reserva e chamada real ficam simétricas por
// construção, e não por duas regras que alguém tem de manter alinhadas.

import (
	"context"
	"fmt"

	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// nodeModelAdmission decora a admissão de `integration` com a DEGRADAÇÃO DECLARADA do nó.
// Seguro para uso concorrente (não tem estado mutável próprio).
type nodeModelAdmission struct {
	// inner é a admissão REAL sobre o orçamento por-run. É declarada pela PORTA e não pelo
	// concreto ([integration.ModelTurnAdmission]) porque este decorator não usa nada do
	// concreto: o que ele acrescenta é o que fazer com uma NEGAÇÃO, e isso não depende de
	// como a negação foi calculada. Manter a porta aqui é também o que permite exercer os
	// dois desfechos da degradação sem ter de esgotar um orçamento a sério em cada teste.
	inner  agentruntime.ModelAdmission
	rb     *integration.RunBudget
	prompt *exhaustionPrompt // AOS-263; nil ⇒ o run pára com razão própria
	log    func(format string, args ...any)
}

// newNodeModelAdmission compõe a admissão do turno de modelo do nó. Devolve nil — admissão
// NÃO COMPOSTA, e o banner declara-o — quando não há orçamento por-run: sem tecto não há o
// que admitir, e um adaptador que admite tudo seria uma capacidade-fantasma.
//
// O `prompt` é EXPLÍCITO e não variádico, pela razão de [newRunProgress]: um parâmetro
// omissível tornaria a suspensão desligável POR ESQUECIMENTO, em silêncio.
func newNodeModelAdmission(rb *integration.RunBudget, prompt *exhaustionPrompt, log func(string, ...any)) (*nodeModelAdmission, error) {
	if rb == nil {
		return nil, nil
	}
	inner, err := integration.NewModelTurnAdmission(rb,
		// O detector é o plano de replay da retoma, no ctx por-run.
		integration.WithReplayDetector(replayCoversTurn),
	)
	if err != nil {
		return nil, fmt.Errorf("aos: admissao do turno de modelo (AOS-260): %w", err)
	}
	return &nodeModelAdmission{inner: inner, rb: rb, prompt: prompt, log: log}, nil
}

// replayCoversTurn é o [integration.ReplayDetector] do nó: diz se o turno vai ser servido
// pelo PLANO DE REPLAY da retoma em vez de pelo provider.
//
// Lê exactamente o que o [resumeAwareModelClient] lê — o mesmo plano, a mesma chave (o índice
// do turno) — porque é essa igualdade que garante a simetria «sem chamada, sem cobrança». Um
// run normal não tem plano no ctx e isto devolve false sem alocar nada (caminho quente).
func replayCoversTurn(ctx context.Context, _, _ string, turn int) bool {
	plan := replayPlanFrom(ctx)
	if plan == nil {
		return false
	}
	_, ok := plan[turn]
	return ok
}

// AdmitTurn implementa [agentruntime.ModelAdmission]. Delega a decisão no orçamento e, na
// NEGAÇÃO, escolhe a forma da degradação.
func (a *nodeModelAdmission) AdmitTurn(ctx context.Context, req agentruntime.TurnAdmissionRequest) (agentruntime.TurnAdmissionVerdict, error) {
	v, err := a.inner.AdmitTurn(ctx, req)
	if err != nil || v.Admitted {
		return v, err
	}

	// A LINHA SAI SEMPRE, e sai PRIMEIRO: o operador tem de saber que o run parou por
	// orçamento mesmo que a suspensão a seguir falhe. É a mesma ordem de [runProgress.avisar]
	// → [exhaustionPrompt.raise], e pela mesma razão.
	a.logf("ADMISSAO DO TURNO DE MODELO NEGADA (AOS-260) — run %q, turno %d (modelo %q): %s. O loop NAO retenta: o run para AQUI, uma vez, com razao propria (um deny-loop cego queimaria wall-clock e morreria pelo disjuntor com a causa errada)",
		req.RunID, req.Turn, req.ModelID, v.Reason)

	// PROMPT DE EXAUSTÃO (AOS-263) — a mesma pergunta humana que o aviso de burn-down levanta,
	// agora com o tecto REALMENTE atingido. Devolve [errExhaustionSuspended] embrulhado, que o
	// loop trata como erro da porta (aborta) e o nó reconhece na saída do run como SUSPENSÃO
	// ([NodeService.absorveSuspensaoPorExaustao]) — o run fica retomável em vez de arquivado.
	if a.prompt != nil {
		// A RAZÃO da admissão viaja para o registo durável: ela já traz os DOIS pares
		// (tokens, micro-USD) e nomeia a dimensão que negou — sem isto a pergunta assinada
		// falaria só de tokens mesmo quando quem bloqueou o run foi o tecto em dólares.
		if perr := a.prompt.raise(ctx, req.RunID, a.evaluationFor(req), v.Reason); perr != nil {
			return agentruntime.TurnAdmissionVerdict{}, perr
		}
		// nil ⇒ já havia uma pergunta por responder sobre este run. Não se levanta uma segunda
		// sobre a primeira (o `raise` di-lo no log); o run pára com razão própria, que é o
		// desfecho correcto — a pergunta viva continua a ser a que decide.
	}
	return v, nil
}

// SettleTurn implementa [agentruntime.ModelAdmission] — delegação pura (o saldo não tem
// dimensão node-local: é aritmética sobre o tecto).
func (a *nodeModelAdmission) SettleTurn(ctx context.Context, s agentruntime.TurnSettlement) error {
	return a.inner.SettleTurn(ctx, s)
}

// evaluationFor constrói a avaliação que o prompt de exaustão consome.
//
// # PORQUE OS NÚMEROS SÃO OS DO TECTO E NÃO OS DO LEDGER
//
// No caminho de AOS-262 o consumo vem do LEDGER DURÁVEL de turnos (cumulativo entre
// incarnações). Aqui a pergunta é outra e os números têm de a acompanhar: o que se esgotou foi
// o tecto de ENFORCEMENT desta incarnação, e é ele que o operador tem de decidir levantar ou
// não. Reportar o total do ledger faria a pergunta falar de uma grandeza que não é a que
// bloqueou o run. A assimetria entre as duas leituras é a MESMA que o banner já declara
// (aviso cumulativo vs enforcement por-incarnação).
//
// # AS DUAS DIMENSÕES, PORQUE QUALQUER DELAS PODE TER SIDO A QUE NEGOU
//
// Uma reserva só cabe se couber nas DUAS ([budget.Amount]), pelo que com
// `AOS_BUDGET_MAX_COST_MICRO_USD` configurada o run tanto pode ter sido negado pelos tokens
// como pelos dólares — e com tokens de sobra. Reportar só tokens produzia uma pergunta
// AUTO-CONTRADITÓRIA («5000 de 1000000 tokens consumidos» com fracção 1.00) que levava o
// operador a responder `continue` sobre a grandeza errada e a ver o run re-negado no turno
// seguinte pelo mesmo tecto. A dimensão $ só entra quando TEM tecto: sem ele o remanescente é o
// de [integration.UnlimitedCostMicroUSD] e o par consumido/tecto não teria significado.
//
// A grandeza que de facto negou vem nomeada na RAZÃO da admissão, que o chamador propaga para o
// registo durável — este par de números é o CONTEXTO, a razão é o veredicto.
//
// `Threshold` e `Fraction` são 1.0 porque não é um limiar de aviso que foi cruzado — é o
// tecto. `SpanEmitted` é true porque é ele que destranca o `raise`: aqui não há latch de
// superfície a governar a entrada, e não é preciso — o loop pára no primeiro `deny`, pelo que
// esta chamada acontece no máximo uma vez por hospedagem.
func (a *nodeModelAdmission) evaluationFor(req agentruntime.TurnAdmissionRequest) progresssurface.RunEvaluation {
	limite := a.rb.MaxTokensPerRun()
	consumido := limite
	if disponivel, ok := a.rb.AvailableTokens(req.RunID); ok {
		consumido = limite - disponivel
	}
	ev := progresssurface.RunEvaluation{}
	ev.Burndown.Limit.Tokens = limite
	ev.Burndown.Consumed.Tokens = consumido
	if tecto, capped := a.rb.MaxCostMicroUSDPerRun(); capped {
		gastoEmDolares := tecto
		if disponivel, ok := a.rb.AvailableCostMicroUSD(req.RunID); ok {
			gastoEmDolares = tecto - disponivel
		}
		ev.Burndown.Limit.CostMicroUSD = tecto
		ev.Burndown.Consumed.CostMicroUSD = gastoEmDolares
	}
	ev.Burndown.Fraction = 1
	ev.Warning = &progresssurface.BudgetWarning{
		RunID:       req.RunID,
		Turn:        req.Turn,
		Fraction:    1,
		Threshold:   1,
		SpanEmitted: true,
	}
	return ev
}

// logf escreve no log do nó quando há writer (nil-safe, molde de [runProgress.logf]).
func (a *nodeModelAdmission) logf(format string, args ...any) {
	if a == nil || a.log == nil {
		return
	}
	a.log(format, args...)
}

// port devolve a [agentruntime.ModelAdmission] a entregar ao runtime, ou nil quando a
// admissão não está composta (um `*nodeModelAdmission` nil embrulhado numa interface não-nil
// passaria o teste `!= nil` do kernel e ligaria um admissor fantasma que negaria tudo).
func (a *nodeModelAdmission) port() agentruntime.ModelAdmission {
	if a == nil {
		return nil
	}
	return a
}

var _ agentruntime.ModelAdmission = (*nodeModelAdmission)(nil)
