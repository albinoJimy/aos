package natsjs

import (
	"strings"
)

// Cabeçalhos do JetStream de que o Event Store depende. Os nomes são os do protocolo —
// não são nossos para escolher, e foram CONFIRMADOS contra um cluster real a
// 2026-08-31 (ver docs/reports/medicao-jetstream-arbitragem-2026-08-31.md).
const (
	// HdrExpectedLastSubjectSeq afirma qual é o último seq DESTE subject. Se não
	// bater, o servidor recusa e nada fica durável. É a concorrência optimista
	// (expected_seq) do contrato C2, imposta do lado do servidor — a propriedade que
	// o Event Store de referência não tem entre processos (DEF-282).
	HdrExpectedLastSubjectSeq = "Nats-Expected-Last-Subject-Sequence"

	// HdrMsgID é a chave de deduplicação do servidor.
	//
	// ATENÇÃO — MEDIDO: a deduplicação do JetStream é uma JANELA TEMPORAL, não um
	// índice. Com dupe-window=1s a mesma chave ficou committed TRÊS vezes. A
	// idempotência do AOS por (run_id, step_id) não tem prazo, pelo que NÃO pode
	// assentar neste cabeçalho: ele é rede de segurança para retries imediatos, e a
	// garantia continua a ser o índice derivado do log sob CAS.
	HdrMsgID = "Nats-Msg-Id"
)

// Header é o conjunto de cabeçalhos de uma mensagem. Chave única por nome — o Event
// Store não precisa de valores repetidos e admiti-los só criaria ambiguidade.
type Header map[string]string

// prefixo da versão do bloco de cabeçalhos no protocolo.
const versaoHeader = "NATS/1.0"

// codificar produz o bloco de cabeçalhos no formato do protocolo:
//
//	NATS/1.0\r\nChave: valor\r\n\r\n
//
// O bloco termina SEMPRE em linha vazia, e o seu comprimento inclui-a — é isso que o
// hdr_len do HPUB conta.
func (h Header) codificar() []byte {
	var b strings.Builder
	b.WriteString(versaoHeader)
	b.WriteString("\r\n")
	for k, v := range h {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String())
}

// descodificarHeader lê o bloco de cabeçalhos de um HMSG. Uma primeira linha que não
// seja a versão devolve cabeçalhos vazios em vez de erro: o corpo da mensagem continua
// a ser entregue, e quem decide se a falta de um cabeçalho é fatal é o chamador.
func descodificarHeader(b []byte) Header {
	linhas := strings.Split(string(b), "\r\n")
	if len(linhas) == 0 || !strings.HasPrefix(linhas[0], versaoHeader) {
		return nil
	}
	h := Header{}
	for _, l := range linhas[1:] {
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		h[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(h) == 0 {
		return nil
	}
	return h
}
