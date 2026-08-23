package audit

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-092 — JOB de expiração automática por TTL-por-classe.
//
// Onde o [Shredder.PurgeExpired] (AOS-083) apaga a PII de UM titular quando a sua
// retenção expira, o [ExpirationJob] é o VARREDOR: percorre um conjunto de
// registos classificados (o Event Store/memória, atrás de uma porta) e expira
// TODOS os que ultrapassaram o TTL da sua classe — diagnósticos cedo, audit
// conforme a retenção (AC2). É IDEMPOTENTE (idempotency key por passo, ver
// [ExpirationJob.Run]), respeita o [LegalHold] (AC3) e sela cada expiração como um
// evento auditável `retention.expired` na hash-chain WORM + um span OTel (AC5).

// ExpirableRecord é um registo CLASSIFICADO candidato à expiração, tal como a
// fonte ([RecordSource]) o expõe ao job. Traz só o necessário para decidir e
// auditar a expiração — NUNCA o payload nem segredos.
type ExpirableRecord struct {
	// ID identifica univocamente o registo na fonte. Participa na idempotency key
	// (ID+classe+versão-da-política) e é selado no evento de expiração.
	ID string
	// Class é a classe de dado (diagnostic/trajectory/audit/pii_operational).
	Class DataClass
	// CreatedAt é o instante de criação; a idade (agora−CreatedAt) é comparada ao
	// TTL da classe. O relógio "agora" é injectável no job (determinista em teste).
	CreatedAt time.Time
	// SubjectID é o titular (opcional): sujeita o registo ao legal hold por-titular.
	SubjectID string
	// Partition é a partição do registo (opcional): sujeita-o ao legal hold
	// por-partição, verificado directamente (o registo carrega a sua partição).
	Partition string
}

// RecordSource é a PORTA de leitura dos registos classificados a avaliar. O job
// varre-a sem se acoplar a um store concreto — produção liga o Event Store/a
// memória por trás desta interface; os testes ligam uma fonte in-memory.
type RecordSource interface {
	// List devolve os registos classificados candidatos à expiração.
	List(ctx context.Context) ([]ExpirableRecord, error)
}

// ExpirationSink é a PORTA de escrita que MATERIALIZA a expiração de um registo
// (marca/apaga na fonte real: memória, Event Store, ou o crypto-shred de AOS-093
// para PII). DEVE ser IDEMPOTENTE: expirar um registo já expirado é um no-op que
// devolve nil — é o que torna a reexecução do job segura mesmo se o sink for
// invocado mais do que uma vez para o mesmo registo.
type ExpirationSink interface {
	// Expire marca/remove o registo. Idempotente (já-expirado ⇒ no-op, nil).
	Expire(ctx context.Context, rec ExpirableRecord) error
}

// IdempotencyStore guarda as idempotency keys já processadas pelo job, para que a
// REEXECUÇÃO não re-sele o mesmo evento de expiração (AC2). É uma porta para
// permitir durabilidade em produção; o default in-memory
// ([InMemoryIdempotencyStore]) basta para um job de processo único.
type IdempotencyStore interface {
	// Seen indica se a key já foi processada (deve ser saltada).
	Seen(key string) bool
	// Add marca a key como processada (idempotente).
	Add(key string)
}

// InMemoryIdempotencyStore é a implementação de referência do [IdempotencyStore],
// segura para concorrência. Produção liga um índice durável por trás da porta.
type InMemoryIdempotencyStore struct {
	mu   sync.Mutex
	seen map[string]bool
}

// NewInMemoryIdempotencyStore constrói um seen-set vazio.
func NewInMemoryIdempotencyStore() *InMemoryIdempotencyStore {
	return &InMemoryIdempotencyStore{seen: make(map[string]bool)}
}

// Seen implementa [IdempotencyStore].
func (s *InMemoryIdempotencyStore) Seen(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[key]
}

