package eventstore

import (
	crand "crypto/rand"
	"time"
)

// crockford é o alfabeto Crockford base32 (sem I, L, O, U) usado por ULID.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID gera um ULID de 128 bits: 48 bits de timestamp (ms desde epoch) +
// 80 bits de aleatoriedade criptográfica, codificado em 26 caracteres Crockford
// base32. Implementação inline (só stdlib), sem dependências externas.
//
// O ULID serve apenas de identificador globalmente único do evento; a ordem
// total é dada por seq, nunca pelo ULID nem pelo relógio.
func newULID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// 10 bytes (80 bits) de aleatoriedade. crypto/rand.Read nunca falha
	// parcialmente sem erro; em caso de erro (impossível em prática) o array fica
	// a zero, o que ainda produz um id válido — mas propagamos via panic seria
	// pior; mantemos determinístico e simples.
	_, _ = crand.Read(b[6:])
	return encodeULID(b)
}

// encodeULID codifica 128 bits em 26 caracteres base32 Crockford, MSB primeiro,
// com 2 bits de padding à esquerda (128 + 2 = 130 = 26 × 5).
func encodeULID(b [16]byte) string {
	out := make([]byte, 26)
	for i := 0; i < 26; i++ {
		var v byte
		for j := 0; j < 5; j++ {
			bitpos := i*5 + j - 2 // 2 bits de padding à esquerda
			var bit byte
			if bitpos >= 0 {
				bit = (b[bitpos/8] >> (7 - uint(bitpos%8))) & 1
			}
			v = v<<1 | bit
		}
		out[i] = crockford[v]
	}
	return string(out)
}
