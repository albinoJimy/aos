package main

// RETOMA DE UM RUN SUSPENSO (AOS-021) — explícita e re-autenticada.
//
// Um run que escalou uma tool call parou e largou tudo (lease, goroutine, heartbeat). Aqui
// volta a correr: os turnos já dados são REPRODUZIDOS da captura (nunca reinterrogando o
// modelo — ver resume_model.go) e a acção escalada é re-mediada, encontrando agora a
// aprovação humana.
//
// A CREDENCIAL VEM DE FORA, FRESCA. Não foi persistida (é um bearer token, e teria
// expirado — 15 min de janela de aprovação contra TTL de classe NHI de 5-15). Quem retoma
// re-autentica-se, e a cadeia de mediação COMPLETA volta a correr sobre a credencial nova.

import (
	"context"
	"errors"
	"fmt"
	"github.com/aos-ref/kernel/agent-runtime/state"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/replay"
)

// Erros da retoma (fail-closed).
var (
	// ErrRunNotSuspended — pediu-se a retoma de um run que não está à espera de humano.
	ErrRunNotSuspended = errors.New("aos: run nao esta suspenso a espera de aval humano")
	// ErrNoResumeRecord — o run está suspenso mas não há registo de retoma (não é
	// reconstituível). Fail-closed: não se inventa um Goal.
	ErrNoResumeRecord = errors.New("aos: sem registo de retoma para este run — nao e reconstituivel")
	// ErrResumePrincipalMismatch — a credencial de retoma é de OUTRO principal. A retoma
	// não é uma via para terceiros continuarem o run de alguém.
	ErrResumePrincipalMismatch = errors.New("aos: a credencial de retoma nao corresponde ao principal do run")
	// ErrResumeUnavailable — o nó não tem o registo de retoma composto (sem four-eyes).
	ErrResumeUnavailable = errors.New("aos: retoma indisponivel (four-eyes nao composto)")
	// ErrExhaustionPromptUnanswered — o run tem um PROMPT DE EXAUSTÃO por responder (AOS-263)
	// e a retoma recusa até ele ser decidido.
	//
	// PORQUÊ: o prompt pergunta «parar ou continuar?» e SUSPENDE o run à espera da resposta.
	// Se a retoma passasse por cima dele, a metade «continuar» da decisão seria tomada com uma
	// credencial NHI — a MESMA classe de credencial com que o run já corria — sem assinatura de
	// operador pinado e sem registo no WORM de quem decidiu deixar queimar orçamento acima do
	// limiar. A resposta ARRISCADA ficaria mais barata do que a segura, que é a regressão de
	// postura que a decisão do dono (i) recusou. Além disso a pergunta continuaria na lista, e
	// o varrimento acabaria por anunciar no log «expirado sem decisao» sobre algo decidido.
	//
	// NÃO É UMA PRISÃO: a decisão entra em `POST /runs/{id}/exhaustion` (`continue` ou
	// `abort`), e se ninguém decidir o TTL de pendentes expira a pergunta e a retoma volta a
	// ser aceite — a mesma escotilha fail-safe que o pendente sempre teve.
	ErrExhaustionPromptUnanswered = errors.New("aos: run com prompt de exaustao por responder — decida em POST /runs/{id}/exhaustion (continue|abort) antes de retomar")
)

