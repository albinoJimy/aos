package main

// CRASH-RESUME POR VARREDURA DE ARRANQUE (AOS-253, achados F9+F13).
//
// O PROBLEMA que este ficheiro fecha: o [durable.Resumer] (AOS-015) NUNCA era composto. Os
// checkpoints intra-iteração eram ESCRITOS a cada turno (EventStoreCheckpointer, ligado no
// Bootstrap) mas NUNCA LIDOS — [worker.Assigner.TryAcquire] só corria no submit de um run NOVO e
// não havia varredura de arranque nenhuma. Consequência: um crash a meio de um run NÃO era
// retomado por ninguém; a única "recuperação" era re-submeter, e re-submeter RECOMEÇA do turno 1
// (perdendo a trajectória e arriscando repetir efeitos já aplicados).
//
// O QUE ESTA VARREDURA FAZ, e com que peças EXISTENTES (nada aqui é reinventado):
//
//   1. ENUMERA os streams do Event Store e reconstrói o estado DURÁVEL de cada um pela MESMA
//      máquina de estados de AOS-017 ([runStateGates.currentState]). Só um estado `running` sem
//      desfecho terminal é o rasto de um crash — é a metade negativa que AOS-252 (estados
//      terminais duráveis) tornou distinguível: um run que terminou sela complete/failed/killed/
//      timed_out; um que crashou fica em `running` para sempre ("claim → … → NADA"). Ready,
//      suspenso (waiting_on_human, AOS-021), pausado (steer) e os terminais NÃO são órfãos de
//      crash e são deixados em paz.
//   2. RECLAMA a posse pela MESMA maquinaria de lease — [worker.Assigner.TryAcquire], através de
//      [NodeService.submit]. Um run cujo lease ainda é detido VIVO por outra réplica devolve
//      [ErrRunLeaseHeldElsewhere] e é SALTADO (sem roubo de partição — AOS-018/AC3).
//   3. RECONSTRÓI O CURSOR com o [durable.Resumer] (a peça que nunca fora composta): relê os
//      checkpoints e devolve onde o run parou. Aqui é usado para DECLARAR o progresso reconstruído
//      (turno/fromScratch) — a fronteira concreta de retoma é reproduzida pelo plano de replay
//      abaixo, a MESMA mecânica de AOS-021.
//   4. RETOMA SEM RE-EXECUTAR EFEITOS reutilizando o caminho replay-then-continue de AOS-021: o
//      [NodeService.replayPlanFor] carrega as respostas do modelo já capturadas e [submit] re-
//      hospeda com o plano no ctx. Na re-hospedagem o [hostRun] chama RebuildLedger (AOS-180) e o
//      loop reproduz os turnos capturados — os efeitos já aplicados batem no already-applied do
//      step-ledger e NÃO voltam a correr (o already-applied PRECEDE a mediação, ver
//      activity/dispatch.go), e o modelo NÃO é re-interrogado nesses turnos (o cliente de retoma
//      devolve a resposta registada).
//
// FAIL-CLOSED por passo: um estado, cursor ou captura que não se conseguem LER NÃO admitem a
// retoma às cegas — o run é deixado como órfão e a razão é DECLARADA no log. Um run em `running`
// sem registo de retoma (o Goal não é reconstituível) também não se retoma — é exactamente o caso
// de um "crash simulado" que reclamou a máquina de estados sem nunca ter sido hospedado.
//
// ALCANCE HONESTO da retoma automática (declarado no banner): não há CREDENCIAL FRESCA — um crash
// não tem um humano no lacete como a retoma de AOS-021 tem. Os turnos JÁ CAPTURADOS não precisam
// dela (o already-applied precede a mediação, pelo que nenhuma tool call replayed é re-mediada),
// mas uma continuação AO VIVO que exija identidade de modelo (AOS-278, cutover duro) é NEGADA
// ATRIBUIVELMENTE — sem principal forjado. É a postura correcta: uma retoma que completa o que já
// estava capturado, e que fecha fail-closed onde precisaria de uma identidade que já expirou.

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// ResumeInterruptedRuns varre o substrato no ARRANQUE por runs interrompidos a meio por um crash
// (estado `running` sem desfecho terminal, lease reclamável) e retoma-os pela cadeia REAL, sem
// re-executar efeitos. Devolve quantos órfãos foram vistos e quantos foram efectivamente
// retomados. Só devolve erro numa falha de COMPOSIÇÃO do varredor (fail-closed no arranque); as
// falhas POR-RUN são declaradas no log e saltadas (nunca se retoma às cegas).
//
// Deve correr ANTES de o nó aceitar submissões novas (o [main] chama-o entre [NewNodeService] e
// [APIServer.Serve]). É idempotente e seguro de re-correr: um run já re-hospedado por esta réplica
// devolve [ErrRunAlreadyInProgress] e é saltado.
func (s *NodeService) ResumeInterruptedRuns(ctx context.Context) (scanned, resumed int, err error) {
	return s.resumeInterruptedRuns(ctx, true)
}

