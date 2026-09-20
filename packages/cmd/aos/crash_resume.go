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
//   0. EXCLUI OS RUNS COM DONO VIVO (AOS-411), ANTES de reconstituir o que quer que seja. Um run
//      HOSPEDADO por esta réplica (está em [NodeService.runs]) ou com LEASE AINDA VÁLIDO noutra
//      réplica NÃO é um órfão: é um run a correr. Até AOS-411 a exclusão existia — mas só no
//      passo 5, dentro do `submit` —, e o preço dessa ordem foi pago em produção (v0.1.22,
//      2026-09-18): a cada ciclo da re-varredura, cada run VIVO era classificado como órfão,
//      tinha o cursor reconstruído, o registo de retoma lido e as capturas DECIFRADAS SOB A
//      CHAVE DO TITULAR — e um run vivo antes do 1.º turno capturado saía como «capturas
//      ILEGIVEIS — NAO retomado (fail-closed)», contado como FALHA, com o banner a anunciar
//      «1 run órfão» sobre um nó que nunca tinha reiniciado. Nenhum dano (o passo 5 segurava a
//      não-duplicação), mas um SINAL FALSO de crash, trabalho inútil e um acesso a PII sem
//      razão. A guarda passa para o princípio; o `submit` fica como defesa em profundidade.
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
//
// AOS-411: o mesmo `anuncia` passou a NOMEAR a passagem no banner. Antes era só um interruptor
// de silêncio, e a passagem periódica — quando falava — apresentava-se como «varredura de
// arranque» num nó que nunca tinha reiniciado. O dado existia; não estava a ser usado.
//
// `scanned` conta ÓRFÃOS VERDADEIROS: `running` SEM dono vivo. Runs hospedados por esta réplica
// ou com lease vivo noutra são excluídos ANTES de qualquer leitura e não entram na conta.
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
		// AOS-352 — DEGRADAÇÃO DECLARADA, e NÃO um erro. A direcção é deliberadamente
		// diferente da de `governance_restore`, e a razão é a assimetria do dano: um run
		// órfão fica órfão até ao arranque seguinte e o operador vê-o no log; um nó que
		// não sobe não retoma nada NEM SERVE NADA. Sobre `AOS_EVENTSTORE_NATS`, um socket
		// que caia entre `jetstream.Abrir` e este varredor faria o processo sair — e o
		// supervisor reinicia, o cluster continua lento, e sai outra vez: crash-loop por
		// indisponibilidade TRANSITÓRIA do substrato.
		//
		// O que muda face ao defeito de AOS-352 é o SINAL, não o desfecho. Antes, uma
		// enumeração falhada produzia «0 órfãos retomados» com a mesma cara de um arranque
		// limpo, e a única diferença estava num erro que ninguém devolvia. Agora é dito em
		// voz alta, e o contrato de [ResumeInterruptedRuns] — «só devolve erro numa falha
		// de COMPOSIÇÃO do varredor» — mantém-se verdadeiro.
		s.log("crash-resume: NAO foi possivel enumerar os streams do Event Store (%v) — "+
			"NENHUM run orfao foi procurado nesta passagem. Isto NAO e 'nao havia orfaos': "+
			"e 'nao se chegou a perguntar'. O arranque CONTINUA (um no que nao sobe nao "+
			"retoma nada nem serve nada); os runs orfaos, se existirem, continuam orfaos "+
			"ate o substrato responder e alguem reiniciar o no", serr)
		return 0, 0, nil
	}
	var heldElsewhere, failed, vivosAqui, vivosNoutra int
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

		// (1-bis) TEM DONO VIVO? — AOS-411. `running` é o estado durável de um run que crashou E
		// o de um run que está a correr NESTE INSTANTE; a máquina de estados não os distingue,
		// e é por isso que a pergunta pelo DONO tem de ser feita AQUI, e não no fim.
		//
		// Silenciosamente: um run vivo não é um acontecimento. Uma linha por run vivo por ciclo
		// afogaria o sinal que a re-varredura existe para dar — os números vão ao resumo, que
		// num ciclo periódico sem órfãos continua a não ser escrito.
		if s.hospedadoNestaReplica(runID) {
			vivosAqui++
			continue
		}
		vivo, lerr := s.leaseAindaVivo(ctx, runID)
		if lerr != nil {
			// FAIL-CLOSED, e pela razão de sempre: sem saber se o run tem dono, retomá-lo seria
			// retomar às cegas — exactamente o que o passo 5 recusa quando o `Claim` falha.
			// Antes de AOS-411 este erro aparecia mais tarde e depois de decifrar as capturas.
			failed++
			s.log("crash-resume: lease do run %q ILEGIVEL — NAO retomado (fail-closed, AOS-411): %v", runID, lerr)
			continue
		}
		if vivo {
			vivosNoutra++
			continue
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
	//
	// AOS-411: `scanned` conta agora ÓRFÃOS VERDADEIROS (sem dono vivo), e é por isso que este
	// mesmo `if` passa a calar o ciclo periódico que só encontrou runs a correr — que era o caso
	// observado em produção. Runs vivos saltados NÃO abrem a boca do varredor.
	if anuncia || scanned > 0 {
		s.log("%s", crashResumeBannerDaPassagem(anuncia, resumoVarredura{
			streams:       len(streams),
			orfaos:        scanned,
			retomados:     resumed,
			vivosAqui:     vivosAqui,
			vivosNoutra:   vivosNoutra,
			heldElsewhere: heldElsewhere,
			failed:        failed,
		}))
	}
	return scanned, resumed, nil
}

