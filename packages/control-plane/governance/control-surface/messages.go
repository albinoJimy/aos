package controlsurface

import (
	"encoding/json"

	"github.com/aos-ref/kernel/agent-runtime/control"
)

// Kind é o discriminador do CONTRATO ÚNICO de mensagens de controlo (AC1). Os quatro
// kinds são o vocabulário estável, INDEPENDENTE DO CANAL, que qualquer superfície
// (desktop/chatbot/API) emite. Os nomes são estáveis (namespaced "control.*") para
// que os adaptadores de plataforma (AOS-122) se liguem a strings, não à
// implementação.
type Kind string

const (
	// KindInterrupt pede a PAUSA GRACIOSA do run no fim do turno. Traduz-se em
	// [control.SteerChannel.Pause] — marca a pausa PENDENTE (sinal sempre aceite,
	// mesmo a meio do turno); a transição running→paused materializa-se no fim do
	// turno via [control.SteerChannel.GracefulPause] (delegada no runtime).
	KindInterrupt Kind = "control.interrupt"
	// KindSteer injecta uma CORRECÇÃO autenticada. Traduz-se em
	// [control.SteerChannel.Steer] — a correcção fica pendente e entra na retoma como
	// instrução CONFIÁVEL (control-plane), nunca como dado untrusted.
	KindSteer Kind = "control.steer"
	// KindResume RETOMA um run paused. Traduz-se em [control.SteerChannel.Resume]
	// (paused→running via o [control.StateGate]). Se a mensagem trouxer uma correcção,
	// a superfície injecta-a primeiro (steer) e só depois retoma — o "resume com
	// payload de correcção" do contrato.
	KindResume Kind = "control.resume"
	// KindState é a QUERY do estado durável corrente do run. É uma LEITURA pura —
	// nenhuma escrita, nenhum sinal — reflectida do read-model (running/paused/
	// waiting_on_human/...).
	KindState Kind = "control.state"
)

// ChannelID identifica a SUPERFÍCIE de onde a mensagem vem (desktop/chatbot/API). É
// presentation: um rótulo de observabilidade (entra no span de interacção como
// aos.control.channel), NÃO afecta a tradução — o protocolo é idêntico em todos os
// canais; muda só o adaptador. Opcional.
type ChannelID string

const (
	// ChannelDesktop — superfície de desktop.
	ChannelDesktop ChannelID = "desktop"
	// ChannelChatbot — superfície de chatbot.
	ChannelChatbot ChannelID = "chatbot"
	// ChannelAPI — superfície de API/programática.
	ChannelAPI ChannelID = "api"
)

// ControlMessage é o CONTRATO ÚNICO (AC1): uma união versionada, independente do
// canal, para steer / interrupt / resume(correction) / state. Tem um codec JSON
// estável (round-trip) e uma [ControlMessage.Validate] fail-closed. Os nomes de
// campo são estáveis para os adaptadores.
//
// A identidade do emissor viaja como (EmitterID, Signature) — o material que o
// [control.Authenticator] de AOS-023 verifica. A Signature é o campo que dá o
// NÃO-REPÚDIO e a FRONTEIRA out-of-band: conteúdo untrusted não a consegue produzir,
// pelo que a sua mensagem é rejeitada na autenticação e nunca vira sinal.
type ControlMessage struct {
	// SchemaVersion é a versão SemVer do contrato desta mensagem (AC5). Validada
	// contra a versão corrente da superfície (MAJOR compatível).
	SchemaVersion string `json:"schema_version"`
	// Kind é o discriminador (um dos quatro do contrato).
	Kind Kind `json:"kind"`
	// RunID é a âncora ao run (stream_id no Event Store).
	RunID string `json:"run_id"`
	// Channel é a superfície de origem (presentation/observabilidade). Opcional.
	Channel ChannelID `json:"channel,omitempty"`
	// EmitterID é a identidade autenticada de quem emite (a NHI do operador/serviço).
	// Obrigatório para steer/interrupt/resume (não-repúdio).
	EmitterID string `json:"emitter_id,omitempty"`
	// Signature é a assinatura do emissor sobre (run_id ‖ kind ‖ payload), verificada
	// pelo [control.Authenticator]. base64 no wire (codec de []byte). Uma assinatura
	// ausente/inválida ⇒ sinal rejeitado (fronteira de segurança). No steer cobre o
	// sinal steer+correcção; no interrupt/resume cobre o sinal pause/resume (payload
	// nil).
	Signature []byte `json:"signature,omitempty"`
	// Correction é o payload de correcção. Obrigatório no steer; OPCIONAL no resume
	// (um resume sem correcção é uma retoma limpa; com correcção é o "resume com
	// payload de correcção" do contrato — a superfície injecta-a como steer autenticado
	// ANTES de retomar). Ignorado noutros kinds.
	Correction []byte `json:"correction,omitempty"`
	// CorrectionSignature autentica a INJECÇÃO da correcção inline de um resume — a
	// assinatura do emissor sobre o sinal STEER dessa correcção (run_id ‖ "steer" ‖
	// correction). É um segundo material de assinatura porque o resume-com-correcção é,
	// no mecanismo de AOS-023, DUAS operações autenticadas distintas (Steer, depois
	// Resume), cada uma assinada sobre o seu próprio (kind ‖ payload). Obrigatória sse
	// (e só se) um resume traz Correction; ignorada nos demais casos.
	CorrectionSignature []byte `json:"correction_signature,omitempty"`
}

