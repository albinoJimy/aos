package main

// AOS-263 (PARTE 2) — O PROMPT DE EXAUSTÃO DE ORÇAMENTO É EMITIDO E FICA VISÍVEL.
//
// AOS-262 entregou o AVISO: ao cruzar o limiar de burn-down, o nó escreve uma linha no log
// (e um span, com OTLP) e o run CONTINUA. Este ficheiro é a segunda metade — a que
// transforma o aviso numa DECISÃO HUMANA com a mesma força das que o nó já tem:
//
//	aviso de burn-down (AOS-262, progress_wiring.go)   ← a FONTE do sinal, não se recalcula
//	  └→ pendente DURÁVEL de EXAUSTÃO (2.º tipo de PendingRecord, AOS-263 parte 1)
//	  └→ suspensão em `waiting_on_human` pelo runGate JÁ EXISTENTE (steer_gates.go)
//	       └→ visível em GET /runs/{id}; TTL varrido pelo sweeper de pendentes JÁ EXISTENTE
//
// # NADA AQUI É MECANISMO NOVO (decisão do dono (i), 2026-08-12)
//
// A opção de abrir um caminho próprio para esta decisão foi RECUSADA: daria uma decisão
// humana MAIS FRACA do que o four-eyes já entregue. Reutiliza-se, peça a peça, a maquinaria
// HITL de AOS-021 — [integration.PendingApprovals] para o registo durável,
// [runGate.EscalateToHuman] para a suspensão, o balde de suspensos e o registo de retoma do
// [NodeService] para a retomabilidade, e o varrimento de [NodeService.sweepApprovalsOnce]
// para o TTL. O molde deste adaptador é o [nodeEscalationSink], incluindo a ORDEM (registar
// antes de suspender) e o fail-closed.
//
// # O QUE O PROMPT APRESENTA — E O QUE NÃO APRESENTA
//
// `extend` (levantar o tecto) SAI desta entrega por decisão do dono ((iii), 2026-08-12): o
// [budget.Budget] não tem mutador de tecto e os LIMITES são, por desenho declarado,
// configuração FORA do log de eventos (packages/control-plane/budget/events.go) — não
// reconstruíveis por `Rebuild`. Levantar o tecto exigiria quebrar essa decisão de desenho,
// com evento próprio e ADR. O eixo está registado (ver exhaustion_decision.go).
//
// O prompt apresenta, por isso, SÓ o que TEM executor: as DUAS decisões que a rota assinada
// de AOS-263 parte 3 executa — `continue` (o run segue além do limiar) e `abort` (o run pára
// de forma durável). Ambas exigem a MESMA autoridade, e é essa simetria que faz do prompt uma
// pergunta a sério: a resposta ARRISCADA («continua a queimar orçamento») não pode ser mais
// barata de dar do que a segura. É também por isso que o prompt não se arma num nó onde a
// rota de decisão não está composta ([newExhaustionPrompt]): suspender um run para uma
// pergunta que ninguém consegue responder seria matá-lo com outro nome. A lista de opções é
// derivada da composição, não de um literal optimista.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// reasonExhaustionPrompt é o motivo gravado na transição running→waiting_on_human da
// suspensão por exaustão — rótulo de auditoria legível (nunca segredo), DISTINTO do da
// escalada de tool call para que o log atribua a causa certa a cada suspensão.
const reasonExhaustionPrompt = "budget_exhaustion_prompt"

// exhaustionResumeRoute é a rota de RE-HOSPEDAGEM de um run suspenso (AOS-021). NÃO é uma
// opção do prompt e deixou de ser apresentada como tal: é o que se faz DEPOIS de a decisão
// `continue` ter sido tomada e selada — a execução, não a decisão. Enquanto houver uma
// pergunta de exaustão por responder, esta rota RECUSA (ver [NodeService.Resume]).
const exhaustionResumeRoute = "POST /runs/{id}/resume"

