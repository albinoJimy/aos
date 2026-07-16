package messaging

import (
	"encoding/binary"
	"sort"
	"time"
)

// canonicalDomain é o separador de domínio versionado prefixado à serialização
// canónica da mensagem, para que uma assinatura de mensagem inter-agente nunca
// colida com a de outro subsistema (ex.: o token NHI ou a cadeia de audit) e para
// permitir evolução do formato sem ambiguidade. Passou a v2 quando o material
// ANTI-REPLAY (Nonce + IssuedAt) entrou na serialização assinada: o bump garante
// que uma assinatura v1 (sem frescura) nunca é comparada como igual a uma v2.
const canonicalDomain = "aos.messaging.v2"

// nonceMinLen é o comprimento mínimo (em bytes) do Nonce anti-replay. 16 bytes
// (128 bits) de entropia tornam a colisão/adivinhação de nonces impraticável; um
// nonce mais curto é rejeitado como mensagem inválida (fail-closed).
const nonceMinLen = 16

// Reference é o item referenciado por uma mensagem inter-agente (ex.: o resumo ou
// sub-resultado de um sub-agente sobre o qual o receptor é pedido para agir). A
// verificação exige que o item EXISTA e seja AUTÊNTICO: [Reference.Hash] é o hash
// de conteúdo esperado, reconciliado contra o hash autêntico resolvido pelo
// [ReferenceResolver]. Uma referência fabricada (id inexistente) ou adulterada
// (hash divergente) é rejeitada — é esta a fronteira que distingue "o ID existe"
// de "a referência é autêntica".
type Reference struct {
	// ID identifica o item referenciado.
	ID string
	// Hash é o hash de conteúdo esperado do item. Coberto pela assinatura e
	// reconciliado com o hash autêntico na verificação.
	Hash []byte
}

// Message é uma mensagem inter-agente. Os metadados canónicos de ORIGEM (Origin),
// AUTORIDADE (Authority), REFERÊNCIA (Reference), a acção pedida (Action) e o
// material anti-replay (Nonce, IssuedAt), mais o corpo (Payload), são TODOS
// cobertos pela assinatura ([Signature]) — adulterar qualquer um invalida a
// assinatura. [Signature] é preenchida por [SignMessage] e verificada por
// [Verifier.Verify].
type Message struct {
	// Origin é a NHI do emissor CLAMADO (quem a mensagem DIZ ser). A verificação
	// valida a assinatura contra a chave pública PINADA desta NHI: um emissor
	// forjado (que clama ser Origin mas assinou com outra chave) não valida.
	Origin string
	// Authority são as capabilities que o emissor CLAMA ter para justificar a
	// acção. Coberto pela assinatura; a verificação reconcilia contra a autoridade
	// AUTORITATIVA da NHI (a mensagem não se pode auto-conceder autoridade).
	Authority []string
	// Action é a acção/capability que a mensagem pede ao receptor para executar. A
	// verificação exige que esteja coberta pela autoridade autoritativa do emissor.
	Action string
	// Nonce é um valor único (>= [nonceMinLen] bytes aleatórios) por mensagem,
	// COBERTO pela assinatura. É a base da deduplicação anti-replay: o Verifier
	// consome cada par (Origin, Nonce) UMA só vez — reenviar uma mensagem legítima
	// capturada re-apresenta o mesmo nonce e é rejeitado ([ErrReplayedNonce]).
	Nonce []byte
	// IssuedAt é o instante de emissão, COBERTO pela assinatura. O Verifier impõe
	// sobre ele uma janela de frescura: uma mensagem demasiado antiga (replay de
	// uma captura antiga) ou com timestamp futuro além do skew tolerado é rejeitada
	// ([ErrStaleMessage]). Sem este campo a assinatura autenticaria a ORIGEM mas não
	// a FRESCURA, deixando aberto o vetor de captura-e-reenvio.
	IssuedAt time.Time
	// Reference é o item referenciado (resumo/sub-resultado). A verificação exige
	// que exista e seja autêntico.
	Reference Reference
	// Payload é o corpo opaco da mensagem.
	Payload []byte
	// Signature é a assinatura ed25519 do emissor sobre a serialização canónica de
	// TODOS os campos acima (excepto a própria Signature). Preenchida por
	// [SignMessage].
	Signature []byte
}

// canonicalAuthority devolve uma cópia ordenada e sem duplicados de authority,
// para que a serialização canónica seja INDEPENDENTE da ordem em que o emissor
// listou as capabilities (a autoridade é um conjunto, não uma sequência).
func canonicalAuthority(authority []string) []string {
	if len(authority) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(authority))
	out := make([]string, 0, len(authority))
	for _, a := range authority {
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// canonicalBytes devolve a serialização canónica, determinística e estável
// cross-SO dos campos ASSINÁVEIS da mensagem (tudo excepto Signature). Signer e
// Verifier constroem exactamente estes bytes; qualquer divergência de um campo
// produz bytes distintos e invalida a assinatura.
//
// Determinismo: domínio versionado à cabeça, ordem de campos fixa, strings e
// blobs com length-prefixing (uvarint), autoridade ordenada e deduplicada. Sem
// mapas nem ordenação dependente de runtime.
func canonicalBytes(m Message) []byte {
	buf := make([]byte, 0, 256)
	buf = putString(buf, canonicalDomain)
	buf = putString(buf, m.Origin)
	// Material anti-replay coberto pela assinatura: o nonce (base da dedup) e o
	// instante de emissão (base da janela de frescura). O timestamp é serializado em
	// UnixNano UTC de largura fixa (determinístico, sem fuso nem componente
	// monotónica), à imagem da cadeia de audit.
	buf = putBytes(buf, m.Nonce)
	buf = putInt64(buf, m.IssuedAt.UTC().UnixNano())
	auth := canonicalAuthority(m.Authority)
	buf = putUint64(buf, uint64(len(auth)))
	for _, a := range auth {
		buf = putString(buf, a)
	}
	buf = putString(buf, m.Action)
	buf = putString(buf, m.Reference.ID)
	buf = putBytes(buf, m.Reference.Hash)
	buf = putBytes(buf, m.Payload)
	return buf
}

// putUint64 escreve um uint64 em big-endian (8 bytes, largura fixa).
func putUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

// putInt64 escreve um int64 em big-endian (8 bytes, largura fixa). Usado para o
// timestamp UnixNano da mensagem (pode ser negativo antes de 1970).
func putInt64(buf []byte, v int64) []byte {
	return putUint64(buf, uint64(v))
}

// putBytes escreve um blob com length-prefix (uvarint) seguido dos bytes.
func putBytes(buf []byte, b []byte) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(b)))
	buf = append(buf, lb[:n]...)
	return append(buf, b...)
}

// putString escreve uma string com length-prefix (uvarint) seguido dos bytes.
func putString(buf []byte, s string) []byte {
	return putBytes(buf, []byte(s))
}
