package main

// posse_de_laco.go — A POSSE DE UM LAÇO DE SERVIÇO (AOS-283).
//
// # O DEFEITO QUE FECHA, medido antes de ser escrito
//
// O varredor de retenção seriava-se por [NodeService.expireInFlight], um `atomic.Bool`
// NO PROCESSO. O comentário desse campo diz, e diz bem, que o guard «tem de ser UM SÓ».
// Com N réplicas há N guards e NENHUMA exclusão entre eles.
//
// MEDIDO a 2026-09-01 com duas réplicas sobre o MESMO substrato (Event Store, cadeia
// WORM e custódia da KEK partilhados), em `aos283_lacos_sob_lease_test.go`: o MESMO
// facto foi selado DUAS vezes —
//
//	selos retention.expired por record_id: map[01M1FC6QYCRXYSM36B8VE0Z0JJ:2]
//
// A causa não é o guard estar mal escrito: é a idempotência do [audit.ExpirationJob]
// viver num seen-set in-memory por-processo ([audit.NewInMemoryIdempotencyStore], cujo
// próprio doc diz que «basta para um job de PROCESSO ÚNICO»), re-hidratado UMA vez no
// arranque. Duas réplicas que arrancam antes da primeira passagem têm ambas o seen-set
// vazio, ambas vêem o registo por expirar, e ambas selam. A cadeia gapless de auditoria
// fica com duas destruições do mesmo facto — que é exactamente o que a idempotência do
// job existe para impedir.
//
// # PORQUE A POSSE, e não uma idempotência durável
//
// A nota de escopo de AOS-283 pede que a alternativa seja AVALIADA antes de se construir
// a eleição. Avaliada, e é esta a razão de não ser ela:
//
// Uma idempotency key durável (um índice partilhado com `Seen`/`Add`) fecha a janela
// LARGA — a de duas passagens desfasadas — mas não a estreita: `Seen(key)`…`Add(key)` é
// um check-then-act sem atomicidade, e é o próprio código do job que o declara. Duas
// réplicas que leiam a mesma key por-marcar no mesmo instante voltam a selar as duas.
// Torná-lo atómico exigiria um CAS por-registo no substrato — um SEGUNDO mecanismo de
// exclusão, com a sua própria semântica de expiração e de observabilidade, ao lado do
// que o repositório já tem. É o que ADR-023 e o handoff deste ticket proíbem por escrito.
//
// A posse resolve o problema numa camada acima e com o mecanismo que já existe: se só
// UMA réplica corre a passagem, não há duas a selar, e a idempotência in-memory volta a
// ser suficiente porque volta a haver um processo único a expirar. O guard
// `expireInFlight` continua a valer para o que ele sempre valeu — a rota e o tick DENTRO
// da mesma réplica — e passa a ter, por cima, a exclusão ENTRE réplicas que lhe faltava.
//
// # NÃO É UM SEGUNDO MECANISMO DE POSSE
//
// É o [durable.LeaseManager] de AOS-018 — o mesmo que possui os runs — sobre uma chave de
// SERVIÇO em vez de uma chave de run: `lease:svc:<nome-do-laço>`. Mesma disciplina de
// ADR-023 §2.5: claim → heartbeat → `Release` como ÚLTIMO acto. A arbitragem é do Event
// Store (concorrência optimista no `expected_seq` do stream de posse), não deste ficheiro.
//
// Deliberadamente NÃO se importa `runlifecycle.Tenure`, que é o mesmo padrão já escrito:
// o nó está PROIBIDO por guard-test de importar o orquestrador concorrente
// (`TestBoundary_NodeDoesNotImportConcurrentOrchestratorOrScheduler`, ADR-018 §5 e
// ADR-023 §2.6). O que se partilha é a AUTORIDADE (o `LeaseManager`), que é onde a
// duplicação teria consequência; o invólucro é local e minúsculo.
//
// # LIMITE DECLARADO, herdado do substrato
//
// A exclusão entre PROCESSOS vale exactamente na medida em que o `expected_seq` do stream
// de posse for atómico entre escritores. Sobre `--nats` (AOS-100) é (o servidor impõe-o);
// sobre o WAL de referência NÃO é, e é por isso que o guard de arranque de AOS-285/286
// impede duas réplicas sobre o mesmo ficheiro. Este ficheiro não dá ao substrato uma
// garantia que ele não tenha — usa a que ele tem.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// lacoRetencao é o nome do laço de retenção no stream de posse. O stream efectivo é
// `lease:svc:retention` — o prefixo `svc:` separa o espaço de nomes das posses de SERVIÇO
// do das posses por-RUN (`lease:<run_id>`), que são de outra natureza e têm outro ciclo
// de vida.
const lacoRetencao = "retention"

