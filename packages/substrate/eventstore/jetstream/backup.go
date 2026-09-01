package jetstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// backup.go — o adaptador replicado como ORIGEM e DESTINO de backup/PITR (AOS-101).
//
// # O que estava em falta, e o que isso significava
//
// As portas [eventstore.BackupSource] e [eventstore.RestoreSink] eram satisfeitas SÓ pelo
// Store de referência. Consequência prática: com o Event Store a correr replicado, o
// módulo de backup nem se conseguia CONSTRUIR sobre ele — a história do log não tinha como
// sair nem entrar. Era também o que tornava impossível mover um log do WAL para o cluster:
// o Append público reatribui o envelope, pelo que reproduzi-lo por lá não moveria a
// história, fabricaria outra parecida.
//
// # O que o append-only do servidor obriga a mudar no PITR
//
// O stream é criado com deny_delete/deny_purge (AOS-100): a imutabilidade é imposta pelo
// SERVIDOR, não por convenção do cliente. Isso tem uma consequência que não se contorna e
// não se deve esconder: NÃO SE TRUNCA UM STREAM. «Restaurar até ao seq N» deixa de poder
// significar «rebobinar» e passa a significar MATERIALIZAR UM STREAM NOVO com 1..N,
// reapontando o nó (AOS_EVENTSTORE_NATS_STREAM). É o preço da imutabilidade que o AOS-100
// comprou, e é operacional, não algorítmico.
//
// Daí a segunda consequência, que é a que desenha o [Store.IngestStream]: um restauro que
// falhe a meio NÃO SE LIMPA. Ver a regra de recuperação em [eventstore.RestoreSink].
//
// # O que NÃO se faz aqui, declarado
//
// Um restauro não emite span nem passa pelo [Observador]. Não é esquecimento: a semconv
// (AOS-076) tem operações para append e para read, e inventar uma terceira seria mudar um
// contrato partilhado a pretexto deste ticket; e contar eventos restaurados no observador
// de appends faria um restauro parecer tráfego vivo a quem audita. A visibilidade de um
// restauro é a evidência que o restaurador devolve, que é onde o AC6 a quer.

// Garantias de tipo: o Store replicado satisfaz ambas as portas de backup/PITR.
var (
	_ eventstore.BackupSource = (*Store)(nil)
	_ eventstore.RestoreSink  = (*Store)(nil)
)

// StreamHead devolve o último seq committed do stream (0 se não existe).
//
// Lê do SERVIDOR (hidrata), e não da vista local: o head de um stream partilhado é dele e
// não nosso — foi essa a distinção que o DEF-282 mediu.
func (s *Store) StreamHead(ctx context.Context, streamID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.estaFechado() {
		return 0, eventstore.ErrClosed
	}
	subject, err := s.subjectDe(streamID)
	if err != nil {
		return 0, err
	}
	st := s.estadoDe(streamID)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.hidratado = false // o head de AGORA, não o da última escrita nossa
	if err := s.hidratar(ctx, st, subject, s.prazoDe(ctx)); err != nil {
		return 0, err
	}
	return st.aosSeq, nil
}

// SnapshotStream devolve os eventos committed com seq <= throughSeq (0 ⇒ todos), com o
// ENVELOPE INTACTO e ordenados por seq.
//
// O envelope vem intacto por construção e não por cuidado: no substrato replicado o
// envelope É o corpo da mensagem, pelo que lê-lo é tê-lo. O risco de reatribuição existe no
// caminho de ESCRITA (o Append), não aqui.
//
// A consistência é POR-STREAM, como na porta: entre streams distintos não há ponto de
// consistência global — nem faria sentido, porque a ordem total do log é por stream.
func (s *Store) SnapshotStream(ctx context.Context, streamID string, throughSeq uint64) ([]eventstore.Event, error) {
	evs, err := s.Read(ctx, streamID, 1)
	if err != nil {
		return nil, err
	}
	if throughSeq == 0 {
		return evs, nil
	}
	for i, ev := range evs {
		if ev.Seq > throughSeq {
			return evs[:i], nil
		}
	}
	return evs, nil
}