// Add implementa [IdempotencyStore].
func (s *InMemoryIdempotencyStore) Add(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[key] = true
}

// ExpirationReport resume o resultado de uma passagem do job.
type ExpirationReport struct {
	// Scanned é o total de registos avaliados.
	Scanned int
	// Expired é o número de registos expirados NESTA passagem (selados + sink).
	Expired int
	// Held é o número saltados por legal hold (suspensos, AC3).
	Held int
	// Skipped é o número saltados por já terem sido expirados (idempotência).
	Skipped int
	// NotExpired é o número ainda dentro do TTL (ou de classe sem período).
	NotExpired int
	// ExpiredIDs são os IDs expirados nesta passagem (ordem determinista).
	ExpiredIDs []string
}

// ExpirationJob é o job varredor de expiração por TTL. Construa-o com
// [NewExpirationJob] e execute [ExpirationJob.Run] periodicamente (ex. por um
// scheduler). Seguro para uso sequencial; o seen-set default é seguro para
// concorrência mas o Run em si é pensado para uma execução de cada vez.
type ExpirationJob struct {
	config    RetentionConfig
	policy    RetentionPolicy
	holds     *LegalHold
	source    RecordSource
	sink      ExpirationSink
	idem      IdempotencyStore
	index     SubjectPartitionIndex
	audit     Store
	partition string
	clock     func() time.Time
	tracer    otelgenai.Tracer
	// porReconciliar guarda os registos selados cujo sink NAO confirmou a destruicao (achado 1.7).
	porReconciliar ReconciliationSet
}

// ExpirationOption configura o [ExpirationJob].
type ExpirationOption func(*ExpirationJob)

// WithExpirationClock injecta o relógio (determinista em teste). Por omissão
// time.Now. A idade de cada registo é agora−CreatedAt segundo este relógio.
func WithExpirationClock(clock func() time.Time) ExpirationOption {
	return func(j *ExpirationJob) {
		if clock != nil {
			j.clock = clock
		}
	}
}

// WithExpirationTracer liga o [otelgenai.Tracer] que instrumenta a passagem
// (span "aos.retention.sweep") e cada expiração (span "aos.retention.expire") —
// DoD AOS-092. Sem esta opção usa-se o [otelgenai.NoopTracer] (sem overhead).
func WithExpirationTracer(t otelgenai.Tracer) ExpirationOption {
	return func(j *ExpirationJob) {
		if t != nil {
			j.tracer = t
		}
	}
}

// WithExpirationAudit liga o [Store] (hash-chain WORM) e a partição onde o job
// SELA cada expiração como um evento `retention.expired` (AC5). Sem ele, o job
// expira sem selar o evento auditável — em produção liga-se SEMPRE um store.
func WithExpirationAudit(store Store, partition string) ExpirationOption {
	return func(j *ExpirationJob) {
		j.audit = store
		if partition != "" {
			j.partition = partition
		}
	}
}

// WithExpirationIdempotency injecta o [IdempotencyStore] (durável em produção).
// Por omissão um [InMemoryIdempotencyStore] novo por job.
func WithExpirationIdempotency(store IdempotencyStore) ExpirationOption {
	return func(j *ExpirationJob) {
		if store != nil {
			j.idem = store
		}
	}
}

// WithExpirationSubjectIndex liga o [SubjectPartitionIndex] que o job consulta
// para fazer valer o legal hold POR PARTIÇÃO a partir do titular (como o
// [Shredder]). O registo já carrega a sua própria Partition (verificada
// directamente); este índice acrescenta as OUTRAS partições do titular.
func WithExpirationSubjectIndex(idx SubjectPartitionIndex) ExpirationOption {
	return func(j *ExpirationJob) { j.index = idx }
}