// chaveDePosseDeLaco devolve a chave de lease de um laço de serviço.
func chaveDePosseDeLaco(nome string) string { return "svc:" + nome }

// posseDeLaco é a posse de UM laço de serviço por ESTA réplica.
//
// Seguro para uso concorrente: o estado da posse vive sob `mu`; o veredicto que
// `/metrics` lê é um atómico, para que a raspagem nunca contenda com o varrimento.
type posseDeLaco struct {
	nome   string
	leases *durable.LeaseManager
	hb     time.Duration
	log    func(format string, args ...any)

	mu     sync.Mutex
	lease  durable.Lease
	detida bool
	// parar junta o renovador desta posse. Guardado para que uma posse PERDIDA e
	// re-assumida não deixe um renovador órfão a bater contra um token já superado.
	parar func()

	// lider é o veredicto LOCAL da última tentativa de posse, lido sem lock por
	// `/metrics`. É um facto local: um `true` não prova que o token continue a ser o
	// corrente no log — a prova durável é sempre a do heartbeat, que é quem o desarma.
	lider atomic.Bool
}

// novaPosseDeLaco constrói a posse de um laço. `hb` é o período de renovação e tem de ser
// MENOR que o TTL do lease — senão a posse expira entre renovações e o laço passa a
// bater-se a si próprio pela liderança. `leases` nil ⇒ nil (sem autoridade não há posse).
func novaPosseDeLaco(nome string, leases *durable.LeaseManager, hb time.Duration, log func(string, ...any)) *posseDeLaco {
	if leases == nil || nome == "" || hb <= 0 {
		return nil
	}
	return &posseDeLaco{nome: nome, leases: leases, hb: hb, log: log}
}

// souLider é a leitura para observabilidade: esta réplica detém a posse do laço?
// nil ⇒ false (não há posse composta, logo não há liderança a reportar).
func (p *posseDeLaco) souLider() bool {
	if p == nil {
		return false
	}
	return p.lider.Load()
}

// assumir TOMA (ou confirma) a posse do laço. Devolve true só se esta réplica é a líder.
//
// FAIL-CLOSED em todos os ramos que não sejam uma posse confirmada: com o lease detido
// por outra réplica ([durable.ErrLeaseHeld]) o laço NÃO corre — que é o critério de
// aceitação à letra —, e um erro de substrato também não deixa correr. Correr sem saber
// se se é o dono é precisamente a falha que este ficheiro fecha; a passagem re-tenta no
// tick seguinte, e a expiração continua conduzível por `POST /dsar/expire`.
//
// Uma posse já detida por ESTA réplica é confirmada sem tocar no log: o `Claim` de um
// lease vivo devolveria [durable.ErrLeaseHeld] mesmo ao próprio detentor, e quem mantém a
// posse viva é o renovador, não o varrimento.
func (p *posseDeLaco) assumir(ctx context.Context, stop <-chan struct{}) bool {
	if p == nil {
		return false
	}
	// FASE 1 — junta um renovador de uma posse já PERDIDA, FORA do lock. O renovador
	// desarma `detida` sob `mu`, pelo que juntá-lo com `mu` detido seria um deadlock.
	p.mu.Lock()
	if p.detida {
		p.mu.Unlock()
		return true
	}
	anterior := p.parar
	p.parar = nil
	p.mu.Unlock()
	if anterior != nil {
		anterior()
	}

	// FASE 2 — reclama. O lock cobre a reclamação para que duas passagens concorrentes
	// da MESMA réplica não reclamem duas vezes (a segunda veria o seu próprio lease vivo
	// e concluiria, erradamente, que outra réplica é a líder).
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.detida {
		return true
	}
	lease, err := p.leases.Claim(ctx, chaveDePosseDeLaco(p.nome))
	if err != nil {
		p.lider.Store(false)
		if errors.Is(err, durable.ErrLeaseHeld) {
			p.logf("posse do laco %q: NAO SOU LIDER — outra replica detem lease:%s com lease vivo; a passagem NAO corre (e a exclusao que impede o mesmo facto de ser selado duas vezes)",
				p.nome, chaveDePosseDeLaco(p.nome))
			return false
		}
		p.logf("posse do laco %q: reclamacao FALHOU no substrato — a passagem NAO corre (fail-closed: sem posse confirmada nao se sabe se outra replica esta a correr); re-tenta no proximo tick: %v", p.nome, err)
		return false
	}
	p.lease = lease
	p.detida = true
	p.lider.Store(true)
	p.parar = p.manter(stop)
	p.logf("posse do laco %q: ASSUMIDA (lease:%s, token=%d, TTL=%s, renovacao=%s) — esta replica e a UNICA que corre este laco",
		p.nome, chaveDePosseDeLaco(p.nome), lease.Token.Value(), lease.TTL, p.hb)
	return true
}

