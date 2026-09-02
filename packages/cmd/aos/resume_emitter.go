package main

// O EMISSOR DA RETOMA VIAJA NO CONTEXTO, do handler até à hospedagem (AOS-292).
//
// # PORQUE PRECISA DE VIAJAR
//
// A retoma de um run PAUSADO tem de passar por [control.SteerChannel.Resume] — é o único
// sítio que limpa `pauseRequested` e consome a correcção pendente. Esse método exige duas
// coisas que não estão disponíveis no mesmo instante:
//
//   - um [control.Emitter] ASSINADO, que só existe no corpo do pedido HTTP;
//   - um [control.StateGate] do run, que `runStateGates.Resolve` devolve NIL enquanto o run
//     não estiver hospedado — e um run pausado não está.
//
// O gate nasce em `hostRun`, depois do claim do lease. Logo a chamada ao canal tem de
// acontecer LÁ, e o emissor tem de lá chegar. Viaja pelo contexto, no mesmo molde de
// [withReplayPlan] — que existe pela mesma razão e pelo mesmo caminho (`NodeService.Resume`
// → `submit` → `hostRun`).
//
// # PORQUE NÃO UM CAMPO NO GOAL
//
// O `Goal` é o que o run É — objectivo, credencial, escopo — e é persistido no registo de
// retoma. Um emissor de retoma não pertence lá: é um facto de UMA hospedagem, com nonce de
// uso único, e persisti-lo seria guardar material de autenticação que já foi consumido.

import (
	"context"
	"errors"

	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// resumeEmitterKey é a chave (tipo privado — nenhum pacote externo a forja) do emissor da
// retoma no contexto.
type resumeEmitterKey struct{}

// withResumeEmitter devolve um ctx que carrega o emissor assinado da retoma. Só o caminho de
// retoma o chama; uma submissão normal nunca tem emissor, e a ausência é o que distingue as
// duas na hospedagem.
func withResumeEmitter(ctx context.Context, emitter control.Emitter) context.Context {
	if emitter.ID == "" {
		return ctx
	}
	return context.WithValue(ctx, resumeEmitterKey{}, emitter)
}

// resumeEmitterFrom extrai o emissor do ctx. O segundo valor distingue «não há emissor» de
// «há um emissor vazio» — e a distinção decide: sem emissor, uma retoma de run pausado é
// RECUSADA em vez de transitar à revelia do canal.
func resumeEmitterFrom(ctx context.Context) (control.Emitter, bool) {
	em, ok := ctx.Value(resumeEmitterKey{}).(control.Emitter)
	return em, ok && em.ID != ""
}

// ErrResumeNeedsEmitter — pediu-se a retoma de um run PAUSADO sem o emissor assinado do
// operador. A pausa foi imposta por um operador e só um operador a levanta: o
// [control.SteerChannel.Resume] autentica o sinal como qualquer outro (ADR-013/005), e não há
// forma de o satisfazer com a credencial do run.
//
// Não é apenas uma exigência técnica do canal. Antes de AOS-292, quem detivesse a credencial
// NHI do run desfazia a pausa de um operador sem assinar nada — a assimetria com o `POST
// /pause`, que sempre exigiu operador pinado.
var ErrResumeNeedsEmitter = errors.New("aos: retoma de run PAUSADO exige o emissor assinado de um operador (a pausa foi imposta por um; a credencial do run nao a levanta)")

// ErrResumePausaExigeCanal — um run pausado chegou à hospedagem sem via de retoma pelo canal.
// Fail-closed: transitar paused→running à margem do canal é o defeito de AOS-292 (a projecção
// mantinha `pauseRequested` e o run re-pausava no primeiro turno, com a correcção por
// consumir). Não deve ocorrer pelo caminho HTTP, que injecta sempre a função.
var ErrResumePausaExigeCanal = errors.New("aos: run pausado sem via de retoma pelo canal de controlo — a pausa nao se levanta a margem do SteerChannel")

// retomarPausaPeloCanal levanta a pausa PELO CANAL DE CONTROLO, que é o único caminho que
// limpa `pauseRequested` (AOS-292).
//
// O gate vem de quem chama porque só existe DEPOIS do claim do lease: `runStateGates.Resolve`
// devolve nil enquanto o run não estiver hospedado, e um run pausado não está. Foi por isso que
// esta chamada não pôde ficar no handler HTTP — lá há emissor e não há gate; aqui há gate e o
// emissor chega pelo contexto.
func (s *NodeService) retomarPausaPeloCanal(ctx context.Context, runID string, gate *runGate) error {
	if s.node == nil || s.node.Steer == nil {
		return ErrResumePausaExigeCanal
	}
	em, ok := resumeEmitterFrom(ctx)
	if !ok {
		return ErrResumeNeedsEmitter
	}
	// A correcção devolvida NÃO se usa aqui, e é deliberado: desde AOS-292 o `Resume` deixa-a
	// PENDENTE para o loop a injectar no turno seguinte, e é a entrega que a consome. Usá-la
	// aqui seria injectá-la duas vezes.
	_, err := s.node.Steer.Resume(ctx, runID, em, gate)
	return err
}