// Emitter projecta a identidade da mensagem no contrato [control.Emitter] de AOS-023
// (o que o [control.Authenticator] verifica) para o SINAL do próprio kind. É a ponte
// que mantém o contrato do wire desacoplado do tipo interno do canal.
func (m ControlMessage) Emitter() control.Emitter {
	return control.Emitter{ID: m.EmitterID, Signature: m.Signature}
}

// correctionEmitter projecta a identidade para a INJECÇÃO da correcção inline de um
// resume — usa a [ControlMessage.CorrectionSignature] (assinatura sobre o sinal steer
// da correcção), não a assinatura do sinal resume.
func (m ControlMessage) correctionEmitter() control.Emitter {
	return control.Emitter{ID: m.EmitterID, Signature: m.CorrectionSignature}
}

// signalKind mapeia o kind do contrato ao [control.SignalKind] de AOS-023 (o
// vocabulário que o autenticador e o log de controlo usam). O state não é um sinal
// (é leitura) — devolve ("", false).
func (m ControlMessage) signalKind() (control.SignalKind, bool) {
	switch m.Kind {
	case KindInterrupt:
		return control.SignalPause, true
	case KindSteer:
		return control.SignalSteer, true
	case KindResume:
		return control.SignalResume, true
	default:
		return "", false
	}
}

// signalLabel devolve o rótulo do sinal para o span de interacção (aos.control.signal).
// Reusa o vocabulário de [control.SignalKind] (pause/steer/resume) e nomeia a query
// como "state".
func (m ControlMessage) signalLabel() string {
	if sk, ok := m.signalKind(); ok {
		return string(sk)
	}
	return "state"
}

// mutates indica se o kind altera o run (exige emissor e autenticação), por oposição
// à leitura pura do state.
func (k Kind) mutates() bool {
	switch k {
	case KindInterrupt, KindSteer, KindResume:
		return true
	default:
		return false
	}
}

// known indica se k é um dos quatro kinds do contrato.
func (k Kind) known() bool {
	return k == KindInterrupt || k == KindSteer || k == KindResume || k == KindState
}

