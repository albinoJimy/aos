package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	backup "github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-101 — a RETOMA do manifesto, medida sobre o NÓ e não só sobre o módulo.
//
// O platform/backup prova que um exportador reiniciado continua a cadeia (reinicio_test.go) e que
// recusa uma cadeia que não prova ser sua (retoma_falha_fechada_test.go). O que só o nó pode
// provar é a COMPOSIÇÃO: que dois arranques do mesmo nó — mesmo Event Store durável, mesmo destino,
// mesma chave, mesma CUSTÓDIA DE KEK — dão uma cadeia só que RESTAURA; que sem a mesma custódia o
// segundo arranque é recusado em vez de anunciar uma retoma que não restaura; que um log atrás do
// cursor deixa o nó SUBIR e pára o laço; e que o banner diz qual dos estados foi ligado (AOS-248).

// aos101NoDuravel compõe um nó com Event Store em FICHEIRO (sobrevive ao Close), backup ligado e,
// se kek != nil, essa custódia de KEK injectada. É uma FIXTURE: um InMemoryKeyVault partilhado entre
// arranques modela uma custódia que sobrevive ao processo e ENTREGA a KEK — o que nenhuma custódia do
// deployment faz hoje (a do nó, AOS_DSAR_VAULT_ADDR, é key-never-leaves e não sela segmentos:
// AOS-453). kek == nil é o vault de referência em memória, que nasce vazio a cada arranque.
func aos101NoDuravel(t *testing.T, walPath string, dst backup.ImmutableStore, chave []byte, kek audit.KeyVault, arranque io.Writer) (*Node, error) {
	t.Helper()
	cfg := aos101Config(t, dst, time.Hour)
	cfg.EventStorePath = walPath
	cfg.BackupSigningKey = chave
	if kek != nil {
		cfg.DSARVault = kek
	}
	return Bootstrap(context.Background(), cfg, arranque)
}

// aos101PrimeiroArranque sela o ciclo 1 com `n` eventos em run-retoma e desliga o nó.
func aos101PrimeiroArranque(t *testing.T, wal string, dst backup.ImmutableStore, chave []byte, kek audit.KeyVault, n int) {
	t.Helper()
	ctx := context.Background()
	no1, err := aos101NoDuravel(t, wal, dst, chave, kek, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (1.º arranque): %v", err)
	}
	svc1, logs1 := aos101Service(t, no1)
	if !strings.Contains(logs1.String(), "cadeia NOVA") {
		t.Errorf("sobre um destino virgem o banner tem de dizer cadeia NOVA; log=%q", logs1.String())
	}
	aos101Seed(t, no1, "run-retoma", n)
	if !svc1.ExportBackupNow(ctx) {
		t.Fatal("ciclo 1 do 1.º arranque parou o laco")
	}
	if got := no1.BackupExporter.Checkpoint().Cycle; got != 1 {
		t.Fatalf("o 1.º arranque devia ter selado o ciclo 1; got %d", got)
	}
	sdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_ = svc1.Shutdown(sdCtx)
	cancel()
	if err := no1.Close(); err != nil {
		t.Fatalf("Close (1.º arranque): %v", err)
	}
}

