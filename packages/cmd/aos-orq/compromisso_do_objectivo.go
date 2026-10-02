package main

// compromisso_do_objectivo.go — O REGISTO DO PLANO CHEGA AO OBJECTIVO E AO PEDIDO (AOS-477).
//
// Até aqui o `plan.proposed` levava `plan_id`, `plan_hash`, `planner_meta` e `attempt`. Do
// registo do plano não se chegava ao objectivo — nem a um compromisso dele —, e a ligação ao
// pedido de origem era a convenção `<run>-plan` (`docs/reports/e2e-pegadas-bidireccional-2026-10-01.md`
// §7, achado 2). O `plan_hash` NÃO serve de compromisso do objectivo: cobre o `objective` do
// documento, que é texto do MODELO, e o documento nem está no log (ver
// `plannerevents.ProposedPayload.ObjectiveCommitment` e o teste
// `TestAOS477OObjectivoDoDocumentoEDoModeloENaoDoPedido` em `orchestrator/decompose`).
//
// Este ficheiro decide o que o `plan.proposed` passa a levar:
//
//   - `objective_commitment` = HMAC-SHA256(sal, objectivo RECEBIDO). No caminho da fila o sal
//     vem do nó (selado lá sob o titular) e o compromisso tem de bater com o do pedido — se não
//     bater, o objectivo que chegou não é o que foi pedido, e o `serve` recusa ANTES da posse.
//     No `serve --goal` manual não há pedido: o sal é tirado aqui, e sai UMA vez no stdout de
//     quem lançou o comando, que é quem tem o texto. Nunca vai ao log.
//   - `request` = o `planrequest.submitted` (stream e `seq`), no caminho da fila.
//
// PORQUE É QUE O SAL DA FILA NÃO SE IMPRIME: lá ele está selado sob a KEK do titular, e é isso
// que põe o compromisso ao alcance do `/dsar/erase`. Escrevê-lo no stdout do drenador — que vai
// para o journal do host — anularia o selo.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// tamanhoDoSal é o do nó (`packages/cmd/aos/plan_origem.go`): 32 bytes.
const tamanhoDoSal = 32

// errObjectivoNaoEOdoPedido — o compromisso recalculado sobre o objectivo recebido não é o que o
// pedido gravou. Genérico (transitório) de propósito: a classe é a de uma avaria entre o nó e o
// drenador, e o tecto de gerações (AOS-467) fecha o pedido se ela se repetir.
var errObjectivoNaoEOdoPedido = errors.New("aos-orq: o objectivo recebido nao e o do pedido (o compromisso nao bate)")

// compromissoDoObjectivo é HMAC-SHA256(chave=sal, mensagem=objectivo), com o esquema do
// `aos.planner.v1`. O nó calcula o MESMO valor na ingestão com a sua própria cópia; o vector do
// contrato está em `aos477_compromisso_test.go` e no teste gémeo do nó.
func compromissoDoObjectivo(sal []byte, objectivo string) string {
	m := hmac.New(sha256.New, sal)
	m.Write([]byte(objectivo))
	return plannerevents.CommitmentScheme + hex.EncodeToString(m.Sum(nil))
}

// origemNoLog é o que o `plan.proposed` cita além do documento: o compromisso do objectivo e o
// pedido de onde ele veio. Zero ⇒ nada a citar (um `serve --plan-doc` ou `--nodes`).
type origemNoLog struct {
	compromisso string
	pedido      *plannerevents.PlanRequestRef
}

