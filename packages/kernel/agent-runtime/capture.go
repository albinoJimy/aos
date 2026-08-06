package agentruntime

import (
	"context"

	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// CAPTURA DE NÃO-DETERMINISMO (AOS-016) — ponto de ligação ADITIVO.
//
// O loop base de AOS-013 grava o evento "turn.recorded" com o MANIFESTO (hashes,
// model-id/params/seed, versões pinadas) mas NÃO persiste os inputs
// não-determinísticos crus — a resposta do modelo (texto + tool calls) nem os
// outputs das tools. Sem eles o replay (AOS-016) consegue DETECTAR divergência
// (pelo prompt_hash) mas não RECONSTRUIR a trajectória.
//
// Este ficheiro fixa o PONTO DE LIGAÇÃO (interface opcional com default no-op)
// para que AOS-016 capture esses inputs de forma ADITIVA, sem alterar a forma do
// loop nem o contrato de AOS-013. O default [noopCapturer] não persiste nada — o
// comportamento de AOS-013 fica byte-idêntico quando nenhum capturer é injectado.
// A implementação real ([replay.EventStoreCapturer]) vive no subpacote replay.
// ---------------------------------------------------------------------------

// CapturedToolResult é o registo de UMA tool call despachada num turno, com a
// intenção original (a invocação pretendida pelo modelo) e o resultado observado
// (output untrusted + eventual erro de execução). É o que o replay precisa para
// devolver o resultado REGISTADO sem re-executar o efeito externo.
type CapturedToolResult struct {
	// Invocation é a tool call PRETENDIDA pelo modelo (ToolID/Capability/Input…).
	Invocation ToolInvocation
	// Result é o resultado devolvido ao loop (SEMPRE untrusted, ADR-005).
	Result Tainted
	// ToolError é o erro de execução da tool PERMITIDA (nil se não houve). É
	// materializado no tail do turno seguinte — o replay tem de o reproduzir para
	// o prompt_hash bater.
	ToolError error
	// Denial é a decisão SANITIZADA quando o RM NÃO permitiu a call (nil em permit).
	// Pela MESMA razão do ToolError: é materializada no tail, logo o replay tem de a
	// reproduzir ou o prompt_hash de qualquer run com uma negação divergiria.
	Denial *ToolDenial
}

// TurnCapture é o pacote de inputs não-determinísticos de um turno entregue ao
// [Capturer]: a resposta do modelo COMPLETA (texto + tool calls + uso + custo +
// final) e o resultado de cada tool call despachada. O relógio de captura é
// carimbado pelo próprio capturer (concern de captura, não do loop); o seed vem
// do manifesto por trajectória (model.seed).
type TurnCapture struct {
	RunID       string
	StepID      string
	Turn        int
	Response    ModelResponse
	ToolResults []CapturedToolResult
	Producer    eventstore.Producer
	// Subject é o TITULAR do run (o NHI-id do principal, ADR-003) sob cuja chave
	// POR-TITULAR o conteúdo não-determinístico é cifrado antes de tocar o Event
	// Store (AOS-093). Vazio ⇒ sem cifra por-titular (retro-compat). Não é PII de
	// conteúdo: é o identificador que localiza a KEK a destruir no crypto-shredding.
	Subject string
	// LeadingCorrection é a correcção de steer TRUSTED (AOS-023) que o loop injectou
	// no tail no FIM do turno ANTERIOR e que, por isso, faz parte do prompt DESTE
	// turno (ver o consumo do [SteerSource] em [Runtime.Run] e [tailFromCorrection]).
	// Capturá-la é o que torna o replay de um run STEERADO fiel (AOS-218): sem ela a
	// reconstrução do tail omitiria o segmento de correcção e o prompt_hash divergiria
	// espuriamente. Vazio ⇒ turno sem correcção pendente (retro-compat byte-idêntica).
	LeadingCorrection []byte
}

// ContentSealer cifra o CONTEÚDO PII de um run por chave POR-TITULAR (envelope
// DEK/KEK) ANTES de ele ser persistido no Event Store, e regista a ligação
// titular→stream para o alcance do crypto-shredding (AOS-093). A implementação
// concreta vive no composition root (que detém o KeyVault e o índice titular→
// partição) — o substrato depende só desta porta, não do platform/audit.
//
// Retro-compat: quando NENHUM sealer é injectado o conteúdo é persistido como
// antes (o comportamento de AOS-013/016 é byte-idêntico); a produção liga o sealer
// e o conteúdo passa a ser cifrado por-titular por omissão.
type ContentSealer interface {
	// SealContent cifra plaintext sob a KEK do titular (subject) e regista
	// subject→streamID no índice de partições do DSAR (para o hold/shred alcançarem
	// o substrato). Devolve o envelope serializável (opaco) a persistir no lugar do
	// texto-claro. FAIL-CLOSED: um erro aborta a escrita — nunca se persiste em claro
	// por baixo de um sealer activo.
	SealContent(ctx context.Context, subject, streamID string, plaintext []byte) (sealed []byte, err error)
}

// ContentOpener é o lado de LEITURA da cifra por-titular (AOS-093): decifra o que um
// [ContentSealer] selou. FAIL-CLOSED após crypto-shredding — se a KEK do titular foi
// destruída devolve erro e o conteúdo é irrecuperável. Usado na reconstrução durável
// (rebuild do step-ledger) para re-hidratar o resultado memorizado; um titular já
// apagado deixa de ser reconstruível, por desenho.
type ContentOpener interface {
	OpenContent(ctx context.Context, subject string, sealed []byte) (plaintext []byte, err error)
}

// ContentCipher combina as duas metades da cifra por-titular. É o que o step-ledger
// exige (sela na escrita, decifra no rebuild); o capturer só precisa do [ContentSealer].
type ContentCipher interface {
	ContentSealer
	ContentOpener
}

// Capturer persiste os inputs não-determinísticos de um turno para o replay
// determinístico (AOS-016). É o ponto de ligação ADITIVO: o default no-op não
// persiste nada, mantendo AOS-013 inalterado.
type Capturer interface {
	Capture(ctx context.Context, c TurnCapture) error
}

// noopCapturer é o default: não persiste nada. Preserva o comportamento de
// AOS-013 quando nenhum capturer é injectado.
type noopCapturer struct{}

func (noopCapturer) Capture(context.Context, TurnCapture) error { return nil }
