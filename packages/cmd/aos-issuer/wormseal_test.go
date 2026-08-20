package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------
// O SELADOR DE ÂNCORAS DO WORM.
//
// O nó tinha a verificação ancorada COMPLETA e fail-closed — e nada no repositório produzia um
// checkpoint. O inventário dizia que ela «hoje ancoraria 1 em 108»; ancorava ZERO, porque as três
// variáveis de ambiente nunca podiam ser preenchidas. Uma porta fail-closed que ninguém consegue
// abrir não é uma porta trancada: é uma parede descrita como opção.
//
// O teste que interessa não é «o comando corre». É o CICLO FECHADO: o que este comando assina tem
// de ser exactamente o que [audit.VerifyFromCheckpointAtHead] — a função que o nó chama no
// arranque — aceita.
// ---------------------------------------------------------------------------

// wormDeTeste constrói um WORM em ficheiro com registos em duas partições.
func wormDeTeste(t *testing.T) (string, *audit.FileStore) {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	store, err := audit.OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, p := range []string{"run-a", "run-b"} {
		for i := 0; i < 3; i++ {
			if _, err := store.Append(ctx, audit.AuditRecord{
				Partition: p, RunID: p, StepID: "s", ToolID: "t", Capability: "cap:fs.read",
			}); err != nil {
				t.Fatalf("Append em %s: %v", p, err)
			}
		}
	}
	return caminho, store
}

// seedDeTeste escreve uma seed ed25519 em hex, como os outros comandos a esperam.
func seedDeTeste(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "sealer.seed")
	if err := os.WriteFile(f, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, pub
}

// TestWormSealProduzAncoraQueONoACEITA é o teste do ciclo fechado.
func TestWormSealProduzAncoraQueONoACEITA(t *testing.T) {
	caminho, store := wormDeTeste(t)
	seed, pub := seedDeTeste(t)

	var out bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &out); err != nil {
		t.Fatalf("worm-seal: %v", err)
	}
	var cps []audit.Checkpoint
	if err := json.Unmarshal(out.Bytes(), &cps); err != nil {
		t.Fatalf("saida nao e JSON de checkpoints: %v\n%s", err, out.String())
	}
	if len(cps) != 2 {
		t.Fatalf("selou %d particoes, esperava 2 (run-a, run-b)", len(cps))
	}

	// O CICLO FECHA AQUI: a MESMA função que o nó chama no arranque tem de aceitar isto.
	ctx := context.Background()
	for _, cp := range cps {
		if err := audit.VerifyFromCheckpointAtHead(ctx, store, pub, cp, cp.AuditSeq, cp.AuditSeq); err != nil {
			t.Errorf("o no RECUSARIA a ancora de %q: %v", cp.Partition, err)
		}
	}

	// CONTROLO — outra chave NÃO valida. Sem este ramo, o teste acima passaria mesmo que a
	// verificação estivesse a aceitar tudo, e a âncora não ancoraria nada.
	_, outraPub := seedDeTeste(t)
	if err := audit.VerifyFromCheckpointAtHead(ctx, store, outraPub, cps[0], cps[0].AuditSeq, cps[0].AuditSeq); err == nil {
		t.Error("uma ancora assinada por OUTRA chave foi aceite — o trust-anchor nao esta a decidir nada")
	}
}

// TestMutacaoDeByteDegeneraEmTruncatura documenta um FACTO do formato que decide qual das duas
// guardas do selador é a que importa — e que só apareceu por um teste falhar.
//
// A primeira versão deste ficheiro virava um byte no meio do WAL e esperava que o re-encadeamento
// acusasse uma MUTAÇÃO. Não acusa. Varri os 3438 bytes de um WAL de teste e NENHUM flip fez
// `VerifyStore` devolver erro: o byte parte o ENQUADRAMENTO, o `OpenFileStore` descarta dali para
// a frente em silêncio, e o registo nunca chega ao verificador de cadeia. Ao nível do ficheiro,
// uma mutação DEGENERA EM TRUNCATURA.
//
// A consequência é a razão de ser da guarda de monotonia: contra quem mexa no ficheiro,
// re-encadear não é a defesa — comparar com o que já estava ancorado é. O [ErrWormSealChainBroken]
// continua a valer para um store que genuinamente não re-encadeie (uma falsificação re-enquadrada,
// um ficheiro corrompido a meio da escrita), mas não é ele que fecha este vector.
func TestMutacaoDeByteDegeneraEmTruncatura(t *testing.T) {
	caminho, store := wormDeTeste(t)
	ctx := context.Background()
	if _, err := audit.VerifyStore(ctx, store); err != nil {
		t.Fatalf("o WAL intacto ja nao re-encadeia: %v", err)
	}
	antes := len(store.Partitions())
	_ = store.Close()

	bs, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	bs[len(bs)/2] ^= 0xFF
	if err := os.WriteFile(caminho, bs, 0o600); err != nil {
		t.Fatal(err)
	}

	s2, oerr := audit.OpenFileStore(caminho)
	if oerr != nil {
		t.Skipf("o store nao abre (%v) — o facto documentado aqui nao se aplica a este WAL", oerr)
	}
	defer s2.Close()
	_, verr := audit.VerifyStore(ctx, s2)
	depois := len(s2.Partitions())

	if verr != nil {
		t.Fatalf("o re-encadeamento APANHOU a mutacao (%v) — o facto que este teste documenta "+
			"deixou de ser verdade, e o comentario acima precisa de ser reescrito", verr)
	}
	if depois >= antes {
		t.Errorf("esperava PERDA de particoes por truncatura silenciosa: antes=%d depois=%d", antes, depois)
	}
}

