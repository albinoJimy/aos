package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeLedgerApplied é o tipo canónico do evento que materializa uma entrada
// do step-ledger no Event Store. Um evento por passo lógico aplicado.
const EventTypeLedgerApplied = "step.ledger.applied"

// ledgerStepPrefix namespaceia o step_id do evento de ledger no envelope do Event
// Store, para que a sua idempotency_key (run_id + ":ledger-" + step_id) seja
// DISTINTA da do turno (run_id:step_id) e da tool call (run_id:step_id-tool-n) —
// senão o ES deduplicaria o registo do ledger contra o evento de turno homónimo.
//
// O namespace é RESERVADO: [StepLedger.Apply] rejeita ([ErrReservedStepID])
// qualquer chave cujo step_id comece por este prefixo, fechando estruturalmente a
// colisão global de dedup do ES (um step de negócio "ledger-x" contra o registo do
// ledger de "x", ambos com idempotency_key "run:ledger-x").
const ledgerStepPrefix = "ledger-"

// Result é o resultado de um efeito de passo, memorizado no ledger. Payload é
// opaco (bytes do resultado da activity); Status classifica o desfecho.
//
// # Segredos (ver também [WithSensitiveResults] e README, secção "segredos")
//
// O Payload é persistido EM CLARO no evento durável do Event Store (o cifrado
// por-titular do ES é dívida de AOS-093). Para resultados SENSÍVEIS, o chamador
// deve passar uma REFERÊNCIA (hash/URI) em vez dos bytes em claro e marcar
// [Result.Reference]. Por defeito o ledger persiste o Payload tal como o recebe
// (convenção de chamador); com [WithSensitiveResults] activo, o ledger RECUSA
// ([ErrClearResultInSensitiveMode]) um Payload não-vazio não marcado como Reference.
type Result struct {
	// Status classifica o desfecho do efeito (p.ex. "ok"). Livre para o chamador.
	Status string `json:"status"`
	// Payload são os bytes do resultado a memorizar e devolver no retry.
	Payload []byte `json:"payload,omitempty"`
	// Reference declara que Payload é uma REFERÊNCIA (hash/URI) e não bytes de
	// resultado em claro. Só é consultado em modo sensível ([WithSensitiveResults])
	// como guarda de escrita; não é persistido (o evento durável guarda apenas
	// Status/Payload/hash).
	Reference bool `json:"-"`
}

// hash devolve o SHA-256 hex do payload do resultado (integridade/deduplicação de
// conteúdo; nunca revela o payload em claro).
func (r Result) hash() string {
	sum := sha256.Sum256(r.Payload)
	return hex.EncodeToString(sum[:])
}

// ledgerRecord é o corpo JSON do evento step.ledger.applied. Guarda a associação
// {key → status, resultado, hash(resultado)} exigida pelo contrato (AOS-014).
type ledgerRecord struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Result []byte `json:"result,omitempty"`
	Hash   string `json:"result_hash"`
}

func (r ledgerRecord) result() Result { return Result{Status: r.Status, Payload: r.Result} }

// Observer é o gancho de observabilidade do ledger (contadores apply/dedup). Só
// recebe a forma OPACA (hash) da chave — nunca a chave em claro nem o resultado —
// para honrar "sem segredos em logs" (DoD AOS-014). Default: [NopObserver].
type Observer interface {
	// Applied é chamado quando Apply corre o efeito e regista uma entrada NOVA.
	Applied(keyHash string)
	// Deduplicated é chamado quando Apply encontra a chave já aplicada (in-memory
	// ou por corrida no Event Store) e devolve o resultado memorizado sem novo
	// efeito observável.
	Deduplicated(keyHash string)
}

// NopObserver descarta os eventos de observabilidade. É o default.
type NopObserver struct{}

func (NopObserver) Applied(string)      {}
func (NopObserver) Deduplicated(string) {}