// NewExpirationJob constrói o job sobre a policy-as-code de retenção (config), o
// legal hold, a fonte de registos e o sink de expiração. holds nil ⇒ "sem holds
// activos". source/sink são obrigatórios (senão [ExpirationJob.Run] devolve
// [ErrNoExpirationSource]).
func NewExpirationJob(config RetentionConfig, holds *LegalHold, source RecordSource, sink ExpirationSink, opts ...ExpirationOption) *ExpirationJob {
	j := &ExpirationJob{
		config:    config,
		policy:    config.ToPolicy(),
		holds:     holds,
		source:    source,
		sink:      sink,
		partition: DefaultRetentionPartition,
		clock:     time.Now,
		tracer:    otelgenai.NoopTracer{},
	}
	for _, o := range opts {
		o(j)
	}
	if j.idem == nil {
		j.idem = NewInMemoryIdempotencyStore()
	}
	return j
}

// Run faz UMA passagem varredora: lista os registos classificados, e para cada um
// que ultrapassou o TTL da sua classe E não está sob legal hold, sela o evento
// `retention.expired` no audit e materializa a expiração no sink. É IDEMPOTENTE e
// DETERMINISTA (processa por ordem de ID):
//
//   - IDEMPOTÊNCIA: a idempotency key de cada passo é ID+classe ([ExpirableRecord]),
//     ESTÁVEL face a bumps de versão de política. Uma key já vista é saltada, pelo
//     que reexecutar o job — OU alterar o TTL (AC4) — NÃO re-sela o evento nem
//     re-conta a expiração de um registo já expirado ainda visível numa fonte de
//     soft-mark. O sink é, além disso, idempotente por contrato.
//   - LEGAL HOLD (AC3): antes de expirar CADA registo, o job consulta o legal hold
//     (por-titular e por-partição, como o [Shredder]); um registo sob hold é
//     SALTADO fail-closed — não expira mesmo depois de o TTL passar.
//   - ORDEM AUDIT→SINK (fail-closed): a expiração é PRIMEIRO selada no audit e SÓ
//     DEPOIS materializada no sink. Nada é destruído sem o seu evento auditável; se
//     a selagem falhar, o registo NÃO é expirado (é retentado na próxima passagem).
//     A key só é marcada vista após a selagem, pelo que a reexecução não duplica o
//     evento. Uma falha do sink após a selagem deixa a expiração AUDITADA e o sink
//     idempotente reconcilia-a na próxima passagem (a key já está marcada).
//
// Devolve o [ExpirationReport] da passagem e, se algum passo falhou, um erro
// agregado (errors.Join) — os restantes registos são processados na mesma.
func (j *ExpirationJob) Run(ctx context.Context) (ExpirationReport, error) {
	var report ExpirationReport
	if j.source == nil || j.sink == nil {
		return report, ErrNoExpirationSource
	}

	sweepCtx, sweep := j.tracer.StartSpan(ctx, opRetentionSweep)
	sweep.SetAttribute(otelgenai.AttrOperationName, opRetentionSweep)
	sweep.SetAttribute(attrRetentionConfigVer, j.config.Version())
	defer sweep.End()

	records, err := j.source.List(sweepCtx)
	if err != nil {
		sweep.SetAttribute(otelgenai.AttrErrorType, "source_list_failed")
		return report, err
	}
	// Ordem determinista por ID (a fonte pode devolver por ordem de mapa).
	sort.Slice(records, func(a, b int) bool { return records[a].ID < records[b].ID })
	report.Scanned = len(records)

	now := j.clock()
	var errs []error
	for _, rec := range records {
		age := now.Sub(rec.CreatedAt)
		if !j.policy.Expired(rec.Class, age) {
			report.NotExpired++
			continue
		}
		// BARREIRA DE DESTRUIÇÃO (ver [LegalHold.BeginDestruction]). Tomada AQUI e largada só
		// depois do sink, porque a propriedade não é «li o hold atomicamente» — é «entre ter lido
		// o hold e ter destruído, nenhuma colocação de hold pôde intercalar-se». Entre as duas
		// coisas há um `fsync` de ~30 ms, e era essa a janela em que um /dsar/hold respondido 200
		// não protegia nada.
		//
		// POR REGISTO, não por passagem: um operador a colocar um hold espera no máximo por um
		// passo de destruição, nunca pelo varrimento inteiro.
		// A SECÇÃO SOB BARREIRA vive numa função PRÓPRIA, para que a barreira seja largada por
		// `defer` — em TODOS os caminhos de saída, incluindo um `panic`.
		//
		// PORQUÊ, e o achado é da verificação de completude de 2026-08-23: aqui a barreira era
		// tomada e largada À MÃO em cinco sítios. Auditados um a um, os cinco cobrem todos os
		// `return`/`continue` — sem pânico não havia fuga. Mas os outros dois tomadores da MESMA
		// barreira (`Shredder.Shred` e `handleLegalHold`) usam `defer` e são panic-safe, e o
		// contrato escrito em `retention.go` exige que ela seja largada «em TODOS os caminhos de
		// saída».
		//
		// O QUE UM PÂNICO CUSTARIA: o `RLock` fica detido para sempre. O `POST /dsar/hold`
		// seguinte bloqueia em `Lock()` sem timeout e, com um escritor pendente, TODO
		// `BeginDestruction()` posterior bloqueia também — `/dsar/erase` e este varredor param, com
		// `/healthz` e `/readyz` a 200. Pela via do `/dsar/expire` o `net/http` recupera o pânico e
		// o processo SOBREVIVE encravado; pela via do tick o processo morre e o Docker reinicia-o.
		//
		// GRAVIDADE HONESTA: nenhum pânico é hoje alcançável neste caminho — o cliente do Vault
		// devolve erro em todos os ramos e os candidatos a nil estão fechados nos construtores. A
		// porta abre-se com um `Store`, `KeyVault` ou `ExpirationSink` de TERCEIRO, que é
		// precisamente o contrato que este pacote publica.
		d := j.processarSobBarreira(sweepCtx, rec, now)
		if d.held {
			report.Held++
			continue
		}
		if d.expired {
			report.Expired++
			report.ExpiredIDs = append(report.ExpiredIDs, rec.ID)
		}
		if d.skipped {
			report.Skipped++
		}
		if d.err != nil {
			errs = append(errs, d.err)
		}
	}

	sweep.SetAttribute(attrRetentionExpiredCount, report.Expired)
	sweep.SetAttribute(attrRetentionHeldCount, report.Held)
	sweep.SetAttribute(attrRetentionSkippedCount, report.Skipped)
	return report, errors.Join(errs...)
}

