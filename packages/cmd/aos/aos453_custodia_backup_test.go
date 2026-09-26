package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	backup "github.com/aos-ref/platform/backup"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-453 — a custódia da KEK do backup no NÓ: uma segunda instância do Vault Transit num mount
// PRÓPRIO, que lê o token da custódia DSAR, sela por envelope, e é independente do portão da
// reconciliação de apagamentos (AOS-436) e da política de delete do DSAR.

// vaultPorMount é um Vault falso com um motor Transit POR MOUNT (o fakeTransit de
// vaultkeyvault_test.go, um por mount), e que regista o token de cada pedido.
type vaultPorMount struct {
	mu      sync.Mutex
	mounts  map[string]*fakeTransit
	tokens  map[string]string // mount -> último X-Vault-Token visto
	pedidos map[string]int    // mount -> nº de pedidos
}

func novoVaultPorMount(mounts ...string) *vaultPorMount {
	v := &vaultPorMount{mounts: map[string]*fakeTransit{}, tokens: map[string]string{}, pedidos: map[string]int{}}
	for _, m := range mounts {
		v.mounts[m] = &fakeTransit{live: map[string]bool{}}
	}
	return v
}

func (v *vaultPorMount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mount := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/"), "/", 2)[0]
	v.mu.Lock()
	ft, ok := v.mounts[mount]
	v.tokens[mount] = r.Header.Get("X-Vault-Token")
	v.pedidos[mount]++
	v.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	ft.ServeHTTP(w, r)
}

func (v *vaultPorMount) chaves(mount string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.mounts[mount].live)
}

func (v *vaultPorMount) destruirTudo(mount string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.mounts[mount].live = map[string]bool{}
}

// aos453Fonte devolve um Event Store do nó (o mesmo substrato dos runs) com n eventos.
func aos453Fonte(t *testing.T, n int) *Node {
	t.Helper()
	node := aos101Node(t, tnBaseConfig()) // sem backup: só para ter o EventStorePort real
	aos101Seed(t, node, "run-453", n)
	return node
}