// resumeInterruptedRuns é o varredor. `anuncia` distingue a passagem de ARRANQUE — que declara
// sempre a postura, ligada ou desligada, porque postura anunciada = postura ligada — da
// RE-VARREDURA periódica de [StartOrphanSweeper], que só fala quando encontrou alguma coisa. Sem
// essa distinção, a re-varredura escreveria o banner completo a cada ciclo e afogaria no ruído
// exactamente o sinal que ela existe para dar.
func (s *NodeService) resumeInterruptedRuns(ctx context.Context, anuncia bool) (scanned, resumed int, err error) {
	// SUBSTRATO MÍNIMO para distinguir órfão de terminado (AOS-252) e para o reconstituir
	// (AOS-021). Sem qualquer uma destas peças a varredura não teria como decidir com verdade —
	// e decidir sem verdade seria retomar às cegas. Declara-se DESLIGADA em vez de calar.
	if s.node == nil || s.node.EventStore == nil || s.node.stateGates == nil || s.node.ResumeRecords == nil {
		if anuncia {
			s.log("%s", crashResumeDisabledBanner())
		}
		return 0, 0, nil
	}
	// O [durable.Resumer] — a peça de AOS-015 que nunca fora composta. O default de step-identity
	// ("step-" + 6 dígitos) coincide com o do loop (sequentialStepIdentity), pelo que a verificação
	// de acoplamento do Resumer não dispara; um formato incompatível fail-closaria por-run abaixo.
	resumer, rerr := durable.NewResumer(s.node.EventStore)
	if rerr != nil {
		return 0, 0, fmt.Errorf("aos: compor o Resumer de crash-resume (AOS-253): %w", rerr)
	}

	// AOS-352 — DEGRADAÇÃO DECLARADA, e a escolha é diferente da de `governance_restore`
	// de propósito. Aqui o dano de falhar o arranque é maior do que o de não retomar: um
	// run órfão fica órfão até ao arranque seguinte, e o operador vê-o; um nó que não sobe
	// não retoma nada nem serve nada. Mas «zero streams» não pode continuar a ser a forma
	// como isto se sabe — antes, uma falha de enumeração produzia «0 retomados» com a
	// mesma cara de um arranque limpo, e a única diferença estava num erro que ninguém
	// devolvia. Passa a ser dito em voz alta, e devolvido como erro do varredor.
	streams, serr := s.node.EventStore.Streams()
	if serr != nil {
		s.log("crash-resume: NAO foi possivel enumerar os streams do Event Store (%v) — "+
			"NENHUM run orfao foi procurado nesta passagem. Isto NAO e 'nao havia orfaos': "+
			"e 'nao se chegou a perguntar'. Os runs orfaos continuam orfaos ate o substrato "+
			"responder e o no reiniciar", serr)
		return 0, 0, fmt.Errorf("aos: crash-resume: enumerar streams do Event Store: %w", serr)
	}
	var heldElsewhere, failed int
	for _, id := range streams {
		runID := id
		// (1) Estado DURÁVEL do stream. Um stream que não é de run (lease:, gov.approvals, …) não
		// tem transições e reconstrói para `ready` — é saltado sem ruído. FAIL-CLOSED: um estado
		// ilegível NÃO se retoma às cegas.
		st, serr := s.node.stateGates.currentState(ctx, runID)
		if serr != nil {
			failed++
			s.log("crash-resume: estado do stream %q ILEGIVEL — NAO retomado (fail-closed): %v", runID, serr)
			continue
		}
		if st != state.Running {
			continue // ready / terminal / suspenso / pausado — não é órfão de crash
		}
		scanned++

		// (2) CURSOR reconstruído pelos checkpoints (o Resumer que passa a ser LIDO no arranque).
		// Fail-closed: sem cursor não se retoma.
		rp, cerr := resumer.Resume(ctx, runID)
		if cerr != nil {
			failed++
			s.log("crash-resume: reconstrucao do cursor do run %q FALHOU — NAO retomado (fail-closed): %v", runID, cerr)
			continue
		}

		// (3) Goal reconstituível? Sem registo de retoma o run não é re-hospedável (não há Goal em
		// lado nenhum do log). É o caso do crash reclamado-mas-nunca-hospedado — deixado órfão.
		rec, ok, gerr := s.node.ResumeRecords.Get(ctx, runID)
		if gerr != nil {
			failed++
			s.log("crash-resume: registo de retoma do run %q ILEGIVEL — NAO retomado (fail-closed): %v", runID, gerr)
			continue
		}
		if !ok {
			s.log("crash-resume: run %q em `running` SEM registo de retoma — nao reconstituivel, deixado como orfao (nao ha Goal para re-hospedar)", runID)
			continue
		}

		// (4) Plano de replay das capturas — a MESMA mecânica de dedup de AOS-021. Fail-closed:
		// capturas ilegíveis (ex.: titular apagado por crypto-shredding) não se retomam.
		plan, perr := s.replayPlanFor(ctx, runID, rec.Principal.NHIID)
		if perr != nil {
			failed++
			s.log("crash-resume: capturas do run %q ILEGIVEIS — NAO retomado (fail-closed): %v", runID, perr)
			continue
		}

		// (5) RE-HOSPEDA pela MESMA cadeia de submissão (resuming=true). A TryAcquire lá dentro
		// SALTA sem roubo se outra réplica detiver o lease vivo. A credencial vai VAZIA (um crash
		// não tem humano): os turnos capturados reproduzem sem re-mediar (already-applied precede a
		// mediação) e a continuação ao vivo que exija identidade de modelo é negada atribuivelmente.
		goal := rec.GoalWith("")
		herr := s.submit(withReplayPlan(ctx, plan), goal, true)
		switch {
		case herr == nil:
			resumed++
			s.log("crash-resume: run %q RETOMADO pela varredura de arranque — cursor: proximo_turno=%d fromScratch=%v; %d turno(s) reproduzidos das capturas (efeitos ja aplicados deduplicam no step-ledger, sem re-execucao; modelo nao re-interrogado nesses turnos)", runID, rp.NextTurn, rp.FromScratch, len(plan))
		case errors.Is(herr, ErrRunLeaseHeldElsewhere):
			heldElsewhere++
			s.log("crash-resume: run %q em `running` mas com LEASE VIVO noutra replica — saltado (sem roubo de particao)", runID)
		case errors.Is(herr, ErrRunAlreadyInProgress), errors.Is(herr, ErrRunAlreadyCompleted), errors.Is(herr, ErrRunSuspended):
			s.log("crash-resume: run %q ja tratado por esta replica entretanto — saltado: %v", runID, herr)
		default:
			failed++
			s.log("crash-resume: re-hospedagem do run %q FALHOU: %v", runID, herr)
		}
	}

	// A re-varredura periódica só declara quando há o que declarar. O arranque declara sempre.
	if anuncia || scanned > 0 {
		s.log("%s", crashResumeBanner(len(streams), scanned, resumed, heldElsewhere, failed))
	}
	return scanned, resumed, nil
}