// expireOne sela o evento de expiração no audit e materializa-a no sink, sob um
// span "aos.retention.expire". Ordem fail-closed: SELA primeiro (o facto
// auditável), só depois EXPIRA (a destruição). Devolve sealed=true assim que a
// selagem tem sucesso (ou não há audit ligado) e, NESSE instante, MARCA a
// idempotency key — porque a WORM é append-only e o seal não pode ser desfeito, a
// key acompanha o FACTO SELADO, não o sucesso do sink. Assim, se o sink falhar a
// seguir, a expiração fica AUDITADA e a reexecução NÃO re-sela um segundo evento
// (o sink, idempotente por contrato, é reconciliado noutra passagem). Se a
// SELAGEM falhar, nada é destruído, a key NÃO é marcada e o erro propaga-se com
// sealed=false (retentável na próxima passagem).
func (j *ExpirationJob) expireOne(ctx context.Context, rec ExpirableRecord, key string, ttl time.Duration, at time.Time) (bool, error) {
	_, span := j.tracer.StartSpan(ctx, opRetentionExpire)
	span.SetAttribute(otelgenai.AttrOperationName, opRetentionExpire)
	span.SetAttribute(attrRetentionRecordID, rec.ID)
	span.SetAttribute(attrRetentionClass, string(rec.Class))
	span.SetAttribute(attrRetentionTTL, ttl.String())
	span.SetAttribute(attrRetentionConfigVer, j.config.Version())
	defer span.End()

	if j.audit != nil {
		recRec := BuildRetentionExpiredRecord(rec, ttl, j.config.Version(), at, j.partition)
		if _, err := j.audit.Append(ctx, recRec); err != nil {
			span.SetAttribute(attrRetentionResult, retentionResultSealFailed)
			span.SetAttribute(otelgenai.AttrErrorType, "seal_failed")
			return false, err
		}
	}
	// Facto auditável imutável selado (ou sem audit ligado): marca a key ANTES do
	// sink, para que uma falha do sink não deixe a key por-marcar e a reexecução
	// re-sele um evento duplicado na hash-chain WORM.
	j.idem.Add(key)
	if err := j.sink.Expire(ctx, rec); err != nil {
		span.SetAttribute(attrRetentionResult, retentionResultSinkFailed)
		span.SetAttribute(otelgenai.AttrErrorType, "sink_failed")
		// O SINK FALHOU E A CADEIA PASSA A DIZÊ-LO (achado 1.7). Sem este selo, o único registo
		// do facto é o `retention.expired` — byte-idêntico ao caso em que a destruição correu — e
		// a marca de idempotência, reconstruída dele, tornava a falha PERMANENTE: o registo era
		// contado como `skipped` em todas as passagens seguintes e o sink nunca voltava a ser
		// chamado. A KEK ficava viva com a cadeia a afirmar que morreu.
		j.marcarPorReconciliar(ctx, rec, at)
		return true, err
	}
	span.SetAttribute(attrRetentionResult, retentionResultExpired)
	return true, nil
}