// EventStore é o subconjunto do Event Store de que o [StepLedger] depende:
// Append (persistência durável com dedup por idempotency_key) e Read (reconstrução
// do estado a partir do log). *eventstore.Store satisfaz esta interface.
type EventStore interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// StepLedger é o LEDGER DE RESULTADO por passo sobre o Event Store (AOS-014). É a
// interface pública consumível por AOS-020 (sagas), AOS-021 (activities) e AOS-022.
//
// # Garantia (ADR-001) — honesta
//
// O contrato é AT-LEAST-ONCE + idempotência downstream = ZERO efeitos OBSERVÁVEIS
// duplicados. NÃO é exactly-once do efeito externo (impossível sem cooperação
// downstream). O ledger garante que:
//   - a verificação "already-applied" PRECEDE qualquer efeito;
//   - a chave (run_id:step_id) que o effect propaga ao downstream é determinística
//     e idêntica entre tentativas — logo um downstream que a honre deduplica o
//     efeito e regista-o UMA vez observável, mesmo que o effect corra >1 vez
//     (crash entre o efeito e o commit do ledger).
//
// # Alcance EXACTO da precedência "already-applied" (âmbitos de dedup)
//
// A frase "already-applied precede qualquer efeito" NÃO é, por si só, um
// single-flight durável nem um read-through ao Event Store. Há DOIS âmbitos de
// deduplicação, que só juntos dão <=1 efeito observável:
//   - IN-PROCESS: a verificação in-memory (l.records) mais o SINGLE-FLIGHT por-key
//     (l.inflight) colapsam Applies concorrentes E repetidos da mesma key DENTRO
//     do mesmo processo — o effect corre no máximo uma vez por processo por key
//     em voo. NÃO cobre um processo NOVO cujo estado in-memory está vazio.
//   - DURÁVEL: a dedup do ES no commit (StatusDuplicate, key run_id:ledger-step_id)
//     mais a idempotência DOWNSTREAM (pela key run_id:step_id que o effect propaga).
//     É esta camada — não a in-memory — que garante <=1 efeito OBSERVÁVEL após um
//     restart-sem-Rebuild ou entre workers distintos: nesse caso o effect PODE
//     correr outra vez (at-least-once) e a segurança assenta na idempotência
//     downstream + na dedup do ES no commit, obtendo-se StatusDuplicate pós-efeito.
//
// Ou seja: a verificação in-memory é um ATALHO barato (fast-path) e um single-flight
// intra-processo; a garantia de "zero duplicados observáveis" repousa na dedup
// durável + idempotência downstream. Um consumidor NÃO deve saltar a dedup
// downstream em passos "baratos" confiando só na precedência in-memory. Para
// eliminar a re-execução após restart no MESMO processo, chame [StepLedger.Rebuild]
// antes do primeiro Apply de um run.
//
// # Reconstrutibilidade
//
// O estado do ledger é derivado do log do Event Store: [StepLedger.Rebuild] relê o
// stream do run e reconstrói o mapa key→resultado. Sobrevive, por isso, ao
// reinício do worker.
//
// Seguro para uso concorrente.
type StepLedger struct {
	store     EventStore
	obs       Observer
	producer  eventstore.Producer
	sensitive bool

	mu       sync.Mutex
	records  map[string]ledgerRecord
	inflight map[string]*inflightCall
}

// inflightCall coordena Applies concorrentes da MESMA key DENTRO do processo
// (single-flight): o primeiro a chegar corre o effect; os restantes esperam em
// done e partilham o desfecho canónico, colapsando N efeitos concorrentes em 1.
// Fecha a janela em que a verificação in-memory (que liberta o lock antes do
// effect) deixaria duas goroutines de primeira-vez correr o effect em paralelo.
type inflightCall struct {
	done    chan struct{} // fechado quando o líder termina; publica rec/applied/err
	rec     ledgerRecord  // resultado canónico memorizado (para os seguidores)
	applied bool          // desfecho do líder (só o líder devolve applied=true)
	err     error         // erro do líder, se houve (seguidores propagam-no)
}

// LedgerOption configura o [StepLedger].
type LedgerOption func(*StepLedger)

// WithObserver injecta o gancho de observabilidade (default [NopObserver]).
func WithObserver(o Observer) LedgerOption { return func(l *StepLedger) { l.obs = o } }

