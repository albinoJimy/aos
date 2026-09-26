package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
)

// AOS-453 F2 — o adaptador DURÁVEL em disco da porta ImmutableStore, medido contra o contrato do
// README do módulo: Put condicional (ErrImmutable numa ref existente), ErrNotFound SÓ para «não
// existe», object-lock no Delete, e a sonda de escrita condicional do exportador a passar.

func novoFileStore(t *testing.T, region string) (*FileImmutableStore, string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewFileImmutableStore(root, region)
	if err != nil {
		t.Fatalf("NewFileImmutableStore: %v", err)
	}
	return s, root
}

func TestAOS453_FileStore_PutCondicionalEGetFiel(t *testing.T) {
	s, root := novoFileStore(t, "EU-West ")
	if s.Region() != "eu-west" {
		t.Fatalf("regiao normalizada %q", s.Region())
	}
	retain := t0.Add(time.Hour)
	if err := s.Put("eu-west/seg-1", []byte("primeiro"), retain); err != nil {
		t.Fatalf("Put#1: %v", err)
	}
	if err := s.Put("eu-west/seg-1", []byte("sobrescrita"), retain); !errors.Is(err, ErrImmutable) {
		t.Fatalf("Put#2 na mesma ref tinha de ser ErrImmutable; got %v", err)
	}
	got, err := s.Get("eu-west/seg-1")
	if err != nil || string(got) != "primeiro" {
		t.Fatalf("Get: %q %v (o conteudo original tem de ficar intacto)", got, err)
	}
	// Nenhum temporário fica para trás (nem do Put recusado).
	ents, _ := os.ReadDir(filepath.Join(root, "eu-west"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temporario abandonado: %s", e.Name())
		}
	}
	if _, err := s.Get("eu-west/nada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(inexistente) tinha de ser ErrNotFound; got %v", err)
	}
	if err := s.Delete("eu-west/nada", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(inexistente) tinha de ser ErrNotFound; got %v", err)
	}
}

func TestAOS453_FileStore_ObjectLockNoDelete(t *testing.T) {
	s, _ := novoFileStore(t, "eu-west")
	retain := t0.Add(time.Hour)
	if err := s.Put("eu-west/x", []byte("b"), retain); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("eu-west/x", t0); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("Delete dentro do lock tinha de ser ErrObjectLocked; got %v", err)
	}
	if err := s.Delete("eu-west/x", retain); err != nil {
		t.Fatalf("Delete depois do lock: %v", err)
	}
	if _, err := s.Get("eu-west/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("depois do Delete o Get tinha de ser ErrNotFound; got %v", err)
	}
}

// TestAOS453_FileStore_UmObjectoIlegivelNaoEInexistente: um ficheiro sem o cabeçalho (lixo,
// adulteração, outro escritor) é ERRO, nunca ErrNotFound — senão a retoma lia o destino como virgem.
func TestAOS453_FileStore_UmObjectoIlegivelNaoEInexistente(t *testing.T) {
	s, root := novoFileStore(t, "eu-west")
	if err := os.MkdirAll(filepath.Join(root, "eu-west"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "eu-west", "cycle-00000001"), []byte("lixo"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get("eu-west/cycle-00000001")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("objecto ilegivel tinha de ser erro e NAO ErrNotFound; got %v", err)
	}
}

func TestAOS453_FileStore_RefsForaDoAlfabetoSaoRecusadas(t *testing.T) {
	s, _ := novoFileStore(t, "eu-west")
	for _, ref := range []string{"", "../fora", "eu-west/../../fora", "/abs", "eu-west//x", "eu-west/.tmp-x", `eu-west\x`, "eu west/x"} {
		if err := s.Put(ref, []byte("b"), t0); err == nil || errors.Is(err, ErrImmutable) {
			t.Errorf("ref %q devia ser recusada; got %v", ref, err)
		}
	}
}

func TestAOS453_FileStore_RaizTemDeExistirEserAbsoluta(t *testing.T) {
	if _, err := NewFileImmutableStore("relativo/dir", "eu-west"); !errors.Is(err, ErrConfig) {
		t.Fatalf("caminho relativo tinha de ser ErrConfig; got %v", err)
	}
	if _, err := NewFileImmutableStore(filepath.Join(t.TempDir(), "nao-existe"), "eu-west"); !errors.Is(err, ErrConfig) {
		t.Fatalf("raiz inexistente tinha de ser ErrConfig (o adaptador nao a cria); got %v", err)
	}
	f := filepath.Join(t.TempDir(), "ficheiro")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileImmutableStore(f, "eu-west"); !errors.Is(err, ErrConfig) {
		t.Fatalf("raiz que e ficheiro tinha de ser ErrConfig; got %v", err)
	}
}

// TestAOS453_FileStore_PutConcorrenteTemUmSoVencedor: o link é a decisão atómica.
func TestAOS453_FileStore_PutConcorrenteTemUmSoVencedor(t *testing.T) {
	s, _ := novoFileStore(t, "eu-west")
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, imut := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Put("eu-west/disputada", []byte{byte(i)}, t0)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrImmutable):
				imut++
			default:
				t.Errorf("Put concorrente: erro inesperado %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || imut != n-1 {
		t.Fatalf("exactamente UM Put vence; ok=%d immutable=%d", ok, imut)
	}
}

