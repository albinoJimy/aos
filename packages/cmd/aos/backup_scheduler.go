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
// avaliador MEDE, este ESCREVE. Cinco erros deste exportador não são transitórios e re-tentá-los
// 2880 vezes por dia produziria ruído em vez de sinal:
//
//   - [backup.ErrSovereigntyViolation] — o destino deixou de respeitar a fronteira regional
//     (o exportador revalida a soberania a CADA ciclo, fail-closed). Cada re-tentativa é uma
//     tentativa de cópia cross-border negada. O laço PÁRA.
//   - [backup.ErrChainOwned] — o registo do ciclo já está no destino, é AUTÊNTICO e não é nenhum
//     dos que este exportador tentou escrever: outro escritor com a mesma chave, dois donos da mesma
//     cadeia. (Só a NOSSA escrita ambígua — um Put que fez commit e devolveu erro — é adoptada e não
//     chega aqui.) O laço PÁRA e o log manda corrigir a CONFIGURAÇÃO (um destino por exportador) —
//     o oposto do que uma mensagem de adulteração mandaria fazer, a mesma distinção do AOS-284.
//   - [backup.ErrCycleRecordInvalid] — o registo que ocupa a referência do ciclo NÃO verifica com a
//     nossa chave: adulteração, lixo, ou um segundo exportador com OUTRA chave. O laço PÁRA e escala.
//   - [backup.ErrSegmentRefCollision] — o destino tem, na referência endereçada por conteúdo do
//     segmento, um blob DIFERENTE. Continuar selaria no manifesto um content-hash que o destino
//     não guarda, e o sintoma só apareceria no restauro, como adulteração. O laço PÁRA e escala.
//   - [backup.ErrSourceBehindBackup] — o log da fonte está ATRÁS do cursor da cadeia (foi
//     rebobinado debaixo do nó, ou a enumeração deixou de devolver um stream que o backup já
//     cobre). Re-tentar não cura: quando o head voltasse a passar o cursor, o ciclo exportaria uma
//     história diferente por cima da que o backup tem. O laço PÁRA; a correcção é de operação (um
//     destino novo para a cadeia nova). NÃO é verificado no ARRANQUE: um PITR, um WAL truncado ou o
//     DR real deixam o log atrás do cursor, e o nó tem de subir — é o primeiro ciclo que recusa, sem
//     escrever, e o laço que pára.
//
// A LISTA MUDOU DE FORMA. Havia aqui uma paragem por [backup.ErrImmutable] na referência do
// segmento —, que era o que acontecia a CADA arranque sobre um destino que sobrevivesse ao
// processo, porque o exportador começava sempre do génesis. Deixou de existir: [backup.NewExporter]
// RETOMA a cadeia do destino e a referência do segmento passou a ser endereçada por conteúdo
// (`packages/platform/backup/resume.go`). O reinicio_test.go, que media esse limite, mede agora o
// seu fecho.
//
// Em todos os casos a paragem é DEFINITIVA e fica marcada ([NodeService.backupParado]), para que
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
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

	// ErrBackupVaultMountMissing — a custódia DSAR do nó é o Vault Transit (AOS-215) e o backup não
	// tem mount PRÓPRIO (AOS-453). Usar o mount do DSAR poria a KEK do backup (1) atrás do portão
	// da reconciliação de apagamentos (AOS-436), que corre DEPOIS da composição e fecha o
	// embrulho até à primeira passagem — a retoma abortaria a dizer «KEK errada» sobre uma cadeia
	// boa —, e (2) sob a política do nó, que tem `delete` sobre as chaves desse mount. O backup
	// exige o seu mount, numa política sem `delete`.
	ErrBackupVaultMountMissing = errors.New("aos: a custodia da KEK do nó e o Vault Transit (AOS_DSAR_VAULT_ADDR) e o backup nao tem mount PROPRIO — defina AOS_BACKUP_VAULT_TRANSIT_MOUNT (ex.: transit-backup), um mount diferente do do DSAR, numa politica SEM delete (AOS-453). O mount do DSAR poria a KEK do backup atras do portao da reconciliacao de apagamentos (AOS-436) e ao alcance do delete da politica do nó")
)