// Resume retoma um run SUSPENSO com uma credencial NHI FRESCA.
//
// Passos, por esta ordem (cada um fail-closed):
//  1. o run TEM de estar suspenso (não terminado, não em curso);
//  2. TEM de haver registo de retoma (o Goal, sem credencial);
//  3. carrega-se o plano de replay das capturas do run;
//  4. re-submete-se com a credencial fresca e o plano no contexto.
//
// O principal do registo é preservado: a credencial fresca autentica-o de novo, mas o run
// continua a ser de quem era. O hook de identidade do RM valida a credencial na primeira
// tool call mediada — uma credencial de outro principal é apanhada aí, além da verificação
// explícita em (4).
func (s *NodeService) Resume(ctx context.Context, runID, credential string) error {
	if runID == "" {
		return ErrEmptyRunID
	}
	if s.node.ResumeRecords == nil {
		return ErrResumeUnavailable
	}

	// (0) UMA RETOMA DE CADA VEZ POR RUN, e é aqui e não mais abaixo.
	//
	// A reclamação do balde de suspensos (passo 4) fecha a via EM MEMÓRIA. NÃO fecha a via
	// DURÁVEL — e isto não é teoria: a asserção «ninguém chega ao submit sem ser dono» apanhou-o
	// na primeira correcção que escrevi. Quando outra retoma já removeu a entrada, o chamador
	// seguinte lê `susp=false`, cai no caminho durável (que continua a dizer «suspenso», porque
	// é verdade), chega ao submit com `rs == nil` e colide.
	//
	// Sem fantasma — com `rs == nil` não há reposição — mas com um submit desperdiçado e um erro
	// ao chamador que descreve mal o que aconteceu.
	//
	// Molde de [NodeService.expireInFlight], que já serializa a rota de expiração e o varredor
	// pela mesma razão; aqui a chave é o run, porque retomas de runs DIFERENTES não competem.
	libertar, souDono := s.reclamarRetoma(runID)
	if !souDono {
		// Está suspenso — mas não para este chamador: outra retoma tem a transição. Devolve-se o
		// erro EXISTENTE em vez de inventar estado novo na API; quem perdeu não tem nada a fazer,
		// porque o vencedor faz exactamente o mesmo trabalho.
		return ErrRunNotSuspended
	}
	defer libertar()

	// (1) Está suspenso? O balde em memória é um cache — um restart do nó esvazia-o sem
	// que a suspensão deixe de ser verdade (registo de retoma, pendente, grant e transição
	// são todos duráveis). Um run que esta réplica não conhece é procurado NO LOG; sem
	// isso, um restart tornaria irretomável um run perfeitamente recuperável.
	//
	// FAIL-CLOSED na leitura: não se retoma sobre um estado que não se conseguiu ler.
	s.mu.Lock()
	rs, susp := s.suspended[runID]
	_, done := s.completed[runID]
	_, running := s.runs[runID]
	s.mu.Unlock()
	if !susp || rs == nil {
		rs = nil
		if running {
			// Um run VIVO não se retoma: a memória é autoritativa e não há nada a repor.
			return ErrRunNotSuspended
		}
		// UMA PAUSA NÃO É UM DESFECHO, e o balde `completed` confundia as duas coisas.
		//
		// `service.go` arquiva em `completed` tudo o que não seja `rs.suspended` — e
		// `rs.suspended` só é verdade na escalada e na exaustão. Um run PAUSADO caía aí, e
		// esta linha, ao tratar a memória como autoritativa, recusava-o antes de olhar para o
		// log. Era o primeiro dos dois travões que faziam de `paused` um estado absorvente.
		//
		// A MEMÓRIA CONTINUA AUTORITATIVA PARA OS DESFECHOS — é a regra que
		// `TestSuspensaoDuravel_NaoSobrepoeAContabilidadeLocal` guarda, e ela mantém-se: um run
		// que ESTA réplica viu terminar não é retomável, mesmo que o log diga o contrário.
		//
		// O que se abre é SÓ a pausa: pergunta-se à máquina de estados e prossegue-se apenas se
		// ela disser `paused`. Qualquer outro estado — incluindo `waiting_on_human` em conflito
		// com um desfecho local — continua a ser recusado como antes.
		if done {
			st, derr := s.DurableState(ctx, runID)
			if derr != nil {
				return fmt.Errorf("aos: ler o estado duravel do run %q: %w", runID, derr)
			}
			if st != state.Paused {
				return ErrRunNotSuspended
			}
		}
		durable, derr := s.suspendedDurably(ctx, runID)
		if derr != nil {
			return fmt.Errorf("aos: ler o estado duravel do run %q: %w", runID, derr)
		}
		if !durable {
			return ErrRunNotSuspended
		}
	}

	// (1-bis) HÁ UMA PERGUNTA DE EXAUSTÃO POR RESPONDER? (AOS-263) A retoma é a EXECUÇÃO de um
	// `continue` já decidido — nunca a decisão. Ver [ErrExhaustionPromptUnanswered].
	//
	// A ORDEM importa: depois da verificação de suspensão, para que um run já abortado continue
	// a devolver [ErrRunNotSuspended] (e o 404 uniforme) em vez de anunciar que tem uma pergunta
	// aberta — um run morto não tem perguntas.
	//
	// FAIL-CLOSED na leitura: não se retoma sobre uma lista de pendentes que não se conseguiu
	// ler. Degradar para "não há pergunta" seria transformar uma falha de substrato na via de
	// contorno exacta que este travão fecha.
	if err := s.exhaustionPromptPorResponder(ctx, runID); err != nil {
		return err
	}

	// (2) É reconstituível?
	rec, ok, err := s.node.ResumeRecords.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("aos: ler registo de retoma do run %q: %w", runID, err)
	}
	if !ok {
		return ErrNoResumeRecord
	}

	// (3) Plano de replay: as respostas do modelo JÁ REGISTADAS, por turno. Sem elas a
	// retoma reinterrogaria o modelo e a aprovação — amarrada à preview da call original —
	// nunca se aplicaria.
	plan, err := s.replayPlanFor(ctx, runID, rec.Principal.NHIID)
	if err != nil {
		return fmt.Errorf("aos: carregar capturas do run %q para a retoma: %w", runID, err)
	}

	// (4) Sai do balde de suspensos e volta a ser submetido. A REMOÇÃO acontece ANTES do
	// Submit porque, enquanto lá estiver, GET /runs/{id} continuaria a reportar
	// `waiting_on_human` para um run que já voltou a correr; se o Submit falhar, repõe-se
	// — o run não pode ficar nem suspenso nem submetido. Depois de um restart não há nada
	// a repor (rs == nil): a suspensão continua verdadeira no log e é de lá que a próxima
	// tentativa a lê.
	// RECLAMAR, e não apagar. Achado 1.12 da varredura adversarial de 2026-08-21.
	//
	// O `delete` incondicional não distingue «tirei-o eu» de «já não estava lá», e duas retomas
	// concorrentes passam as verificações acima com o MESMO `rs`: a primeira apaga, submete e o
	// run corre; a segunda apaga um nada, o `submit` recusa-o (o run já existe) e o ramo de erro
	// REPÕE o estado velho por cima de um run que já avançou.
	//
	// O resultado era um FANTASMA: `GET /runs/{id}` a responder `waiting_on_human` sobre um run
	// `complete`, com o RunID bloqueado e a escotilha de contorno a recusar 409 (exige estado
	// durável `waiting_on_human`, e o do fantasma é `complete`). Permanente durante a vida do
	// processo — só um restart o limpava.
	//
	// Quem NÃO reclamou não submete. Não é uma optimização: é a única forma de o segundo saber
	// que não é dono da transição.
	s.mu.Lock()
	_, reclamado := s.suspended[runID]
	delete(s.suspended, runID)
	s.mu.Unlock()
	if rs != nil && !reclamado {
		// Entrámos pela via EM MEMÓRIA (rs != nil) e a entrada desapareceu entretanto.
		//
		// COM a reclamação de (0), não pode ter sido outra retoma. Pode ter sido um ABORTO: a
		// decisão de exaustão chama [NodeService.esqueceSuspensao], que remove a entrada e NÃO
		// consulta `resumeInFlight`. Sem este ramo, uma retoma em voo re-submeteria um run que um
		// humano acabou de mandar parar — pior do que o fantasma que (0) fecha.
		//
		// NÃO ESTÁ PROVADO POR TESTE, e fica dito: a mutação que o remove não cai em nenhum dos
		// testes deste ficheiro, porque exercitá-lo exige um aborto entre o passo (1) e o (4) e
		// não há gancho para o interleaving. Mantém-se por defender um caminho REAL e nomeado,
		// não por precaução genérica.
		return ErrRunNotSuspended
	}

	goal := rec.GoalWith(credential)
	// resuming=true: é este o run suspenso a ser re-hospedado, e o log só passa a dizer
	// `running` dentro do arranque — a recusa por suspensão não se aplica a si próprio.
	if err := s.submit(withReplayPlan(ctx, plan), goal, true); err != nil {
		if rs != nil {
			// A reposição fica sujeita ao estado, pela mesma razão do ramo acima e com a mesma honestidade: repor
			// por cima de um run que entretanto avançou é como o fantasma nascia, e a asserção custa
			// duas leituras sob o lock que já se tem. TAMBÉM NÃO ESTÁ PROVADA POR TESTE — a mutação
			// que a torna incondicional não cai.
			s.mu.Lock()
			_, done := s.completed[runID]
			_, running := s.runs[runID]
			if !done && !running {
				s.suspended[runID] = rs
			}
			s.mu.Unlock()
		}
		return fmt.Errorf("aos: re-submeter run %q na retoma: %w", runID, err)
	}
	s.log("run %q RETOMADO: %d turno(s) reproduzidos da captura, credencial fresca, cadeia de mediacao COMPLETA a correr de novo", runID, len(plan))
	return nil
}

