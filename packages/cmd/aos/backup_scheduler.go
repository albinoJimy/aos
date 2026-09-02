package main

// AGENDADOR DE EXPORTAÇÃO DE BACKUP (AOS-101) — vive no LOOP DE SERVIÇO, no molde EXACTO do
// varredor de retenção (retention_sweeper.go) e do avaliador de SLOs (slo_evaluator.go): um
// ticker que termina com o MESMO `sweepStop` fechado pelo Shutdown.
//
// # O DEFEITO QUE FECHA, MEDIDO ANTES DE ESCRITO
//
// O `platform/backup` implementa exportação incremental cifrada, manifesto hash-chain assinado e
// PITR verificado. Está testado. Duas coisas foram MEDIDAS a 2026-09-01, e são o que este
// ficheiro fecha:
//
//  1. `grep -rn "time.NewTicker\|for {" packages/platform/backup/*.go` (sem testes) ⇒ VAZIO.
//     `Exporter.Export(ctx)` é UM ciclo. Não havia laço nem agendador em lado nenhum.
//  2. `go list -deps ./...` no módulo do nó ⇒ ZERO ocorrências de `aos-ref/platform/backup`. O
//     `go.mod` do nó tinha o `replace` e NÃO tinha o `require`: nenhum binário o importava. Fora
//     do próprio módulo, só `platform/dr` (que restaura, não exporta) e testes lhe tocavam.
//
// A consequência das duas juntas é a que o critério de aceitação AC1 do AOS-101 nomeia: «o log é
// exportado de forma CONTÍNUA» descrevia uma CAPACIDADE, não um comportamento. E o RPO efectivo
// em produção não era o `<= 1 min` que `TestRPO_WithinOneMinute` mede — era o do cron do
// `deploy/server/backup.sh`: 24 HORAS, e de uma coisa diferente (cópia do VOLUME, não exportação
// de segmentos cifrados e encadeados).
//
// # A PERIODICIDADE TEM UMA FONTE SÓ, E É O EXPORTADOR
//
// Este laço NÃO tem cadência própria: usa [backup.Exporter.Periodicity]. A tentação era uma
// `AOS_BACKUP_*_INTERVAL` lida aqui, e teria sido um defeito — o `WithinRPO`/`RPOWindow` do
// exportador são propriedades DA PERIODICIDADE DELE, e duas fontes de verdade fariam o nó
// anunciar um RPO que o laço não cumpria. A env alimenta [Config.BackupPeriodicity], que alimenta
// `WithPeriodicity`, que é o que este ticker lê. Uma só.
//
// # FAIL-OPEN, PELA MESMA RAZÃO DO AVALIADOR DE SLOs — COM DUAS EXCEPÇÕES NOMEADAS
//
// Um ciclo de exportação falhado NÃO derruba o nó nem recusa runs (ponto 2 do fail-open de
// slo_evaluator.go), e um pânico na goroutine é CONTIDO (ponto 1) — sem `recover`, um pânico aqui
// mataria o PROCESSO, que é o modo de falha exacto que o fail-open existe para excluir. A
// assimetria de risco é a mesma: o custo de um ciclo de backup falhado é uma janela de RPO maior;
// o custo de um nó em baixo por causa do exportador é a indisponibilidade em si.
//
// MAS o laço NÃO é fail-open para tudo, e a diferença face ao avaliador de SLOs é deliberada: o
// avaliador MEDE, este ESCREVE. Dois erros deste exportador não são transitórios e re-tentá-los
// 2880 vezes por dia produziria ruído em vez de sinal:
//
//   - [backup.ErrSovereigntyViolation] — o destino deixou de respeitar a fronteira regional
//     (o exportador revalida a soberania a CADA ciclo, fail-closed). Cada re-tentativa é uma
//     tentativa de cópia cross-border negada. O laço PÁRA.
//   - [backup.ErrImmutable] — a referência do segmento já existe no destino. Num destino que
//     sobrevive ao processo é o que acontece a CADA arranque depois do primeiro, e é permanente:
//     [backup.NewExporter] começa sempre do génesis e o índice nunca avança. Está MEDIDO em
//     `packages/platform/backup/reinicio_test.go`. O laço PÁRA e o log NOMEIA a causa — sem isso,
//     o operador leria «o backup avariou» em vez de «este destino não é utilizável».
//
// Em ambos os casos a paragem é DEFINITIVA e fica marcada ([NodeService.backupParado]), para que
// `/metrics` a possa dizer: um nó que deixou de exportar tem de ser distinguível de um nó que
// exporta bem, e a única forma de o distinguir não pode ser alguém estar a ler o log.
//
// # O QUE ESTE FICHEIRO NÃO FAZ (e é deliberado)
//
//   - NÃO reimplementa nada da exportação. Corre o MESMO [backup.Exporter.Export]: a cifra em
//     repouso, o encadeamento no manifesto, o checkpoint assinado e a revalidação de soberania
//     são os dele. Um segundo caminho de exportação seria um segundo sítio onde a fronteira de
//     soberania podia ficar mais fraca.
//   - NÃO agenda o ensaio de restauro (`deploy/server/restore-drill.sh`) — é o AC6, outro eixo.
//   - NÃO traduz instante→seq no PITR — é o AC2, outro eixo.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
	backup "github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
)