// marcarPorReconciliar sela o desmentido e regista a pendência em memória.
//
// FAIL-OPEN DELIBERADO E DECLARADO, ao contrário de quase tudo o resto neste ficheiro: se o selo
// do desmentido não entrar, NÃO se propaga um segundo erro nem se aborta a passagem. O erro do
// sink já vai subir ao chamador, e transformar uma falha de destruição numa falha de selagem
// esconderia a primeira atrás da segunda. A pendência em memória vale para esta vida do processo;
// o que se perde é a sobrevivência ao restart, e isso fica dito em vez de fingido.
func (j *ExpirationJob) marcarPorReconciliar(ctx context.Context, rec ExpirableRecord, at time.Time) {
	if j.porReconciliar != nil {
		j.porReconciliar.Add(rec.ID)
	}
	if j.audit == nil {
		return
	}
	_, _ = j.audit.Append(ctx, buildRetentionSinkOutcome(rec, false, j.config.Version(), at, j.partition))
}

// confirmarReconciliacao sela o desfecho POSITIVO de uma reconciliação e limpa a pendência.
func (j *ExpirationJob) confirmarReconciliacao(ctx context.Context, rec ExpirableRecord, at time.Time) {
	if j.porReconciliar != nil {
		j.porReconciliar.Remove(rec.ID)
	}
	if j.audit == nil {
		return
	}
	_, _ = j.audit.Append(ctx, buildRetentionSinkOutcome(rec, true, j.config.Version(), at, j.partition))
}

// held indica se o registo está sob legal hold — por-titular OU por qualquer
// partição onde tem dados (a sua própria Partition e, via o índice, as demais do
// titular). Espelha [Shredder.held]: fail-closed, basta UMA via retida para
// suspender a expiração deste registo.
func (j *ExpirationJob) held(rec ExpirableRecord) bool {
	if j.holds == nil {
		return false
	}
	if rec.SubjectID != "" && j.holds.HeldSubject(rec.SubjectID) {
		return true
	}
	if rec.Partition != "" && j.holds.HeldPartition(rec.Partition) {
		return true
	}
	if rec.SubjectID != "" && j.index != nil {
		for _, part := range j.index.Partitions(rec.SubjectID) {
			if j.holds.HeldPartition(part) {
				return true
			}
		}
	}
	return false
}