// IngestStream reinsere eventos PRESERVANDO o envelope (EventID/Ts/Seq originais).
//
// Validação fail-closed ANTES de tocar no servidor: envelope coerente (StreamID igual,
// EventID não-vazio ⇒ [eventstore.ErrRestoreEnvelope]) e lote contíguo por seq
// (⇒ [eventstore.ErrRestoreOrder]).
//
// RETOMÁVEL, e é aqui que difere do Store de referência. Se o lote se sobrepõe ao que já
// está no log, o prefixo sobreposto é VERIFICADO evento a evento pelo EventID e depois
// saltado; só o sufixo é escrito. Um prefixo que não bata certo é
// [eventstore.ErrRestoreDivergent] — o alvo tem outra história, e costurar duas histórias
// produziria um log que verifica como íntegro e não é o de ninguém.
//
// Um lote que começasse DEPOIS do head abriria um buraco: recusado com ErrRestoreOrder.
//
// Escrita concorrente no stream-alvo durante um restauro é ERRO, e não retentativa: um
// restauro pressupõe posse exclusiva do alvo, e retomar por cima de um escritor vivo
// entrelaçaria a história restaurada com a nova. O CAS do servidor deteta-o, e o restauro
// pára a dizer o que aconteceu.
func (s *Store) IngestStream(ctx context.Context, streamID string, events []eventstore.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.estaFechado() {
		return eventstore.ErrClosed
	}
	if len(events) == 0 {
		return nil
	}
	s.marcarUsado()
	subject, err := s.subjectDe(streamID)
	if err != nil {
		return err
	}

	// (1) FORMA DO LOTE — antes de qualquer rede. Um lote malformado nunca chega a tocar no
	// log, e é a única parte do fail-closed que este substrato dá de graça (o resto
	// resolve-se por retoma; ver o doc da porta).
	for i := range events {
		if events[i].StreamID != streamID || events[i].EventID == "" {
			return eventstore.ErrRestoreEnvelope
		}
		if i > 0 && events[i].Seq != events[i-1].Seq+1 {
			return eventstore.ErrRestoreOrder
		}
	}
	if events[0].Seq == 0 {
		return fmt.Errorf("%w: o seq do AOS é gapless desde 1 e o lote começa em 0", eventstore.ErrRestoreOrder)
	}

	st := s.estadoDe(streamID)
	st.mu.Lock()
	defer st.mu.Unlock()
	prazo := s.prazoDe(ctx)
	st.hidratado = false
	if err := s.hidratar(ctx, st, subject, prazo); err != nil {
		return err
	}

	primeiro := events[0].Seq
	if primeiro > st.aosSeq+1 {
		return fmt.Errorf("%w: o lote começa em %d e o log está em %d — restaurar abriria um buraco",
			eventstore.ErrRestoreOrder, primeiro, st.aosSeq)
	}

	// (2) SOBREPOSIÇÃO — a retoma. Verificar é obrigatório: saltar o prefixo sem o
	// confrontar aceitaria em silêncio um alvo com outra história.
	inicio := 0
	if primeiro <= st.aosSeq {
		presentes, errL := s.Read(ctx, streamID, primeiro)
		if errL != nil {
			return fmt.Errorf("jetstream: verificar o prefixo já presente: %w", errL)
		}
		for i := range events {
			if events[i].Seq > st.aosSeq {
				break
			}
			if i >= len(presentes) || presentes[i].Seq != events[i].Seq || presentes[i].EventID != events[i].EventID {
				return fmt.Errorf("%w: seq %d", eventstore.ErrRestoreDivergent, events[i].Seq)
			}
			inicio = i + 1
		}
	}
	if inicio == len(events) {
		return nil // já lá estava, inteiro e igual — uma retoma sem nada que fazer
	}

	// (3) SUFIXO — um publish por evento, com CAS sobre o token do servidor.
	for i := inicio; i < len(events); i++ {
		ev := events[i]
		corpo, errM := json.Marshal(ev)
		if errM != nil {
			return fmt.Errorf("jetstream: serializar envelope de restauro (seq %d): %w", ev.Seq, errM)
		}
		h := natsjs.Header{}
		if ev.IdempotencyKey != "" {
			h[natsjs.HdrMsgID] = streamID + "|" + ev.IdempotencyKey
		}
		ack, errP := s.cn.PublishExpectingSeq(subject, st.jsSeq, h, corpo, prazo)
		switch {
		case errP == nil && ack.Duplicate:
			// O servidor deduplicou dentro da janela: este evento já tinha sido escrito por
			// uma tentativa anterior DESTE restauro. A nossa vista é que estava atrasada.
			st.hidratado = false
			if err := s.hidratar(ctx, st, subject, prazo); err != nil {
				return err
			}
			if st.aosSeq < ev.Seq {
				return fmt.Errorf("jetstream: o servidor deduplicou o seq %d mas o log está em %d", ev.Seq, st.aosSeq)
			}

		case errP == nil:
			st.aosSeq, st.jsSeq = ev.Seq, ack.Seq
			if ev.IdempotencyKey != "" {
				st.dedup[ev.IdempotencyKey] = ev
			}

		case errors.Is(errP, natsjs.ErrWrongLastSeq):
			return fmt.Errorf("%w: o stream %q foi escrito por outro durante o restauro (seq %d) — um restauro pressupõe posse exclusiva do alvo",
				eventstore.ErrSeqConflict, streamID, ev.Seq)

		case errors.Is(errP, natsjs.ErrIndeterminate):
			// NÃO SE SABE se ficou durável. Resolve-se OLHANDO, como no Append.
			aplicado, errV := s.escritaAplicada(ctx, st, subject, ev.EventID, prazo)
			if errV != nil {
				return fmt.Errorf("jetstream: restauro indeterminado e não verificável no seq %d: %w (causa: %w)", ev.Seq, errV, errP)
			}
			if !aplicado {
				return fmt.Errorf("jetstream: o restauro parou no seq %d: %w", ev.Seq, errP)
			}
			st.hidratado = false
			if err := s.hidratar(ctx, st, subject, prazo); err != nil {
				return err
			}

		default:
			return fmt.Errorf("jetstream: restaurar o seq %d: %w", ev.Seq, errP)
		}
	}
	return nil
}
