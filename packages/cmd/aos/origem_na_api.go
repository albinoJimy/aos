package main

// A ORIGEM DA SAÍDA NA API DO NÓ (AOS-498, ADR-038 §2.1 e §2.6).
//
// O AOS-497 pôs o kernel a designar e a selar a origem da saída de um run — o resultado da
// chamada efectiva da tool declarada —, mas a declaração só entrava por código e a âncora não
// saía do nó. Este ficheiro é a superfície HTTP dela:
//
//   - o `POST /runs` aceita `output_from_tool` e `output_source_binding`, e valida-os contra a
//     lista-branca do mesmo pedido;
//   - o `GET /tools` anuncia que o nó os aceita, para quem submete saber ANTES de submeter;
//   - o `GET /runs/{id}` devolve a âncora (`output_source`) e, com a origem designada, os bytes
//     do resultado (`output`), nos dois ramos.
//
// DE ONDE VÊM OS BYTES. Do step-ledger, no log, nos DOIS ramos (ADR-038 §2.3, «A fonte dos
// bytes»): é onde o digest selado confere em todas as vidas do run. A captura do turno não os
// tem quando o turno designado mudou de desfecho entre vidas, e o registo de desfechos em memória
// nunca os teve — o kernel não guarda resultados, só a âncora. Conferem-se SEMPRE contra o digest
// da âncora antes de saírem; se não conferirem, não saem.
//
// O QUE NÃO MUDA. Um run sem origem declarada responde os bytes de antes: os três campos novos só
// existem com âncora. `final_text` continua a sair, como sempre.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"unicode/utf8"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Mensagens das recusas da declaração de origem no `POST /runs`. São do pedido, e nenhuma repete
// um valor que veio do corpo.
const (
	erroVinculoSemOrigem      = "output_source_binding sem output_from_tool"
	erroOrigemSemVinculo      = "output_from_tool exige output_source_binding (measure ou binding)"
	erroVinculoDesconhecido   = "output_source_binding desconhecido (aceites: measure, binding)"
	erroOrigemComNomeInvalido = "output_from_tool nao tem a forma de um nome de tool (ate 128 bytes, sem espacos nem caracteres de controlo)"
	erroOrigemSemListaBranca  = "output_from_tool exige a lista-branca do run (tools)"
	erroOrigemForaDaLista     = "output_from_tool nomeia uma tool fora da lista-branca do run (tools)"
)

// validarOrigemDaSaida verifica a declaração de origem de um `POST /runs` contra a lista-branca
// do MESMO pedido. Devolve a mensagem da recusa, ou vazio.
//
// A ORDEM É A DAS CAUSAS: primeiro a declaração em si (as duas metades vêm juntas, e o vínculo
// é do vocabulário), depois o nome, depois a pertença. São as condições com que o kernel recusa o
// arranque ([agentruntime.ErrBadOutputSourceBinding], [agentruntime.ErrImpossibleOutputSource]),
// decididas aqui onde se podem decidir sem o tool set — para quem compôs mal o pedido receber um
// 400 com a causa, e não um run `failed`.
//
// A FORMA DO NOME pergunta-se à função do selo ([agentruntime.OutputSource.BemFormada]): a porta e
// o kernel não podem discordar sobre o que é um nome admissível.
//
// SEM LISTA-BRANCA NÃO HÁ ORIGEM, pela razão do contrato ([validarContratoDeConclusao]). A
// pertença decide-se por [referencemonitor.RunAllowsTool], comparação exacta.
//
// O QUE ISTO NÃO VÊ: se o nó oferece a tool. Essa metade é do kernel, no arranque do run, e chega
// a quem submete pelo `GET /runs/{id}` (`failed`, com o erro).
func validarOrigemDaSaida(origem, vinculo string, listaBranca []string) string {
	switch {
	case origem == "" && vinculo == "":
		return ""
	case origem == "":
		return erroVinculoSemOrigem
	case vinculo == "":
		return erroOrigemSemVinculo
	}
	conhecido := false
	for _, v := range agentruntime.OutputSourceBindings() {
		if string(v) == vinculo {
			conhecido = true
		}
	}
	if !conhecido {
		return erroVinculoDesconhecido
	}
	forma := agentruntime.OutputSource{Tool: origem, Binding: agentruntime.OutputSourceBinding(vinculo), State: agentruntime.OutputSourceMissing}
	if !forma.BemFormada() {
		return erroOrigemComNomeInvalido
	}
	if listaBranca == nil {
		return erroOrigemSemListaBranca
	}
	if !referencemonitor.RunAllowsTool(listaBranca, origem) {
		return erroOrigemForaDaLista
	}
	return ""
}

