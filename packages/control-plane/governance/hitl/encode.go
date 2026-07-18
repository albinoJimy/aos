package hitl

import "encoding/binary"

// Serialização canónica, determinística e estável cross-SO — o mesmo molde
// length-prefixed usado por messaging/audit (domínio versionado à cabeça, ordem de
// campos fixa, inteiros big-endian de largura fixa, blobs/strings com uvarint). É a
// base para que Signer (aprovador) e Verifier (este módulo) construam EXACTAMENTE os
// mesmos bytes: qualquer divergência de um campo invalida a assinatura.

// putUint64 escreve um uint64 em big-endian (8 bytes, largura fixa).
func putUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

// putInt64 escreve um int64 em big-endian (8 bytes). Usado para o timestamp
// UnixNano (pode ser negativo antes de 1970).
func putInt64(buf []byte, v int64) []byte {
	return putUint64(buf, uint64(v))
}

// putBytes escreve um blob com length-prefix (uvarint) seguido dos bytes.
func putBytes(buf, b []byte) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(b)))
	buf = append(buf, lb[:n]...)
	return append(buf, b...)
}

// putString escreve uma string com length-prefix (uvarint) seguido dos bytes.
func putString(buf []byte, s string) []byte {
	return putBytes(buf, []byte(s))
}

// contains indica se s está em set. Usado na verificação de autoridade do aprovador.
func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}
