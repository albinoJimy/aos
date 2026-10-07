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
//     no stream do run um `run.plan_origin` — o pedido (stream, `run_id`, geração) e o plano e o
//     nó que o drenador declara.
//
// # PORQUE É QUE O `run.plan_origin` NÃO LEVA O `seq` DO PEDIDO (revisão do AOS-477)
//
// O run filho lê-se com autorização POR REGIÃO, e a trajectória serve todos os tipos de evento. O
// stream da fila é UM só para o nó inteiro — todas as regiões, e pedidos, reclamações e desfechos
// no mesmo contador. O `seq` de um pedido diz quanta actividade de fila houve no nó até ele: um
// agregado sobre recursos de OUTRAS regiões, entregue a quem não pode agir sobre eles. É a classe
// que o ADR-030 §2.1 fecha (e mais do que o bit que o AOS-464 aceitou, porque atravessa regiões).
// O `run_id` do pedido é um id equivalente e não conta nada: é único na fila (a idempotency-key
// do `planrequest.submitted` é `req-<run_id>`, de primeira escrita) e já está no prefixo do id do
// próprio run filho. O `seq` continua no `plan.proposed`, que vive no WAL do `aos-orq` e não é
// servido a leitores de runs. O auditor que tem os dois ficheiros casa-os por `run_id` e confere
// o `seq` do plano contra o do facto que encontrou.
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
// # O QUE O NÓ PROVA, E O QUE NÃO PROVA
//
// O NÓ PROVA o pedido: o `plan_request` do `run.plan_origin` só é gravado com o vínculo do AOS-439
// verificado contra o seu próprio log (o pedido existe, a reclamação viva é do chamador, a região
// coincide). E prova que o `node_id` declarado é o do `run_id`: o run tem de ser
// `<pedido>~<node_id escapado>`, na forma que o `aos-orq` compõe ([idDoRunFilho]).
//
// O NÓ NÃO PROVA o `plan_id`, nem que o `node_id` pertence a esse plano: o nó não conhece o
// documento (ADR-018; o ADR-035 §5 já o diz para a forma `<plano>~<nó>`). Os dois são DECLARADOS
// pelo drenador, e todo o lado do plano — o WAL do `aos-orq` — é ATESTADO PELO DRENADOR: nada nele
// é assinado nem verificado pelo nó. Daí a verificação de quem audita ter de casar os DOIS lados:
//
//  1. no stream `plan_id` do WAL do `aos-orq`, o `plan.proposed.request` tem de citar O MESMO pedido
//     que o `run.plan_origin.plan_request` (o `run_id`; e o `seq` do plano tem de ser o do facto
//     `planrequest.submitted` com esse `run_id`);
//  2. e só então o `task.node.created`/`plan.materialized` com o `node_id`.
//
// Sem (1), (2) aceitaria atribuição cruzada: um drenador com a reclamação viva do pedido da Alice
// declarava o `plan_id` do plano do Bob, e passava sempre que o plano do Bob tivesse um nó com o
// mesmo id (`n1` é comum).

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
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

// refDoPedidoDeOrigem referencia o `planrequest.submitted` deste nó pelo stream e pelo `run_id`
// do pedido (único na fila), mais a geração que o vínculo nomeou. SEM o `seq`: ver o cabeçalho.
type refDoPedidoDeOrigem struct {
	Stream  string `json:"stream"`
	RunID   string `json:"run_id"`
	Geracao int    `json:"generation"`
}

