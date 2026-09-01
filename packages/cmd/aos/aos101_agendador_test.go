package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	backup "github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-101 — O AGENDADOR DE EXPORTAÇÃO DE BACKUP, e o que estes testes tornam falsificável.
//
// O DEFEITO MEDIDO que isto fecha (2026-09-01): `Exporter.Export` é UM ciclo (zero tickers em
// `platform/backup`), e `go list -deps` no módulo do nó dava ZERO ocorrências de
// `aos-ref/platform/backup` — nenhum binário o importava. O critério AC1 («o log é exportado de
// forma CONTÍNUA») descrevia uma capacidade; o RPO efectivo era o do cron do `backup.sh`: 24h.
//
// O que cada teste prova, e o que cai se a costura for desfeita:
//
//  1. POR OMISSÃO NADA MUDA. Sem destino, o exportador não é composto e o laço não arranca.
//  2. O LAÇO EXISTE E CORRE SOZINHO — provado por sincronização num `Put` REAL do destino, não
//     por espera cega. Remover o `go s.exportarBackups(...)` faz este teste cair.
//  3. O QUE O LAÇO ESCREVE É O BACKUP REAL: o manifesto que ele produziu VERIFICA contra o
//     checkpoint assinado, pelo `backup.Restorer` do módulo. Um agendador que "corresse" sem
//     exportar nada passaria (2) e cai aqui.
//  4. A JANELA DE RPO fecha, medida com RELÓGIO INJECTADO (nunca `time.Sleep`).
//  5. FAIL-OPEN: um ciclo falhado e um PÂNICO no ciclo NÃO derrubam o nó.
//  6. As duas paragens PERMANENTES (soberania, colisão de referência) param o laço e ficam
//     legíveis no `/metrics` — em vez de 2880 re-tentativas por dia.
//  7. A composição é FAIL-CLOSED: destino sem região e destino sem chave ABORTAM o arranque.
//  8. A PERIODICIDADE e o DESTINO são observáveis, e a cadência do laço é a DO EXPORTADOR (fonte
//     única — duas fontes fariam o nó anunciar um RPO que não cumpre).

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// aos101Key gera a chave ed25519 que sela os checkpoints do backup (trust domain próprio).
func aos101Key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return priv
}

// aos101Config devolve a config base do nó COM backup ligado para dst.
func aos101Config(t *testing.T, dst backup.ImmutableStore, periodicity time.Duration) Config {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.BackupDestination = dst
	cfg.BackupSigningKey = aos101Key(t)
	cfg.BackupPeriodicity = periodicity
	return cfg
}

// aos101Node compõe o nó a partir de cfg.
func aos101Node(t *testing.T, cfg Config) *Node {
	t.Helper()
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

// aos101Service compõe o loop de serviço sobre o nó. Devolve também o log do serviço, porque a
// POSTURA DECLARADA (o banner) é parte do que o critério exige poder ler.
func aos101Service(t *testing.T, node *Node) (*NodeService, *strings.Builder) {
	t.Helper()
	var logs strings.Builder
	svc, err := NewNodeService(node,
		WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute),
		WithSLOEvalInterval(0), WithServiceLog(&logs))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	return svc, &logs
}

// aos101Seed acrescenta n eventos ao Event Store do nó — o MESMO substrato que serve os runs, para
// que o que se exporta seja o log real e não uma cópia montada para o teste.
func aos101Seed(t *testing.T, node *Node, stream string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		in := eventstore.EventInput{
			Type:    "aos101.test.fact",
			Payload: []byte(fmt.Sprintf(`{"i":%d}`, i)),
			RunID:   stream,
			StepID:  fmt.Sprintf("s-%d-%d", time.Now().UnixNano(), i),
		}
		if _, err := node.EventStore.Append(ctx, stream, in); err != nil {
			t.Fatalf("Append %s#%d: %v", stream, i, err)
		}
	}
}

// lojaQueFalha é um [backup.ImmutableStore] que devolve um erro escolhido no Put e sinaliza cada
// tentativa. Serve as pernas de fail-open e de paragem permanente sem depender de I/O real.
type lojaQueFalha struct {
	regiao  string
	erro    error
	entrar  chan struct{} // sinalizado a CADA Put (buffered)
	panicar bool

	mu     sync.Mutex
	blobs  map[string][]byte
	chamou int
}