// idempotencyKey é a chave ESTÁVEL de um passo de expiração: ID do registo +
// classe. NÃO inclui a versão da política de retenção — uma vez expirado, um
// registo está expirado independentemente de bumps de versão de TTL. Incluir a
// versão faria com que uma alteração de política (AC4, o caso de uso normal de
// policy-as-code) gerasse uma key nova e RE-SELASSE um segundo evento
// `retention.expired` para um registo já expirado ainda visível numa fonte de
// soft-mark, poluindo a hash-chain WORM. A versão continua selada em CADA evento e
// no changelog (rastreabilidade), apenas não participa na deduplicação. Um registo
// NÃO-expirável nunca marca a sua key (o job salta-o em NotExpired antes de a
// calcular), pelo que uma redução de TTL que o torne expirável continua a
// expirá-lo — a key ID+classe nunca chegou a ser marcada.
func idempotencyKey(id string, class DataClass) string {
	return IdempotencyKeyFor(id, class)
}

// IdempotencyKeyFor é a forma EXPORTADA de [idempotencyKey]. Existe para que um
// composition-root possa RE-HIDRATAR o [IdempotencyStore] a partir dos eventos
// `retention.expired` já selados na hash-chain WORM — que carregam `record_id` e
// `class` ([BuildRetentionExpiredRecord]) — em vez de recalcular à mão um formato
// privado deste pacote. Sem ela, um job com seen-set in-memory volta a re-selar,
// no primeiro varrimento após CADA restart, um segundo `retention.expired` para
// cada facto já selado, poluindo a cadeia gapless que a idempotência protege.
func IdempotencyKeyFor(id string, class DataClass) string {
	return id + "|" + string(class)
}

// ReconciliationSet é o conjunto DURÁVEL-POR-RECONSTRUÇÃO de registos cuja expiração ficou
// SELADA e cuja destruição o sink NÃO confirmou.
//
// A porta existe para que a durabilidade seja de quem compõe o nó, não deste pacote: o
// composition root reconstrói o conjunto a partir da própria cadeia no arranque (a mesma
// disciplina de `restoreExpirationIdempotency` e `restoreShredPending`), e injecta-o aqui.
type ReconciliationSet interface {
	Add(recordID string)
	Remove(recordID string)
	Pending(recordID string) bool
}

// InMemoryReconciliationSet é a implementação de referência, segura para concorrência.
type InMemoryReconciliationSet struct {
	mu  sync.Mutex
	ids map[string]bool
}

// NewInMemoryReconciliationSet constrói um conjunto vazio.
func NewInMemoryReconciliationSet() *InMemoryReconciliationSet {
	return &InMemoryReconciliationSet{ids: make(map[string]bool)}
}

// Add implementa [ReconciliationSet].
func (s *InMemoryReconciliationSet) Add(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = true
}

// Remove implementa [ReconciliationSet].
func (s *InMemoryReconciliationSet) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

// Pending implementa [ReconciliationSet].
func (s *InMemoryReconciliationSet) Pending(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id]
}