// origemDoRunFilho é o payload de `run.plan_origin`.
type origemDoRunFilho struct {
	Versao string              `json:"v"`
	Pedido refDoPedidoDeOrigem `json:"plan_request"`
	// PlanID e NodeID são DECLARADOS pelo drenador: o `node_id` é conferido contra o `run_id`, o
	// `plan_id` só na forma (ver o cabeçalho). Vazios quando o drenador é anterior ao AOS-477.
	PlanID string `json:"plan_id,omitempty"`
	NodeID string `json:"node_id,omitempty"`
	// Attempt e RetryOf existem só num run que é uma NOVA TENTATIVA de um nó do plano (AOS-502,
	// ADR-039): a tentativa (≥ 2) e o id do run anterior, que o nó PROVOU não ter pedido tool
	// nenhuma antes de hospedar este. Aditivos e `omitempty`: o evento de um run sem tentativa
	// tem os bytes de sempre, e quem lê eventos antigos não parte.
	Attempt int    `json:"attempt,omitempty"`
	RetryOf string `json:"retry_of,omitempty"`
	// RetryNotice existe só numa tentativa que o nó hospedou COM o aviso constante na semente do
	// tail (AOS-506): o valor de vocabulário fechado que o nó declarou ao kernel
	// ([agentruntime.RetryNotice]). É por ele que quem audita ou reproduz o run sabe que a
	// semente levou o aviso. Aditivo e `omitempty`: uma tentativa sem aviso tem os bytes de antes.
	RetryNotice string `json:"retry_notice,omitempty"`
	// RetryReason existe só numa tentativa que o nó admitiu pela classe da RESPOSTA VAZIA
	// (AOS-510): `empty_output`, a razão do veredicto do run anterior, que o nó leu no seu log.
	// Uma tentativa do AOS-502 não o leva. Aditivo e `omitempty`: o evento de uma tentativa da
	// primeira classe e o de um run sem tentativa têm os bytes de antes.
	RetryReason string `json:"retry_reason,omitempty"`
}

// errOrigemMalformada — o vínculo traz um `plan_id`/`node_id` que não tem forma de id.
var errOrigemMalformada = errors.New("origem do run filho malformada")

// maxNodeIDDeclarado é o tecto do `node_id` declarado — o mesmo da gramática do plano
// (`plan.ValidNodeID`, 128), que o nó não pode importar (ADR-018).
const maxNodeIDDeclarado = 128

// validarOrigemDeclarada confere o que o drenador declara: os dois ou nenhum; o `plan_id` é um nome
// de stream (é-o, no WAL do orquestrador); o `node_id` segue a gramática do plano
// (`[A-Za-z0-9_.:-]`, até 128 — [TestAOS477GramaticaDoNodeIDCasaComOPlano] prende-a à fonte) E é o
// do próprio run: `runID` tem de ser [idDoRunFilho](pedido, node_id). Não confere a pertença ao
// plano — o nó não conhece o documento.
func validarOrigemDeclarada(v vinculoAoPedido, runID string) error {
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
	// AOS-502: com `attempt`, o id é o da tentativa; sem ele, [idDaTentativa] é o [idDoRunFilho].
	if runID != idDaTentativa(v.RunID, v.NodeID, v.Attempt) {
		return fmt.Errorf("%w: o node_id declarado nao e o do run_id", errOrigemMalformada)
	}
	return nil
}

// marcaDeEscapeDoRunFilho é a marca de escape do `node_id` no id do run filho — a do `aos-orq`
// (`marcaDeEscape` em `packages/cmd/aos-orq/node_executor.go`).
const marcaDeEscapeDoRunFilho = '+'