// manter arranca o renovador da posse e devolve a função que o pára E o AGUARDA (join) —
// nunca deixa um renovador órfão a bater contra uma posse já largada.
//
// PORQUE A RENOVAÇÃO É PRECISA. A cadência do varrimento de retenção é de UMA HORA por
// omissão e o TTL do lease é de minutos: sem renovação, a posse expirava entre dois ticks
// e a liderança saltaria de réplica a cada passagem — o oposto de exclusão estável.
func (p *posseDeLaco) manter(stop <-chan struct{}) func() {
	parar := make(chan struct{})
	saiu := make(chan struct{})
	go func() {
		defer close(saiu)
		t := time.NewTicker(p.hb)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-parar:
				return
			case <-t.C:
				if !p.renovar(context.Background()) {
					return
				}
			}
		}
	}()
	var uma sync.Once
	return func() {
		uma.Do(func() { close(parar) })
		<-saiu
	}
}

// renovar estende o TTL da posse SEM mintar novo token. Devolve false quando a posse se
// perdeu — e nesse caso desarma-a, para que a passagem seguinte volte a RECLAMAR em vez
// de correr a descoberto. É o desenho do `heartbeat()` por-run: quem perde a posse pára,
// não insiste.
func (p *posseDeLaco) renovar(ctx context.Context) bool {
	p.mu.Lock()
	if !p.detida {
		p.mu.Unlock()
		return false
	}
	lease := p.lease
	p.mu.Unlock()

	renovado, err := p.leases.Heartbeat(ctx, lease)
	if err != nil {
		p.mu.Lock()
		p.detida = false
		p.mu.Unlock()
		p.lider.Store(false)
		p.logf("posse do laco %q: PERDIDA na renovacao (%v) — o laco DEIXA de correr nesta replica e volta a reclamar no proximo tick", p.nome, err)
		return false
	}
	p.mu.Lock()
	p.lease = renovado
	p.mu.Unlock()
	return true
}

// largar ANUNCIA que esta réplica deixou de servir o laço, tornando-o IMEDIATAMENTE
// reclamável em vez de esperar o TTL (ADR-023 §2.5). Idempotente e nil-safe.
//
// O ANÚNCIO É O ÚLTIMO ACTO: o renovador é parado e AGUARDADO antes do `Release`. Pela
// ordem inversa, um heartbeat em voo podia estender a expiração DEPOIS de ela ter sido
// encurtada para `now` — e a réplica seguinte voltaria a esperar o TTL inteiro, que é
// exactamente o que o anúncio existe para evitar.
func (p *posseDeLaco) largar(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.detida {
		p.mu.Unlock()
		return
	}
	lease, parar := p.lease, p.parar
	p.detida = false
	p.parar = nil
	p.mu.Unlock()

	if parar != nil {
		parar()
	}
	p.lider.Store(false)
	if err := p.leases.Release(ctx, lease); err != nil && !errors.Is(err, durable.ErrLeaseSuperseded) {
		p.logf("posse do laco %q: ANUNCIO de largada FALHOU (%v) — a replica seguinte tera de esperar o TTL do lease em vez de assumir de imediato", p.nome, err)
		return
	}
	p.logf("posse do laco %q: LARGADA por anuncio (lease:%s) — a replica seguinte assume SEM esperar o TTL", p.nome, chaveDePosseDeLaco(p.nome))
}

// logf é o log da posse, tolerante a um logger não composto (testes que não o injectam).
func (p *posseDeLaco) logf(format string, args ...any) {
	if p.log == nil {
		return
	}
	p.log(format, args...)
}