// exhaustionPromptPorResponder devolve [ErrExhaustionPromptUnanswered] quando o run tem um
// prompt de exaustão à espera de decisão, nil quando não tem, e o erro de leitura embrulhado
// quando não se conseguiu saber.
//
// Lê a MESMA listagem que o operador vê em `GET /runs/{id}` e que a rota de decisão consulta
// — [integration.PendingApprovals.ListForRun] já exclui expirados e decididos —, pelo que o
// que barra a retoma é exactamente o que está por responder, nem mais nem menos. Um nó sem
// registo de pendentes composto não tem prompts: não há nada a barrar.
func (s *NodeService) exhaustionPromptPorResponder(ctx context.Context, runID string) error {
	if s.node == nil || s.node.PendingApprovals == nil {
		return nil
	}
	recs, err := s.node.PendingApprovals.ListForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("aos: ler pendentes de exaustao do run %q antes da retoma (AOS-263): %w", runID, err)
	}
	for _, r := range recs {
		if r.Kind.Resolved() == integration.PendingKindExhaustion {
			return fmt.Errorf("%w: run=%q passo=%q", ErrExhaustionPromptUnanswered, runID, r.StepID)
		}
	}
	return nil
}

// replayPlanFor carrega as respostas do modelo REGISTADAS do run, indexadas por turno.
// Usa o mesmo [replay.ReplayEngine.Reconstruct] que o read-path soberano — e portanto o
// mesmo gate de decifração por-titular: um run cujo conteúdo foi apagado por
// crypto-shredding NÃO é retomável, por desenho.
func (s *NodeService) replayPlanFor(ctx context.Context, runID, subject string) (replayPlan, error) {
	// O conteúdo capturado é CIFRADO POR-TITULAR (AOS-093). Sem o opener, as capturas
	// seladas não abrem e a retoma falha — foi exactamente o que aconteceu na primeira
	// execução ao vivo. O acessor é o PRÓPRIO principal do run: é o run a continuar o seu
	// trabalho, não um terceiro a ler conteúdo alheio (menor privilégio).
	var opts []replay.EngineOption
	if s.node.contentOpener != nil {
		opts = append(opts, replay.WithContentOpener(s.node.contentOpener, replay.Accessor{
			Principal: subject,
			Scopes:    []string{replay.DefaultSovereignContentScope},
		}))
	}
	engine, err := replay.NewEngine(s.node.EventStore, opts...)
	if err != nil {
		return nil, err
	}
	turns, err := engine.Reconstruct(ctx, runID)
	if err != nil {
		if errors.Is(err, replay.ErrNoTrajectory) {
			// Sem capturas não há o que reproduzir. Deixar seguir reinterrogaria o modelo
			// e a aprovação nunca se aplicaria — pior do que recusar.
			return nil, err
		}
		return nil, err
	}
	plan := make(replayPlan, len(turns))
	for _, t := range turns {
		plan[t.Turn] = t.Response
	}
	return plan, nil
}