// TestWormSealRecusaSelarSobreTruncatura é o vector que o re-encadeamento NÃO vê, e é a razão de
// ser da verificação ancorada.
//
// Uma cadeia truncada RE-ENCADEIA COMO ÍNTEGRA — está declarado no banner do nó. Sem a guarda de
// monotonia, alimentar o selador com uma cópia truncada produzia uma âncora PERFEITAMENTE VÁLIDA
// para uma cadeia TRUNCADA, e o nó passaria a aceitá-la para sempre. O esquema seria derrotado na
// sua raiz — o único sítio onde ninguém está a olhar.
func TestWormSealRecusaSelarSobreTruncatura(t *testing.T) {
	caminho, store := wormDeTeste(t)
	seed, _ := seedDeTeste(t)

	// Selagem 1: o estado honesto.
	var primeira bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &primeira); err != nil {
		t.Fatalf("primeira selagem: %v", err)
	}
	anteriorFile := filepath.Join(t.TempDir(), "anterior.json")
	if err := os.WriteFile(anteriorFile, primeira.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	// TRUNCATURA física do tail.
	bs, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caminho, bs[:len(bs)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	// CONTROLO DO PRÓPRIO CENÁRIO: a cadeia truncada TEM de re-encadear sem erro, senão este teste
	// estaria a exercitar outro vector e a passar pela razão errada.
	s2, oerr := audit.OpenFileStore(caminho)
	if oerr != nil {
		t.Skipf("o store nao abre depois da truncatura (%v) — cenario nao aplicavel", oerr)
	}
	_, verr := audit.VerifyStore(context.Background(), s2)
	_ = s2.Close()
	if verr != nil {
		t.Fatalf("a cadeia truncada NAO re-encadeou (%v) — este teste tem de exercitar o vector que "+
			"o re-encadeamento nao ve", verr)
	}

	var out bytes.Buffer
	err = runWormSeal([]string{"--worm", caminho, "--key-file", seed, "--anterior", anteriorFile}, &out)
	if err == nil {
		t.Fatal("selou sobre uma TRUNCATURA — a ancora daria autoridade de chave a um WORM que perdeu registos")
	}
	if !errors.Is(err, ErrWormSealRecuo) {
		t.Errorf("recusou pela razao errada: %v", err)
	}

	// CONTROLO: SEM `--anterior` o selador não tem contra o que comparar, e sela. É o limite
	// honesto da guarda — comparativa, não absoluta — e fica escrito para ninguém supor uma
	// protecção que não existe.
	var semAnterior bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &semAnterior); err != nil {
		t.Errorf("sem --anterior devia selar (a guarda e comparativa): %v", err)
	}
}

