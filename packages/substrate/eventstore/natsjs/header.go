package natsjs

import (
	"fmt"
	"strconv"
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
	// índice. Com uma janela de 3s, a mesma chave deduplica dentro dela (controlo
	// positivo) e volta a ser aceite depois — ver TestIntegracao_DedupExpiraComAJanela.
	// A idempotência do AOS por (run_id, step_id) não tem prazo, pelo que NÃO pode
	// assentar neste cabeçalho: ele é rede de segurança para retries imediatos, e a
	// garantia continua a ser o índice derivado do log sob CAS.
	HdrMsgID = "Nats-Msg-Id"
)

// Header é o conjunto de cabeçalhos de uma mensagem. Chave única por nome — o Event
// Store não precisa de valores repetidos e admiti-los só criaria ambiguidade.
type Header map[string]string

// Clonar devolve uma cópia. Existe porque acrescentar um cabeçalho ao mapa DO CHAMADOR
// é um defeito de dupla face, medido a 2026-08-31: contamina publicações seguintes com
// um expected_seq que ninguém pediu, e duas goroutines a partilhar um Header dão
// `fatal error: concurrent map writes` — que nem `recover` apanha.
func (h Header) Clonar() Header {
	cp := make(Header, len(h)+1)
	for k, v := range h {
		cp[k] = v
	}
	return cp
}

// prefixo da versão do bloco de cabeçalhos no protocolo.
const versaoHeader = "NATS/1.0"

// codificar produz o bloco de cabeçalhos no formato do protocolo:
//
//	NATS/1.0\r\nChave: valor\r\n\r\n
//
// O bloco termina SEMPRE em linha vazia, e o seu comprimento inclui-a — é isso que o
// hdr_len do HPUB conta.
//
// # Porque valida, e o que a validação impede
//
// MEDIDO a 2026-08-31: um valor com CRLF forja cabeçalhos adicionais. Como o hdr_len é
// calculado DEPOIS da codificação, o enquadramento fica válido e o servidor parseia as
// linhas injectadas como cabeçalhos verdadeiros. Um HdrMsgID contendo
// "\r\nNats-Expected-Last-Subject-Sequence: 99999" injecta um segundo CAS — e qual dos
// dois o servidor lê depende da ordem de iteração do mapa, que é aleatória. Seria uma
// via não-determinista de contornar a arbitragem em que todo o AOS-100 assenta, a
// partir de dados de workflow (a chave é f(run_id, step_id)).
func (h Header) codificar() ([]byte, error) {
	var b strings.Builder
	b.WriteString(versaoHeader)
	b.WriteString("\r\n")
	for k, v := range h {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") {
			return nil, fmt.Errorf("%w: nome de cabeçalho inválido %q", ErrProtocolo, k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("%w: valor do cabeçalho %q contém fim-de-linha — "+
				"seria injecção de cabeçalhos", ErrProtocolo, k)
		}
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return []byte(b.String()), nil
}

// descodificarHeader lê o bloco de cabeçalhos de um HMSG e devolve também o CÓDIGO DE
// ESTADO da linha de versão, quando existe (ex.: "NATS/1.0 503" → 503).
//
// O estado é devolvido em separado porque uma mensagem de estado NÃO TEM cabeçalhos: sem
// isto, um 503 («ninguém serve este subject» — JetStream desligado, stream inexistente,
// sem permissões) era indistinguível de uma resposta sem cabeçalhos e chegava ao
// chamador como «PubAck ilegível», mandando o operador caçar um bug do cliente.
func descodificarHeader(b []byte) (Header, int) {
	linhas := strings.Split(string(b), "\r\n")
	if len(linhas) == 0 || !strings.HasPrefix(linhas[0], versaoHeader) {
		return nil, 0
	}
	estado := 0
	if campos := strings.Fields(linhas[0]); len(campos) > 1 {
		if n, err := strconv.Atoi(campos[1]); err == nil {
			estado = n
		}
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
		return nil, estado
	}
	return h, estado
}
