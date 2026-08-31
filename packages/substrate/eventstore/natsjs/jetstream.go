package natsjs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Códigos de erro do JetStream que o Event Store tem de distinguir. São do servidor;
// o valor foi CONFIRMADO contra um cluster real (resposta literal registada em
// docs/reports/medicao-jetstream-arbitragem-2026-08-31.md §1).
const (
	// CodSeqErrada — o expected_seq afirmado não corresponde ao último seq do
	// subject. É a RECUSA que dá a arbitragem entre escritores.
	CodSeqErrada = 10071
)

// ErrSeqErrada — o servidor recusou a escrita por o expected_seq não bater. Nada ficou
// durável (o PubAck traz seq:0), o que é exactamente o que o contrato C2 do Event Store
// exige de uma recusa.
var ErrSeqErrada = errors.New("natsjs: expected_seq não corresponde ao último seq do subject")

// PubAck é a resposta do JetStream a uma publicação.
type PubAck struct {
	Stream string `json:"stream"`
	Seq    uint64 `json:"seq"`
	// Duplicate indica que a Nats-Msg-Id já existia DENTRO DA JANELA de deduplicação
	// e que Seq é o seq ORIGINAL. Ver a advertência em [HdrMsgID]: é uma janela, não
	// um índice — nunca é a garantia de idempotência do AOS.
	Duplicate bool     `json:"duplicate"`
	Error     *JSError `json:"error"`
}

// JSError é o erro estruturado que o JetStream devolve dentro do PubAck.
type JSError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *JSError) Error() string {
	return fmt.Sprintf("jetstream: %s (code=%d err_code=%d)", e.Description, e.Code, e.ErrCode)
}

// Publicar escreve em subject e devolve o PubAck.
//
// Um erro devolvido aqui significa que NADA FICOU DURÁVEL, com uma excepção nomeada:
// [ErrTimeout], que significa INDETERMINADO. Ver [Conn.Request] para porque é seguro
// repetir nesse caso — e é seguro só porque a escrita leva expected_seq.
func (cn *Conn) Publicar(subject string, h Header, dados []byte, timeout time.Duration) (PubAck, error) {
	m, err := cn.Request(subject, h, dados, timeout)
	if err != nil {
		return PubAck{}, err
	}
	var ack PubAck
	if err := json.Unmarshal(m.Data, &ack); err != nil {
		return PubAck{}, fmt.Errorf("%w: PubAck ilegível (%q): %v", ErrProtocolo, m.Data, err)
	}
	if ack.Error != nil {
		if ack.Error.ErrCode == CodSeqErrada {
			return ack, fmt.Errorf("%w: %s", ErrSeqErrada, ack.Error.Description)
		}
		return ack, ack.Error
	}
	return ack, nil
}

// PublicarComCAS é a via que o Event Store usa: afirma qual é o último seq do subject e
// só escreve se ainda for verdade.
//
// É esta chamada — e não o cliente, e não o lease — que arbitra entre escritores. A
// disciplina de posse do AOS (LeaseManager/AOS-018, FencedAppender, ADR-023) é correcta
// SOB esta propriedade e vacuosa sem ela.
func (cn *Conn) PublicarComCAS(subject string, ultimoSeq uint64, h Header, dados []byte, timeout time.Duration) (PubAck, error) {
	if h == nil {
		h = Header{}
	}
	h[HdrExpectedLastSubjectSeq] = strconv.FormatUint(ultimoSeq, 10)
	return cn.Publicar(subject, h, dados, timeout)
}

// --- Gestão mínima de streams ----------------------------------------------

// ConfigStream é o subconjunto da configuração de stream que o Event Store fixa. Os
// campos que aqui NÃO aparecem ficam no default do servidor de propósito: cada opção
// que fixamos é uma que temos de justificar e manter.
type ConfigStream struct {
	Name     string   `json:"name"`
	Subjects []string `json:"subjects"`
	// NumReplicas é o quórum do AOS-100. 3 ou 5 (R3/R5); 1 é dev.
	NumReplicas int `json:"num_replicas"`
	// Storage "file" — o log é a fonte de verdade e tem de sobreviver a reinício.
	Storage string `json:"storage"`
	// DenyDelete/DenyPurge impõem o append-only DO LADO DO SERVIDOR. MEDIDO: sem
	// eles o log é apagável por API; com eles o servidor recusa (10057/10110).
	DenyDelete bool `json:"deny_delete"`
	DenyPurge  bool `json:"deny_purge"`
	// Duplicates é a janela de deduplicação, em nanossegundos. NÃO é a garantia de
	// idempotência do AOS — ver [HdrMsgID].
	Duplicates int64 `json:"duplicate_window,omitempty"`
}

// respostaStream é o envelope das respostas da API de streams.
type respostaStream struct {
	Error *JSError `json:"error"`
}

// CriarStream cria o stream. Um stream já existente com configuração diferente é
// recusado pelo servidor — deliberadamente não se faz update às cegas: mudar
// num_replicas ou deny_delete por baixo de um log vivo é uma operação de migração, não
// um efeito secundário de arrancar um nó.
func (cn *Conn) CriarStream(cfg ConfigStream, timeout time.Duration) error {
	corpo, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	m, err := cn.Request("$JS.API.STREAM.CREATE."+cfg.Name, nil, corpo, timeout)
	if err != nil {
		return err
	}
	var r respostaStream
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return fmt.Errorf("%w: resposta de CREATE ilegível (%q): %v", ErrProtocolo, m.Data, err)
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

// InfoStream devolve o número de mensagens e o último seq do stream. É o mínimo de que
// o Event Store precisa para se re-hidratar; não é uma vista completa.
type InfoStream struct {
	Messages uint64
	LastSeq  uint64
}

type respostaInfo struct {
	Error *JSError `json:"error"`
	State struct {
		Messages uint64 `json:"messages"`
		LastSeq  uint64 `json:"last_seq"`
	} `json:"state"`
}

// InfoDoStream lê o estado do stream.
func (cn *Conn) InfoDoStream(nome string, timeout time.Duration) (InfoStream, error) {
	m, err := cn.Request("$JS.API.STREAM.INFO."+nome, nil, nil, timeout)
	if err != nil {
		return InfoStream{}, err
	}
	var r respostaInfo
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return InfoStream{}, fmt.Errorf("%w: resposta de INFO ilegível (%q): %v", ErrProtocolo, m.Data, err)
	}
	if r.Error != nil {
		return InfoStream{}, r.Error
	}
	return InfoStream{Messages: r.State.Messages, LastSeq: r.State.LastSeq}, nil
}