// TestAOS101_ONoREINICIADORetomaACadeiaEOBannerDizQueRetomou: o primeiro arranque sela o ciclo 1
// num destino que sobrevive ao processo; o segundo — um processo novo sobre o mesmo WAL e a mesma
// custódia de KEK — retoma, ANUNCIA que retomou, sela o ciclo 2, e a cadeia dos dois processos
// RESTAURA como uma só (verificar não bastava: com a KEK perdida verificava e não restaurava).
func TestAOS101_ONoREINICIADORetomaACadeiaEOBannerDizQueRetomou(t *testing.T) {
	ctx := context.Background()
	wal := filepath.Join(t.TempDir(), "events.wal")
	dst := backup.NewInMemoryImmutableStore("eu-west") // o MESMO objecto nos dois arranques = durável
	chave := aos101Key(t)
	kek := audit.NewInMemoryKeyVault(nil) // FIXTURE de uma custódia que sobrevive ao processo (não é o deployment: AOS-453)

	aos101PrimeiroArranque(t, wal, dst, chave, kek, 3)

	// 2.º arranque: processo novo, mesmo WAL, mesmo destino, mesma chave, mesma custódia.
	var arranque2 registoSeguro
	no2, err := aos101NoDuravel(t, wal, dst, chave, kek, &arranque2)
	if err != nil {
		t.Fatalf("Bootstrap (2.º arranque) devia RETOMAR e nao abortar: %v", err)
	}
	t.Cleanup(func() { _ = no2.Close() })
	if !strings.Contains(arranque2.String(), "cadeia RETOMADA do ciclo 1 do destino") {
		t.Errorf("o banner da COMPOSICAO tem de declarar a retoma; log=%q", arranque2.String())
	}
	if got := no2.BackupExporter.ResumedFrom(); got != 1 {
		t.Fatalf("o 2.º arranque devia retomar do ciclo 1; got %d", got)
	}
	svc2, logs2 := aos101Service(t, no2)
	if !strings.Contains(logs2.String(), "cadeia RETOMADA do ciclo 1") {
		t.Errorf("o banner tem de DECLARAR que a cadeia foi retomada, e de que ciclo; log=%q", logs2.String())
	}
	aos101Seed(t, no2, "run-retoma", 2)
	if !svc2.ExportBackupNow(ctx) {
		t.Fatalf("o 2.º arranque parou o laco no primeiro ciclo (a colisao antiga?); log=%q", logs2.String())
	}
	if got := no2.BackupExporter.Checkpoint().Cycle; got != 2 {
		t.Fatalf("o 2.º arranque devia ter selado o ciclo 2; got %d", got)
	}

	// A cadeia atravessa os dois processos e RESTAURA como uma só — reconstruída do DESTINO.
	rst, err := backup.NewRestorer(dst, no2.BackupExporter.Vault(), no2.BackupExporter.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("a cadeia devia ter os 2 ciclos dos DOIS arranques; got %d", len(m.Segments))
	}
	sink, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard("board", "eu-west"))
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer sink.Close()
	ev, err := rst.RestoreTo(ctx, m, cp, 0, nil, sink)
	if err != nil {
		t.Fatalf("a cadeia de dois arranques devia RESTAURAR: %v", err)
	}
	if !ev.Verified || ev.EventsRestored < 5 {
		t.Fatalf("o restauro devia cobrir os eventos dos DOIS arranques (>= 5); got %+v", ev)
	}
}

// TestAOS101_ONoREINICIADOSemACustodiaDaKEKABORTAOArranque: sem custódia que sobreviva (o vault de
// referência nasce vazio a cada arranque) a KEK que selou o ciclo 1 morreu com o 1.º processo. O
// 2.º arranque NÃO pode anunciar «RETOMADA … verificada» sobre uma cadeia que já não restaura: a
// composição aborta e o erro nomeia a KEK.
func TestAOS101_ONoREINICIADOSemACustodiaDaKEKABORTAOArranque(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")
	dst := backup.NewInMemoryImmutableStore("eu-west")
	chave := aos101Key(t)

	aos101PrimeiroArranque(t, wal, dst, chave, nil, 3)

	no2, err := aos101NoDuravel(t, wal, dst, chave, nil, io.Discard)
	if no2 != nil {
		_ = no2.Close()
	}
	if !errors.Is(err, backup.ErrResumeUnverifiable) {
		t.Fatalf("sem a custodia da KEK o 2.º arranque tinha de abortar com ErrResumeUnverifiable; got %v", err)
	}
	if !strings.Contains(err.Error(), "KEK do backup NAO e a que selou") {
		t.Errorf("o erro tem de NOMEAR a KEK; got %v", err)
	}
}