// ErrBadBackupExportInterval — AOS_BACKUP_EXPORT_INTERVAL está definida mas não é uma duração Go
// > 0. FAIL-CLOSED de config, no molde de [ErrBadRetentionSweepInterval]: o nó recusa arrancar em
// vez de degradar para o default.
//
// E, como no scheduler de retenção, NENHUM valor aqui DESLIGA o agendador — nem "0". O
// interruptor é o DESTINO ([Config.BackupDestination]): sem destino não há exportação nenhuma, e
// é esse o estado por omissão. Uma cadência a fazer de interruptor daria ao operador duas formas
// de desligar a mesma coisa e nenhuma de saber qual estava em vigor.
var ErrBadBackupExportInterval = errors.New("aos: AOS_BACKUP_EXPORT_INTERVAL invalida (esperada uma duracao Go > 0, ex.: \"30s\") — e a periodicidade com que o Event Store e exportado para o backup imutavel, e a base do RPO; NENHUM valor a desliga (o interruptor e o DESTINO: sem Config.BackupDestination o exportador nem e composto). Deixe a variavel POR DEFINIR para manter o default do modulo")

// backupExportIntervalFromEnv resolve a periodicidade da exportação a partir do ambiente. Vazia ⇒
// 0, que o [Bootstrap] traduz no default do próprio `platform/backup` (30s) — não se duplica aqui
// uma constante que é dele. Malformada ou <= 0 ⇒ [ErrBadBackupExportInterval] (aborta o arranque).
func backupExportIntervalFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_BACKUP_EXPORT_INTERVAL"))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: AOS_BACKUP_EXPORT_INTERVAL=%q", ErrBadBackupExportInterval, raw)
	}
	return d, nil
}

// Erros de COMPOSIÇÃO do exportador. Todos fail-closed: um operador que pediu backup e ficou sem
// ele em silêncio não tem forma de o notar — o sintoma seria a ausência do backup no dia em que
// precisasse dele.
var (
	// ErrBackupSourceUnsupported — há destino de backup configurado mas o Event Store composto
	// não satisfaz [eventstore.BackupSource] (não sabe fazer um snapshot com o envelope intacto).
	// As duas implementações do repo satisfazem-na; uma terceira que não a satisfaça não pode ser
	// exportada, e é melhor sabê-lo no arranque do que no restauro.
	ErrBackupSourceUnsupported = errors.New("aos: Config.BackupDestination esta definido mas o Event Store composto nao satisfaz eventstore.BackupSource (Streams/StreamHead/SnapshotStream/Region) — nao ha snapshot consistente que exportar; retire o destino ou componha um substrato que suporte backup")

	// ErrBackupSigningKeyMissing — há destino mas não há chave para selar os checkpoints. O nó NÃO
	// auto-gera uma: uma chave nova a cada arranque tornaria inverificáveis os checkpoints
	// anteriores, e o operador só descobriria no restauro.
	ErrBackupSigningKeyMissing = errors.New("aos: Config.BackupDestination exige Config.BackupSigningKey (chave ed25519 que sela os checkpoints do manifesto hash-chain do backup) — trust domain PROPRIO (ADR-017 §5): nao se reutiliza a chave do issuer nem a de release, e o no nao gera uma sozinho (uma chave nova por arranque tornaria os checkpoints anteriores inverificaveis)")
)