// WithSensitiveResults activa a guarda de segredos ao nível do módulo: com ela, o
// [StepLedger.Apply] RECUSA ([ErrClearResultInSensitiveMode]) memorizar um Result
// com Payload não-vazio que não esteja marcado como [Result.Reference] — porque o
// Payload é persistido em claro no Event Store (o cifrado por-titular é dívida de
// AOS-093). É OPT-IN: o default preserva o contrato documentado (Payload tal-como-
// recebido, redacção a cargo do chamador). Consumidores AOS-021 com resultados de
// tool calls sensíveis devem activá-la e propagar uma referência (hash/URI).
func WithSensitiveResults() LedgerOption { return func(l *StepLedger) { l.sensitive = true } }

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada
// nos eventos do ledger. Default: Producer zero (aceitável em teste; em produção
// o run injecta o principal do agente).
func WithProducer(p eventstore.Producer) LedgerOption {
	return func(l *StepLedger) { l.producer = p }
}

// NewStepLedger constrói um ledger sobre o Event Store dado. store é obrigatório.
func NewStepLedger(store EventStore, opts ...LedgerOption) (*StepLedger, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	l := &StepLedger{
		store:    store,
		obs:      NopObserver{},
		records:  make(map[string]ledgerRecord),
		inflight: make(map[string]*inflightCall),
	}
	for _, o := range opts {
		o(l)
	}
	if l.obs == nil {
		l.obs = NopObserver{}
	}
	return l, nil
}

// Apply é o ponto de entrada do contrato de idempotência por passo.
//
//	res, wasApplied, err := ledger.Apply(ctx, key, effect)
//
// Semântica (ADR-001):
//
//  1. VERIFICA already-applied ANTES de qualquer efeito. Se a chave já tem
//     resultado registado (in-memory, reconstruído do Event Store), devolve o
//     resultado memorizado com wasApplied=false e NÃO corre o effect.
//  2. Caso contrário corre o effect (que é responsável por propagar a chave ao
//     downstream — é o downstream que deduplica os efeitos externos), REGISTA
//     {key → status, resultado, hash(resultado)} de forma durável no Event Store,
//     e devolve o resultado com wasApplied=true.
//
// Se o effect devolver erro, NADA é registado (o passo não ficou aplicado) e o
// retry volta a correr o effect — a convergência sem duplicação observável fica
// a cargo da idempotência downstream sobre a mesma key.
//
// Corrida entre workers: se dois workers correrem o effect em paralelo, o Event
// Store deduplica o registo (StatusDuplicate) e Apply devolve o resultado
// CANÓNICO do vencedor com wasApplied=false — garantindo que o resultado devolvido
// é idêntico independentemente de quem ganhou a corrida.
//
// key deve ser a forma canónica run_id:step_id (produto de [IdempotencyKey]);
// caso contrário devolve [ErrMalformedKey].
func (l *StepLedger) Apply(ctx context.Context, key string, effect func(context.Context) (Result, error)) (Result, bool, error) {
	if effect == nil {
		return Result{}, false, ErrNilEffect
	}
	runID, stepID, err := SplitKey(key)
	if err != nil {
		return Result{}, false, err
	}
	// O prefixo "ledger-" é o namespace que o próprio ledger usa no envelope do ES;
	// aceitá-lo num step de negócio colidiria com o registo do ledger homónimo na
	// dedup GLOBAL do ES. Recusa estruturalmente essa colisão.
	if strings.HasPrefix(stepID, ledgerStepPrefix) {
		return Result{}, false, ErrReservedStepID
	}
	keyHash := HashKey(key)

	// (1) already-applied / single-flight ANTES de qualquer efeito.
	l.mu.Lock()
	if rec, ok := l.records[key]; ok {
		l.mu.Unlock()
		l.obs.Deduplicated(keyHash)
		return rec.result(), false, nil
	}
	if call, ok := l.inflight[key]; ok {
		// Outra goroutine deste processo já corre o effect desta key: espera e
		// partilha o desfecho canónico — colapsa efeitos concorrentes em 1.
		l.mu.Unlock()
		<-call.done
		if call.err != nil {
			return Result{}, false, call.err
		}
		l.obs.Deduplicated(keyHash)
		return call.rec.result(), false, nil
	}
	call := &inflightCall{done: make(chan struct{})}
	l.inflight[key] = call
	l.mu.Unlock()

	// Somos o líder do single-flight. Publica o desfecho aos seguidores e limpa o
	// slot in-flight SEMPRE (mesmo em erro/panic do effect) para não os bloquear.
	var (
		res     Result
		applied bool
		rec     ledgerRecord
	)
	defer func() {
		l.mu.Lock()
		call.rec, call.applied, call.err = rec, applied, err
		delete(l.inflight, key)
		l.mu.Unlock()
		close(call.done)
	}()

	res, applied, rec, err = l.runEffect(ctx, key, runID, stepID, keyHash, effect)
	return res, applied, err
}