// TestAOS453_ACustodiaDoBackupNoMountProprioSelaRetomaERestauraComOPortaoDSARFechado é o critério
// 2 sobre o nó: com a custódia DSAR ARMADA (portão AOS-436 fechado — a reconciliação ainda não
// correu), o exportador compõe na mesma sobre o mount do backup, com o token da DSAR, as chaves
// nascem SÓ no mount do backup, um 2.º «arranque» retoma e a cadeia dos dois restaura.
func TestAOS453_ACustodiaDoBackupNoMountProprioSelaRetomaERestauraComOPortaoDSARFechado(t *testing.T) {
	ctx := context.Background()
	fv := novoVaultPorMount("transit", "transit-backup")
	srv := httptest.NewServer(fv)
	defer srv.Close()

	dsarKV := newVaultKeyVault(srv.URL, "transit", "tok-do-no")
	dsarKV.exigirReconciliacao() // o portão do DSAR FECHADO, como no arranque real
	kvBackup := newVaultKeyVault(srv.URL, "transit-backup", "", withVaultTokenFrom(dsarKV))

	dst := backup.NewInMemoryImmutableStore("eu-west") // o mesmo objecto nos dois = durável
	chave := aos101Key(t)
	node := aos453Fonte(t, 3)

	cfg := tnBaseConfig()
	cfg.BackupDestination = dst
	cfg.BackupSigningKey = chave
	cfg.BackupVault = kvBackup
	cfg.BackupRetention = 90 * 24 * time.Hour

	exp1, err := comporExportadorDeBackup(cfg, node.EventStore, dsarKV)
	if err != nil {
		t.Fatalf("com o portao DSAR fechado o exportador TEM de compor no mount do backup: %v", err)
	}
	if r, err := exp1.Export(ctx); err != nil || r.Cycle != 1 {
		t.Fatalf("ciclo 1: %+v %v", r, err)
	}
	if fv.chaves("transit") != 0 {
		t.Fatalf("a KEK do backup NAO pode nascer no mount do DSAR; chaves la=%d", fv.chaves("transit"))
	}
	if fv.chaves("transit-backup") != 1 {
		t.Fatalf("a KEK do backup tem de estar no mount do backup; chaves=%d", fv.chaves("transit-backup"))
	}
	fv.mu.Lock()
	tok := fv.tokens["transit-backup"]
	fv.mu.Unlock()
	if tok != "tok-do-no" {
		t.Fatalf("a custodia do backup tem de usar o token da custodia DSAR; viu %q", tok)
	}
	if !strings.Contains(descreverCustodiaDoBackup(exp1.Vault()), `Vault Transit mount="transit-backup"`) {
		t.Errorf("o banner tem de nomear a custodia; got %q", descreverCustodiaDoBackup(exp1.Vault()))
	}

	// 2.º «arranque»: instâncias NOVAS dos dois adaptadores, mesmo Vault.
	dsar2 := newVaultKeyVault(srv.URL, "transit", "tok-do-no")
	dsar2.exigirReconciliacao()
	cfg.BackupVault = newVaultKeyVault(srv.URL, "transit-backup", "", withVaultTokenFrom(dsar2))
	aos101Seed(t, node, "run-453", 2)
	exp2, err := comporExportadorDeBackup(cfg, node.EventStore, dsar2)
	if err != nil {
		t.Fatalf("o 2.º arranque tinha de RETOMAR: %v", err)
	}
	if exp2.ResumedFrom() != 1 {
		t.Fatalf("ResumedFrom=%d", exp2.ResumedFrom())
	}
	if r, err := exp2.Export(ctx); err != nil || r.Cycle != 2 {
		t.Fatalf("ciclo 2: %+v %v", r, err)
	}
	rst, err := backup.NewRestorer(dst, cfg.BackupVault, exp2.Public())
	if err != nil {
		t.Fatal(err)
	}
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	sink, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard("board", "eu-west"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	ev, err := rst.RestoreTo(ctx, m, cp, 0, nil, sink)
	if err != nil || ev.EventsRestored < 5 {
		t.Fatalf("a cadeia de dois arranques no Vault devia restaurar >= 5 eventos: %+v %v", ev, err)
	}

	// Critério 4 sobre o nó: destruída a KEK no mount do backup, o restauro aborta sem escrever.
	fv.destruirTudo("transit-backup")
	sink2, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard("board", "eu-west"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink2.Close()
	if _, err := rst.RestoreTo(ctx, m, cp, 0, nil, sink2); !errors.Is(err, backup.ErrRestoreVerify) {
		t.Fatalf("com a KEK do backup destruida o restauro tinha de ser ErrRestoreVerify; got %v", err)
	}
}

// TestAOS453_OBackupRecusaAMountDoDSAR: sem mount próprio, a custódia Vault do DSAR é RECUSADA
// (portão AOS-436 + delete da política); a instância do DSAR, ou o mesmo mount, injectados como
// custódia do backup, também.
func TestAOS453_OBackupRecusaAMountDoDSAR(t *testing.T) {
	fv := novoVaultPorMount("transit", "transit-backup")
	srv := httptest.NewServer(fv)
	defer srv.Close()
	dsarKV := newVaultKeyVault(srv.URL, "transit", "t")
	node := aos453Fonte(t, 1)
	cfg := tnBaseConfig()
	cfg.BackupDestination = backup.NewInMemoryImmutableStore("eu-west")
	cfg.BackupSigningKey = aos101Key(t)

	if _, err := comporExportadorDeBackup(cfg, node.EventStore, dsarKV); !errors.Is(err, ErrBackupVaultMountMissing) {
		t.Fatalf("sem mount proprio a custodia DSAR tinha de ser recusada; got %v", err)
	}
	cfg.BackupVault = dsarKV
	if _, err := comporExportadorDeBackup(cfg, node.EventStore, dsarKV); !errors.Is(err, ErrBackupVaultMountMissing) {
		t.Fatalf("a propria instancia DSAR como custodia do backup tinha de ser recusada; got %v", err)
	}
	cfg.BackupVault = newVaultKeyVault(srv.URL, "transit", "", withVaultTokenFrom(dsarKV))
	if _, err := comporExportadorDeBackup(cfg, node.EventStore, dsarKV); !errors.Is(err, ErrBackupVaultMountMissing) {
		t.Fatalf("o MESMO mount do DSAR tinha de ser recusado; got %v", err)
	}
	if fv.chaves("transit") != 0 {
		t.Fatal("nenhuma recusa pode ter criado chaves no mount do DSAR")
	}
}

// TestAOS453_UmaCustodiaDoBackupEmBaixoNaoSeLeComoKEKErrada: o Vault não responde ⇒ o erro nomeia
// a custódia indisponível, e não «a KEK nao e a que selou».
func TestAOS453_UmaCustodiaDoBackupEmBaixoNaoSeLeComoKEKErrada(t *testing.T) {
	dsarKV := newVaultKeyVault("http://127.0.0.1:1", "transit", "t")
	node := aos453Fonte(t, 1)
	cfg := tnBaseConfig()
	cfg.BackupDestination = backup.NewInMemoryImmutableStore("eu-west")
	cfg.BackupSigningKey = aos101Key(t)
	cfg.BackupVault = newVaultKeyVault("http://127.0.0.1:1", "transit-backup", "", withVaultTokenFrom(dsarKV))
	_, err := comporExportadorDeBackup(cfg, node.EventStore, dsarKV)
	if !errors.Is(err, backup.ErrKEKCustodyUnavailable) || errors.Is(err, backup.ErrResumeUnverifiable) {
		t.Fatalf("Vault em baixo tinha de ser ErrKEKCustodyUnavailable; got %v", err)
	}
	if !strings.Contains(err.Error(), `mount="transit-backup"`) {
		t.Errorf("o erro tem de nomear a custodia; got %v", err)
	}
}

// TestAOS453_OBootstrapUsaACustodiaDoBackupEOsDoisBannersANomeiam: pela composição completa (o
// [Bootstrap]), Config.BackupVault chega ao exportador e os banners de composição e do agendador
// dizem que custódia sela a KEK do backup (critério 5).
func TestAOS453_OBootstrapUsaACustodiaDoBackupEOsDoisBannersANomeiam(t *testing.T) {
	wal := filepath.Join(t.TempDir(), "events.wal")
	dst := backup.NewInMemoryImmutableStore("eu-west")
	chave := aos101Key(t)
	custodia := audit.NewInMemoryKeyWrapper(nil)

	arrancar := func(w io.Writer) *Node {
		cfg := aos101Config(t, dst, time.Hour)
		cfg.EventStorePath = wal
		cfg.BackupSigningKey = chave
		cfg.BackupVault = custodia
		n, err := Bootstrap(context.Background(), cfg, w)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		return n
	}
	var log1 registoSeguro
	no1 := arrancar(&log1)
	if no1.BackupExporter.Vault() != audit.KeyVault(custodia) {
		t.Fatal("o exportador nao usa Config.BackupVault")
	}
	if !strings.Contains(log1.String(), "KEK do backup selada por: custodia de envelope") {
		t.Errorf("o banner da composicao tem de nomear a custodia; log=%q", log1.String())
	}
	svc1, logs1 := aos101Service(t, no1)
	if !strings.Contains(logs1.String(), "KEK do backup selada por: custodia de envelope") {
		t.Errorf("o banner do agendador tem de nomear a custodia; log=%q", logs1.String())
	}
	aos101Seed(t, no1, "run-453b", 2)
	if !svc1.ExportBackupNow(context.Background()) {
		t.Fatal("ciclo 1 parou o laco")
	}
	sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = svc1.Shutdown(sd)
	cancel()
	if err := no1.Close(); err != nil {
		t.Fatal(err)
	}

	var log2 registoSeguro
	no2 := arrancar(&log2)
	t.Cleanup(func() { _ = no2.Close() })
	if !strings.Contains(log2.String(), "cadeia RETOMADA do ciclo 1") {
		t.Fatalf("o 2.º arranque com a mesma custodia de envelope tinha de retomar; log=%q", log2.String())
	}
}

// ---------------------------------------------------------------------------
// Superfície de ambiente (F2)
// ---------------------------------------------------------------------------

// aos453Env limpa a superfície do backup e devolve um directório de destino e uma seed válida.
func aos453Env(t *testing.T) (destDir, seedPath string) {
	t.Helper()
	for _, k := range []string{"AOS_BACKUP_DEST", "AOS_BACKUP_DEST_REGION", "AOS_BACKUP_SIGNING_KEY_PATH", "AOS_BACKUP_RETENTION", "AOS_BACKUP_VAULT_TRANSIT_MOUNT"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	destDir = filepath.Join(dir, "backup")
	if err := os.Mkdir(destDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seedPath = filepath.Join(dir, "backup-signing.seed")
	if err := os.WriteFile(seedPath, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return destDir, seedPath
}

func fileURL(p string) string { return "file:///" + strings.TrimPrefix(filepath.ToSlash(p), "/") }

func aos453SetEnvCompleto(t *testing.T, destDir, seedPath string) {
	t.Helper()
	t.Setenv("AOS_BACKUP_DEST", fileURL(destDir))
	t.Setenv("AOS_BACKUP_DEST_REGION", "eu")
	t.Setenv("AOS_BACKUP_SIGNING_KEY_PATH", seedPath)
	t.Setenv("AOS_BACKUP_RETENTION", "2160h")
}

var aos453Board = map[string]string{"board:demo": "eu"}

func TestAOS453_Env_SemDestinoNadaMudaEAsOutrasSaoDeclaradasIgnoradas(t *testing.T) {
	aos453Env(t)
	got, err := backupFromEnv(true, aos453Board, nil)
	if err != nil || got.dest != nil || got.vault != nil || len(got.ignoradas) != 0 {
		t.Fatalf("sem AOS_BACKUP_DEST nada se compoe, tambem em producao: %+v %v", got, err)
	}
	t.Setenv("AOS_BACKUP_RETENTION", "2160h")
	got, err = backupFromEnv(true, aos453Board, nil)
	if err != nil || got.dest != nil || len(got.ignoradas) != 1 || got.ignoradas[0] != "AOS_BACKUP_RETENTION" {
		t.Fatalf("variaveis sem destino ficam declaradas IGNORADAS: %+v %v", got, err)
	}
}

func TestAOS453_Env_DestinoEmDiscoCompoeComTudoObrigatorio(t *testing.T) {
	destDir, seedPath := aos453Env(t)
	aos453SetEnvCompleto(t, destDir, seedPath)
	got, err := backupFromEnv(false, aos453Board, nil)
	if err != nil {
		t.Fatalf("config completa devia compor: %v", err)
	}
	fs, ok := got.dest.(*backup.FileImmutableStore)
	if !ok || fs.Region() != "eu" {
		t.Fatalf("destino %T regiao %q", got.dest, got.dest.Region())
	}
	if got.retention != 2160*time.Hour || len(got.signingKey) != ed25519.PrivateKeySize {
		t.Fatalf("retencao/chave: %v %d", got.retention, len(got.signingKey))
	}
	// A seed é LIDA e nunca criada: a mesma seed dá a mesma chave, e o ficheiro não muda.
	antes, _ := os.ReadFile(seedPath)
	got2, err := backupFromEnv(false, aos453Board, nil)
	if err != nil || !got2.signingKey.Equal(got.signingKey) {
		t.Fatalf("a mesma seed tem de dar a mesma chave: %v", err)
	}
	depois, _ := os.ReadFile(seedPath)
	if string(antes) != string(depois) {
		t.Fatal("o no NAO pode escrever na seed")
	}
}

func TestAOS453_Env_FailClosed(t *testing.T) {
	cases := []struct {
		nome  string
		ajust func(t *testing.T, destDir, seedPath string)
		prod  bool
		dsar  audit.KeyVault
		quero error
	}{
		{"s3 e a F4", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_DEST", "s3://bucket/aos") }, false, nil, ErrBackupDestNotImplemented},
		{"esquema desconhecido", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_DEST", "nfs://x/y") }, false, nil, ErrBadBackupDest},
		{"file com host", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_DEST", "file://host/x") }, false, nil, ErrBadBackupDest},
		{"directorio inexistente", func(t *testing.T, d, _ string) { t.Setenv("AOS_BACKUP_DEST", fileURL(filepath.Join(d, "nao-existe"))) }, false, nil, ErrBadBackupDest},
		{"sem regiao", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_DEST_REGION", "") }, false, nil, ErrBadBackupDestRegion},
		{"regiao fora do board", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_DEST_REGION", "us-east") }, false, nil, ErrBadBackupDestRegion},
		{"sem chave", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_SIGNING_KEY_PATH", "") }, false, nil, ErrBadBackupSigningKey},
		{"chave inexistente NAO e criada", func(t *testing.T, d, _ string) {
			t.Setenv("AOS_BACKUP_SIGNING_KEY_PATH", filepath.Join(d, "nova.seed"))
		}, false, nil, ErrBadBackupSigningKey},
		{"chave nao-hex", func(t *testing.T, d, _ string) {
			p := filepath.Join(d, "lixo.seed")
			_ = os.WriteFile(p, []byte("nao-e-hex"), 0o600)
			t.Setenv("AOS_BACKUP_SIGNING_KEY_PATH", p)
		}, false, nil, ErrBadBackupSigningKey},
		{"sem retencao", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_RETENTION", "") }, false, nil, ErrBadBackupRetention},
		{"retencao zero", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_RETENTION", "0s") }, false, nil, ErrBadBackupRetention},
		{"producao sem mount", func(t *testing.T, _, _ string) {}, true, nil, ErrBadBackupVault},
		{"mount sem Vault DSAR", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_VAULT_TRANSIT_MOUNT", "transit-backup") }, false, audit.NewInMemoryKeyVault(nil), ErrBadBackupVault},
		{"mount igual ao do DSAR", func(t *testing.T, _, _ string) { t.Setenv("AOS_BACKUP_VAULT_TRANSIT_MOUNT", "transit") }, false, newVaultKeyVault("https://vault:8200", "transit", "t"), ErrBadBackupVault},
	}
	for _, c := range cases {
		t.Run(c.nome, func(t *testing.T) {
			destDir, seedPath := aos453Env(t)
			aos453SetEnvCompleto(t, destDir, seedPath)
			c.ajust(t, filepath.Dir(destDir), seedPath)
			if _, err := backupFromEnv(c.prod, aos453Board, c.dsar); !errors.Is(err, c.quero) {
				t.Fatalf("quero %v; got %v", c.quero, err)
			}
		})
	}
	// A seed pedida e inexistente NÃO foi criada.
	d, _ := aos453Env(t)
	t.Setenv("AOS_BACKUP_DEST", fileURL(d))
	t.Setenv("AOS_BACKUP_DEST_REGION", "eu")
	t.Setenv("AOS_BACKUP_RETENTION", "1h")
	nova := filepath.Join(filepath.Dir(d), "nunca.seed")
	t.Setenv("AOS_BACKUP_SIGNING_KEY_PATH", nova)
	_, _ = backupFromEnv(false, aos453Board, nil)
	if _, err := os.Stat(nova); !os.IsNotExist(err) {
		t.Fatal("o no criou a seed — tem de a LER e nunca a criar")
	}
}

// TestAOS453_Env_SeedLegivelPorOutrosERecusada: a seed é material privado (INFO da revisão).
func TestAOS453_Env_SeedLegivelPorOutrosERecusada(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissoes POSIX nao se medem em Windows")
	}
	destDir, seedPath := aos453Env(t)
	aos453SetEnvCompleto(t, destDir, seedPath)
	if err := os.Chmod(seedPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backupFromEnv(false, aos453Board, nil); !errors.Is(err, ErrBadBackupSigningKey) {
		t.Fatalf("seed legivel por outros tinha de ser recusada; got %v", err)
	}
	if err := os.Chmod(seedPath, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := backupFromEnv(false, aos453Board, nil); err != nil {
		t.Fatalf("seed 0400 tinha de passar: %v", err)
	}
}

// TestAOS453_APoliticaDoBackupUsaMaisENaoAsterisco é um teste de CONTEÚDO da política do nó sobre o
// mount do backup em deploy/server/provision-identity.sh (achado ALTO da revisão de segurança,
// confirmado num Vault 1.18 real). NÃO prova a ACL — o fakeTransit não modela políticas; a prova
// viva é o `verificar_acl_backup` do próprio script (capacidades pedidas ao Vault) e o guião com
// `vault server -dev` em deploy/server/README.md. Prova só a FORMA que o Vault real exige:
//   - no Vault o `*` só é glob no FIM; um `*` a meio é literal (a regra `aos-kek-*/*` não negava nada);
//   - `aos-kek-*` no fim casa também `aos-kek-X/config`, `/rotate`, `/trim` — daí o `+` (um segmento);
//   - nenhuma capacidade `delete` sobre o mount do backup;
//   - a política prova-se numa CANDIDATA antes de substituir a `aos-node`.
func TestAOS453_APoliticaDoBackupUsaMaisENaoAsterisco(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "server", "provision-identity.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	var regras []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), `path "transit-backup/`) {
			regras = append(regras, strings.TrimSpace(l))
		}
	}
	if len(regras) < 3 {
		t.Fatalf("esperadas as regras keys/encrypt/decrypt do transit-backup; achei %d", len(regras))
	}
	for _, r := range regras {
		caminho := strings.SplitN(r, `"`, 3)[1]
		if strings.Contains(caminho, "*") {
			t.Errorf("regra do backup com `*` (casa sub-caminhos, ou e literal a meio): %s", r)
		}
		if !strings.HasSuffix(caminho, "/+") {
			t.Errorf("regra do backup tem de acabar em `/+` (um segmento): %s", r)
		}
		if strings.Contains(r, `"delete"`) || strings.Contains(r, `"sudo"`) {
			t.Errorf("o no nao pode ter delete/sudo no mount do backup: %s", r)
		}
	}
	cand := strings.Index(s, "vault policy write aos-node-candidata")
	final := strings.Index(s, "vault policy write aos-node -")
	if cand < 0 || final < 0 || cand > final {
		t.Fatal("a politica tem de ser provada numa CANDIDATA antes de substituir a aos-node")
	}
	if !strings.Contains(s, "for sub in config rotate trim") {
		t.Fatal("a verificacao pela ACL tem de exigir deny em /config, /rotate e /trim")
	}
}

// TestAOS453_Env_MountProprioReutilizaEnderecoETokenDaDSAR: o mount do backup é uma instância nova
// sobre o mesmo endereço, que lê o token da instância DSAR (a que o renovador mantém).
func TestAOS453_Env_MountProprioReutilizaEnderecoETokenDaDSAR(t *testing.T) {
	destDir, seedPath := aos453Env(t)
	aos453SetEnvCompleto(t, destDir, seedPath)
	t.Setenv("AOS_BACKUP_VAULT_TRANSIT_MOUNT", "/transit-backup/")
	dsarKV := newVaultKeyVault("https://vault:8200", "transit", "tok-a")
	got, err := backupFromEnv(true, aos453Board, dsarKV)
	if err != nil {
		t.Fatalf("producao com mount proprio devia compor: %v", err)
	}
	kv, ok := got.vault.(*vaultKeyVault)
	if !ok || kv.mount != "transit-backup" || kv.addr != dsarKV.addr || kv.tokenDe != dsarKV {
		t.Fatalf("custodia do backup mal composta: %+v", got.vault)
	}
	// O token RODADO na DSAR chega ao backup sem mais nada.
	dsarKV.mu.Lock()
	dsarKV.token = "tok-b"
	dsarKV.mu.Unlock()
	if kv.currentToken() != "tok-b" {
		t.Fatalf("a custodia do backup tem de seguir o token da DSAR; got %q", kv.currentToken())
	}
}

// TestAOS453_Env_ProducaoSemMountAbortaONo: a guarda de produção pelo caminho REAL do nó
// (nodeConfigFromEnv), não só pela função.
func TestAOS453_Env_ProducaoSemMountAbortaONo(t *testing.T) {
	dir := prodKEKEnvBase(t)
	destDir, seedPath := aos453Env(t)
	tok := filepath.Join(dir, "vault-token")
	if err := os.WriteFile(tok, []byte("dev-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_DSAR_VAULT_ADDR", "https://vault:8200")
	t.Setenv("AOS_DSAR_VAULT_TOKEN_PATH", tok)
	aos453SetEnvCompleto(t, destDir, seedPath)
	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadBackupVault) {
		t.Fatalf("producao com destino e sem mount do backup tinha de abortar com ErrBadBackupVault; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Prefixo `aos.` reservado no DSAR
// ---------------------------------------------------------------------------

// TestAOS453_OPrefixoAosEReservadoNoDSAR: um /dsar/erase (e um hold) de `aos.backup:<região>` é
// recusado com o erro nomeado, e a KEK desse titular SOBREVIVE.
func TestAOS453_OPrefixoAosEReservadoNoDSAR(t *testing.T) {
	node := newGovNode(t, &countingModel{})
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatal(err)
	}
	const interno = "aos.backup:eu-west"
	if _, _, err := node.DSARVault.EnsureKey(interno); err != nil {
		t.Fatal(err)
	}
	for _, sid := range []string{interno, "AOS.backup:eu-west", "aos.qualquer"} {
		rec := postReq(h, "/dsar/erase", dsarRequestWire{RequestID: "r-" + sid, SubjectID: sid}, govHeaders())
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "prefixo RESERVADO") {
			t.Fatalf("erase de %q tinha de ser 400 com o erro nomeado; veio %d %s", sid, rec.Code, rec.Body.String())
		}
		hold := postReq(h, "/dsar/hold", holdRequestWire{RequestID: "h-" + sid, SubjectID: sid}, govHeaders())
		if hold.Code != http.StatusBadRequest {
			t.Fatalf("hold de %q tinha de ser 400; veio %d", sid, hold.Code)
		}
	}
	if _, ok := node.DSARVault.Key(audit.KeyRefFor(interno)); !ok {
		t.Fatal("a KEK do titular interno tem de SOBREVIVER ao pedido recusado")
	}
	// Um titular externo que só PARECE o prefixo passa a guarda.
	for _, sid := range []string{"aosx", "aos-backup", "aos_backup:eu"} {
		if err := titularReservado(sid); err != nil {
			t.Errorf("%q nao e do dominio reservado; got %v", sid, err)
		}
	}
	if !errors.Is(titularReservado("aos.backup:eu"), ErrSubjectIDReservado) {
		t.Fatal("aos.backup:eu tem de ser reservado")
	}
}