// comporExportadorDeBackup compõe o [backup.Exporter] do nó a partir da config, ou devolve
// (nil, nil) quando não há destino — o estado POR OMISSÃO, em que o nó não exporta nada.
//
// O que aqui se REUTILIZA em vez de reinventar, e porquê:
//
//   - o Event Store composto como fonte, pela porta [eventstore.BackupSource] — é a MESMA
//     instância que serve os runs, pelo que o backup é do log real e não de uma cópia;
//   - o [audit.KeyVault] do nó (a custódia da KEK de AOS-215) como cofre da KEK do backup. Sem
//     isto, [backup.NewExporter] construiria um vault in-memory PRÓPRIO e a KEK dos segmentos
//     morreria com o processo — segmentos cifrados que ninguém voltaria a decifrar. Ligando a
//     custódia do nó, um deployment com Vault Transit composto tem a KEK do backup na MESMA
//     custódia externa que já audita e roda as outras;
//   - a soberania é a do [backup.NewExporter] (fail-closed, ADR-011): destino noutra região, ou
//     sem região, ABORTA o arranque. Não se re-valida aqui — uma segunda guarda podia divergir
//     da primeira, e a primeira é a que corre também a cada ciclo.
func comporExportadorDeBackup(cfg Config, es EventStorePort, vault audit.KeyVault) (*backup.Exporter, error) {
	if cfg.BackupDestination == nil {
		return nil, nil
	}
	src, ok := any(es).(eventstore.BackupSource)
	if !ok {
		return nil, ErrBackupSourceUnsupported
	}
	if len(cfg.BackupSigningKey) != ed25519.PrivateKeySize {
		return nil, ErrBackupSigningKeyMissing
	}
	signer, err := backup.NewEd25519Signer(cfg.BackupSigningKey)
	if err != nil {
		return nil, fmt.Errorf("aos: assinador de checkpoints do backup (AOS-101): %w", err)
	}
	opts := []backup.ExporterOption{}
	if vault != nil {
		opts = append(opts, backup.WithKeyVault(vault))
	}
	// <= 0 ⇒ NÃO se passa a opção: fica o default do próprio módulo. Duplicar aqui a constante
	// dele seria criar um segundo sítio onde o default do RPO pode divergir.
	if cfg.BackupPeriodicity > 0 {
		opts = append(opts, backup.WithPeriodicity(cfg.BackupPeriodicity))
	}
	if cfg.BackupClock != nil {
		opts = append(opts, backup.WithClock(cfg.BackupClock))
	}
	exp, err := backup.NewExporter(src, cfg.BackupDestination, signer, opts...)
	if err != nil {
		return nil, fmt.Errorf("aos: exportador de backup (AOS-101, soberania fail-closed ADR-011): %w", err)
	}
	return exp, nil
}

// backupSchedulerArmed decide se o laço ARRANCA. É a conjunção mínima e honesta:
//
//  1. nó composto e EXPORTADOR composto — o [Bootstrap] só o compõe com destino explícito
//     ([Config.BackupDestination]), pelo que este predicado É o "alguém pediu backup";
//  2. periodicidade > 0 — a cadência vem do exportador, fonte única (ver o cabeçalho).
//
// Não se exige mais nada de propósito: a soberania já foi validada fail-closed na CONSTRUÇÃO do
// exportador (o arranque abortou se o destino cruzasse a fronteira), e revalidá-la aqui seria uma
// segunda guarda a poder divergir da primeira.
func backupSchedulerArmed(node *Node) bool {
	if node == nil || node.BackupExporter == nil {
		return false
	}
	return node.BackupExporter.Periodicity() > 0
}