// hospedadoNestaReplica diz se o run está no registo de em-curso DESTE processo — a mesma
// verdade que o passo (2) do [NodeService.submit] consulta para devolver
// [ErrRunAlreadyInProgress], lida sob o MESMO mutex. Um run aqui dentro é, por definição, um run
// que esta réplica está a correr: não é órfão de coisa nenhuma.
//
// A janela entre esta leitura e o `submit` lá em baixo continua a existir (um run pode ser
// submetido no meio da varredura) e continua fechada por quem sempre a fechou — a reserva sob
// mutex do `submit`. Esta guarda não a substitui; retira-lhe é o trabalho e o acesso a PII de
// chegar até lá.
func (s *NodeService) hospedadoNestaReplica(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[runID]
	return ok
}

// leaseAindaVivo diz se o run tem um lease por expirar no relógio da MESMA autoridade de lease
// que o `submit` usa. É exactamente o predicado que faz [durable.LeaseManager.Claim] devolver
// ErrLeaseHeld (`agora < expira`) e, com ele, o `submit` devolver [ErrRunLeaseHeldElsewhere] —
// lido aqui SEM mintar, renovar ou mutar nada ([durable.LeaseManager.CurrentLeaseExpired] é
// declaradamente inerte). Um lease EXPIRADO, ou a ausência de lease, deixa o run seguir para a
// retoma como antes: a re-varredura de A4 continua a apanhar o órfão cujo lease morreu, e nunca
// reclama um lease mais cedo do que reclamava.
//
// Sem autoridade de lease composta a pergunta não se faz e o run segue o caminho antigo — o
// `submit` continua lá. Na prática [NewNodeService] compõe-a sempre.
func (s *NodeService) leaseAindaVivo(ctx context.Context, runID string) (bool, error) {
	if s.leases == nil {
		return false, nil
	}
	expirado, existe, err := s.leases.CurrentLeaseExpired(ctx, runID)
	if err != nil {
		return false, err
	}
	return existe && !expirado, nil
}

// resumoVarredura são os números de UMA passagem do varredor. Estrutura, e não sete inteiros
// posicionais, porque AOS-411 acrescentou dois e a próxima leitura errada de um banner de sete
// argumentos seria a que ninguém detectaria.
type resumoVarredura struct {
	streams int
	// orfaos são os órfãos VERDADEIROS — `running` e SEM dono vivo. É o número que o operador
	// lê como «houve um crash»; antes de AOS-411 incluía runs a correr.
	orfaos    int
	retomados int
	// vivosAqui / vivosNoutra — runs saltados ANTES de qualquer leitura (AOS-411).
	vivosAqui   int
	vivosNoutra int
	// heldElsewhere é o que a DEFESA EM PROFUNDIDADE do `submit` ainda apanhou: um lease que
	// ficou vivo entre a guarda (1-bis) e o passo 5. Esperado zero em regime normal — se não
	// for, é sinal de corrida real, e por isso continua a ser contado à parte.
	heldElsewhere int
	failed        int
}