var (
	// errExhaustionSuspended — SINAL INTERNO, não é uma avaria: o run foi SUSPENSO em
	// `waiting_on_human` por exaustão de orçamento e o loop tem de parar.
	//
	// PORQUE VIAJA COMO ERRO: a porta [agentruntime.ProgressObserver] devolve só `error` —
	// deliberadamente, porque AOS-262 não podia parar runs e um `(bool, error)` teria mentido
	// sobre quem manda. Esta entrega precisa de parar o run, e o caminho de paragem que a
	// porta oferece é o retorno de erro (o loop devolve imediatamente). O nó — que é quem
	// suspendeu e portanto quem sabe — RECONHECE este sentinela na saída do run
	// ([NodeService.absorveSuspensaoPorExaustao]) e converte-o na MESMA contabilidade de
	// suspensão de AOS-021, em vez de o tratar como falha. O kernel continua a cumprir o que
	// promete (um erro do observador aborta o turno); a REINTERPRETAÇÃO é toda node-local.
	errExhaustionSuspended = errors.New("aos: run SUSPENSO por exaustao de orcamento — aguarda decisao humana (AOS-263)")

	// ErrExhaustionPromptUnwired — pediu-se o prompt de exaustão sem uma das peças sem as
	// quais ele seria uma armadilha. Fail-closed na COMPOSIÇÃO (o nó não arranca com um prompt
	// meio-ligado), não em tempo de execução.
	ErrExhaustionPromptUnwired = errors.New("aos: prompt de exaustao (AOS-263) sem a maquinaria HITL completa")
)

// exhaustionPrompt é o adaptador que converte o aviso de burn-down de AOS-262 numa decisão
// humana durável. Seguro para uso concorrente: não tem estado mutável — o latch é o do
// [progresssurface.ProgressSurface] (uma vez por run) e a memória do que já foi perguntado é
// o REGISTO DURÁVEL de pendentes, não um mapa em memória.
type exhaustionPrompt struct {
	gates   *runStateGates
	pending *integration.PendingApprovals
	clock   func() time.Time
	log     func(format string, args ...any)
}

// newExhaustionPrompt compõe o prompt. Devolve nil — prompt DESARMADO, o nó continua a
// avisar como em AOS-262 e o banner declara-o — quando falta uma das peças sem as quais a
// pergunta seria uma armadilha em vez de uma decisão.
//
// As exigências, e porque cada uma é fatal para a utilidade do prompt:
//
//   - pending — sem registo durável o operador não tem O QUE decidir, e o run ficaria parado
//     sem ninguém saber porquê;
//   - resumeRecords — sem registo de retoma o run suspenso NÃO É RETOMÁVEL
//     ([NodeService.Resume] devolve [ErrResumeUnavailable]): mesmo um `continue` decidido não
//     teria como re-hospedar o run. Suspender aí seria matar o run com outro nome;
//   - steerAuth COM operadores registados e worm — são a ROTA DE DECISÃO (parte 3). Sem
//     autenticador com pubkey pinada não há quem possa responder, e sem WORM não há onde
//     selar a resposta (o selo é pré-condição do efeito). Um prompt armado sobre um nó assim
//     suspenderia runs para uma pergunta SEM VIA — e a única saída seria esperar pelo TTL.
//     É a mesma regra das duas anteriores («não se apresenta o que não tem executor»),
//     aplicada à decisão em vez de à opção;
//   - gates — sem [runStateGates] não há como suspender; um prompt que não pára o run é o
//     aviso de AOS-262 com mais passos. É a ÚNICA que dá ERRO em vez de desarmar: o nó compõe
//     sempre os gates de estado, pelo que um nil aqui é um defeito de cablagem e não uma
//     configuração — desarmar em silêncio esconderia um bug.
//
// `resumeRecords`, `steerAuth` e `worm` são VALIDADOS e não guardados: quem os usa é o
// [NodeService] na saída do run e o [apiHandler] na rota de decisão (já os têm). O que este
// construtor faz com eles é AMARRAR a composição — o prompt não se arma num nó onde o que ele
// apresenta não corre.
func newExhaustionPrompt(gates *runStateGates, pending *integration.PendingApprovals, resumeRecords *integration.ResumeRecords, steerAuth *integration.Ed25519Authenticator, worm audit.Store, log func(string, ...any)) (*exhaustionPrompt, error) {
	if pending == nil || resumeRecords == nil {
		return nil, nil // sem maquinaria HITL ⇒ prompt desarmado (AOS-262 puro), sem erro
	}
	if steerAuth == nil || steerAuth.EmitterCount() == 0 || worm == nil {
		return nil, nil // sem rota de decisão composta ⇒ prompt desarmado, pela mesma regra
	}
	if gates == nil {
		return nil, fmt.Errorf("%w: ha maquinaria HITL composta mas nao ha registo de gates de estado — nao haveria como suspender o run", ErrExhaustionPromptUnwired)
	}
	return &exhaustionPrompt{gates: gates, pending: pending, clock: time.Now, log: log}, nil
}