func TestAOS453_FileStore_ASondaDeEscritaCondicionalPassa(t *testing.T) {
	s, _ := novoFileStore(t, "eu-west")
	if err := probeConditionalPut(s, "eu-west", t0.Add(time.Hour)); err != nil {
		t.Fatalf("a sonda do exportador tinha de passar no destino em disco: %v", err)
	}
	// E volta a passar num 2.º arranque (a sonda do anterior já lá está).
	if err := probeConditionalPut(s, "eu-west", t0.Add(time.Hour)); err != nil {
		t.Fatalf("2.ª sonda: %v", err)
	}
}

// TestAOS453_FileStore_DoisProcessosSobreODirectorioRetomamERESTAURAM: a prova de ponta a ponta
// com o destino durável REAL deste repositório — duas instâncias do adaptador sobre o mesmo
// directório (dois processos), a custódia de envelope partilhada, retoma, e a cadeia restaura.
func TestAOS453_FileStore_DoisProcessosSobreODirectorioRetomamERESTAURAM(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	root := t.TempDir()
	signer := newSigner(t)
	custodia := audit.NewInMemoryKeyWrapper(nil)
	ret := WithRetention(audit.NewRetentionPolicy(map[audit.DataClass]time.Duration{audit.ClassAudit: 30 * 24 * time.Hour}), audit.ClassAudit)

	d1, err := NewFileImmutableStore(root, "eu-west")
	if err != nil {
		t.Fatal(err)
	}
	seed(t, src, "run-a", 2, "s")
	exp1, err := NewExporter(src, d1, signer, WithKeyVault(custodia), ret)
	if err != nil {
		t.Fatalf("NewExporter (1.º): %v", err)
	}
	if _, err := exp1.Export(ctx); err != nil {
		t.Fatalf("ciclo 1: %v", err)
	}

	d2, err := NewFileImmutableStore(root, "eu-west")
	if err != nil {
		t.Fatal(err)
	}
	exp2, err := NewExporter(src, d2, signer, WithKeyVault(custodia), ret)
	if err != nil {
		t.Fatalf("o 2.º processo devia retomar do disco: %v", err)
	}
	if exp2.ResumedFrom() != 1 {
		t.Fatalf("ResumedFrom=%d", exp2.ResumedFrom())
	}
	seed(t, src, "run-a", 1, "s")
	if r, err := exp2.Export(ctx); err != nil || r.Cycle != 2 {
		t.Fatalf("ciclo 2: %+v %v", r, err)
	}
	// A retenção FINITA chega ao object-lock: o segmento não se apaga antes do prazo.
	m, cp, err := mustRestorer(t, d2, custodia, exp2).LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if err := d2.Delete(m.Segments[0].Ref, time.Now()); !errors.Is(err, ErrObjectLocked) {
		t.Fatalf("o segmento tem de estar sob object-lock dentro da retencao; got %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	ev, err := mustRestorer(t, d2, custodia, exp2).RestoreTo(ctx, m, cp, 0, nil, out)
	if err != nil || ev.EventsRestored != 3 {
		t.Fatalf("a cadeia em disco de dois processos devia restaurar 3 eventos: %+v %v", ev, err)
	}
}
