package main

// plan_origem.go — DO REGISTO SE CHEGA AO PEDIDO POR CAMPOS, E NÃO POR NOMES (AOS-477).
//
// # O DEFEITO
//
// O E2E de 2026-10-01 seguiu, em produção, um `deny` selado até ao pedido de plano. A cadeia
// fechou — mas o último troço fez-se À MÃO: cortar `plan-e2e-447-1790511800~n1` pelo `~`, juntar
// `-plan` ao prefixo, e procurar o pedido pelo `run_id` no outro ficheiro. Nenhum evento dizia
// «este run é o nó N do plano P, do pedido R»; e do plano não se chegava ao objectivo, nem a um
// compromisso dele (`docs/reports/e2e-pegadas-bidireccional-2026-10-01.md` §4 e §7, achado 2).
//
// # AS TRÊS PEÇAS DESTE LADO
//
//  1. O COMPROMISSO DO OBJECTIVO nasce aqui, na ingestão: `objective_commitment` no
//     `planrequest.submitted`, com um SAL aleatório que fica selado sob a KEK do titular, ao lado
//     do objectivo. A reclamação entrega o sal ao drenador (pelo mesmo canal autenticado por onde
//     já lhe entrega o objectivo em claro) e o `aos-orq` recalcula o compromisso sobre o objectivo
//     que RECEBEU e grava-o no `plan.proposed`. Os dois valores batem por igualdade de campo.
//  2. A REFERÊNCIA AO PEDIDO: a reclamação entrega o stream e o `seq` do `planrequest.submitted`,
//     e o `aos-orq` grava-os no `plan.proposed` (`request`).
//  3. A ORIGEM DO RUN FILHO: quando o `POST /runs` traz o vínculo VERIFICADO ao pedido, o nó grava
//     no stream do run um `run.plan_origin` — o pedido (stream, `seq`, `run_id`, geração) e o
//     plano e o nó que o drenador declara.
//
// # PORQUE É UM HMAC COM SAL, E O QUE ISSO DÁ E NÃO DÁ
//
// Um SHA-256 do objectivo inverte-se por dicionário: «auditar o pipeline de faturas» adivinha-se,
// e o hash ficaria no log do plano para sempre, fora do alcance do crypto-shredding — um
// identificador estável de texto pessoal. É o argumento que o `deploy/server/avisar-planos.sh`
// já usa para o pseudónimo do run. Aqui a chave do HMAC é um sal de 32 bytes POR PEDIDO, e o sal
// fica onde o objectivo fica: selado sob a KEK do titular. Daí:
//
//   - quem tem só o log NÃO inverte o compromisso (falta-lhe o sal);
//   - quem abre o pedido (o nó, com a custódia) recalcula-o e confirma — é VERIFICÁVEL;
//   - depois de um `/dsar/erase` o sal deixa de abrir, e o compromisso fica inverificável e
//     não-ligável a texto nenhum: o apagamento alcança-o como alcança o objectivo;
//   - dois pedidos com o MESMO objectivo têm compromissos DIFERENTES — não se correlacionam
//     titulares por igualdade de objectivo (o `prompt_hash` do turno, que é determinístico, é
//     outra coisa e serve outro fim: ver o relatório, §2).
//
// SEM TITULAR (nó sem gate soberano) o objectivo fica em claro no pedido — já ficava — e o sal
// fica em claro ao lado dele: não há KEK sob a qual o selar, e ele não protege nada que o texto
// em claro não exponha já.
//
// # O QUE O NÓ NÃO PROVA
//
// O `plan_id` e o `node_id` do `run.plan_origin` são DECLARADOS pelo drenador: o nó não conhece o
// documento do plano (ADR-018; o ADR-035 §5 já o diz para a forma `<plano>~<nó>`). Verifica-os
// na forma, e só os grava com o vínculo verificado — o que os prende ao drenador que tem AGORA a
// reclamação viva do pedido. A pertença do nó ao plano confere-se do outro lado: o
// `task.node.created` com esse `node_id` no stream `plan_id` do WAL do `aos-orq`.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// esquemaDoCompromisso é o prefixo do compromisso do objectivo. O `aos-orq` escreve o MESMO valor
// (`plannerevents.CommitmentScheme`); os dois binários não se importam, e o contrato prende-se pelo
// vector conhecido em [TestAOS477CompromissoTemOVectorDoContrato] — o mesmo nos dois.
const esquemaDoCompromisso = "hmac-sha256:"

// tamanhoDoSal é o tamanho do sal do compromisso: 32 bytes, o tamanho do bloco de saída do
// SHA-256, que é o que o RFC 2104 recomenda como mínimo para a chave do HMAC.
const tamanhoDoSal = 32

// compromissoDoObjetivo é HMAC-SHA256(chave=sal, mensagem=objectivo), em hex, com o esquema.
func compromissoDoObjetivo(sal []byte, objetivo string) string {
	m := hmac.New(sha256.New, sal)
	m.Write([]byte(objetivo))
	return esquemaDoCompromisso + hex.EncodeToString(m.Sum(nil))
}