// exportarBackups é o laço periódico. Termina quando stop fecha (shutdown do serviço) OU quando
// um ciclo devolve um erro PERMANENTE — ver [NodeService.exportarBackupUmCiclo].
func (s *NodeService) exportarBackups(stop <-chan struct{}) {
	if !backupSchedulerArmed(s.node) {
		return
	}
	// RETOMA ANTES DO PRIMEIRO CICLO. Sem isto, um processo novo sobre um destino que
	// sobreviva ao processo tenta reescrever `<região>/seg-00000001` e o write-once
	// recusa-o — para sempre, porque o índice não avança. Ver platform/backup/retoma.go.
	//
	// Fail-closed, ao contrário do resto deste laço: um ciclo falhado re-tenta-se, mas uma
	// retoma falhada significa que NÃO SE SABE onde o backup ia. Continuar daí ou escreveria
	// por cima de história alheia ou colidiria em cada ciclo — e nos dois casos o laço
	// estaria a produzir ruído em vez de backup. Pára e diz porquê.
	if err := s.node.BackupExporter.RetomarDoDestino(context.Background(), s.node.BackupExporter.Public()); err != nil {
		s.log("agendador de backup (AOS-101): PARADO — nao foi possivel retomar do destino: %v", err)
		return
	}
	t := time.NewTicker(s.node.BackupExporter.Periodicity())
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if !s.exportarBackupUmCiclo(context.Background()) {
				return
			}
		}
	}
}

// exportarBackupUmCiclo conduz UM ciclo de exportação. Devolve false — e SÓ então — quando o erro
// é PERMANENTE e o laço deve parar (ver as duas excepções nomeadas no cabeçalho); todos os outros
// erros são registados e re-tentados no tick seguinte, sem consequência para os runs em curso.
//
// Corre sob `recover`: é o ponto (1) do fail-open. Sem ele, um pânico nesta goroutine derrubaria o
// PROCESSO — o backup a matar o nó, que é o oposto do que um backup existe para fazer.
//
// Exportada-por-teste através de [NodeService.ExportBackupNow].
func (s *NodeService) exportarBackupUmCiclo(ctx context.Context) (continua bool) {
	defer func() {
		if r := recover(); r != nil {
			s.backupPanicos.Add(1)
			s.backupFalhas.Add(1)
			continua = true
			s.log("agendador de backup (AOS-101): PANICO CONTIDO no ciclo — o no NAO cai (fail-open: uma falha de exportacao nunca derruba o no); o ciclo seguinte volta a tentar. CORRIJA: %v", r)
		}
	}()

	res, err := s.node.BackupExporter.Export(ctx)
	if err != nil {
		s.backupFalhas.Add(1)
		switch {
		case errors.Is(err, backup.ErrSovereigntyViolation):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — o destino deixou de respeitar a fronteira regional de soberania (ADR-011) e o exportador RECUSOU fail-closed. Nao se re-tenta: cada tentativa e uma copia cross-border negada. O backup deixa de correr ate o no ser reiniciado com um destino na regiao do board: %v", err)
			return false
		case errors.Is(err, backup.ErrImmutable):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — a referencia do segmento JA EXISTE no destino. Num destino que sobrevive ao processo isto acontece em TODOS os arranques depois do primeiro e e PERMANENTE: o exportador comeca sempre do genesis e o indice nunca avanca (medido em platform/backup/reinicio_test.go). Este destino NAO e utilizavel enquanto o modulo nao souber RETOMAR um manifesto: %v", err)
			return false
		default:
			s.log("agendador de backup (AOS-101): ciclo com erro (fail-open — os runs nao sao afectados); re-tenta no proximo tick: %v", err)
			return true
		}
	}

	s.ciclosDeBackup.Add(1)
	s.ultimoBackupUnix.Store(time.Now().Unix())
	if res.Created {
		s.log("agendador de backup (AOS-101): ciclo %d exportou %d evento(s) para %q (segmento cifrado AES-256-GCM, encadeado no manifesto e selado num checkpoint assinado)", res.Cycle, res.Events, res.Ref)
	}
	// Um ciclo SEM novidade nao e ruido: e a prova de que o backup confirmou estar EM DIA com o
	// head do Store — e e isso que mantem a janela de RPO fechada. Nao se loga a cada tick (seriam
	// 2880 linhas/dia num no calmo); fica no contador e na idade que o /metrics publica.
	return true
}

