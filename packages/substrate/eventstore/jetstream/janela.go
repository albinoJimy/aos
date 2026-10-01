package jetstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// ErrStreamNaoServe — o stream existe e captura o subject, mas ninguém serviu a escrita dentro
// do prazo: todas as publicações receberam 503. Sai embrulhado em [eventstore.ErrNoQuorum] (é
// indisponibilidade do substrato, não um conflito) e com [natsjs.ErrNoResponders] na cadeia (a
// causa que o servidor deu). NUNCA é uma recusa de posse: quem lê isto não tem dono a procurar.
var ErrStreamNaoServe = errors.New("jetstream: o stream existe mas ninguém serviu a escrita dentro do prazo")

// publicarCAS é a publicação com CAS do Event Store, e atravessa a JANELA em que o stream
// existe mas ninguém serve os subjects dele (AOS-455).
//
// # A janela que o AOS-432 não fechou
//
// O [Abrir] espera até o INFO anunciar líder. Isso não chega: no nats-server 2.10.22 o Raft
// passa a líder (e o INFO anuncia-o) ANTES de o stream subscrever os subjects —
// `switchState(Leader)` marca o estado e só depois a goroutine do stream consome a mudança e
// corre `processStreamLeaderChange` → `setLeader` → `subscribeToStream`. Uma publicação nesse
// intervalo recebe 503. Medido contra o cluster do gate: em 100 corridas de 4 ligações a criar
// o mesmo stream, esperar pelo líder e publicar com CAS 0, duas tiveram um 503 DEPOIS de o INFO
// anunciar líder; uma ligação sozinha, 1 em 100. A mesma janela reabre numa re-eleição.
//
// Era a falha do CI (PR #399: `A.Publish: natsjs: ninguém serve este subject (503)`, e
// `TestAOS432_LeaseSobreStreamFrescoNegaPeloLease` a falhar em 0,14 s): o `Claim` recebia o
// 503 em vez da recusa do CAS, devolvia-o cru, e o perdedor saía 1 em vez de 3.
//
// # Porque re-tentar é seguro, e quando NÃO se re-tenta
//
// Um 503 é a resposta do SERVIDOR a dizer que ninguém recebeu a mensagem: nada ficou durável
// (ver [natsjs.Conn.Request]). Repetir a MESMA publicação, com o mesmo CAS e o mesmo
// `Nats-Msg-Id`, não pode duplicar nada: ou passa, ou é recusada pelo CAS de quem escreveu
// entretanto — que é exactamente a resposta de que o `Claim` precisa.
//
// Só se re-tenta o 503 que É a janela: o de um subject que o stream captura
// ([Store.servidoEmBreve]). O 503 pela razão que o 503 existe para dizer — subject fora de
// qualquer stream, stream inexistente, sem JetStream — sobe na hora e tal qual, que é o que o
// AOS-432 fixou (`TestAOS432_503VerdadeiroNaoEPosseNegada`).
//
// # Limite e fail-closed
//
// A janela partilha o `prazo` da operação, contado da primeira publicação. Esgotado,
// devolve [ErrStreamNaoServe] embrulhado em [eventstore.ErrNoQuorum], com o último 503 na
// cadeia — um erro de transporte nomeado, distinto de qualquer conflito, que nunca é sucesso
// silencioso e nunca é posse negada. O `ctx` cancelado interrompe a espera.
func (s *Store) publicarCAS(ctx context.Context, subject string, ultimo uint64, h natsjs.Header,
	corpo []byte, prazo time.Duration) (natsjs.PubAck, error) {
	limite := time.Now().Add(prazo)
	intervalo := intervaloLiderInicial
	resta := prazo
	for publicacoes := 1; ; publicacoes++ {
		ack, err := s.cn.PublishExpectingSeq(subject, ultimo, h, corpo, resta)
		if !errors.Is(err, natsjs.ErrNoResponders) || !s.servidoEmBreve(subject, limite) {
			return ack, err
		}
		if resta = time.Until(limite); resta > 0 {
			espera := time.NewTimer(min(intervalo, resta))
			select {
			case <-ctx.Done():
				espera.Stop()
				return ack, fmt.Errorf("%w (a última publicação em %q recebeu: %w)", ctx.Err(), subject, err)
			case <-espera.C:
			}
			intervalo = min(2*intervalo, intervaloLiderMaximo)
			resta = time.Until(limite)
		}
		if resta <= 0 {
			return ack, fmt.Errorf("%w: %w: %q, %d publicação(ões) em %s: %w",
				eventstore.ErrNoQuorum, ErrStreamNaoServe, subject, publicacoes, prazo, err)
		}
	}
}

// servidoEmBreve diz se um 503 em subject é a JANELA (o stream existe e captura o subject,
// só ainda não o serve) e não o 503 de um subject que ninguém vai servir.
//
// Um Store que CRIOU o stream sabe-o sem perguntar: o CREATE com `<prefixo>.>` foi aceite, e o
// servidor recusa um CREATE sobre um stream existente com configuração diferente. Um Store
// aberto com [SemCriarStream] não sabe, e pergunta a configuração ARMAZENADA:
//
//   - responde, e um dos subjects dela casa com o nosso → janela;
//   - não responde (silêncio do grupo em formação, ver [consultaLiderInicial]) → janela; a
//     próxima publicação volta a perguntar, e o prazo continua a ser um só;
//   - responde com erro (stream inexistente, sem JetStream), ou nada casa → NÃO é janela.
func (s *Store) servidoEmBreve(subject string, limite time.Time) bool {
	if s.capturaPropria {
		return true
	}
	resta := time.Until(limite)
	if resta <= 0 {
		return true // o chamador esgota o prazo e devolve o erro nomeado
	}
	cfg, err := s.cn.ConfigDoStream(s.stream, min(consultaLiderInicial, resta))
	if errors.Is(err, natsjs.ErrTimeout) {
		return true
	}
	if err != nil {
		return false
	}
	for _, filtro := range cfg.Subjects {
		if subjectCasa(filtro, subject) {
			return true
		}
	}
	return false
}

// subjectCasa aplica a regra de curingas do NATS: `*` casa um token, `>` casa um ou mais
// tokens e só pode ser o último.
func subjectCasa(filtro, subject string) bool {
	f, s := strings.Split(filtro, "."), strings.Split(subject, ".")
	for i, tok := range f {
		switch {
		case tok == ">":
			return i == len(f)-1 && len(s) > i
		case i >= len(s):
			return false
		case tok != "*" && tok != s[i]:
			return false
		}
	}
	return len(f) == len(s)
}