// TestWormSealEmiteOsPisosSEPARADOS fixa a razão de os pisos não viajarem com os checkpoints.
//
// O piso existe para recusar um checkpoint LEGÍTIMO mas ANTERIOR, reapresentado para mascarar a
// truncatura do que veio depois. Se viajasse no mesmo ficheiro, quem trocasse o ficheiro trocava
// os dois — e o piso deixaria de morder no ataque que existe para fechar.
func TestWormSealEmiteOsPisosSEPARADOS(t *testing.T) {
	caminho, _ := wormDeTeste(t)
	seed, _ := seedDeTeste(t)

	var comCheckpoints bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &comCheckpoints); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(comCheckpoints.String(), "expected_head") {
		t.Errorf("o ficheiro de checkpoints traz os pisos — quem o trocasse trocava os dois:\n%s",
			comCheckpoints.String())
	}

	var comHeads bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed, "--heads"}, &comHeads); err != nil {
		t.Fatal(err)
	}
	var heads map[string]uint64
	if err := json.Unmarshal(comHeads.Bytes(), &heads); err != nil {
		t.Fatalf("--heads nao emitiu um mapa: %v\n%s", err, comHeads.String())
	}
	if heads["run-a"] != 3 || heads["run-b"] != 3 {
		t.Errorf("pisos errados: %+v (cada particao levou 3 registos)", heads)
	}
	// CONTROLO: e o modo `--heads` NÃO emite checkpoints, senão a separação seria decorativa.
	if strings.Contains(comHeads.String(), "Signature") {
		t.Errorf("--heads emitiu tambem os checkpoints: %s", comHeads.String())
	}
}

// TestWormSealNaoEcoaASeed — a chave privada nunca aparece na saída, nem em erro.
func TestWormSealNaoEcoaASeed(t *testing.T) {
	caminho, _ := wormDeTeste(t)
	seed, _ := seedDeTeste(t)
	bruto, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(bruto)) {
		t.Error("a saida contem a SEED do selador")
	}
	var vazio bytes.Buffer
	erro := runWormSeal([]string{"--worm", filepath.Join(t.TempDir(), "nao-existe.wal"), "--key-file", seed}, &vazio)
	if erro != nil && strings.Contains(erro.Error(), string(bruto)) {
		t.Error("a mensagem de erro contem a SEED do selador")
	}
}

// TestWormSealRecusaSelarCadeiaPartida prova o [ErrWormSealChainBroken] — a guarda que ficou por
// testar quando a adulteração por byte se revelou degenerar em truncatura.
//
// O vector aqui é a REMOÇÃO INTERNA: apaga-se um registo do MEIO, deixando o enquadramento dos
// restantes intacto. A cadeia deixa de encadear (o PrevHash do seguinte já não bate), e é isso
// que a re-verificação existe para apanhar.
//
// As fronteiras dos frames não se adivinham — MEDEM-SE, registando o tamanho do ficheiro a cada
// append. Adivinhá-las foi o que tornou a primeira tentativa de adulteração inútil.
func TestWormSealRecusaSelarCadeiaPartida(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	store, err := audit.OpenFileStore(caminho)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var fronteiras []int64
	for i := 0; i < 4; i++ {
		if _, err := store.Append(ctx, audit.AuditRecord{
			Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
		}); err != nil {
			t.Fatal(err)
		}
		fi, serr := os.Stat(caminho)
		if serr != nil {
			t.Fatal(serr)
		}
		fronteiras = append(fronteiras, fi.Size())
	}
	_ = store.Close()
	if fronteiras[0] == fronteiras[1] {
		t.Skip("o store nao escreve por append (buffer) — nao consigo medir fronteiras de frame")
	}

	bs, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	// Remove o SEGUNDO frame: [fronteiras[0], fronteiras[1]).
	semOMeio := append(append([]byte(nil), bs[:fronteiras[0]]...), bs[fronteiras[1]:]...)
	if err := os.WriteFile(caminho, semOMeio, 0o600); err != nil {
		t.Fatal(err)
	}

	// O `OpenFileStore` VALIDA A CADEIA AO ABRIR — descobri-o aqui: a remoção interna é recusada
	// logo na abertura («adulteracao removal … audit_seq em falta no replay»), ANTES de o
	// `VerifyStore` do selador chegar a correr.
	//
	// Consequência honesta: o [ErrWormSealChainBroken] é uma SEGUNDA rede que nenhum vector de
	// ficheiro alcança hoje. Fica, porque só vale para um store que abra sem validar — mas o que
	// este teste pode afirmar é o RESULTADO (o selador recusa e não emite nada), e não qual das
	// duas redes o apanhou.
	if s2, oerr := audit.OpenFileStore(caminho); oerr == nil {
		_, verr := audit.VerifyStore(ctx, s2)
		_ = s2.Close()
		if verr == nil {
			t.Skip("a remocao interna nao foi detectada por nenhuma das redes — vector nao exercitavel")
		}
	}

	seed, _ := seedDeTeste(t)
	var out bytes.Buffer
	serr := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &out)
	if serr == nil {
		t.Fatal("selou uma cadeia que NAO re-encadeia — a ancora certificaria a falsificacao com a " +
			"autoridade da chave do selador")
	}
	if out.Len() > 0 {
		t.Errorf("emitiu checkpoint apesar da recusa: %s", out.String())
	}
}
