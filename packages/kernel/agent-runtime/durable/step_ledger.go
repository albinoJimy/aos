package durable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
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
// # Segredos (ver também [WithContentSealer], [WithSensitiveResults] e README)
//
// Com [WithContentSealer] ligado (AOS-093, o caminho de PRODUÇÃO), o Payload é
// CIFRADO por chave POR-TITULAR (envelope DEK/KEK) antes de tocar o Event Store — o
// texto-claro nunca vai ao WAL e o crypto-shredding torna-o irrecuperável. Sem sealer,
// o Payload é persistido tal como recebido (convenção de chamador); nesse caso, para
// resultados SENSÍVEIS o chamador passa uma REFERÊNCIA (hash/URI) e marca
// [Result.Reference], e [WithSensitiveResults] RECUSA
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
//
// # Cifra por-titular (AOS-093)
//
// Com [WithContentSealer] activo e um titular resolúvel, Result carrega o ciphertext
// de envelope (DEK/KEK) em vez do payload em claro e Sealed=true marca-o; Subject é o
// titular (NHI-id) sob cuja KEK foi cifrado — o identificador que o crypto-shredding
// destrói. O texto-claro NUNCA toca o WAL. Hash é sempre o SHA-256 do payload EM CLARO
// (integridade/dedup por conteúdo; um hash não revela nem recupera o plaintext).
type ledgerRecord struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Result  []byte `json:"result,omitempty"`
	Hash    string `json:"result_hash"`
	Sealed  bool   `json:"sealed,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Fingerprint é a impressão digital da ACÇÃO que produziu esta entrada — não do seu
	// resultado (esse é o `Hash`). Existe porque a chave de idempotência é `f(run_id,
	// step_id)` e o step_id é POSICIONAL: nada da acção entra nele.
	//
	// A premissa de [StepSequencer.StepID] — «o mesmo passo lógico recebe SEMPRE o mesmo
	// step_id» — é falsa numa retoma em que o turno não tenha captura: o modelo é
	// re-interrogado ao vivo, pode emitir OUTRA tool call, e essa call recebe o step_id já
	// aplicado. Sem esta impressão, o dedup devolvia-lhe o resultado da acção ANTERIOR — sem
	// executar, sem passar pelo Reference Monitor, e sem que o laço distinguisse.
	//
	// `omitempty` de propósito: entradas ANTERIORES a este campo não o têm, e a ausência é
	// tratada como «não verificável» e não como «diferente» — ver [ledgerRecord.mesmaAccao].
	Fingerprint string `json:"action_fingerprint,omitempty"`
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
	// cipher cifra o Result.Payload por chave POR-TITULAR antes de o persistir e
	// decifra-o no rebuild (AOS-093). nil ⇒ sem cifra (payload tal-como-recebido,
	// retro-compat). O titular vem de [ContextWithTitular] (o principal do RUN, que é
	// por-chamada) e, na sua ausência, do [eventstore.Producer.NHIID] de [WithProducer]
	// (identidade de COMPOSIÇÃO, fixa no arranque) — ver [StepLedger.titularOf].
	cipher agentruntime.ContentCipher
	// requireTitular liga a guarda fail-closed de [WithRequireTitular]: com cifra
	// composta, um Apply sem titular resolvível é RECUSADO ([ErrNoTitular]).
	requireTitular bool

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

// WithSensitiveResults activa a guarda de segredos ao nível do módulo (via de
// REFERÊNCIA, para quem NÃO liga a cifra por-titular): com ela, o [StepLedger.Apply]
// RECUSA ([ErrClearResultInSensitiveMode]) memorizar um Result com Payload não-vazio
// não marcado como [Result.Reference], porque sem sealer o Payload iria em claro para o
// Event Store. O caminho de PRODUÇÃO (AOS-093) é [WithContentSealer], que CIFRA o
// Payload por-titular — dispensa esta recusa (a cifra é a garantia). É OPT-IN: o default
// preserva o contrato documentado (Payload tal-como-recebido, redacção a cargo do
// chamador). Consumidores AOS-021 sem cifra devem activá-la e propagar uma referência.
func WithSensitiveResults() LedgerOption { return func(l *StepLedger) { l.sensitive = true } }

// WithContentSealer liga a cifra POR-TITULAR do Result.Payload (AOS-093): com ela,
// o ledger cifra o payload de cada passo sob a KEK do TITULAR do run (o
// [WithProducer].NHIID) por envelope DEK/KEK ANTES de o persistir no Event Store — o
// texto-claro nunca toca o WAL — e decifra-o no [StepLedger.Rebuild]. Destruir a KEK
// do titular (crypto-shredding) torna o resultado memorizado irrecuperável sem mutar
// o log; um passo cujo titular foi apagado deixa de ser reconstruído no rebuild.
//
// É OPT-IN e coexiste com [WithSensitiveResults]: com o sealer activo, um payload em
// claro NÃO é recusado — é cifrado (a cifra é a garantia de confidencialidade). Sem
// sealer, o comportamento documentado mantém-se (payload tal-como-recebido; recusa em
// modo sensível). Requer um titular resolúvel ([ContextWithTitular] ou, em fallback,
// Producer.NHIID): sem ele o payload é persistido como antes (retro-compat honesto —
// declarado, não silencioso), a menos que [WithRequireTitular] esteja ligada, que
// transforma essa degradação numa RECUSA ([ErrNoTitular]).
func WithContentSealer(cipher agentruntime.ContentCipher) LedgerOption {
	return func(l *StepLedger) { l.cipher = cipher }
}

// WithRequireTitular exige um TITULAR resolvível quando a cifra por-titular está
// composta (AOS-245). É a postura de PRODUÇÃO: com ela, [StepLedger.Apply] recusa
// ([ErrNoTitular]) — ANTES de correr qualquer efeito — sempre que
// [WithContentSealer] está ligada e nem o contexto ([ContextWithTitular]) nem o
// produtor ([WithProducer]) dão um titular.
//
// PORQUÊ: sem ela, um ledger com cifrador composto mas sem titular NÃO falha — cai no
// caminho retro-compatível e persiste o Result.Payload EM CLARO no WAL, ficando fora do
// alcance do crypto-shredding por-titular (AOS-093). Era uma degradação SILENCIOSA de
// uma garantia de protecção de dados: o cifrador ligado e inerte parece cobertura, e não
// é. Com a guarda, a única forma de o WAL não levar o payload cifrado é o nó recusar o
// passo — o modo de falha correcto. Sem cifrador composto a opção é inerte (nada a
// proteger); é OPT-IN para não alterar o contrato de quem usa o ledger sem cifra.
func WithRequireTitular() LedgerOption {
	return func(l *StepLedger) { l.requireTitular = true }
}

// WithProducer define a identidade emissora (NHI + cadeia de delegação) gravada
// nos eventos do ledger. Default: Producer zero (aceitável em teste; em produção
// o run injecta o principal do agente).
//
// TITULAR (AOS-093/AOS-245): o NHIID do produtor serve de FALLBACK para o titular da
// cifra por-titular, mas só é correcto quando o ledger é composto POR-RUN (o caso do
// [engine] adapter). Um ledger PARTILHADO por vários runs — o do nó `aos`, composto uma
// vez no arranque — não pode ter aqui o titular: ele é por-run e chega por
// [ContextWithTitular], que tem precedência.
func WithProducer(p eventstore.Producer) LedgerOption {
	return func(l *StepLedger) { l.producer = p }
}

// titularKey é a chave (tipo privado, não-colidível) do titular do run no contexto.
type titularKey struct{}

// ContextWithTitular anexa ao contexto o TITULAR do run — o NHI-id do principal
// (ADR-003) sob cuja KEK por-titular o ledger cifra o Result.Payload antes de o
// persistir (AOS-093/AOS-245). Um subject vazio devolve o contexto inalterado.
//
// PORQUÊ NO CONTEXTO. O titular é POR-RUN; o [StepLedger] do nó é composto UMA vez no
// arranque e servido a TODOS os runs. Fixá-lo na composição ([WithProducer]) selaria
// todo o conteúdo sob a identidade do nó em vez da do titular — a chave errada para o
// crypto-shredding — e mudar a assinatura de [StepLedger.Apply] partiria a porta
// [activity.Ledger] e todos os seus implementadores. O contexto é a via que já
// atravessa o despacho intacta (o mesmo idioma do plano de replay e da correlação de
// sandbox), pelo que o titular chega ao ponto de selagem sem que nenhuma camada
// intermédia o tenha de transportar.
//
// Quem o anexa é o ponto que CONHECE o run: o [activity.Dispatcher], a partir do
// Principal da activity — o mesmo valor que o capturer sela (goal.Principal.NHIID),
// para que os MESMOS bytes fiquem sob a MESMA KEK em replay.captured e em
// step.ledger.applied.
func ContextWithTitular(ctx context.Context, subject string) context.Context {
	if subject == "" {
		return ctx
	}
	return context.WithValue(ctx, titularKey{}, subject)
}

// TitularFrom devolve o titular anexado por [ContextWithTitular] ("" se ausente).
func TitularFrom(ctx context.Context) string {
	s, _ := ctx.Value(titularKey{}).(string)
	return s
}

// titularOf resolve o titular sob cuja KEK selar: o do RUN (contexto) tem PRECEDÊNCIA
// sobre o da COMPOSIÇÃO (produtor). A ordem é deliberada — o titular dos dados é o
// principal do run, não a identidade emissora do nó; o produtor só cobre o caso do
// ledger composto por-run (engine adapter) e a retro-compatibilidade.
func (l *StepLedger) titularOf(ctx context.Context) string {
	if s := TitularFrom(ctx); s != "" {
		return s
	}
	return l.producer.NHIID
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
func (l *StepLedger) Apply(ctx context.Context, key string, effect func(context.Context) (Result, error), opts ...ApplyOption) (Result, bool, error) {
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
	// GUARDA FAIL-CLOSED DO TITULAR (AOS-245), ANTES de qualquer efeito e antes do
	// fast-path de dedup: com a cifra por-titular composta e o modo estrito ligado, um
	// Apply sem titular resolvível NÃO corre. Recusar aqui — e não no momento da
	// selagem — evita a única alternativa possível, que seria correr o efeito externo e
	// só depois descobrir que o seu resultado não pode ser persistido em segurança.
	if l.requireTitular && l.cipher != nil && l.titularOf(ctx) == "" {
		return Result{}, false, ErrNoTitular
	}
	keyHash := HashKey(key)

	cfg := aplicarOpcoes(opts)

	// (1) already-applied / single-flight ANTES de qualquer efeito.
	l.mu.Lock()
	if rec, ok := l.records[key]; ok {
		l.mu.Unlock()
		if err := rec.mesmaAccao(cfg.fingerprint); err != nil {
			return Result{}, false, err
		}
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
		// A MESMA verificação do ramo acima. Sem ela, duas acções DIFERENTES com o mesmo
		// step_id que chegassem em concorrência colapsavam no single-flight e a segunda
		// recebia o resultado da primeira — a janela é estreita, mas é a mesma corrupção.
		if err := call.rec.mesmaAccao(cfg.fingerprint); err != nil {
			return Result{}, false, err
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

	res, applied, rec, err = l.runEffect(ctx, key, runID, stepID, keyHash, cfg.fingerprint, effect)
	return res, applied, err
}

// runEffect corre o effect e materializa o registo durável. Devolve o resultado,
// se foi o líder a aplicar, e o registo CANÓNICO (o do vencedor, em caso de corrida
// entre workers) para o single-flight publicar aos seguidores.
func (l *StepLedger) runEffect(ctx context.Context, key, runID, stepID, keyHash, fingerprint string, effect func(context.Context) (Result, error)) (Result, bool, ledgerRecord, error) {
	// (2) corre o effect (efeito externo; propaga a key ao downstream).
	res, err := effect(ctx)
	if err != nil {
		return Result{}, false, ledgerRecord{}, err
	}

	// (2b) guarda de segredos opt-in: com o [ContentCipher] ligado a CIFRA é a
	// garantia de confidencialidade (o payload em claro é cifrado, não recusado); sem
	// ele, mantém-se a recusa opt-in de memorizar resultado em claro.
	if l.cipher == nil && l.sensitive && len(res.Payload) > 0 && !res.Reference {
		return Result{}, false, ledgerRecord{}, ErrClearResultInSensitiveMode
	}

	// (3) constrói o registo EM CLARO (memória/retorno) e o registo a PERSISTIR. Com a
	// cifra por-titular ligada (AOS-093) e um titular resolúvel, o payload é cifrado por
	// envelope DEK/KEK ANTES de tocar o WAL; o texto-claro fica só em memória (dedup
	// intra-processo) e nunca é persistido. O Hash é sempre do payload EM CLARO —
	// integridade/dedup por conteúdo, sem revelar nem recuperar o plaintext.
	clearRec := ledgerRecord{Key: key, Status: res.Status, Result: res.Payload, Hash: res.hash(), Fingerprint: fingerprint}
	persistRec := clearRec
	// O titular é o do RUN (contexto) e, em fallback, o da composição (produtor) — ver
	// [StepLedger.titularOf]. Com [WithRequireTitular] ligada, chegar aqui com o
	// cifrador composto garante subject != "" (a guarda de Apply precede o efeito).
	subject := l.titularOf(ctx)
	if l.cipher != nil && subject != "" && len(res.Payload) > 0 {
		sealed, serr := l.cipher.SealContent(ctx, subject, runID, res.Payload)
		if serr != nil {
			return Result{}, false, ledgerRecord{}, serr
		}
		persistRec = ledgerRecord{
			Key: key, Status: res.Status, Result: sealed, Hash: clearRec.Hash,
			Sealed: true, Subject: subject, Fingerprint: fingerprint,
		}
	}

	// O envelope usa uma idempotency_key namespaced (run_id:ledger-step_id) para não
	// colidir com o evento de turno.
	payload, err := json.Marshal(persistRec)
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
		// dele. Reconstrói a partir do evento devolvido, DECIFRA-o (se cifrado) e
		// devolve-o em claro (idêntico ao que o vencedor memorizou).
		canonical, perr := decodeRecord(appendRes.Event.Payload)
		if perr != nil {
			return Result{}, false, ledgerRecord{}, perr
		}
		clearCanonical, cerr := l.toClear(ctx, canonical)
		if cerr != nil {
			return Result{}, false, ledgerRecord{}, cerr
		}
		l.mu.Lock()
		l.records[key] = clearCanonical
		l.mu.Unlock()
		l.obs.Deduplicated(keyHash)
		return clearCanonical.result(), false, clearCanonical, nil
	}

	// Committed: registo novo. Guarda o CLARO em memória (o WAL tem o cifrado).
	l.mu.Lock()
	l.records[key] = clearRec
	l.mu.Unlock()
	l.obs.Applied(keyHash)
	return res, true, clearRec, nil
}

// toClear devolve o registo com o Result EM CLARO: se rec.Sealed (cifrado por-titular,
// AOS-093), decifra-o via o [ContentCipher] sob a KEK do titular; caso contrário
// devolve-o inalterado. FAIL-CLOSED: um registo cifrado sem cipher ligado devolve
// [ErrSealedResultNoCipher]; se a KEK do titular foi destruída (crypto-shredding), a
// decifragem falha e o erro propaga-se — um passo de um titular apagado não é
// recuperável, por desenho.
func (l *StepLedger) toClear(ctx context.Context, rec ledgerRecord) (ledgerRecord, error) {
	if !rec.Sealed {
		return rec, nil
	}
	if l.cipher == nil {
		return ledgerRecord{}, ErrSealedResultNoCipher
	}
	plain, err := l.cipher.OpenContent(ctx, rec.Subject, rec.Result)
	if err != nil {
		return ledgerRecord{}, err
	}
	rec.Result = plain
	rec.Sealed = false
	rec.Subject = ""
	return rec, nil
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
		clear, cerr := l.toClear(ctx, rec)
		if cerr != nil {
			// AOS-093: um registo cifrado cujo titular foi crypto-shredded (KEK
			// destruída) já NÃO é decifrável — o passo deixa de ser reconstruído (o run
			// está a ser apagado; a re-execução é irrelevante). O blob mantém-se no log
			// (a cadeia continua a validar); apenas não re-hidrata o estado in-memory.
			// Um wiring sem cipher sobre um store cifrado ([ErrSealedResultNoCipher]) é
			// erro de composição e propaga-se — nunca se devolve ciphertext como claro.
			if errors.Is(cerr, ErrSealedResultNoCipher) {
				return cerr
			}
			continue
		}
		l.records[clear.Key] = clear
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

// ---------------------------------------------------------------------------
// IMPRESSÃO DIGITAL DA ACÇÃO — a chave não muda; o que muda é o que se compara.
// ---------------------------------------------------------------------------

// ApplyOption configura uma chamada a [StepLedger.Apply]. Variádica de propósito: os
// chamadores que não a usam compilam e comportam-se como antes.
type ApplyOption func(*applyConfig)

type applyConfig struct{ fingerprint string }

func aplicarOpcoes(opts []ApplyOption) applyConfig {
	var c applyConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	return c
}

// WithActionFingerprint declara QUE ACÇÃO este Apply pretende aplicar.
//
// PORQUE EXISTE, e porque não vai na chave. A forma `f(run_id, step_id)` da chave está
// fixada no ADR-001 e [IdempotencyKey] é uma BIJECÇÃO declarada, com [SplitKey] como
// inversa exacta que o próprio [StepLedger.Apply] usa para recuperar o par. Meter o digest
// da acção na chave partiria as três coisas e obrigaria a migrar todos os registos.
//
// A alternativa é a que o [integration.ApprovalBroker] já usa para os grants de aprovação:
// mesmo id, conteúdo diferente ⇒ RECUSA (`ErrGrantIDReused`). Aqui é o mesmo problema uma
// camada abaixo — e a mesma resposta.
func WithActionFingerprint(fp string) ApplyOption {
	return func(c *applyConfig) { c.fingerprint = fp }
}

// ErrActionMismatch — a chave já tem uma entrada aplicada por uma acção DIFERENTE.
//
// Fail-closed: devolver o resultado memorizado seria executar zero vezes a acção submetida
// e responder-lhe com o desfecho de outra. É a corrupção que este erro existe para tornar
// visível — e que, sem ele, não deixava rasto nenhum.
var ErrActionMismatch = errors.New("durable: idempotency key ja aplicada por uma ACCAO DIFERENTE (o step_id e posicional; a accao submetida nao e a que produziu o resultado memorizado)")

// mesmaAccao decide se o resultado memorizado pode ser devolvido a esta submissão.
//
// TRÊS CASOS, e o do meio é o residual declarado:
//
//	registada != "" e submetida != "" e diferentes ⇒ RECUSA (o defeito)
//	registada == ""                                ⇒ ACEITA — entrada anterior a este campo
//	submetida == ""                                ⇒ ACEITA — chamador que não declara acção
//
// A ausência é tratada como «não verificável», nunca como «diferente». Recusar aí fecharia a
// janela por completo mas tornaria IRRETOMÁVEL qualquer run cujo ledger seja anterior à
// mudança — trocaria uma correcção de segurança por uma perda de disponibilidade. A janela
// que fica aberta é estreita: exige um crash a meio do despacho seguido de re-interrogação
// do modelo, e só afecta entradas já existentes.
func (r ledgerRecord) mesmaAccao(submetida string) error {
	if r.Fingerprint == "" || submetida == "" {
		return nil
	}
	if r.Fingerprint != submetida {
		return fmt.Errorf("%w: registada=%s submetida=%s", ErrActionMismatch, r.Fingerprint, submetida)
	}
	return nil
}