// resolverOrigemNoLog compõe a origem a partir dos flags do `serve`. Corre ANTES da posse: um
// objectivo que não é o do pedido aborta sem reclamar o lease nem chamar o modelo.
//
//   - `salHex` vazio, `goal` não-vazio e fora da fila: `serve` manual — sal novo, devolvido em
//     `imprimirSal`.
//   - `salHex` vazio NA FILA (`daFila`): um pedido anterior ao AOS-477, ou um nó anterior — sem
//     compromisso. Tirar aqui um sal e imprimi-lo poria no journal do drenador a chave de um
//     compromisso sobre um objectivo que o nó selou sob o titular.
//   - `salHex` dado: o do nó; se `esperado` vier, o recalculado tem de lhe ser igual.
//   - `seq` > 0: cita o pedido (`stream`, `seq`, `runID`).
func resolverOrigemNoLog(goal, salHex, esperado, stream string, seq uint64, runID string, daFila bool) (o origemNoLog, imprimirSal string, err error) {
	if seq > 0 {
		if stream == "" {
			return origemNoLog{}, "", errors.New("--plan-request-seq exige --plan-request-stream")
		}
		o.pedido = &plannerevents.PlanRequestRef{Stream: stream, Seq: seq, RunID: runID}
	}
	if goal == "" {
		if salHex != "" || esperado != "" {
			return origemNoLog{}, "", errors.New("--objective-salt/--objective-commitment só fazem sentido com --goal")
		}
		return o, "", nil
	}
	var sal []byte
	switch {
	case salHex == "" && esperado != "":
		return origemNoLog{}, "", errors.New("--objective-commitment exige --objective-salt: sem o sal não há como o conferir")
	case salHex == "" && daFila:
		return o, "", nil
	case salHex == "":
		sal = make([]byte, tamanhoDoSal)
		if _, err := rand.Read(sal); err != nil {
			return origemNoLog{}, "", fmt.Errorf("sal do compromisso do objectivo: %w", err)
		}
		imprimirSal = hex.EncodeToString(sal)
	default:
		sal, err = hex.DecodeString(salHex)
		if err != nil || len(sal) != tamanhoDoSal {
			return origemNoLog{}, "", fmt.Errorf("--objective-salt tem de ter %d bytes em hex", tamanhoDoSal)
		}
	}
	o.compromisso = compromissoDoObjectivo(sal, goal)
	if esperado != "" && !hmac.Equal([]byte(esperado), []byte(o.compromisso)) {
		return origemNoLog{}, "", fmt.Errorf("%w: pedido %s, recebido %s", errObjectivoNaoEOdoPedido, esperado, o.compromisso)
	}
	return o, imprimirSal, nil
}

// linhaDoCompromisso é a pegada do compromisso no stdout do `serve`. Com o sal só quando foi
// tirado aqui (ver o cabeçalho).
func linhaDoCompromisso(o origemNoLog, salImpresso string) string {
	switch {
	case o.compromisso == "":
		return ""
	case salImpresso != "":
		return "compromisso do objectivo: " + o.compromisso + " sal=" + salImpresso +
			" (gerado aqui e so aqui: com o objectivo e este sal verifica-se o plan.proposed; sem o sal o compromisso nao se inverte)"
	case o.pedido != nil:
		return fmt.Sprintf("compromisso do objectivo: %s (confere com o do pedido %s#%d)", o.compromisso, o.pedido.Stream, o.pedido.Seq)
	default:
		return "compromisso do objectivo: " + o.compromisso
	}
}

// leitorDoPlano é o que [propostaJaRegistada] precisa do Event Store.
type leitorDoPlano interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// propostaJaRegistada diz se o plano já tem um `plan.proposed` e devolve o compromisso dessa
// PRIMEIRA proposta (revisão do AOS-477, B-1).
//
// O passo do `plan.proposed` é fixo: só a primeira proposta fica no log. Um `serve --goal` repetido
// no mesmo run que tirasse um sal NOVO e o imprimisse estaria a imprimir a chave de um compromisso
// que o log NÃO tem — e a linha dizia que com ele «se verifica o plan.proposed». É falso, e um
// auditor que o seguisse concluiria que o log não bate com o objectivo.
func propostaJaRegistada(ctx context.Context, store leitorDoPlano, planID string) (compromisso string, ja bool, err error) {
	evs, err := store.Read(ctx, planID, 1)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("ler o plano %q para saber se ja tem proposta: %w", planID, err)
	}
	for _, e := range evs {
		if e.Type != plannerevents.EventProposed {
			continue
		}
		var p plannerevents.ProposedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return "", false, fmt.Errorf("plan.proposed ilegivel no plano %q: %w", planID, err)
		}
		return p.ObjectiveCommitment, true, nil
	}
	return "", false, nil
}

// linhaDaPropostaAnterior é a pegada de um `serve --goal` sobre um plano que já tem proposta.
func linhaDaPropostaAnterior(planID, compromisso string) string {
	if compromisso == "" {
		compromisso = "sem compromisso"
	}
	return "compromisso do objectivo: o plano " + planID + " ja tem proposta registada, e o log guarda so a PRIMEIRA (" +
		compromisso + "); este serve NAO tira sal novo — verifica-se com o sal impresso pelo serve que a fez"
}