// TestAOS101_UmNoComOutroLogSobreACadeiaSOBEEOLacoPARA: o destino tem a cadeia de um nó; um nó com
// um Event Store DIFERENTE (vazio — outro log, ou o log perdido e reposto de uma cópia anterior) e a
// mesma chave e custódia. O nó SOBE — é o arranque de um DR, e recusá-lo seria um tijolo sem
// superfície de ambiente para o desfazer —, e o laço de backup PÁRA no primeiro ciclo com a causa
// nomeada, sem escrever nada no destino. É também a prova, sem injecção, do mapeamento
// log-atrás-do-cursor → paragem.
func TestAOS101_UmNoComOutroLogSobreACadeiaSOBEEOLacoPARA(t *testing.T) {
	ctx := context.Background()
	dst := backup.NewInMemoryImmutableStore("eu-west")
	chave := aos101Key(t)
	kek := audit.NewInMemoryKeyVault(nil)

	aos101PrimeiroArranque(t, filepath.Join(t.TempDir(), "a.wal"), dst, chave, kek, 3)

	no2, err := aos101NoDuravel(t, filepath.Join(t.TempDir(), "b.wal"), dst, chave, kek, io.Discard)
	if err != nil {
		t.Fatalf("o no tem de SUBIR com o log atras do cursor (a recusa e do ciclo): %v", err)
	}
	t.Cleanup(func() { _ = no2.Close() })
	svc2, logs2 := aos101Service(t, no2)
	antes := dst.Len()
	if svc2.ExportBackupNow(ctx) {
		t.Fatal("o laco tinha de PARAR no primeiro ciclo sobre um log atras do cursor")
	}
	if !svc2.backupParado.Load() {
		t.Fatal("a paragem tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs2.String(), "ATRAS do cursor") {
		t.Errorf("o log tem de nomear a causa; log=%q", logs2.String())
	}
	if dst.Len() != antes {
		t.Fatalf("o ciclo recusado NAO pode escrever no destino; objectos %d -> %d", antes, dst.Len())
	}
}

// TestAOS101_AFonteAtrasDoCursorPARAOLaco: a meio da vida do nó o log fica atrás do cursor
// (rebobinado debaixo dele). Re-tentar não cura — quando o head voltasse a passar o cursor, o
// ciclo exportaria outra história por cima da que o backup tem. O laço PÁRA e manda corrigir na
// operação. (Injecção no destino: prova o switch; o mapeamento real está no teste acima.)
func TestAOS101_AFonteAtrasDoCursorPARAOLaco(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", backup.ErrSourceBehindBackup)
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-rebobinado", 2)
	svc, logs := aos101Service(t, node)

	if svc.ExportBackupNow(context.Background()) {
		t.Fatal("uma fonte atras do cursor e PERMANENTE: o laco tem de parar")
	}
	if !svc.backupParado.Load() {
		t.Fatal("a paragem tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs.String(), "ATRAS do cursor") || !strings.Contains(logs.String(), "um destino novo") {
		t.Errorf("o log tem de nomear a causa e a correccao de operacao; log=%q", logs.String())
	}
}

// TestAOS101_AColisaoDeConteudoNaRefDoSegmentoPARAOLaco: o destino serve, na ref endereçada por
// conteúdo, um blob que não foi este nó a escrever. Continuar selaria um content-hash que o destino
// não guarda. O laço PÁRA e escala.
func TestAOS101_AColisaoDeConteudoNaRefDoSegmentoPARAOLaco(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", backup.ErrSegmentRefCollision)
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-colisao-conteudo", 2)
	svc, logs := aos101Service(t, node)

	if svc.ExportBackupNow(context.Background()) {
		t.Fatal("uma colisao de conteudo e PERMANENTE: o laco tem de parar")
	}
	if !svc.backupParado.Load() {
		t.Fatal("a paragem tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs.String(), "CONTEUDO DIFERENTE") {
		t.Errorf("o log tem de nomear a causa; log=%q", logs.String())
	}
}

// TestAOS101_UmRegistoQueNaoVerificaPARAOLaco: o registo que ocupa a referência do ciclo não
// verifica com a chave do nó — adulteração, lixo, ou outro exportador com OUTRA chave. PÁRA e escala.
func TestAOS101_UmRegistoQueNaoVerificaPARAOLaco(t *testing.T) {
	dst := novaLojaQueFalha("eu-west", backup.ErrCycleRecordInvalid)
	node := aos101Node(t, aos101Config(t, dst, time.Hour))
	aos101Seed(t, node, "run-registo-invalido", 2)
	svc, logs := aos101Service(t, node)

	if svc.ExportBackupNow(context.Background()) {
		t.Fatal("um registo que nao verifica e PERMANENTE: o laco tem de parar")
	}
	if !svc.backupParado.Load() {
		t.Fatal("a paragem tem de ficar MARCADA para o /metrics")
	}
	if !strings.Contains(logs.String(), "NAO verifica com a chave deste no") {
		t.Errorf("o log tem de nomear a causa; log=%q", logs.String())
	}
}