// idDoRunFilho é o id que o `aos-orq` dá ao run do nó `nodeID` do pedido `plano`
// (`childRunID`/`escaparParaStream` em `packages/cmd/aos-orq/node_executor.go`): o separador e o
// `node_id` escapado de forma injectiva — cada byte que um `stream_id` não representa, e o `~`,
// vira `+<hex>`; o `+` vira `++`. Os dois binários não se importam (ADR-018);
// [TestAOS477IdDoRunFilhoTemOsVectoresDoOrquestrador] prende os mesmos vectores dos dois lados.
func idDoRunFilho(plano, nodeID string) string {
	b := make([]byte, 0, len(plano)+len(separadorDoRunFilho)+len(nodeID)+8)
	b = append(b, plano...)
	b = append(b, separadorDoRunFilho...)
	for i := 0; i < len(nodeID); i++ {
		c := nodeID[i]
		switch {
		case c == marcaDeEscapeDoRunFilho:
			b = append(b, marcaDeEscapeDoRunFilho, marcaDeEscapeDoRunFilho)
		case eventstore.CaractereNaoRepresentavel(rune(c)) || c == separadorDoRunFilho[0]:
			b = append(b, marcaDeEscapeDoRunFilho)
			b = append(b, hex.EncodeToString([]byte{c})...)
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// apensadorDaOrigem é o que [declararOrigemDoRunFilho] precisa do Event Store: só o Append.
type apensadorDaOrigem interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// declararOrigemDoRunFilho grava `run.plan_origin` no stream do run.
//
// CHAMA-SE DEPOIS DE O RUN SER HOSPEDADO, e nunca antes. O `POST /runs` não reserva a forma
// `<plano>~<nó>`: um run com esse id pode ter sido criado ANTES por outra via (um `POST /runs` sem
// vínculo). A submissão do drenador com o vínculo chega depois, o `Submit` devolve «já existe», e
// a rota responde-lhe com a re-submissão idempotente (`201 accepted`; `409` só com credencial
// forte e residência coincidente — ADR-030 §2.1). Gravar ANTES do `Submit` poria nesse stream
// ALHEIO uma origem que não é a dele. Depois, `Submit` sem erro diz que foi ESTA chamada que o
// hospedou. [TestAOS477OrigemNaoEntraNumRunAlheio] prende a ordem.
//
// `aviso` é o aviso com que o run foi semeado (AOS-506); só fica gravado numa tentativa
// (`attempt >= 2`), e vazio dá os bytes de sempre.
func declararOrigemDoRunFilho(ctx context.Context, es apensadorDaOrigem, runID string, v vinculoAoPedido, aviso agentruntime.RetryNotice) error {
	return declararOrigemDaTentativa(ctx, es, runID, v, aviso, "")
}

// declararOrigemDaTentativa é o [declararOrigemDoRunFilho] com a razão da classe da tentativa
// (AOS-510): `razao` é o valor de vocabulário fechado que o NÓ decidiu na prova
// ([razaoDaTentativaVazia], ou vazio na classe do AOS-502), e só fica gravado numa tentativa.
func declararOrigemDaTentativa(ctx context.Context, es apensadorDaOrigem, runID string, v vinculoAoPedido, aviso agentruntime.RetryNotice, razao string) error {
	origem := origemDoRunFilho{
		Versao: versaoDaOrigem,
		Pedido: refDoPedidoDeOrigem{Stream: planRequestStream, RunID: v.RunID, Geracao: v.Geracao},
		PlanID: v.PlanID,
		NodeID: v.NodeID,
	}
	if v.Attempt >= 2 {
		// AOS-502: só quem passou a prova chega aqui com `attempt` — o `POST /runs` recusa antes.
		origem.Attempt, origem.RetryOf = v.Attempt, idDaTentativa(v.RunID, v.NodeID, v.Attempt-1)
		// AOS-506: e o aviso com que o run foi semeado, se o levou. Só numa tentativa.
		origem.RetryNotice = string(aviso)
		// AOS-510: e a razão da classe, quando a tentativa é a da resposta vazia.
		origem.RetryReason = razao
	}
	raw, err := json.Marshal(origem)
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

// gravarOrigemDoRunFilho é a chamada do `POST /runs`: [declararOrigemDoRunFilho] sob um contexto
// que o cliente NÃO cancela, com o prazo próprio dos selos pós-efeito ([controlSealTimeout], o
// molde do `control_seal.go`). Com o contexto do pedido, um cliente que desligasse ou esgotasse o
// prazo depois do `Submit` deixava o run sem origem — e o retry dele cai na re-submissão
// idempotente, que não a volta a escrever. Uma falha fica no log do operador: o run já corre.
//
// `aviso` é o aviso com que o run foi semeado (AOS-506); vazio num run sem ele. É a ÚNICA função
// que grava a origem pelo handler — os testes do AOS-477 exercitam esta, a que o `POST /runs`
// chama (revisão do AOS-506, M-3: havia uma segunda, sem aviso, só com chamadores de teste).
func (h *apiHandler) gravarOrigemDoRunFilho(ctx context.Context, runID string, v vinculoAoPedido, aviso agentruntime.RetryNotice) {
	h.gravarOrigemDaTentativa(ctx, runID, v, aviso, "")
}

// gravarOrigemDaTentativa é o corpo do [apiHandler.gravarOrigemDoRunFilho], com a razão da classe
// da tentativa (AOS-510). É esta que o `POST /runs` chama; `razao` vazia dá os bytes de sempre.
func (h *apiHandler) gravarOrigemDaTentativa(ctx context.Context, runID string, v vinculoAoPedido, aviso agentruntime.RetryNotice, razao string) {
	origemCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlSealTimeout)
	defer cancel()
	if err := declararOrigemDaTentativa(origemCtx, h.node.EventStore, runID, v, aviso, razao); err != nil {
		h.logf("submit (AOS-477): o run %q foi hospedado mas a ORIGEM nao ficou gravada (plano=%q geracao=%d): %v",
			runID, v.RunID, v.Geracao, err)
	}
}