// Validate verifica a mensagem contra o schema VERSIONADO, fail-closed (AC1/AC5). É a
// primeira barreira da superfície: uma mensagem que não passe aqui NUNCA é traduzida
// num sinal. Verifica, por esta ordem:
//
//   - schema_version parseável (SemVer) e COMPATÍVEL com current (mesmo MAJOR);
//   - run_id não-vazio;
//   - kind conhecido;
//   - steer/interrupt/resume trazem emitter_id (não-repúdio);
//   - steer traz correction (uma correcção vazia não é instrução).
//
// A autenticação da assinatura NÃO é feita aqui — é a fronteira do [control.SteerChannel]
// (só o canal conhece o [control.Authenticator]); Validate é a barreira de SCHEMA.
func (m ControlMessage) Validate(current ControlSchemaVersion) error {
	v, err := ParseControlSchemaVersion(m.SchemaVersion)
	if err != nil {
		return err
	}
	if !current.Compatible(v) {
		return ErrIncompatibleSchemaVersion
	}
	if m.RunID == "" {
		return ErrEmptyRunID
	}
	if !m.Kind.known() {
		return ErrUnknownKind
	}
	if m.Kind.mutates() && m.EmitterID == "" {
		return ErrEmptyEmitter
	}
	if m.Kind == KindSteer && len(m.Correction) == 0 {
		return ErrEmptyCorrection
	}
	// Resume com correcção inline exige a assinatura da injecção (a segunda operação
	// autenticada). Fail-closed: sem ela, a correcção não poderia ser injectada como
	// steer autenticado.
	if m.Kind == KindResume && len(m.Correction) > 0 && len(m.CorrectionSignature) == 0 {
		return ErrEmptyCorrectionSignature
	}
	return nil
}

// Encode serializa a mensagem para JSON estável (codec do contrato). Round-trip com
// [DecodeMessage].
func (m ControlMessage) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// DecodeMessage descodifica uma mensagem do wire JSON. NÃO valida (a validação é
// [ControlMessage.Validate], separada) — descodificar e validar são passos distintos
// para que um adaptador possa inspeccionar uma mensagem malformada antes de a rejeitar.
func DecodeMessage(b []byte) (ControlMessage, error) {
	var m ControlMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return ControlMessage{}, err
	}
	return m, nil
}

// NewInterrupt constrói uma mensagem control.interrupt na versão corrente (helper de
// adaptador).
func NewInterrupt(runID string, ch ChannelID, emitterID string, sig []byte) ControlMessage {
	return ControlMessage{SchemaVersion: CurrentVersion.String(), Kind: KindInterrupt, RunID: runID, Channel: ch, EmitterID: emitterID, Signature: sig}
}

// NewSteer constrói uma mensagem control.steer na versão corrente.
func NewSteer(runID string, ch ChannelID, emitterID string, sig, correction []byte) ControlMessage {
	return ControlMessage{SchemaVersion: CurrentVersion.String(), Kind: KindSteer, RunID: runID, Channel: ch, EmitterID: emitterID, Signature: sig, Correction: correction}
}

// NewResume constrói uma mensagem control.resume LIMPA (sem correcção inline) na
// versão corrente. sig assina o sinal resume (run_id ‖ "resume" ‖ nil). A retoma
// aplica na mesma qualquer correcção JÁ steered pendente.
func NewResume(runID string, ch ChannelID, emitterID string, sig []byte) ControlMessage {
	return ControlMessage{SchemaVersion: CurrentVersion.String(), Kind: KindResume, RunID: runID, Channel: ch, EmitterID: emitterID, Signature: sig}
}

// NewResumeWithCorrection constrói um resume que injecta uma correcção inline antes de
// retomar. resumeSig assina o sinal resume (run_id ‖ "resume" ‖ nil) e correctionSig
// assina a injecção steer (run_id ‖ "steer" ‖ correction) — as DUAS operações
// autenticadas que o mecanismo de AOS-023 exige.
func NewResumeWithCorrection(runID string, ch ChannelID, emitterID string, resumeSig, correction, correctionSig []byte) ControlMessage {
	return ControlMessage{SchemaVersion: CurrentVersion.String(), Kind: KindResume, RunID: runID, Channel: ch, EmitterID: emitterID, Signature: resumeSig, Correction: correction, CorrectionSignature: correctionSig}
}

// NewStateQuery constrói uma mensagem control.state (query de estado) na versão
// corrente.
func NewStateQuery(runID string, ch ChannelID) ControlMessage {
	return ControlMessage{SchemaVersion: CurrentVersion.String(), Kind: KindState, RunID: runID, Channel: ch}
}
