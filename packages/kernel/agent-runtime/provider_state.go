package agentruntime

// O ESTADO OPACO DO PROVIDER NUM TURNO (AOS-514, ADR-040).
//
// Um provider devolve, com a resposta de um turno, material que o runtime não interpreta e que
// o mesmo provider pode exigir de volta no turno seguinte: o raciocínio em todos os nomes em
// que veio, os blocos assinados e os redigidos, o id que deu a cada tool call. Até aqui só o
// primeiro campo de raciocínio chegava à captura ([ModelResponse.Reasoning]) e o resto
// perdia-se.
//
// [ProviderState] é esse material, num ENVELOPE de bytes que quem fez o pedido — o adaptador
// do gateway — constrói e o runtime transporta sem abrir:
//
//   - vai para a captura do turno, selado com o resto do conteúdo (a mesma cifra por titular);
//   - é referido no tail por DIGEST (o rótulo `state_digest`, layout 1.5.0), e é assim que o
//     `prompt_hash` se compromete com ele sem o conter;
//   - NÃO é devolvido ao provider por este ticket (isso é o AOS-515), NÃO entra em
//     `turn.recorded`, spans, métricas ou logs, e NÃO decide nada.
//
// É SAÍDA DO MODELO E DO PROVIDER: untrusted por definição (ADR-034). Não é resposta
// ([ModelResponse.Text] vem só do texto do turno — decisão D3), não é instrução, e não altera
// a autoridade do turno: o rótulo que o refere vai num segmento que já era untrusted.

// ProviderStateStatus diz o que o runtime tem do estado de um turno, num vocabulário FECHADO.
// O texto de cada valor é o que fica na captura.
type ProviderStateStatus string

const (
	// ProviderStateCaptured — o envelope está em [ProviderState.Bytes], inteiro.
	ProviderStateCaptured ProviderStateStatus = "capturado"
	// ProviderStateNotReturnable — o provider mandou estado e ele NÃO foi guardado: excedia o
	// tecto de bytes do turno, ou quem o capturava não o conseguiu fechar. Nunca se guarda
	// truncado — uma assinatura cortada é inválida. Não há bytes nem digest; o turno segue
	// como um turno sem estado, e fica a marca.
	ProviderStateNotReturnable ProviderStateStatus = "nao_devolvivel"
	// ProviderStateReference — só existe o digest: a captura foi feita em modo sensível, que
	// guarda uma referência no lugar do conteúdo. Para efeitos de devolução o estado conta
	// como inexistente.
	ProviderStateReference ProviderStateStatus = "referencia"
)

// MaxProviderStateBytes é o tecto ABSOLUTO, em bytes, do envelope de estado de um turno que o
// runtime aceita de um cliente de modelo. O tecto que o operador configura vive em quem faz o
// pedido e é menor ou igual a este; este existe porque o cliente de modelo é uma porta, e um
// que não aplicasse tecto nenhum poria na captura — um evento do Event Store, com o limite de
// mensagem do transporte — o que o provider quisesse mandar. 256 KiB: selado e serializado
// fica abaixo de metade do limite de 1 MiB por mensagem do NATS de produção.
const MaxProviderStateBytes = 256 << 10

// ProviderState é o estado opaco do provider num turno. Ver o comentário que abre este
// ficheiro.
type ProviderState struct {
	// Bytes é o envelope, tal como quem fez o pedido o construiu. O runtime não o abre.
	Bytes []byte
	// Digest é `sha256:` + o hash de Bytes. É o runtime que o calcula ([ProviderState.Normalizado])
	// — o que um cliente declare aqui não é lido —, excepto em [ProviderStateReference], onde
	// é tudo o que há e vem da captura. O envelope leva um nonce de 256 bits escolhido por
	// quem o constrói, pelo que o digest não deixa confirmar um palpite sobre um raciocínio
	// curto.
	Digest string
	// Status — ver [ProviderStateStatus].
	Status ProviderStateStatus
}

// Normalizado devolve o estado DENTRO do contrato, ou nil quando não há estado:
//
//   - [ProviderStateNotReturnable] fica só com a marca;
//   - bytes acima de [MaxProviderStateBytes] viram [ProviderStateNotReturnable] — nunca se
//     corta;
//   - bytes presentes ⇒ [ProviderStateCaptured], com o digest RECALCULADO;
//   - [ProviderStateReference] só vale com um digest da forma `sha256:` + 64 hexadecimais;
//   - tudo o resto (sem bytes, estado desconhecido) ⇒ nil.
//
// O loop chama-a à entrada de cada turno: daí para baixo o estado só tem uma destas formas, e
// é a mesma que a captura devolve na retoma e no replay.
func (s *ProviderState) Normalizado() *ProviderState {
	if s == nil {
		return nil
	}
	switch {
	case s.Status == ProviderStateNotReturnable:
		return &ProviderState{Status: ProviderStateNotReturnable}
	case len(s.Bytes) > MaxProviderStateBytes:
		return &ProviderState{Status: ProviderStateNotReturnable}
	case len(s.Bytes) > 0:
		return &ProviderState{
			Bytes:  append([]byte(nil), s.Bytes...),
			Digest: sha256Tagged(s.Bytes),
			Status: ProviderStateCaptured,
		}
	case s.Status == ProviderStateReference:
		if d := NormalizeRouteProfileDigest(s.Digest); d != "" {
			return &ProviderState{Digest: d, Status: ProviderStateReference}
		}
	}
	return nil
}

// TailDigest devolve o digest com que o tail refere este estado, ou vazio quando o turno não
// tem estado a referir (nil, ou [ProviderStateNotReturnable]). Só vale sobre um estado
// [ProviderState.Normalizado].
func (s *ProviderState) TailDigest() string {
	if s == nil {
		return ""
	}
	return s.Digest
}