// Erros da SUPERFÍCIE DE AMBIENTE do backup (AOS-453 F2). Todos abortam o arranque: pedir backup
// com uma config que não se compõe e ficar sem ele em silêncio é o modo de falha que o destino
// explícito existe para excluir.
var (
	// ErrBadBackupDest — AOS_BACKUP_DEST definido com um esquema ou caminho que o nó não compõe.
	ErrBadBackupDest = errors.New("aos: AOS_BACKUP_DEST invalido — esperado file:///caminho/absoluto (um directorio que JA existe, gravavel pelo uid do no); vazio desliga o backup")

	// ErrBackupDestNotImplemented — AOS_BACKUP_DEST=s3://… : o destino fora do host (S3 com Object
	// Lock e escrita condicional) é a fase F4 do desenho e NÃO está implementado. Recusado em vez de
	// aceite e ignorado.
	ErrBackupDestNotImplemented = errors.New("aos: AOS_BACKUP_DEST=s3://… — o destino S3 com Object Lock e a fase F4 do AOS-453 e NAO esta implementado; hoje so file:///")

	// ErrBadBackupDestRegion — AOS_BACKUP_DEST_REGION ausente com destino definido, ou fora das
	// regiões do board (AOS_BOARD_REGIONS). Um destino sem região não prova respeitar a fronteira
	// de soberania (ADR-011).
	ErrBadBackupDestRegion = errors.New("aos: AOS_BACKUP_DEST_REGION invalida — obrigatoria com AOS_BACKUP_DEST, e tem de ser uma das regioes de AOS_BOARD_REGIONS (soberania ADR-011: o backup nunca cruza a fronteira do board)")

	// ErrBadBackupSigningKey — AOS_BACKUP_SIGNING_KEY_PATH ausente, ilegível, ou sem uma seed
	// ed25519 em hex (64 caracteres). O nó LÊ a seed e nunca a cria (não há LoadOrCreate aqui): uma
	// chave nova por arranque tornaria inverificáveis os checkpoints anteriores.
	ErrBadBackupSigningKey = errors.New("aos: AOS_BACKUP_SIGNING_KEY_PATH invalido — obrigatorio com AOS_BACKUP_DEST, ficheiro legivel com a seed ed25519 do backup em hex (64 caracteres), gerada OFFLINE; o no le-a e NUNCA a cria")

	// ErrBadBackupRetention — AOS_BACKUP_RETENTION ausente com destino, ou não é uma duração Go > 0.
	// A retenção é FINITA por decisão do dono (épocas): sem ela o object-lock seria «para sempre».
	ErrBadBackupRetention = errors.New("aos: AOS_BACKUP_RETENTION invalida — obrigatoria com AOS_BACKUP_DEST, duracao Go > 0 (ex.: 2160h = 90 dias); e o object-lock de cada objecto do backup, FINITO por decisao (rotacao por epocas)")

	// ErrBadBackupVault — AOS_BACKUP_VAULT_TRANSIT_MOUNT definido sem a custódia Vault do nó
	// (AOS_DSAR_VAULT_ADDR), igual ao mount do DSAR, ou em falta num nó de produção com destino.
	ErrBadBackupVault = errors.New("aos: AOS_BACKUP_VAULT_TRANSIT_MOUNT invalido — exige a custodia Vault do no (AOS_DSAR_VAULT_ADDR, cujo endereco e token reutiliza), tem de ser um mount DIFERENTE do do DSAR, e e obrigatorio em AOS_MODE=production com AOS_BACKUP_DEST (a KEK do backup tem de sobreviver ao processo)")
)

// backupEnv é o que a superfície de ambiente do backup produz para a [Config].
type backupEnv struct {
	dest       backup.ImmutableStore
	signingKey ed25519.PrivateKey
	retention  time.Duration
	vault      audit.KeyVault
	// ignoradas são variáveis AOS_BACKUP_* definidas SEM AOS_BACKUP_DEST — sem efeito, e o banner
	// di-lo (uma config a meio não é um backup ligado).
	ignoradas []string
}