func novaLojaQueFalha(regiao string, erro error) *lojaQueFalha {
	return &lojaQueFalha{regiao: regiao, erro: erro, entrar: make(chan struct{}, 64), blobs: map[string][]byte{}}
}

func (l *lojaQueFalha) Region() string { return l.regiao }

func (l *lojaQueFalha) Put(ref string, blob []byte, retainUntil time.Time) error {
	l.mu.Lock()
	l.chamou++
	l.mu.Unlock()
	select {
	case l.entrar <- struct{}{}:
	default:
	}
	if l.panicar {
		panic("aos101: panico deliberado no destino de backup")
	}
	if l.erro != nil {
		return l.erro
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cp := make([]byte, len(blob))
	copy(cp, blob)
	l.blobs[ref] = cp
	return nil
}

func (l *lojaQueFalha) Get(ref string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.blobs[ref]
	if !ok {
		return nil, backup.ErrNotFound
	}
	return b, nil
}

func (l *lojaQueFalha) Delete(ref string, now time.Time) error { return backup.ErrObjectLocked }

func (l *lojaQueFalha) tentativas() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.chamou
}

// esperaPut bloqueia até um Put ter acontecido, com prazo. NÃO é um sleep de sincronização: é uma
// espera por um FACTO produzido pelo laço (a escrita no destino). Se o laço não existir, expira.
func esperaPut(t *testing.T, l *lojaQueFalha) {
	t.Helper()
	select {
	case <-l.entrar:
	case <-time.After(10 * time.Second):
		t.Fatal("o agendador NAO correu nenhum ciclo em 10s — o laco de exportacao nao esta ligado ao loop de servico")
	}
}

func aos101Metrics(t *testing.T, svc *NodeService, node *Node) string {
	t.Helper()
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics devia dar 200, veio %d", rec.Code)
	}
	return rec.Body.String()
}

// ---------------------------------------------------------------------------
// (1) POR OMISSÃO — o nó NÃO exporta backups
// ---------------------------------------------------------------------------

// TestAOS101_PorOmissaoONoNAOExportaBackups é a guarda do "desligado por omissão". Um nó que
// passasse a exportar o seu log para algum lado só por ter sido actualizado seria uma mudança de
// comportamento não pedida — e de dados, não de configuração.
func TestAOS101_PorOmissaoONoNAOExportaBackups(t *testing.T) {
	node := aos101Node(t, tnBaseConfig())
	if node.BackupExporter != nil {
		t.Fatal("sem Config.BackupDestination o exportador NAO pode ser composto")
	}
	svc, logs := aos101Service(t, node)
	if svc.BackupSchedulerArmed() {
		t.Fatal("sem exportador o agendador NAO pode estar armado")
	}
	if !strings.Contains(logs.String(), "agendador de backup (AOS-101): DESLIGADO") {
		t.Errorf("o banner tem de DECLARAR a postura desligada; log=%q", logs.String())
	}
	body := aos101Metrics(t, svc, node)
	if !strings.Contains(body, "aos_backup_scheduler_armed 0") {
		t.Error("com o agendador desligado o /metrics tem de o DECLARAR (aos_backup_scheduler_armed 0)")
	}
	// E NÃO pode afirmar uma periodicidade que ninguém corre: um `0` aqui leria-se como cadência
	// configurada, que é a mentira simétrica.
	if strings.Contains(body, "aos_backup_export_periodicity_seconds") {
		t.Error("sem agendador o /metrics NAO pode publicar periodicidade de exportacao")
	}
}

// ---------------------------------------------------------------------------
// (2)+(3) O LAÇO CORRE SOZINHO, e o que escreve é o backup REAL
// ---------------------------------------------------------------------------

