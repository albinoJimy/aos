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

	// (1) Está suspenso?
	s.mu.Lock()
	rs, susp := s.suspended[runID]
	s.mu.Unlock()
	if !susp || rs == nil {
		return ErrRunNotSuspended
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
	// Submit porque este recusaria um run ainda listado como suspenso (ErrRunSuspended);
	// se o Submit falhar, repõe-se — o run não pode ficar nem suspenso nem submetido.
	s.mu.Lock()
	delete(s.suspended, runID)
	s.mu.Unlock()

	goal := rec.GoalWith(credential)
	if err := s.Submit(withReplayPlan(ctx, plan), goal); err != nil {
		s.mu.Lock()
		s.suspended[runID] = rs
		s.mu.Unlock()
		return fmt.Errorf("aos: re-submeter run %q na retoma: %w", runID, err)
	}
	s.log("run %q RETOMADO: %d turno(s) reproduzidos da captura, credencial fresca, cadeia de mediacao COMPLETA a correr de novo", runID, len(plan))
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