// Len devolve quantos registos estão por reconciliar (observabilidade).
func (s *InMemoryReconciliationSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

// WithExpirationReconciliation liga o conjunto de registos POR RECONCILIAR — os que ficaram
// selados como expirados e cuja destruição o sink não confirmou.
//
// SEM ESTA OPÇÃO o comportamento é o anterior ao achado 1.7: uma falha do sink fica selada como
// desmentido na cadeia (isso acontece sempre) mas NÃO é re-tentada, porque não há onde registar
// que ficou por fazer.
func WithExpirationReconciliation(s ReconciliationSet) ExpirationOption {
	return func(j *ExpirationJob) {
		if s != nil {
			j.porReconciliar = s
		}
	}
}

// ReconciliationComposed diz se o conjunto de registos POR RECONCILIAR foi LIGADO a este job.
//
// Existe para o teste de CABLAGEM do composition root: sem ela, a única forma de provar que o
// `WithExpirationReconciliation` está mesmo ligado seria fazer um sink real falhar num nó real —
// e o padrão «a unidade está certa e ninguém a chama» já apareceu doze vezes neste repositório.
func (j *ExpirationJob) ReconciliationComposed() bool { return j != nil && j.porReconciliar != nil }

// desfechoDeRegisto é o que a secção sob barreira decidiu, para o laço contabilizar DEPOIS de a
// barreira ser largada. Existe para que o `defer` possa cobrir a secção inteira sem que os
// `continue` do laço lhe escapem.
//
// `expired` e `err` NÃO são exclusivos: a expiração pode ficar SELADA (e portanto contada) e o
// sink falhar a seguir — é o desenho fail-closed de [ExpirationJob.expireOne], e o relatório tem
// de reflectir as duas coisas.
type desfechoDeRegisto struct {
	held    bool
	skipped bool
	expired bool
	err     error
}

// processarSobBarreira corre o passo POR REGISTO com a BARREIRA DE DESTRUIÇÃO tomada, e larga-a
// por `defer` — em todos os caminhos de saída, incluindo pânico.
//
// A propriedade não é «li o hold atomicamente»: é «entre ter lido o hold e ter destruído, nenhuma
// colocação de hold pôde intercalar-se». Entre as duas coisas há um `fsync` de ~30 ms, e era essa
// a janela em que um `/dsar/hold` respondido 200 não protegia nada.
//
// POR REGISTO, não por passagem: um operador a colocar um hold espera no máximo por um passo de
// destruição, nunca pelo varrimento inteiro.
func (j *ExpirationJob) processarSobBarreira(ctx context.Context, rec ExpirableRecord, now time.Time) desfechoDeRegisto {
	defer j.holds.BeginDestruction()()

	// AC3 — legal hold suspende a expiração (fail-closed).
	if j.held(rec) {
		return desfechoDeRegisto{held: true}
	}
	key := idempotencyKey(rec.ID, rec.Class)
	if j.idem.Seen(key) {
		// RECONCILIAÇÃO (achado 1.7). Um registo já SELADO cujo sink não confirmou a destruição é
		// re-tentado AQUI — sem re-selar o `retention.expired`, que é o segundo evento para o
		// mesmo facto que a marca de idempotência existe para impedir.
		//
		// É a «outra passagem» que o docstring do [ExpirationJob.expireOne] sempre prometeu e que
		// nunca existiu: o `Seen` era permanente (a re-hidratação reconstrói-o dos próprios
		// eventos selados), pelo que ter selado «expirado» tornava a falha eterna.
		if j.porReconciliar != nil && j.porReconciliar.Pending(rec.ID) {
			if err := j.sink.Expire(ctx, rec); err != nil {
				// Continua pendente. NÃO se re-sela o desmentido: a cadeia já o tem, e um segundo
				// por cada passagem enchê-la-ia de ruído de hora a hora.
				return desfechoDeRegisto{skipped: true, err: err}
			}
			j.confirmarReconciliacao(ctx, rec, now)
			// CONTA COMO `Expired`, e é deliberado: é nesta passagem que a destruição aconteceu de
			// facto. Contá-la como `skipped` esconderia no relatório o único momento em que o
			// trabalho foi feito.
			return desfechoDeRegisto{expired: true}
		}
		return desfechoDeRegisto{skipped: true}
	}

	ttl, _ := j.policy.Period(rec.Class)
	sealed, err := j.expireOne(ctx, rec, key, ttl, now)
	// A expiração está AUDITADA e a key MARCADA (dentro de expireOne): conta-se e não se re-sela
	// em reexecuções, MESMO que o sink tenha falhado a seguir — a WORM append-only não pode ganhar
	// um segundo evento para o mesmo facto já selado.
	return desfechoDeRegisto{expired: sealed, err: err}
}