// anuncioDaOrigem é o que o `GET /tools` diz sobre a origem da saída. A PRESENÇA do objecto é o
// anúncio: este nó aceita `output_from_tool` no `POST /runs` e devolve a âncora no
// `GET /runs/{id}`. Um nó anterior não o devolve, e quem submete não envia o campo — o decoder
// desse nó recusava-o com 400.
type anuncioDaOrigem struct {
	// Bindings são os vínculos que o nó aceita, pela ordem do kernel.
	Bindings []agentruntime.OutputSourceBinding `json:"bindings"`
	// MaxBytes é o tecto de transporte de `output`: um resultado designado maior do que isto
	// responde só com os metadados (`output_omitted:"too_large"`).
	MaxBytes int `json:"max_bytes"`
}

// anuncioDaOrigemDoNo compõe o anúncio, ou nil. Um nó com o veredicto desligado NÃO anuncia: em
// `off` o kernel não lê a declaração e nenhum run tem âncora (ADR-038 §5), e anunciar era
// prometer o que o nó não faz.
func anuncioDaOrigemDoNo(n *Node) *anuncioDaOrigem {
	if n != nil && n.completionVerdict == agentruntime.CompletionOff {
		return nil
	}
	return &anuncioDaOrigem{Bindings: agentruntime.OutputSourceBindings(), MaxBytes: maxPlanInputBytes}
}

// Porque é que `output` não veio com uma âncora designada — o `output_omitted` do
// `GET /runs/{id}`. Vocabulário FECHADO.
const (
	// saidaOmitidaGrande — o resultado designado excede o tecto de transporte. Não se trunca.
	saidaOmitidaGrande = "too_large"
	// saidaOmitidaNaoTexto — o resultado designado não é UTF-8 válido: não cabe numa string JSON
	// sem o alterar.
	saidaOmitidaNaoTexto = "not_utf8"
	// saidaOmitidaDeVez — os bytes não se lêem e não se vão ler: titular apagado, passo fora do
	// step-ledger, registo do ledger ilegível ou em claro, âncora cujo passo não forma chave,
	// bytes que não conferem com o digest selado, nó sem o gate de leitura.
	saidaOmitidaDeVez = "unavailable"
	// saidaOmitidaAgora — os bytes não se leram NESTE MOMENTO (custódia fechada, log que não
	// leu). Só num run com o vínculo «só medição»; com o vínculo vinculativo o `GET` responde 503.
	saidaOmitidaAgora = "unavailable_now"
)

// errOrigemNaoConfere — os bytes lidos do step-ledger não são os que o kernel selou: o digest ou
// o tamanho não batem com a âncora. Conta como captura corrompida, e é definitivo.
var errOrigemNaoConfere = errors.New("aos: os bytes do passo designado nao conferem com o digest selado na ancora da saida")

// origemIndisponivelDeVez diz se o erro de [apiHandler.bytesDesignados] é DEFINITIVO. A lista é
// FECHADA: a da saída durável ([saidaDuravelIndisponivelDeVez], AOS-494) e os erros que só esta
// leitura produz. Tudo o resto é transitório.
//
// O REGISTO ILEGÍVEL E A CHAVE INVÁLIDA SÃO DEFINITIVOS ([durable.ErrAppliedResultUnreadable];
// revisão do AOS-498, M1). Caíam do lado transitório por omissão, e um log danificado ou um
// `step_id` de âncora que não forma chave não passam com o tempo: em `measure` o `GET` dizia
// `unavailable_now` para sempre, e em `binding` respondia 503 para sempre — quem sonda ficava à
// espera até ao prazo do plano, sem causa. O registo em claro ([durable.ErrAppliedResultInClear])
// é da mesma família: é o log que é assim.
func origemIndisponivelDeVez(err error) bool {
	if errors.Is(err, durable.ErrConteudoIndisponivel) {
		return false
	}
	return errors.Is(err, durable.ErrAppliedResultNotFound) ||
		errors.Is(err, durable.ErrAppliedResultUnreadable) ||
		errors.Is(err, durable.ErrAppliedResultInClear) ||
		errors.Is(err, errOrigemNaoConfere) ||
		saidaDuravelIndisponivelDeVez(err)
}