// reclamarRetoma dá a UM chamador a transição de retoma de `runID`, e recusa-a aos restantes
// enquanto ela durar. Devolve a função que a liberta e se a reclamação foi concedida.
//
// PORQUE É UMA FUNÇÃO E NÃO CÓDIGO INLINE: inline, a única forma de a exercitar era ganhar uma
// corrida — e o teste concorrente que escrevi para isso NÃO matou a mutação que a remove (10
// corridas, 10 passagens). Uma propriedade que só se observa por acaso não está provada. Extraída,
// prova-se em duas linhas, e a cablagem prova-se reclamando-a a partir do teste e exigindo que o
// `Resume` recuse.
//
// Molde de [NodeService.expireInFlight], que já serializa a rota de expiração e o varredor pela
// mesma razão; aqui a chave é o run, porque retomas de runs DIFERENTES não competem.
func (s *NodeService) reclamarRetoma(runID string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resumeInFlight == nil {
		s.resumeInFlight = make(map[string]struct{})
	}
	if _, jaVai := s.resumeInFlight[runID]; jaVai {
		return func() {}, false
	}
	s.resumeInFlight[runID] = struct{}{}
	return func() {
		s.mu.Lock()
		delete(s.resumeInFlight, runID)
		s.mu.Unlock()
	}, true
}
