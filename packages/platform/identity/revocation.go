package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// Revocations é o registo de revogação de NHIs: um conjunto de jti em memória,
// consultado por [Verifier.Verify] (via [RevocationChecker]) e alimentado por
// [Revocations.Revoke], que grava um evento identity.nhi.revoked no Event Store.
// O TTL curto dos tokens minimiza a janela em que uma revogação tem de valer.
//
// # A PROJECÇÃO É EM MEMÓRIA E TEM DE SER RECONSTRUÍDA (AOS-288)
//
// O conjunto vive em `revoked` e o Event Store tem a verdade. Um processo novo nasce
// com o mapa VAZIO: o evento `identity.nhi.revoked` continua no log, mas nada o lê de
// volta. Sem [Revocations.Rebuild] no arranque, um restart RESSUSCITA todos os tokens
// revogados que ainda não tenham expirado — e fá-lo em silêncio, porque o `Verify`
// devolve um `Principal` completo e utilizável, exactamente como antes da revogação.
//
// É a diferença entre «o mecanismo está por ligar» e «o mecanismo esquece»: quem
// compõe este registo tem de chamar `Rebuild` antes de o passar ao verifier, senão a
// propriedade que anuncia só vale até ao próximo reinício.
type Revocations struct {
	mu      sync.RWMutex
	revoked map[string]struct{}
	store   eventstore.EventStore
	now     func() time.Time
}

// RevocationsOption configura o registo de revogação.
type RevocationsOption func(*Revocations)

// WithRevocationClock injecta o relógio (uso interno/testes).
func WithRevocationClock(f func() time.Time) RevocationsOption {
	return func(r *Revocations) {
		if f != nil {
			r.now = f
		}
	}
}

// NewRevocations constrói o registo. store é o Event Store onde Revoke grava o
// evento; pode ser nil (a revogação regista-se em memória mas não é auditada —
// produção deve injectar um store real).
func NewRevocations(store eventstore.EventStore, opts ...RevocationsOption) *Revocations {
	r := &Revocations{
		revoked: make(map[string]struct{}),
		now:     time.Now,
	}
	if store != nil {
		r.store = store
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Rebuild repovoa o conjunto a partir do stream durável de identidade (AOS-288).
//
// TEM DE SER CHAMADO NO ARRANQUE, antes de o registo ser passado ao [Verifier]. Sem
// isto, um processo reiniciado esquece todas as revogações e volta a aceitar tokens
// revogados que ainda não expiraram — silenciosamente, com um `Principal` completo.
//
// FAIL-CLOSED, e é aqui que difere de uma varredura de conveniência: um erro de
// leitura é PROPAGADO em vez de deixar o conjunto vazio. A alternativa — engolir e
// seguir — produziria exactamente o estado que este método existe para impedir, e
// produzi-lo-ia num arranque que se declarou bem-sucedido. Um stream ainda inexistente
// (nada foi jamais revogado neste substrato) NÃO é erro: é o conjunto vazio legítimo.
//
// É idempotente e aditivo: chamar duas vezes não remove nada, e o conjunto nunca
// encolhe — não há evento de «des-revogação» no vocabulário, por desenho.
func (r *Revocations) Rebuild(ctx context.Context) error {
	if r == nil {
		return nil
	}
	// Sem store não há de onde reconstruir. É a ÚNICA forma de isto acontecer: o parâmetro de
	// [NewRevocations] é [eventstore.EventStore], que já declara `Read` — logo um store
	// presente sabe sempre ler, e não há o caso «escreve mas não lê».
	if r.store == nil {
		return ErrRevocationsNotDurable
	}
	events, err := r.store.Read(ctx, streamIdentity, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil // nada emitido nem revogado ainda: conjunto vazio legítimo
		}
		return fmt.Errorf("identity: reconstruir revogacoes: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range events {
		if ev.Type != EventTypeRevoked {
			continue
		}
		var pl revokedPayload
		// Um payload corrupto SALTA-SE, no mesmo idioma de [decodeIssued]: um registo
		// ilegível não pode apagar as revogações legítimas que vêm a seguir no stream.
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			continue
		}
		if pl.JTI != "" {
			r.revoked[pl.JTI] = struct{}{}
		}
	}
	return nil
}

// Revoke revoga o token identificado por jti: acrescenta-o ao conjunto e grava
// identity.nhi.revoked no Event Store. É idempotente — revogar o mesmo jti duas
// vezes não duplica o efeito nem o evento (idempotency key por jti).
func (r *Revocations) Revoke(ctx context.Context, jti string) error {
	if jti == "" {
		return ErrInvalidRequest
	}
	r.mu.Lock()
	_, jaEstava := r.revoked[jti]
	r.revoked[jti] = struct{}{}
	r.mu.Unlock()

	if r.store == nil {
		return nil
	}
	payload := revokedPayload{JTI: jti, RevokedAt: r.now().Unix()}
	raw, err := json.Marshal(payload)
	if err != nil {
		r.desfazer(jti, jaEstava)
		return err
	}
	_, err = r.store.Append(ctx, streamIdentity, eventstore.EventInput{
		Type:     EventTypeRevoked,
		Payload:  raw,
		RunID:    streamIdentity,
		StepID:   "nhi.revoked:" + jti, // idempotência por jti
		Producer: eventstore.Producer{NHIID: jti},
	})
	if err != nil {
		r.desfazer(jti, jaEstava)
		return err
	}
	return nil
}

// desfazer reverte a marcação em memória quando a escrita durável falhou (AOS-288).
//
// # PORQUE A REVERSÃO É CORRECTA, e porque a ausência dela era um defeito
//
// O contrato do [eventstore.EventStore] garante que um `Append` com erro NÃO deixou nada
// durável — nem neste processo nem num arranque posterior sobre o mesmo substrato (ver
// store.go, «ERRO ⇒ NADA FICOU DURÁVEL»). É essa garantia que torna a reversão segura, e é o
// mesmo idioma que o `GraphBuilder` de AOS-025 já usa; o contrato nomeia-o explicitamente.
//
// Sem reverter, um `Revoke` falhado deixava o nó num estado que NENHUMA resposta descreve: o
// token passava a ser recusado em memória enquanto o chamador recebia erro e o log durável não
// tinha o evento. Ao restart seguinte o [Revocations.Rebuild] não o encontrava e o token
// voltava a valer. O operador via um 500, concluía que nada tinha acontecido, e tinha razão
// metade do tempo — a pior forma de ter razão.
//
// `jaEstava` preserva a IDEMPOTÊNCIA: uma segunda revogação do mesmo jti cujo Append falhe não
// pode apagar a primeira, que teve sucesso e está durável.
func (r *Revocations) desfazer(jti string, jaEstava bool) {
	if jaEstava {
		return
	}
	r.mu.Lock()
	delete(r.revoked, jti)
	r.mu.Unlock()
}

// IsRevoked implementa [RevocationChecker]. Nunca devolve erro nesta
// implementação em memória; a assinatura mantém-no para permitir backends
// remotos (introspecção) que possam falhar — nesse caso o verificador é
// fail-closed.
func (r *Revocations) IsRevoked(_ context.Context, jti string) (bool, error) {
	// Guarda contra um *Revocations nil embrulhado numa interface não-nil (ex.:
	// WithRevocations((*Revocations)(nil))): o guard v.revocations != nil no
	// Verifier passa, e sem esta protecção o r.mu.RLock() sobre receiver nil
	// entraria em panic. Um registo nil não tem jti revogado ⇒ (false, nil).
	if r == nil {
		return false, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.revoked[jti]
	return ok, nil
}