// openerDoLeitor é o opener por-titular do nó ATRÁS do escopo do leitor (revisão do AOS-498, M3).
// É a mesma segunda linha que a reconstrução soberana tem ([apiHandler.newReaderReplayEngine] →
// o gate do opener no motor de replay): o conteúdo só abre para um accessor com um principal e
// com o escopo soberano de conteúdo.
//
// A âncora de negação continua a ser o gate D7 do `GET` ([apiHandler.admitSovereignRead]). Isto
// existe para que a legitimidade da leitura não seja só POSICIONAL — «quem chama fez o gate
// antes» —: um caminho que chegue aqui sem um leitor autenticado na mão não abre nada.
type openerDoLeitor struct {
	dentro   agentruntime.ContentOpener
	accessor replay.Accessor
}

// OpenContent abre o conteúdo selado se, e só se, o accessor tem principal e o escopo soberano.
// A recusa é a do motor de replay ([replay.ErrPayloadAccessDenied]), e é definitiva: é
// composição, não momento.
func (o openerDoLeitor) OpenContent(ctx context.Context, subject string, sealed []byte) ([]byte, error) {
	comEscopo := false
	for _, e := range o.accessor.Scopes {
		if e == replay.DefaultSovereignContentScope {
			comEscopo = true
		}
	}
	if o.dentro == nil || o.accessor.Principal == "" || !comEscopo {
		return nil, replay.ErrPayloadAccessDenied
	}
	return o.dentro.OpenContent(ctx, subject, sealed)
}

// bytesDesignados lê do step-ledger, no LOG, os bytes do passo que a âncora designa, e confere-os
// contra ela. Devolve-os só se conferirem.
//
// # Porque é legítimo ler aqui, e quando não se lê
//
// É a regra da saída durável ([apiHandler.saidaDuravel]): o leitor já passou o gate D7 deste
// `GET` e a leitura já foi selada no WORM. Sem o gate soberano de leitura, sem Event Store ou sem
// a cifra por-titular, não se decifra conteúdo de um titular para um chamador que ninguém
// autenticou — vale também para o ramo em memória, que hoje entrega o `final_text` sem gate
// porque o tem em claro, e não porque o decifre.
//
// O LEITOR VEM NA MÃO (`reader`), como no caminho soberano existente: o opener só abre atrás do
// escopo dele ([openerDoLeitor]). E a leitura só devolve conteúdo que o opener abriu
// ([durable.ReadAppliedResult]): um registo em claro não sai.
//
// # A conferência
//
// `sha256(bytes)` e o tamanho contra a âncora selada. O `result_hash` do registo é do mesmo log
// que os bytes e não prova nada; a âncora vem da transição terminal, que o kernel escreveu.
func (h *apiHandler) bytesDesignados(ctx context.Context, reader readerIdentity, runID string, ancora *agentruntime.OutputSource) ([]byte, error) {
	if h.readGov == nil || h.node == nil || h.node.EventStore == nil || h.node.contentOpener == nil {
		return nil, errSaidaDuravelSemGate
	}
	opener := openerDoLeitor{dentro: h.node.contentOpener, accessor: replay.Accessor{
		Principal: reader.principal,
		Scopes:    []string{replay.DefaultSovereignContentScope},
	}}
	bytesDoPasso, err := durable.ReadAppliedResult(ctx, h.node.EventStore, opener, runID, ancora.StepID)
	if err != nil {
		return nil, err
	}
	soma := sha256.Sum256(bytesDoPasso)
	digest := "sha256:" + hex.EncodeToString(soma[:])
	if len(bytesDoPasso) != ancora.Bytes || subtle.ConstantTimeCompare([]byte(digest), []byte(ancora.Digest)) != 1 {
		return nil, errOrigemNaoConfere
	}
	return bytesDoPasso, nil
}