// ExportBackupNow conduz UM ciclo imediatamente e devolve se o laço continuaria. Existe para os
// testes o conduzirem de forma determinista, sem esperar pelo ticker (molde de
// [NodeService.SweepRetentionNow]/[NodeService.EvaluateSLOsNow]).
func (s *NodeService) ExportBackupNow(ctx context.Context) bool {
	if !backupSchedulerArmed(s.node) {
		return true
	}
	return s.exportarBackupUmCiclo(ctx)
}

// BackupSchedulerArmed reporta se o laço de exportação está ligado nesta réplica.
func (s *NodeService) BackupSchedulerArmed() bool { return backupSchedulerArmed(s.node) }

// backupSchedulerBanner declara a postura do agendador a partir do estado REALMENTE composto —
// nunca da intenção da config (AOS-248: postura anunciada = postura ligada). É emitido por
// [NewNodeService], que é quem sabe se o laço arrancou.
//
// O banner nomeia as DUAS coisas que o critério de aceitação obriga a poder ler: a PERIODICIDADE
// em vigor (que é a base do RPO) e o DESTINO (região + tipo concreto por trás da porta). Um
// destino que não se sabe nomear é um backup que não se sabe ir buscar.
func backupSchedulerBanner(node *Node) string {
	if !backupSchedulerArmed(node) {
		if node == nil || node.BackupExporter == nil {
			return "agendador de backup (AOS-101): DESLIGADO (por omissao) — nenhum destino imutavel composto (Config.BackupDestination). O Event Store NAO e exportado para backup imutavel por este no; o que existe no servidor e o backup.sh (copia de VOLUME, cron diario, RPO de 24h), que e outra coisa. RESSALVA HONESTA: nao ha hoje backend DURAVEL para a porta backup.ImmutableStore — o exportador comeca sempre do genesis e colide (ErrImmutable) no segundo arranque sobre um destino persistente, e o Restorer recebe o manifesto como ARGUMENTO (nada o persiste). Por isso o no nao inventa um destino: exige um injectado, e quem o injecta assume estas duas propriedades"
		}
		return "agendador de backup (AOS-101): DORMENTE — ha destino composto mas a periodicidade do exportador e <= 0; nenhum ciclo corre sozinho"
	}
	exp := node.BackupExporter
	periodicidade := exp.Periodicity()
	veredicto := "NAO satisfaz o alvo de RPO <= 1 min (AOS-102): a janela de perda e limitada por esta periodicidade"
	if exp.WithinRPO(time.Minute) {
		veredicto = "satisfaz o alvo de RPO <= 1 min (AOS-102): sob um ciclo a cada periodicidade, a janela de perda mantem-se <= 1 min"
	}
	return fmt.Sprintf("agendador de backup (AOS-101): LIGADO — o Event Store e exportado de %s em %s (AOS_BACKUP_EXPORT_INTERVAL) para o destino imutavel regiao=%q tipo=%T; %s. Cada ciclo e INCREMENTAL (so o que passou do head anterior), cifrado em repouso (AES-256-GCM, KEK do audit.KeyVault do no) e encadeado num manifesto hash-chain com checkpoint ed25519. FAIL-OPEN: um ciclo falhado NAO derruba o no; a violacao de soberania e a colisao de referencia PARAM o laco (ver /metrics aos_backup_scheduler_stopped)",
		periodicidade, periodicidade, exp.Immutable().Region(), exp.Immutable(), veredicto)
}