// novoSal tira um sal do gerador criptográfico. Fail-closed: sem entropia não há compromisso, e um
// sal previsível (zeros) faria do HMAC um hash invertível por dicionário.
func novoSal() ([]byte, error) {
	sal := make([]byte, tamanhoDoSal)
	if _, err := rand.Read(sal); err != nil {
		return nil, fmt.Errorf("sal do compromisso do objectivo: %w", err)
	}
	return sal, nil
}

// EventTypeRunPlanOrigin — o run declara de que pedido, de que plano e de que nó é trabalho.
//
// Vive no stream DO RUN FILHO, e é escrito pelo nó depois de o run ser hospedado, só quando o
// vínculo ao pedido foi verificado. Família `run.*` porque é um facto sobre o run, lido por quem
// lê o run — é o primeiro evento que um auditor encontra ao subir de um `tool.call.mediated`.
const EventTypeRunPlanOrigin = "run.plan_origin"

// passoDaOrigem é o step_id do facto: um por run, e a idempotency-key (`<run>:plan-origin`) torna
// a escrita idempotente — o primeiro fica.
const passoDaOrigem = "plan-origin"

// origemNHI é a identidade emissora do facto: o nó, que verificou o vínculo.
const origemNHI = "nhi:aos-node/plan-origin"

// versaoDaOrigem é a versão do payload de `run.plan_origin`.
const versaoDaOrigem = "1.0"

// refDoPedidoDeOrigem referencia o `planrequest.submitted` deste nó: por stream e `seq` (o que um
// auditor segue), mais o `run_id` e a geração que o vínculo nomeou.
type refDoPedidoDeOrigem struct {
	Stream  string `json:"stream"`
	Seq     uint64 `json:"seq"`
	RunID   string `json:"run_id"`
	Geracao int    `json:"generation"`
}

// origemDoRunFilho é o payload de `run.plan_origin`.
type origemDoRunFilho struct {
	Versao string              `json:"v"`
	Pedido refDoPedidoDeOrigem `json:"plan_request"`
	// PlanID e NodeID são DECLARADOS pelo drenador e verificados só na forma (ver o cabeçalho).
	// Vazios quando o drenador é anterior ao AOS-477 e não os mandou.
	PlanID string `json:"plan_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
}

// errOrigemMalformada — o vínculo traz um `plan_id`/`node_id` que não tem forma de id.
var errOrigemMalformada = errors.New("origem do run filho malformada")

// maxNodeIDDeclarado é o tecto do `node_id` declarado — o mesmo da gramática do plano
// (`plan.ValidNodeID`, 128), que o nó não pode importar (ADR-018).
const maxNodeIDDeclarado = 128

// validarOrigemDeclarada confere a FORMA do que o drenador declara: os dois ou nenhum; o `plan_id`
// é um nome de stream (é-o, no WAL do orquestrador); o `node_id` segue a gramática do plano
// (`[A-Za-z0-9_.:-]`, até 128). Não confere a pertença — o nó não conhece o documento.
func validarOrigemDeclarada(v vinculoAoPedido) error {
	if v.PlanID == "" && v.NodeID == "" {
		return nil
	}
	if v.PlanID == "" || v.NodeID == "" {
		return fmt.Errorf("%w: plan_id e node_id vão juntos", errOrigemMalformada)
	}
	if runIDInvalido(v.PlanID) || runIDReservado(v.PlanID) {
		return fmt.Errorf("%w: plan_id não é um nome de stream", errOrigemMalformada)
	}
	if len(v.NodeID) > maxNodeIDDeclarado {
		return fmt.Errorf("%w: node_id acima de %d bytes", errOrigemMalformada, maxNodeIDDeclarado)
	}
	for i := 0; i < len(v.NodeID); i++ {
		c := v.NodeID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == ':':
		default:
			return fmt.Errorf("%w: node_id fora da gramática do plano", errOrigemMalformada)
		}
	}
	return nil
}

// apensadorDaOrigem é o que [declararOrigemDoRunFilho] precisa do Event Store: só o Append.
type apensadorDaOrigem interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// declararOrigemDoRunFilho grava `run.plan_origin` no stream do run.
//
// CHAMA-SE DEPOIS DE O RUN SER HOSPEDADO, e nunca antes. Antes, um `run_id` `<plano>~<nó>` criado
// por OUTRA via (o `POST /runs` não reserva a forma) receberia de um drenador legítimo uma
// declaração de origem que não é a sua: a submissão do drenador seria recusada como repetida, mas
// o facto já estaria no stream alheio. Depois, `Submit` sem erro diz que foi ESTA chamada que o
// hospedou.
func declararOrigemDoRunFilho(ctx context.Context, es apensadorDaOrigem, runID string, v vinculoAoPedido, seqDoPedido uint64) error {
	raw, err := json.Marshal(origemDoRunFilho{
		Versao: versaoDaOrigem,
		Pedido: refDoPedidoDeOrigem{Stream: planRequestStream, Seq: seqDoPedido, RunID: v.RunID, Geracao: v.Geracao},
		PlanID: v.PlanID,
		NodeID: v.NodeID,
	})
	if err != nil {
		return err
	}
	_, err = es.Append(ctx, runID, eventstore.EventInput{
		Type:     EventTypeRunPlanOrigin,
		Payload:  raw,
		RunID:    runID,
		StepID:   passoDaOrigem,
		Producer: eventstore.Producer{NHIID: origemNHI},
	})
	return err
}