// runEffect corre o effect e materializa o registo durável. Devolve o resultado,
// se foi o líder a aplicar, e o registo CANÓNICO (o do vencedor, em caso de corrida
// entre workers) para o single-flight publicar aos seguidores.
func (l *StepLedger) runEffect(ctx context.Context, key, runID, stepID, keyHash string, effect func(context.Context) (Result, error)) (Result, bool, ledgerRecord, error) {
	// (2) corre o effect (efeito externo; propaga a key ao downstream).
	res, err := effect(ctx)
	if err != nil {
		return Result{}, false, ledgerRecord{}, err
	}

	// (2b) guarda de segredos opt-in: recusa memorizar resultado em claro.
	if l.sensitive && len(res.Payload) > 0 && !res.Reference {
		return Result{}, false, ledgerRecord{}, ErrClearResultInSensitiveMode
	}

	// (3) regista o resultado de forma durável. O envelope usa uma idempotency_key
	// namespaced (run_id:ledger-step_id) para não colidir com o evento de turno.
	rec := ledgerRecord{Key: key, Status: res.Status, Result: res.Payload, Hash: res.hash()}
	payload, err := json.Marshal(rec)
	if err != nil {
		return Result{}, false, ledgerRecord{}, err
	}
	appendRes, err := l.store.Append(ctx, runID, eventstore.EventInput{
		Type:     EventTypeLedgerApplied,
		Payload:  payload,
		RunID:    runID,
		StepID:   ledgerStepPrefix + stepID,
		Producer: l.producer,
	})
	if err != nil {
		return Result{}, false, ledgerRecord{}, err
	}

	if appendRes.Status == eventstore.StatusDuplicate {
		// Outro worker já tinha commitado este registo — o resultado canónico é o
		// dele. Reconstrói a partir do evento devolvido e devolve-o (idêntico).
		canonical, perr := decodeRecord(appendRes.Event.Payload)
		if perr != nil {
			return Result{}, false, ledgerRecord{}, perr
		}
		l.mu.Lock()
		l.records[key] = canonical
		l.mu.Unlock()
		l.obs.Deduplicated(keyHash)
		return canonical.result(), false, canonical, nil
	}

	// Committed: registo novo.
	l.mu.Lock()
	l.records[key] = rec
	l.mu.Unlock()
	l.obs.Applied(keyHash)
	return res, true, rec, nil
}

// Applied indica se a chave já tem resultado registado no ledger (in-memory).
// Útil para checkpoint/replay (AOS-015/016) inspeccionarem o estado sem efeito.
func (l *StepLedger) Applied(key string) (Result, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.records[key]
	if !ok {
		return Result{}, false
	}
	return rec.result(), true
}

// Rebuild reconstrói o estado do ledger para um run a partir do log do Event
// Store: relê o stream do run_id e reindexa cada evento step.ledger.applied em
// key→resultado. É idempotente e pode ser chamada após reinício do worker para
// recuperar o estado durável (sobrevive à morte do processo). Um stream ainda sem
// eventos não é erro (devolve nil).
func (l *StepLedger) Rebuild(ctx context.Context, runID string) error {
	events, err := l.store.Read(ctx, runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil
		}
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range events {
		if e.Type != EventTypeLedgerApplied {
			continue
		}
		rec, derr := decodeRecord(e.Payload)
		if derr != nil {
			return derr
		}
		l.records[rec.Key] = rec
	}
	return nil
}

func decodeRecord(raw json.RawMessage) (ledgerRecord, error) {
	var rec ledgerRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return ledgerRecord{}, err
	}
	return rec, nil
}