// crashResumeBanner declara o RESULTADO da varredura (AC4 de AOS-253) — postura anunciada =
// postura ligada (AOS-203/AOS-248). É uma função PURA (estado → linha) para os testes cobrirem
// cada desfecho sem levantar um nó, como as restantes funções de banner.
func crashResumeBanner(streams, scanned, resumed, heldElsewhere, failed int) string {
	return fmt.Sprintf("crash-resume / varredura de arranque (AOS-253): CORREU sobre %d stream(s) — %d run(s) orfaos em `running` (claim sem desfecho terminal, o rasto de um crash a meio que AOS-252 tornou distinguivel), %d RETOMADO(s) pela cadeia real (submit->hostRun->RebuildLedger + replay-then-continue de AOS-021: turnos capturados reproduzidos, efeitos ja aplicados DEDUPLICADOS pelo step-ledger sem re-execucao, modelo NAO re-interrogado nesses turnos), %d saltado(s) por LEASE VIVO noutra replica (sem roubo de particao) e %d nao retomado(s) FAIL-CLOSED (estado/cursor/capturas ilegiveis, ou `running` sem registo de retoma). Os checkpoints do Resumer (AOS-015) passam a ser LIDOS no arranque — antes eram escritos e nunca consultados. ALCANCE HONESTO: a retoma automatica NAO traz credencial fresca (um crash nao tem humano no lacete); os turnos ja capturados nao precisam dela (already-applied precede a mediacao), mas uma continuacao AO VIVO que exija identidade de modelo (AOS-278) e negada atribuivelmente — sem principal forjado", streams, scanned, resumed, heldElsewhere, failed)
}

// crashResumeDisabledBanner declara a varredura DESLIGADA e a RAZÃO — sem o substrato para
// distinguir um órfão de um terminado (máquina de estados de AOS-252) ou para o reconstituir
// (registo de retoma de AOS-021), um crash a meio de um run não é retomado por ninguém.
func crashResumeDisabledBanner() string {
	return "crash-resume / varredura de arranque (AOS-253): DESLIGADA — falta o substrato para distinguir um orfao de um terminado ou para o reconstituir (Event Store, maquina de estados duravel de AOS-252 e/ou registo de retoma de AOS-021 nao compostos). Sem eles, um crash a meio de um run NAO e retomado por ninguem e re-submeter recomecaria do turno 1"
}