// backupFromEnv resolve o backup a partir do ambiente (AOS-453 F2). Sem AOS_BACKUP_DEST devolve o
// zero-value: o exportador NÃO é composto e nada muda — o estado por omissão, também em produção.
//
// Com destino, TUDO é obrigatório e fail-closed: região (∈ board), seed de assinatura (lida, nunca
// criada), retenção finita e — em produção — o mount Transit próprio do backup.
func backupFromEnv(production bool, boardRegions map[string]string, dsarVault audit.KeyVault) (backupEnv, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_BACKUP_DEST"))
	if raw == "" {
		var out backupEnv
		// Leituras LITERAIS (o gate da superfície de ambiente exige saber o nome de cada uma).
		for _, kv := range [][2]string{
			{"AOS_BACKUP_DEST_REGION", os.Getenv("AOS_BACKUP_DEST_REGION")},
			{"AOS_BACKUP_SIGNING_KEY_PATH", os.Getenv("AOS_BACKUP_SIGNING_KEY_PATH")},
			{"AOS_BACKUP_RETENTION", os.Getenv("AOS_BACKUP_RETENTION")},
			{"AOS_BACKUP_VAULT_TRANSIT_MOUNT", os.Getenv("AOS_BACKUP_VAULT_TRANSIT_MOUNT")},
		} {
			if strings.TrimSpace(kv[1]) != "" {
				out.ignoradas = append(out.ignoradas, kv[0])
			}
		}
		return out, nil
	}

	// REGIÃO — antes do destino, porque o destino nasce com ela.
	region := strings.ToLower(strings.TrimSpace(os.Getenv("AOS_BACKUP_DEST_REGION")))
	if region == "" {
		return backupEnv{}, fmt.Errorf("%w: AOS_BACKUP_DEST definido sem AOS_BACKUP_DEST_REGION", ErrBadBackupDestRegion)
	}
	if len(boardRegions) > 0 {
		naBoard := false
		for _, r := range boardRegions {
			if strings.EqualFold(strings.TrimSpace(r), region) {
				naBoard = true
				break
			}
		}
		if !naBoard {
			return backupEnv{}, fmt.Errorf("%w: %q nao e uma regiao de AOS_BOARD_REGIONS", ErrBadBackupDestRegion, region)
		}
	} else if production {
		return backupEnv{}, fmt.Errorf("%w: em producao a regiao do destino confronta-se com AOS_BOARD_REGIONS, e este esta vazio", ErrBadBackupDestRegion)
	}

	// DESTINO.
	var dest backup.ImmutableStore
	switch {
	case strings.HasPrefix(raw, "s3://"):
		return backupEnv{}, ErrBackupDestNotImplemented
	case strings.HasPrefix(raw, "file://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
			return backupEnv{}, fmt.Errorf("%w: %q (esperado file:///caminho, sem host, query nem fragmento)", ErrBadBackupDest, raw)
		}
		p := filepath.FromSlash(u.Path)
		// file:///C:/x em Windows chega como "\C:\x" (só dev/testes; o alvo é Linux).
		if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '\\' && p[2] == ':' {
			p = p[1:]
		}
		ds, err := backup.NewFileImmutableStore(p, region)
		if err != nil {
			return backupEnv{}, fmt.Errorf("%w: %w", ErrBadBackupDest, err)
		}
		dest = ds
	default:
		return backupEnv{}, fmt.Errorf("%w: esquema de %q nao suportado", ErrBadBackupDest, raw)
	}

	// CHAVE DE ASSINATURA — lida, nunca criada.
	keyPath := strings.TrimSpace(os.Getenv("AOS_BACKUP_SIGNING_KEY_PATH"))
	if keyPath == "" {
		return backupEnv{}, fmt.Errorf("%w: AOS_BACKUP_DEST definido sem AOS_BACKUP_SIGNING_KEY_PATH", ErrBadBackupSigningKey)
	}
	// Material PRIVADO: recusa-se um ficheiro que outros utilizadores leiam ou que o grupo escreva
	// (0400/0440 servem). Em Windows as permissões POSIX não se medem (só dev/testes).
	if st, serr := os.Stat(keyPath); serr == nil && runtime.GOOS != "windows" && st.Mode().Perm()&0o037 != 0 {
		return backupEnv{}, fmt.Errorf("%w: %q tem permissoes %04o — a seed e material privado (0400, dono uid 65532)", ErrBadBackupSigningKey, keyPath, st.Mode().Perm())
	}
	rawSeed, err := os.ReadFile(keyPath) // #nosec G304 -- caminho do operador (material montado), não input de rede
	if err != nil {
		return backupEnv{}, fmt.Errorf("%w: ler %q: %v", ErrBadBackupSigningKey, keyPath, err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(semBOM(rawSeed))))
	if err != nil || len(seed) != ed25519.SeedSize {
		return backupEnv{}, fmt.Errorf("%w: %q nao contem 64 caracteres hex", ErrBadBackupSigningKey, keyPath)
	}

	// RETENÇÃO FINITA.
	rawRet := strings.TrimSpace(os.Getenv("AOS_BACKUP_RETENTION"))
	ret, perr := time.ParseDuration(rawRet)
	if rawRet == "" || perr != nil || ret <= 0 {
		return backupEnv{}, fmt.Errorf("%w: AOS_BACKUP_RETENTION=%q", ErrBadBackupRetention, rawRet)
	}

	// CUSTÓDIA DA KEK DO BACKUP — mount PRÓPRIO sobre o mesmo Vault e o mesmo token do DSAR.
	out := backupEnv{dest: dest, signingKey: ed25519.NewKeyFromSeed(seed), retention: ret}
	mount := strings.Trim(strings.TrimSpace(os.Getenv("AOS_BACKUP_VAULT_TRANSIT_MOUNT")), "/")
	if mount == "" {
		if production {
			return backupEnv{}, fmt.Errorf("%w: AOS_MODE=production com AOS_BACKUP_DEST exige AOS_BACKUP_VAULT_TRANSIT_MOUNT", ErrBadBackupVault)
		}
		return out, nil // fora de produção: o composer decide (e recusa o mount do DSAR)
	}
	dv, ok := dsarVault.(*vaultKeyVault)
	if !ok {
		return backupEnv{}, fmt.Errorf("%w: AOS_BACKUP_VAULT_TRANSIT_MOUNT sem AOS_DSAR_VAULT_ADDR", ErrBadBackupVault)
	}
	if mount == dv.mount {
		return backupEnv{}, fmt.Errorf("%w: %q e o mount do DSAR", ErrBadBackupVault, mount)
	}
	out.vault = newVaultKeyVault(dv.addr, mount, "", withVaultTokenFrom(dv))
	return out, nil
}