// origemNaResposta escreve na resposta do `GET /runs/{id}` a âncora da saída e, quando a origem
// ficou designada num run CONCLUÍDO, os bytes do resultado. Os dois ramos do `GET` chamam esta
// função, DEPOIS do selo de leitura sensível: é ele a pré-condição de abrir conteúdo. `reader` é
// o leitor que o gate D7 deste `GET` admitiu.
//
// Devolve false quando quem chama tem de responder 503 e não escrever desfecho nenhum.
//
// # O vínculo decide como a falta dos bytes se manifesta
//
// O vínculo diz o que os bytes SÃO para aquele run:
//
//   - `binding` — são a saída do run. Não os conseguir servir é não conseguir servir a saída: de
//     vez, `output_unavailable`; por instantes, 503. As regras do AOS-494. «De vez» inclui o log
//     danificado e a âncora cujo passo não forma chave ([origemIndisponivelDeVez]).
//   - `measure` — são medição. A saída do run continua a ser o texto final, e a falta dos bytes
//     medidos nunca muda o que o `GET` diz sobre ele: nem `output_unavailable` (quem lê fecha o
//     nó do plano `failed`), nem 503 (quem lê fica à espera). Fica só o `output_omitted`.
//
// Em ambos, `output_omitted` diz a causa num vocabulário fechado, e a âncora sai sempre.
//
// # Só num run concluído
//
// Um run `failed` pode ter uma âncora designada (o contrato não se cumpriu por outra tool, o
// turno terminal foi cortado). Não concluiu, e não tem saída: saem os metadados, sem bytes — a
// regra do `final_text`.
func (h *apiHandler) origemNaResposta(r *http.Request, reader readerIdentity, resp *runStateResponse, ancora *agentruntime.OutputSource, concluido bool) bool {
	if ancora == nil {
		return true
	}
	copia := *ancora
	resp.OutputSource = &copia
	if !concluido || ancora.State != agentruntime.OutputSourceDesignated {
		return true
	}
	if ancora.Bytes > maxPlanInputBytes {
		// O tamanho vem da âncora: acima do tecto nem se decifra.
		resp.OutputOmitted = saidaOmitidaGrande
		return true
	}
	bytesDoPasso, err := h.bytesDesignados(r.Context(), reader, resp.RunID, ancora)
	vinculativa := ancora.Binding == agentruntime.OutputSourceBinds
	switch {
	case err == nil && !utf8.Valid(bytesDoPasso):
		resp.OutputOmitted = saidaOmitidaNaoTexto
	case err == nil:
		texto := string(bytesDoPasso)
		resp.Output = &texto
	case origemIndisponivelDeVez(err):
		resp.OutputOmitted = saidaOmitidaDeVez
		if vinculativa {
			resp.OutputUnavailable = true
		}
		// A causa fica no log do operador, sem conteúdo: nenhum destes erros o leva.
		h.logf("GET /runs/%q (AOS-498): os bytes da origem designada NAO se leem do step-ledger, de vez — responde output_omitted=%s (vinculo %s): %v", resp.RunID, saidaOmitidaDeVez, ancora.Binding, err)
	case vinculativa:
		h.logf("GET /runs/%q (AOS-498): os bytes da origem designada NAO se leem do step-ledger NESTE MOMENTO, e o vinculo e vinculativo — responde 503: %v", resp.RunID, err)
		return false
	default:
		resp.OutputOmitted = saidaOmitidaAgora
		h.logf("GET /runs/%q (AOS-498): os bytes da origem designada NAO se leem do step-ledger NESTE MOMENTO — o vinculo e so medicao, o desfecho do run sai como sempre com output_omitted=%s: %v", resp.RunID, saidaOmitidaAgora, err)
	}
	return true
}