// crashResumeBannerDaPassagem declara a passagem inteira e, antes de mais, a sua ORIGEM
// (AOS-411/AC4): a re-varredura periódica anunciava-se como «varredura de arranque» e mandava
// um operador procurar um restart que não tinha havido. A forma da linha de ARRANQUE é
// INTACTA — é a pegada que o roteiro E2E procura —; a periódica troca só o nome da passagem.
func crashResumeBannerDaPassagem(arranque bool, r resumoVarredura) string {
	vivos := fmt.Sprintf(" VIVOS SALTADOS (AOS-411): %d hospedado(s) por ESTA replica e %d com LEASE VIVO noutra replica — nao sao orfaos, nao contam como falha e NAO se lhes leu cursor, registo de retoma nem capturas por-titular (um run em `running` tanto e o rasto de um crash como um run a correr neste instante; a pergunta pelo dono passou a ser a PRIMEIRA, e nao a ultima)", r.vivosAqui, r.vivosNoutra)
	if arranque {
		return crashResumeBanner(r.streams, r.orfaos, r.retomados, r.heldElsewhere, r.failed) + "." + vivos
	}
	return "crash-resume / RE-VARREDURA periodica (AOS-253/A4): " +
		crashResumeNucleo(r.streams, r.orfaos, r.retomados, r.heldElsewhere, r.failed) + "." + vivos
}

// crashResumeBanner declara o RESULTADO da varredura de ARRANQUE (AC4 de AOS-253) — postura
// anunciada = postura ligada (AOS-203/AOS-248). É uma função PURA (estado → linha) para os testes
// cobrirem cada desfecho sem levantar um nó, como as restantes funções de banner. A FORMA desta
// linha é uma pegada declarada do roteiro E2E (`docs/testing/e2e-pegadas-visao-19.md`) e por isso
// AOS-411 não lhe mexeu: o que era falso não era esta linha, era a periódica usá-la.
func crashResumeBanner(streams, scanned, resumed, heldElsewhere, failed int) string {
	return "crash-resume / varredura de arranque (AOS-253): " + crashResumeNucleo(streams, scanned, resumed, heldElsewhere, failed)
}

// crashResumeNucleo são os NÚMEROS da passagem, sem o nome da passagem — o que as duas origens
// (arranque e re-varredura periódica) têm em comum. Separado para que a origem seja escolhida
// por quem varre, e não fixada na frase (AOS-411).
func crashResumeNucleo(streams, scanned, resumed, heldElsewhere, failed int) string {
	return fmt.Sprintf("CORREU sobre %d stream(s) — %d run(s) orfaos em `running` (claim sem desfecho terminal, o rasto de um crash a meio que AOS-252 tornou distinguivel), %d RETOMADO(s) pela cadeia real (submit->hostRun->RebuildLedger + replay-then-continue de AOS-021: turnos capturados reproduzidos, efeitos ja aplicados DEDUPLICADOS pelo step-ledger sem re-execucao, modelo NAO re-interrogado nesses turnos), %d saltado(s) por LEASE VIVO noutra replica (sem roubo de particao) e %d nao retomado(s) FAIL-CLOSED (estado/cursor/capturas ilegiveis, ou `running` sem registo de retoma). Os checkpoints do Resumer (AOS-015) passam a ser LIDOS no arranque — antes eram escritos e nunca consultados. ALCANCE HONESTO: a retoma automatica NAO traz credencial fresca (um crash nao tem humano no lacete); os turnos ja capturados nao precisam dela (already-applied precede a mediacao), mas uma continuacao AO VIVO que exija identidade de modelo (AOS-278) e negada atribuivelmente — sem principal forjado", streams, scanned, resumed, heldElsewhere, failed)
}

// crashResumeDisabledBanner declara a varredura DESLIGADA e a RAZÃO — sem o substrato para
// distinguir um órfão de um terminado (máquina de estados de AOS-252) ou para o reconstituir
// (registo de retoma de AOS-021), um crash a meio de um run não é retomado por ninguém.
func crashResumeDisabledBanner() string {
	return "crash-resume / varredura de arranque (AOS-253): DESLIGADA — falta o substrato para distinguir um orfao de um terminado ou para o reconstituir (Event Store, maquina de estados duravel de AOS-252 e/ou registo de retoma de AOS-021 nao compostos). Sem eles, um crash a meio de um run NAO e retomado por ninguem e re-submeter recomecaria do turno 1"
}