// exhaustionStepID é o passo do pendente de exaustão: o turno em que o limiar foi cruzado,
// mais a ÂNCORA DA OCORRÊNCIA (o instante em que a pergunta foi levantada, em nanossegundos).
//
// É a chave de idempotência (o âmbito de tipo entra por [integration.PendingKind]) e tem de
// ser ÚNICA POR OCORRÊNCIA, não por run e NÃO POR TURNO. O número do turno sozinho não chega,
// e a razão é concreta: o contador de turnos REINICIA em cada re-hospedagem do run (o loop
// conta a partir de 1 em cada incarnação) enquanto o ledger de burn-down é CUMULATIVO entre
// incarnações. Um run retomado volta a cruzar o limiar ao fim do turno 1 — e com uma chave
// só-turno o segundo prompt colidiria com o primeiro: o Event Store deduplicaria o registo em
// silêncio, o facto de retirada do primeiro continuaria a valer, e o run ficaria suspenso com
// uma pergunta que não aparece em `GET /runs/{id}`, que a rota de decisão devolve como 404 e
// que o varrimento já não expira. Um run sem via nenhuma.
//
// A âncora fecha isso, e é composta por duas partes com papéis diferentes:
//
//   - o INSTANTE (nanossegundos UTC) — é o que torna o passo diagnosticável: quem lê o registo
//     sabe QUANDO a pergunta foi levantada sem ter de cruzar com o log;
//   - um sufixo ALEATÓRIO de 64 bits ([exhaustionOccurrenceAnchor]) — é o que torna a chave
//     REALMENTE única. O instante sozinho não chega e isso é medido, não teórico: o relógio de
//     parede de um Windows típico tem granularidade de ~0,5 a 15 ms, e duas travessias do
//     limiar separadas por uma expiração e uma retoma cabem folgadamente no MESMO tick. Uma
//     chave que depende da resolução do relógio é uma chave que colide na máquina errada.
//
// E se alguma vez colidir mesmo assim, a colisão deixou de ser silenciosa —
// [integration.PendingApprovals.Put] recusa-a fail-closed
// ([integration.ErrExhaustionPromptColide]).
//
// O prefixo torna o registo inconfundível na lista, e `chat#<turno>` é o mesmo identificador
// de passo que o [nodeProgressReflector] reflecte.
func exhaustionStepID(turn int, at time.Time, anchor string) string {
	return fmt.Sprintf("exhaustion@chat#%d@%d-%s", turn, at.UTC().UnixNano(), anchor)
}