// TestAOS101_OLacoCorreSozinhoEExportaOLogREAL é o teste que a ausência do laço faz cair. Não
// conduz nenhum ciclo à mão: compõe o nó com destino, semeia eventos no Event Store REAL e espera
// pelo FACTO de o destino ter recebido uma escrita. Se `NewNodeService` não arrancar a goroutine,
// nada acontece e o teste expira.
func TestAOS101_OLacoCorreSozinhoEExportaOLogREAL(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", nil)
	// Cadência mínima: o teste não espera pela cadência, espera pelo facto.
	node := aos101Node(t, aos101Config(t, dst, time.Millisecond))
	aos101Seed(t, node, "run-aos101", 3)

	svc, logs := aos101Service(t, node)
	if !svc.BackupSchedulerArmed() {
		t.Fatal("com destino composto o agendador tinha de estar armado")
	}
	if !strings.Contains(logs.String(), "agendador de backup (AOS-101): LIGADO") {
		t.Errorf("o banner tem de DECLARAR a postura ligada; log=%q", logs.String())
	}

	esperaPut(t, dst)

	// O QUE FOI ESCRITO É O BACKUP REAL: o manifesto que o LAÇO produziu tem de VERIFICAR contra o
	// checkpoint assinado, pelo verificador do próprio módulo. Um agendador que corresse sem
	// exportar nada — ou que escrevesse lixo — passaria a espera acima e cai aqui.
	exp := node.BackupExporter
	m := exp.Manifest()
	cp := exp.Checkpoint()
	if len(m.Segments) == 0 {
		t.Fatal("o laco correu mas o manifesto esta VAZIO — nada foi exportado")
	}
	if m.Segments[0].Events == 0 {
		t.Fatal("o segmento exportado nao tem eventos — o incremento nao foi lido do Event Store")
	}
	restorer, err := backup.NewRestorer(exp.Immutable(), exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	if err := restorer.VerifyManifest(m, cp, 0); err != nil {
		t.Fatalf("o manifesto produzido pelo AGENDADOR tinha de verificar (hash-chain + checkpoint ed25519): %v", err)
	}
}

// ---------------------------------------------------------------------------
// (4) A JANELA DE RPO — com RELÓGIO INJECTADO
// ---------------------------------------------------------------------------

// TestAOS101_AJanelaDeRPOFechaSobOAgendador mede a janela EFECTIVA de RPO do nó COMPOSTO ao longo
// de vários ciclos, com o relógio do exportador INJECTADO por [Config.BackupClock] — nenhum
// `time.Sleep` participa na medição. É o AC4 medido sobre o nó, e não já só sobre o módulo: sem o
// agendador, este número não existia em runtime, porque nada corria os ciclos.
func TestAOS101_AJanelaDeRPOFechaSobOAgendador(t *testing.T) {
	const periodicidade = 30 * time.Second

	agora := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var relogioMu sync.Mutex
	relogio := func() time.Time {
		relogioMu.Lock()
		defer relogioMu.Unlock()
		return agora
	}

	dst := backup.NewInMemoryImmutableStore("eu-west")
	cfg := aos101Config(t, dst, periodicidade)
	cfg.BackupClock = relogio
	// O laço NÃO conduz esta medição: a cadência real seria 30s de relógio de parede. Os ciclos
	// são conduzidos à mão por [NodeService.ExportBackupNow] — o MESMO caminho que o ticker corre.
	node := aos101Node(t, cfg)
	svc, _ := aos101Service(t, node)

	if !node.BackupExporter.WithinRPO(time.Minute) {
		t.Fatalf("periodicidade %v devia satisfazer RPO<=1min", node.BackupExporter.Periodicity())
	}

	var maxJanela time.Duration
	for k := 0; k < 6; k++ {
		aos101Seed(t, node, "run-rpo", 2)
		if !svc.ExportBackupNow(context.Background()) {
			t.Fatalf("ciclo %d: o agendador PAROU — nao devia", k)
		}
		// Instante imediatamente antes do próximo ciclo: o pior caso de frescura.
		mesmoAntes := agora.Add(periodicidade - time.Nanosecond)
		if w := node.BackupExporter.RPOWindow(mesmoAntes); w > maxJanela {
			maxJanela = w
		}
		relogioMu.Lock()
		agora = agora.Add(periodicidade)
		relogioMu.Unlock()
	}
	if maxJanela > time.Minute {
		t.Fatalf("janela efectiva de RPO %v excede 1 min sob o agendador", maxJanela)
	}
	if maxJanela >= periodicidade+time.Second {
		t.Fatalf("janela de RPO %v nao devia exceder a periodicidade %v", maxJanela, periodicidade)
	}
	if svc.ciclosDeBackup.Load() != 6 {
		t.Fatalf("6 ciclos conduzidos, %d contados — o contador que alimenta o /metrics nao esta ligado ao ciclo", svc.ciclosDeBackup.Load())
	}
}

// ---------------------------------------------------------------------------
// (5) FAIL-OPEN — nem um erro nem um pânico derrubam o nó
// ---------------------------------------------------------------------------

// TestAOS101_UmCicloFalhadoNAODerrubaONo: um destino que recusa a escrita por uma razão TRANSITÓRIA
// é registado, contado, e o laço CONTINUA — os runs não são afectados. A alternativa (propagar) era
// deixar o backup derrubar o que ele existe para proteger.
func TestAOS101_UmCicloFalhadoNAODerrubaONo(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", errors.New("disco cheio"))
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-falha", 2)
	svc, _ := aos101Service(t, node)

	if !svc.ExportBackupNow(context.Background()) {
		t.Fatal("um erro TRANSITORIO nao pode parar o laco (fail-open) — so a soberania e a colisao param")
	}
	if got := svc.backupFalhas.Load(); got != 1 {
		t.Fatalf("a falha tinha de ser CONTADA para o /metrics; falhas=%d", got)
	}
	if svc.backupParado.Load() {
		t.Fatal("um erro transitorio NAO pode marcar paragem definitiva")
	}
	// O nó continua a servir: a prontidão do plano de controlo não é afectada pelo backup.
	if !svc.controlPlaneAvailable(context.Background()) {
		t.Fatal("um ciclo de backup falhado NAO pode tornar o plano de controlo indisponivel")
	}
	// E re-tenta: o ciclo seguinte volta a bater no destino.
	if !svc.ExportBackupNow(context.Background()) {
		t.Fatal("o ciclo seguinte tinha de voltar a tentar")
	}
	if dst.tentativas() != 2 {
		t.Fatalf("esperadas 2 tentativas de escrita, %d", dst.tentativas())
	}
}

// TestAOS101_UmPanicoNoCicloEContido: sem o `recover`, um pânico nesta goroutine mataria o
// PROCESSO — o backup a matar o nó. É o ponto (1) do fail-open, tornado executável.
func TestAOS101_UmPanicoNoCicloEContido(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", nil)
	dst.panicar = true
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-panico", 2)
	svc, logs := aos101Service(t, node)

	if !svc.ExportBackupNow(context.Background()) {
		t.Fatal("um panico CONTIDO nao pode parar o laco")
	}
	if got := svc.backupPanicos.Load(); got != 1 {
		t.Fatalf("o panico tinha de ser CONTADO; panicos=%d", got)
	}
	if !strings.Contains(logs.String(), "PANICO CONTIDO") {
		t.Errorf("o panico contido tem de ser GRITADO no log (e um defeito a corrigir); log=%q", logs.String())
	}
}

// ---------------------------------------------------------------------------
// (6) AS DUAS PARAGENS PERMANENTES
// ---------------------------------------------------------------------------

// TestAOS101_ASoberaniaPARAOLacoDefinitivamente: o exportador revalida a fronteira regional a CADA
// ciclo (ADR-011, fail-closed). Se ela passar a ser violada, re-tentar é insistir numa cópia
// cross-border negada — o laço pára e diz porquê.
func TestAOS101_ASoberaniaPARAOLacoDefinitivamente(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", nil)
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-sob", 2)
	svc, logs := aos101Service(t, node)

	// A fronteira cai DEPOIS da construção (que já a validou): o destino perde a região.
	dst.regiao = ""

	if svc.ExportBackupNow(context.Background()) {
		t.Fatal("uma violacao de soberania tem de PARAR o laco (nao se re-tenta uma copia cross-border)")
	}
	if !svc.backupParado.Load() {
		t.Fatal("a paragem por soberania tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs.String(), "PARAGEM DEFINITIVA") || !strings.Contains(logs.String(), "soberania") {
		t.Errorf("a causa da paragem tem de estar NOMEADA no log; log=%q", logs.String())
	}
	if dst.tentativas() != 0 {
		t.Fatalf("a soberania e validada ANTES de escrever: esperadas 0 escritas, %d", dst.tentativas())
	}
	body := aos101Metrics(t, svc, node)
	if !strings.Contains(body, "aos_backup_scheduler_stopped 1") {
		t.Error("um agendador PARADO tem de ser distinguivel de um que exporta bem no /metrics")
	}
}

// TestAOS101_AColisaoDeReferenciaPARAOLaco: é o que acontece a TODOS os arranques depois do
// primeiro sobre um destino que sobrevive ao processo (medido em
// platform/backup/reinicio_test.go). Re-tentar seria 2880 erros por dia sobre uma condição que
// nunca melhora.
func TestAOS101_AColisaoDeReferenciaPARAOLaco(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", backup.ErrImmutable)
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-colisao", 2)
	svc, logs := aos101Service(t, node)

	if svc.ExportBackupNow(context.Background()) {
		t.Fatal("uma colisao de referencia e PERMANENTE: o laco tem de parar")
	}
	if !svc.backupParado.Load() {
		t.Fatal("a paragem por colisao tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs.String(), "JA EXISTE no destino") {
		t.Errorf("o log tem de NOMEAR a causa (o operador leria 'o backup avariou' em vez de 'este destino nao e utilizavel'); log=%q", logs.String())
	}
}

// ---------------------------------------------------------------------------
// (7) A COMPOSIÇÃO É FAIL-CLOSED
// ---------------------------------------------------------------------------

// TestAOS101_UmDestinoSemRegiaoABORTAOArranque: não provar que se respeita a fronteira é diferente
// de a respeitar (ADR-011). O arranque ABORTA — pedir backup e ficar sem ele em silêncio é o modo
// de falha que a guarda exclui.
func TestAOS101_UmDestinoSemRegiaoABORTAOArranque(t *testing.T) {
	cfg := aos101Config(t, backup.NewInMemoryImmutableStore(""), time.Minute)
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if node != nil {
		_ = node.Close()
	}
	if !errors.Is(err, backup.ErrSovereigntyViolation) {
		t.Fatalf("um destino SEM regiao tinha de abortar com ErrSovereigntyViolation; got %v", err)
	}
}

// TestAOS101_UmDestinoSemChaveDeAssinaturaABORTA: o nó não auto-gera a chave que sela os
// checkpoints. Uma chave nova a cada arranque tornaria os checkpoints anteriores inverificáveis, e
// o operador só o descobriria no dia do restauro.
func TestAOS101_UmDestinoSemChaveDeAssinaturaABORTA(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.BackupDestination = backup.NewInMemoryImmutableStore("eu-west")
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if node != nil {
		_ = node.Close()
	}
	if !errors.Is(err, ErrBackupSigningKeyMissing) {
		t.Fatalf("destino sem Config.BackupSigningKey tinha de abortar com ErrBackupSigningKeyMissing; got %v", err)
	}
}

// TestAOS101_CadenciaMalformadaABORTA: a fronteira fail-closed da CONFIG. A exportação é fail-open,
// a configuração dela não — um operador que pede 30s e fica com outra coisa não tem como o notar.
func TestAOS101_CadenciaMalformadaABORTA(t *testing.T) {
	for _, bad := range []string{"lixo", "0", "-1s", "30"} {
		t.Setenv("AOS_BACKUP_EXPORT_INTERVAL", bad)
		if _, err := backupExportIntervalFromEnv(); !errors.Is(err, ErrBadBackupExportInterval) {
			t.Errorf("AOS_BACKUP_EXPORT_INTERVAL=%q tinha de abortar; got %v", bad, err)
		}
	}
	t.Setenv("AOS_BACKUP_EXPORT_INTERVAL", "45s")
	d, err := backupExportIntervalFromEnv()
	if err != nil || d != 45*time.Second {
		t.Fatalf("cadencia valida: got (%v, %v)", d, err)
	}
	// Vazia ⇒ 0, que o Bootstrap traduz no default do MÓDULO — não numa constante duplicada aqui.
	t.Setenv("AOS_BACKUP_EXPORT_INTERVAL", "")
	if d, err := backupExportIntervalFromEnv(); err != nil || d != 0 {
		t.Fatalf("cadencia vazia devia devolver 0 (default do modulo); got (%v, %v)", d, err)
	}
}

// ---------------------------------------------------------------------------
// (8) OBSERVABILIDADE e FONTE ÚNICA DA CADÊNCIA
// ---------------------------------------------------------------------------

// TestAOS101_APeriodicidadeEODestinoSaoObservaveis: as duas coisas que o critério obriga a poder
// ler. A periodicidade é a base do RPO; a região é a fronteira que o backup nunca cruza. Uma sem a
// outra não responde à pergunta do operador.
func TestAOS101_APeriodicidadeEODestinoSaoObservaveis(t *testing.T) {
	dst := backup.NewInMemoryImmutableStore("eu-west")
	node := aos101Node(t, aos101Config(t, dst, 45*time.Second))
	aos101Seed(t, node, "run-obs", 2)
	svc, logs := aos101Service(t, node)
	svc.ExportBackupNow(context.Background())

	body := aos101Metrics(t, svc, node)
	for _, want := range []string{
		"aos_backup_scheduler_armed 1",
		`aos_backup_export_periodicity_seconds{region="eu-west"} 45`,
		"aos_backup_export_cycles_total 1",
		"aos_backup_scheduler_stopped 0",
		"aos_backup_rpo_window_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics devia conter %q", want)
		}
	}
	// O banner declara o mesmo par, para quem lê o arranque em vez do scrape.
	for _, want := range []string{"45s", `regiao="eu-west"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("o banner devia declarar %q; log=%q", want, logs.String())
		}
	}
}

// TestAOS101_ACadenciaDoLacoEADoExportador é a guarda da FONTE ÚNICA. Se alguém introduzir uma
// segunda variável de cadência para o laço, o RPO anunciado (`WithinRPO`, uma propriedade DA
// PERIODICIDADE DO EXPORTADOR) deixa de ser o RPO cumprido, e o nó passa a anunciar uma garantia
// que não tem.
func TestAOS101_ACadenciaDoLacoEADoExportador(t *testing.T) {
	dst := backup.NewInMemoryImmutableStore("eu-west")
	node := aos101Node(t, aos101Config(t, dst, 90*time.Second))
	if got := node.BackupExporter.Periodicity(); got != 90*time.Second {
		t.Fatalf("a periodicidade da config tinha de chegar ao exportador; got %v", got)
	}
	svc, logs := aos101Service(t, node)
	if !svc.BackupSchedulerArmed() {
		t.Fatal("agendador devia estar armado")
	}
	// 90s NÃO satisfaz o alvo de RPO — e o banner tem de o DIZER em vez de o esconder.
	if node.BackupExporter.WithinRPO(time.Minute) {
		t.Fatal("90s nao pode satisfazer RPO<=1min")
	}
	if !strings.Contains(logs.String(), "NAO satisfaz o alvo de RPO") {
		t.Errorf("o banner tem de declarar que a cadencia configurada NAO cumpre o alvo de RPO; log=%q", logs.String())
	}
}

// TestAOS101_OShutdownParaOAgendador: o laço termina pelo MESMO `sweepStop` dos outros cinco — sem
// fuga de goroutine. Com o canal já fechado, o laço tem de RETORNAR, não ficar no ticker.
func TestAOS101_OShutdownParaOAgendador(t *testing.T) {
	dst := backup.NewInMemoryImmutableStore("eu-west")
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	svc, _ := aos101Service(t, node)

	parado := make(chan struct{})
	close(parado)
	fim := make(chan struct{})
	go func() {
		svc.exportarBackups(parado)
		close(fim)
	}()
	select {
	case <-fim:
	case <-time.After(5 * time.Second):
		t.Fatal("o laco NAO retornou com o sweepStop fechado — fuga de goroutine no Shutdown")
	}
}