// comporExportadorDeBackup compõe o [backup.Exporter] do nó a partir da config, ou devolve
// (nil, nil) quando não há destino — o estado POR OMISSÃO, em que o nó não exporta nada.
//
// O que aqui se REUTILIZA em vez de reinventar, e porquê:
//
//   - o Event Store composto como fonte, pela porta [eventstore.BackupSource] — é a MESMA
//     instância que serve os runs, pelo que o backup é do log real e não de uma cópia;
//   - a CUSTÓDIA DA KEK DO BACKUP (AOS-453): [Config.BackupVault] quando composto — em produção,
//     uma segunda instância do Vault Transit num mount PRÓPRIO, que sela por ENVELOPE (a DEK é
//     embrulhada no Vault e a KEK nunca entra no processo). Sem ele, o [audit.KeyVault] do nó
//     (dsarVault) — mas SÓ quando não é o Vault: o mount do DSAR está atrás do portão da
//     reconciliação de apagamentos (AOS-436) e ao alcance do `delete` da política do nó, e é
//     recusado ([ErrBackupVaultMountMissing]). Com o vault de referência em memória o exportador
//     compõe (dev/testes), e a KEK morre com o processo: o 2.º arranque recusa a retoma a nomeá-la;
//   - a soberania é a do [backup.NewExporter] (fail-closed, ADR-011): destino noutra região, ou
//     sem região, ABORTA o arranque. Não se re-valida aqui — uma segunda guarda podia divergir
//     da primeira, e a primeira é a que corre também a cada ciclo.
//
// O portão do DSAR (AOS-436) NÃO se aplica à custódia do backup, de propósito: a garantia do Art.
// 17 continua nas KEKs dos TITULARES — o conteúdo deles vai selado por titular DENTRO dos eventos
// do backup, e destruir a KEK de um titular torna-o ilegível também no backup. A KEK do backup só
// protege o segmento em repouso.
func comporExportadorDeBackup(cfg Config, es EventStorePort, dsarVault audit.KeyVault) (*backup.Exporter, error) {
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
	vault, err := custodiaDoBackup(cfg.BackupVault, dsarVault)
	if err != nil {
		return nil, err
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
	if cfg.BackupRetention > 0 {
		opts = append(opts, backup.WithRetention(audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: cfg.BackupRetention}), audit.ClassAudit))
	}
	if cfg.BackupClock != nil {
		opts = append(opts, backup.WithClock(cfg.BackupClock))
	}
	exp, err := backup.NewExporter(src, cfg.BackupDestination, signer, opts...)
	if err != nil {
		// Desde a retoma a construção falha por mais do que a soberania (ADR-011), e a mensagem
		// nomeia-o: um destino que não é write-once condicional (backup.ErrDestinationNotConditional),
		// uma custódia que não sela segmentos ou não responde (backup.ErrKEKCustodyUnsupported /
		// ErrKEKCustodyUnavailable — AOS-453, verificadas ANTES da retoma para uma custódia em baixo
		// não se ler como KEK errada) e uma cadeia no destino que não prova ser deste exportador —
		// outra chave, outra região, um buraco, ou uma KEK que não abre o último segmento
		// (backup.ErrResumeUnverifiable).
		return nil, fmt.Errorf("aos: exportador de backup (AOS-101/453 — soberania ADR-011, destino write-once condicional, custodia da KEK e retoma da cadeia do destino, todas fail-closed; custodia: %s): %w", descreverCustodiaDoBackup(vault), err)
	}
	return exp, nil
}