// exhaustionOccurrenceAnchor devolve o sufixo aleatório da chave de ocorrência (CSPRNG, 64
// bits em hex). NÃO é material sensível — é um desambiguador público que viaja no wire com a
// pergunta —, mas vem do CSPRNG e não de um contador porque o nó não tem, neste ponto, um
// contador durável de perguntas por run que sobreviva a restart.
//
// FAIL-CLOSED: um CSPRNG indisponível é erro, nunca um fallback previsível. A alternativa
// (cair para o instante sozinho) reintroduziria exactamente a colisão que esta âncora fecha,
// e fá-lo-ia em silêncio.
func exhaustionOccurrenceAnchor() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("aos: gerar ancora de ocorrencia do prompt de exaustao (AOS-263): %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// raise é o que corre quando o aviso de burn-down dispara. Devolve:
//
//   - nil — nada a fazer (prompt desarmado, sem aviso, ou já há um prompt por responder);
//   - [errExhaustionSuspended] embrulhado — o run FOI suspenso e o loop tem de parar;
//   - qualquer outro erro — a suspensão FALHOU (ver o bloco seguinte).
//
// # SE A SUSPENSÃO FALHAR (o aviso de AOS-262 é FAIL-OPEN e uma-vez-por-run)
//
// O sinal que alimenta este adaptador tem um LATCH: a superfície de burn-down marca
// `SpanEmitted` UMA VEZ por run e por incarnação do processo. Se uma falha aqui fosse
// engolida — registada e seguida em frente, que é a postura de um aviso — o run NUNCA MAIS
// seria perguntado: correria até ao tecto, ao MaxTurns ou ao disjuntor com o operador
// convencido de que existe um prompt à espera dele. É o fail-open exacto que esta entrega
// vem fechar, e por isso NADA aqui é best-effort: qualquer falha SOBE e o run aborta como
// FALHADO (o loop devolve o erro; [runGate.sealTerminal] materializa `failed` porque a
// máquina ainda está em `running`). Um run morto e visível é estritamente melhor do que um
// run vivo que ninguém sabe que devia ter parado.
//
// A ORDEM é a do [nodeEscalationSink] e pela mesma razão: REGISTAR o pendente ANTES de
// suspender. Se a suspensão falhar depois do registo, fica um pendente sem run vivo — inócuo
// (não destrava nada) e varrido pelo TTL. A ordem inversa deixaria o run suspenso sem nada
// que dissesse ao operador o que decidir, que é o pior dos dois estados.
//
// # A RAZÃO É UM PARÂMETRO, E É PARÂMETRO DE PROPÓSITO
//
// `razao` é a explicação de quem levantou a pergunta, nas palavras dele: o AVISO de burn-down
// (`limiar cruzado`, [runProgress.razaoDoAviso]) e o TECTO ATINGIDO (a razão atribuível que a
// admissão do turno de modelo produziu, [nodeModelAdmission.evaluationFor]) são situações
// DIFERENTES e a pergunta humana tem de as distinguir. Antes disto a razão vivia só no log do
// processo — que NÃO é o canal que a decisão assinada lê: quando era a dimensão $ a bloquear, o
// operador via uma pergunta que só falava de tokens, respondia `continue` por ver headroom de
// tokens de sobra, e o run era re-hospedado para ser imediatamente re-negado pelo MESMO tecto.
// Um ciclo de decisões humanas sobre a grandeza errada.
//
// Não vem dentro da [progresssurface.RunEvaluation] porque essa é a face de uma porta do plano
// de controlo e a razão aqui é NODE-LOCAL (fala de envs e de rotas deste nó).
func (e *exhaustionPrompt) raise(ctx context.Context, runID string, ev progresssurface.RunEvaluation, razao string) error {
	if e == nil || ev.Warning == nil || !ev.Warning.SpanEmitted {
		return nil
	}
	// JÁ PERGUNTADO? A memória é o registo DURÁVEL, não um mapa em memória: sobrevive a
	// restart e é a mesma verdade que o operador vê em GET /runs/{id}.
	//
	// DEFESA EM PROFUNDIDADE, e não a linha da frente: desde que a retoma passou a RECUSAR
	// enquanto a pergunta está por responder ([NodeService.Resume]), um run não volta a correr
	// com um prompt vivo — pelo que a incarnação seguinte só cruza o limiar depois de a
	// pergunta ter saído da lista (por decisão ou por TTL) e levanta uma pergunta NOVA, com
	// chave de ocorrência própria. Esta consulta cobre o que restar: uma réplica que não
	// conduziu a retoma, um pendente escrito por outro caminho, uma corrida.
	//
	// FAIL-CLOSED: uma leitura que falha NÃO se assume como "não há" — assumir isso levantaria
	// um segundo prompt sobre o primeiro.
	jaPerguntado, err := e.pendenteVivo(ctx, runID)
	if err != nil {
		return fmt.Errorf("aos: ler pendentes de exaustao do run %q (AOS-263): %w", runID, err)
	}
	if jaPerguntado {
		e.logf("prompt de exaustao (AOS-263) do run %q: JA existe um por responder — o run NAO volta a suspender-se nem se levanta uma segunda pergunta sobre a primeira; a que esta em GET /runs/{id} continua a ser a que decide, ate ser respondida ou expirar pelo TTL de pendentes", runID)
		return nil
	}

	// UMA leitura do relógio para as duas amarras temporais: a âncora de OCORRÊNCIA que
	// desambigua a chave e a âncora do TTL. Duas leituras dariam dois instantes e o registo
	// diria de si próprio duas coisas ligeiramente diferentes.
	agora := e.clock().UTC()
	ancora, err := exhaustionOccurrenceAnchor()
	if err != nil {
		return err
	}
	rec := integration.PendingRecord{
		Kind:   integration.PendingKindExhaustion,
		RunID:  runID,
		StepID: exhaustionStepID(ev.Warning.Turn, agora, ancora),
		Turn:   ev.Warning.Turn,
		// A AMARRA do prompt (o que substitui a preview no tipo que não tem efeito exibido
		// para assinar): limiar + montante consumido + tecto. Os três valores vêm do aviso de
		// AOS-262 TAL-QUAL — não se recalcula consumo nenhum aqui, senão haveria duas
		// contabilidades a poder divergir e o operador decidiria sobre a errada.
		Threshold:      ev.Warning.Threshold,
		ConsumedTokens: ev.Burndown.Consumed.Tokens,
		LimitTokens:    ev.Burndown.Limit.Tokens,
		// A dimensão $, quando tem tecto. Zero ⇒ dólares são medidos mas não decidem, e o
		// registo cala-os em vez de escrever um denominador que ninguém configurou (o wire
		// omite-os por `omitempty`, e a superfície de leitura não passa a ter um campo a zero
		// que pareça «consumo nenhum»).
		ConsumedCostMicroUSD: ev.Burndown.Consumed.CostMicroUSD,
		LimitCostMicroUSD:    ev.Burndown.Limit.CostMicroUSD,
		// A RAZÃO — a grandeza que levou à pergunta, escrita por quem a levantou.
		Reason: razao,
		// Âncora do TTL: é daqui que o varrimento de pendentes JÁ EXISTENTE sabe que a
		// pergunta envelheceu. Sem ela o prompt nunca expiraria sozinho (fail-safe).
		CreatedAt: agora.Format(time.RFC3339Nano),
	}
	// Coerência do registo: sem tecto em $ a dimensão não se reporta de todo — um consumido
	// sem denominador leria-se como «gastou X de nada».
	if rec.LimitCostMicroUSD <= 0 {
		rec.ConsumedCostMicroUSD = 0
		rec.LimitCostMicroUSD = 0
	}
	if err := e.pending.Put(ctx, rec); err != nil {
		return fmt.Errorf("aos: registar prompt de exaustao do run %q (AOS-263): %w", runID, err)
	}

	gate := e.gates.resolveGate(runID)
	if gate == nil {
		return fmt.Errorf("%w (prompt de exaustao AOS-263, run %q)", ErrNoStateGateForRun, runID)
	}
	// SUSPENSÃO PELO runGate EXISTENTE — a MESMA aresta running→waiting_on_human da escalada
	// de AOS-021, na MESMA state.Machine. É esta transição que REPÕE o `enteredAt` da máquina
	// (state.doTransition carimba-o em cada transição confirmada): a partir daqui o tempo que
	// corre é tempo em `waiting_on_human`, onde o deadline de AOS-252 — que só conta
	// wall-clock em `running` — não morde. A deliberação humana não pode matar o run pelo
	// relógio, e a garantia é ESTRUTURAL (a transição durável), não uma subtracção de tempo à
	// mão.
	if err := gate.EscalateToHuman(ctx, reasonExhaustionPrompt); err != nil {
		return fmt.Errorf("aos: suspender run %q para o prompt de exaustao (AOS-263): %w", runID, err)
	}
	e.logf("PROMPT DE EXAUSTAO (AOS-263) — run %q SUSPENSO em waiting_on_human no turno %d (passo %q): %s. A pergunta esta em GET /runs/{id} (pending_exhaustion), com esta MESMA razao no campo reason, e as DUAS opcoes com executor sao %s e %s, ambas assinadas por operador pinado em %s; enquanto a pergunta estiver por responder o POST /runs/{id}/resume RECUSA (a retoma nao e via de fuga a decisao). NAO se oferece extend — o tecto nao tem mutador (eixo DEF-220). Sem decisao, o TTL de pendentes expira a pergunta e o run volta a ser RETOMAVEL",
		runID, ev.Warning.Turn, rec.StepID, rec.Reason,
		exhaustionOptionContinue, exhaustionOptionAbort, exhaustionDecisionRoute)
	return fmt.Errorf("%w: run %q turno %d (%s)", errExhaustionSuspended, runID, ev.Warning.Turn, rec.Reason)
}

// pendenteVivo indica se o run já tem um prompt de exaustão por responder. Lê a MESMA
// listagem que a superfície de administração expõe — [integration.PendingApprovals.ListForRun]
// já exclui os expirados —, pelo que o que o nó considera "perguntado" é exactamente o que o
// operador vê por decidir. Corre no máximo uma vez por run e por incarnação (o latch do aviso
// governa a entrada), não uma vez por turno.
func (e *exhaustionPrompt) pendenteVivo(ctx context.Context, runID string) (bool, error) {
	recs, err := e.pending.ListForRun(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, r := range recs {
		if r.Kind.Resolved() == integration.PendingKindExhaustion {
			return true, nil
		}
	}
	return false, nil
}

// logf escreve no log do nó quando há writer (nil-safe, molde de [runProgress.logf]).
func (e *exhaustionPrompt) logf(format string, args ...any) {
	if e == nil || e.log == nil {
		return
	}
	e.log(format, args...)
}

// ---------------------------------------------------------------------------
// A saída do run — converter o sinal em SUSPENSÃO, não em falha
// ---------------------------------------------------------------------------

// absorveSuspensaoPorExaustao reconhece, na saída do [NodeService.hostRun], o sinal de
// suspensão por exaustão e trata-o com a MESMA contabilidade da escalada de AOS-021:
// persiste o registo de RETOMA e devolve suspenso=true, para que o run vá para o balde de
// suspensos (largando lease e heartbeat — não se seguram recursos durante minutos de latência
// humana) em vez de ser arquivado como falhado.
//
// Devolve (suspenso, erro a reter). Para um erro que NÃO é este sinal é um no-op que devolve
// o erro tal-qual.
//
// FAIL-CLOSED, como em AOS-021: se o registo de retoma não persistir, o run NÃO é dado por
// suspenso — fica FALHADO. Um "suspenso" que não se consegue retomar é pior do que uma falha
// visível: ficaria à espera de uma decisão que nunca teria efeito.
//
// O estado DURÁVEL já diz `waiting_on_human` (a transição aconteceu antes do loop parar),
// pelo que [runGate.sealTerminal] é no-op neste caminho e o log não é reescrito.
func (s *NodeService) absorveSuspensaoPorExaustao(ctx context.Context, goal agentruntime.Goal, runErr error) (bool, error) {
	if !errors.Is(runErr, errExhaustionSuspended) {
		return false, runErr
	}
	if s.node == nil || s.node.ResumeRecords == nil {
		// Inalcançável com o prompt composto ([newExhaustionPrompt] exige o registo de
		// retoma); mantém-se como guarda local para o invariante ser auditável no ponto de uso.
		return false, fmt.Errorf("aos: run %q suspenso por exaustao mas o no nao tem registo de retoma composto — tratado como FALHADO: %w", goal.RunID, runErr)
	}
	if perr := s.node.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); perr != nil {
		return false, fmt.Errorf("aos: persistir registo de retoma do run %q suspenso por exaustao (AOS-263): %w", goal.RunID, perr)
	}
	s.log("run %q SUSPENSO por exaustao de orcamento (AOS-263) — largado o lease e o heartbeat; a pergunta esta em GET /runs/{id} e responde-se em %s (%s ou %s, assinado por operador pinado). Depois de um %s decidido, a re-hospedagem e %s com credencial fresca",
		goal.RunID, exhaustionDecisionRoute, exhaustionOptionContinue, exhaustionOptionAbort,
		exhaustionOptionContinue, exhaustionResumeRoute)
	return true, nil
}