// custodiaDoBackup escolhe a custódia da KEK do backup: a própria quando composta; senão a do nó,
// EXCEPTO quando esta é o Vault Transit do DSAR ([ErrBackupVaultMountMissing]). Uma custódia
// própria que seja, afinal, a instância do DSAR ou o mesmo mount também é recusada.
func custodiaDoBackup(propria, dsarVault audit.KeyVault) (audit.KeyVault, error) {
	dv, dsarEVault := dsarVault.(*vaultKeyVault)
	if propria == nil {
		if dsarEVault {
			return nil, ErrBackupVaultMountMissing
		}
		return dsarVault, nil
	}
	if pv, ok := propria.(*vaultKeyVault); ok && dsarEVault && (pv == dv || pv.mount == dv.mount) {
		return nil, fmt.Errorf("%w (a custodia do backup injectada e a do DSAR, ou o mesmo mount %q)", ErrBackupVaultMountMissing, pv.mount)
	}
	return propria, nil
}

// descreverCustodiaDoBackup diz, para os banners e erros, QUE custódia sela a KEK do backup
// (AOS-453, critério 5). Nunca inclui o token nem o endereço com credenciais.
func descreverCustodiaDoBackup(v audit.KeyVault) string {
	switch c := v.(type) {
	case nil:
		return "vault de referencia em memoria do proprio modulo (KEK-crua) — MORRE com o processo"
	case *vaultKeyVault:
		return fmt.Sprintf("Vault Transit mount=%q (ENVELOPE: a DEK de cada segmento e embrulhada DENTRO do Vault, a KEK nunca entra no processo; mount PROPRIO do backup, fora do portao AOS-436, token da custodia DSAR)", c.mount)
	case *audit.InMemoryKeyVault:
		return "vault de referencia em memoria do no (KEK-crua) — a KEK MORRE com o processo: o 2.º arranque recusa a retoma a nomear a KEK (so dev/testes)"
	case audit.KeyWrapper:
		return fmt.Sprintf("custodia de envelope %T (audit.KeyWrapper: a DEK e embrulhada dentro da custodia)", c)
	default:
		return fmt.Sprintf("custodia KEK-crua %T (a KEK entra no processo para embrulhar a DEK)", c)
	}
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
		case errors.Is(err, backup.ErrChainOwned):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — o ciclo JA FOI SELADO neste destino por OUTRO exportador. Nao e adulteracao e nao e um destino avariado: sao DOIS ESCRITORES sobre a mesma cadeia, e a referencia indexada do registo de ciclo existe para que o segundo seja recusado em vez de bifurcar o backup em silencio. Nao se re-tenta, porque cada tentativa e a mesma corrida. CORRIJA: um destino por exportador (ou uma so replica a exportar): %v", err)
			return false
		case errors.Is(err, backup.ErrCycleRecordInvalid):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — o registo que ocupa a referencia do ciclo no destino NAO verifica com a chave deste no (assinatura, indice, regiao ou elo). Nao e uma re-tentativa nossa nem outro exportador com esta chave: e adulteracao, lixo, ou um segundo exportador com OUTRA chave. ESCALE: %v", err)
			return false
		case errors.Is(err, backup.ErrSegmentRefCollision):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — a referencia (enderecada por conteudo) do segmento ja existe no destino com CONTEUDO DIFERENTE. Continuar escreveria no manifesto um content-hash que o destino nao guarda, e isso so apareceria no dia do restauro, como adulteracao. ESCALE: o destino esta a servir conteudo que nao foi este no a escrever: %v", err)
			return false
		case errors.Is(err, backup.ErrSourceBehindBackup):
			s.backupParado.Store(true)
			s.log("agendador de backup (AOS-101): PARAGEM DEFINITIVA — o log do Event Store esta ATRAS do cursor da cadeia de backup (foi rebobinado, ou deixou de enumerar um stream que o backup ja cobre). Nao se re-tenta: quando o head voltasse a passar o cursor, o ciclo exportaria uma historia DIFERENTE por cima da que o backup tem. CORRIJA na operacao: um destino novo para a cadeia deste log (a antiga fica intacta e restauravel): %v", err)
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
			return "agendador de backup (AOS-101): DESLIGADO (por omissao) — nenhum destino imutavel composto (AOS_BACKUP_DEST / Config.BackupDestination). O Event Store NAO e exportado para backup imutavel por este no; o que existe no servidor e o backup.sh (copia de VOLUME, cron diario, RPO de 24h), que e outra coisa. Para ligar (AOS-453): AOS_BACKUP_DEST=file:///<directorio>, AOS_BACKUP_DEST_REGION, AOS_BACKUP_SIGNING_KEY_PATH, AOS_BACKUP_RETENTION e, em producao, AOS_BACKUP_VAULT_TRANSIT_MOUNT (custodia da KEK do backup num mount Transit PROPRIO) — todos fail-closed; o exportador RETOMA a cadeia que ja esteja no destino e a cadeia e reconstruivel para restauro (backup.Restorer.LoadManifest)"
		}
		return "agendador de backup (AOS-101): DORMENTE — ha destino composto mas a periodicidade do exportador e <= 0; nenhum ciclo corre sozinho"
	}
	exp := node.BackupExporter
	periodicidade := exp.Periodicity()
	veredicto := "NAO satisfaz o alvo de RPO <= 1 min (AOS-102): a janela de perda e limitada por esta periodicidade"
	if exp.WithinRPO(time.Minute) {
		veredicto = "satisfaz o alvo de RPO <= 1 min (AOS-102): sob um ciclo a cada periodicidade, a janela de perda mantem-se <= 1 min"
	}
	// A CADEIA é uma das duas coisas que o operador tem de poder ler no arranque, a par da
	// periodicidade: um nó que RETOMOU um backup e um que COMEÇOU um são estados diferentes, e
	// confundi-los é ler «o backup está a correr» quando o que está a correr é um backup novo que
	// não cobre nada do que veio antes.
	cadeia := "cadeia NOVA (destino virgem — o primeiro ciclo com novidade sela o ciclo 1)"
	if retomado := exp.ResumedFrom(); retomado > 0 {
		cadeia = fmt.Sprintf("cadeia RETOMADA do ciclo %d que ja estava no destino (conferido fail-closed no arranque SO o ultimo elo: assinatura do checkpoint, indice, regiao, EntryHash recomputado e o segmento desse elo a abrir com a KEK deste no; a cadeia inteira so e verificada no restauro, e o log contra o cursor a cada ciclo)", retomado)
	}
	return fmt.Sprintf("agendador de backup (AOS-101): LIGADO — o Event Store e exportado de %s em %s (AOS_BACKUP_EXPORT_INTERVAL) para o destino imutavel regiao=%q %s; %s; %s. Cada ciclo e INCREMENTAL (so o que passou do head anterior), cifrado em repouso (AES-256-GCM, DEK fresca por segmento; KEK do backup selada por: %s — AOS-453) e encadeado num manifesto hash-chain com checkpoint ed25519. FAIL-OPEN: um ciclo falhado NAO derruba o no; a violacao de soberania, a cadeia com outro dono, o registo de ciclo que nao verifica, a colisao de conteudo e o log atras do cursor PARAM o laco (ver /metrics aos_backup_scheduler_stopped)",
		periodicidade, periodicidade, exp.Immutable().Region(), descreverDestinoDoBackup(exp.Immutable()), veredicto, cadeia, descreverCustodiaDoBackup(exp.Vault()))
}

// descreverDestinoDoBackup nomeia o destino para os banners: o tipo concreto por trás da porta e,
// quando ele se sabe descrever (o destino em disco diz o directório), a descrição. Um destino que
// não se sabe nomear é um backup que não se sabe ir buscar.
func descreverDestinoDoBackup(dst backup.ImmutableStore) string {
	switch d := dst.(type) {
	case *backup.FileImmutableStore:
		return fmt.Sprintf("tipo=%T %s (directorio local write-once: NAO protege da perda do host nem de root — a copia fora do host e a F4)", d, d.String())
	case *backup.InMemoryImmutableStore:
		return fmt.Sprintf("tipo=%T (referencia em MEMORIA: NAO duravel)", d)
	default:
		return fmt.Sprintf("tipo=%T", d)
	}
}
