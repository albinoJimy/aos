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

// TestWormSealRecusaSelarSobreHistoriaREESCRITA é o vector que a guarda de comprimento NÃO via, e
// é o pior dos dois.
//
// `exigirMonotonia` — como a escrevi — comparava apenas o COMPRIMENTO: recusava uma partição que
// tivesse encolhido. Uma história REESCRITA com o mesmo número de registos passava por ela sem
// tocar em nada, e o selador produzia uma âncora PERFEITAMENTE VÁLIDA sobre a falsificação. O nó
// aceitá-la-ia para sempre: a reescrita desde a génese, assinada pela chave do selador.
//
// O cenário aqui é exactamente esse: MESMA partição, MESMO número de registos, conteúdo
// DIFERENTE. O comprimento não encolhe; a história não é a mesma.
func TestWormSealRecusaSelarSobreHistoriaREESCRITA(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.wal")
	ctx := context.Background()

	// Store 1: a história honesta, e a sua âncora.
	s1, err := audit.OpenFileStore(original)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s1.Append(ctx, audit.AuditRecord{
			Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t",
			Capability: "cap:fs.read", Resource: audit.Resource{Value: "doc://honesto"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s1.Close()

	seed, _ := seedDeTeste(t)
	var ancora bytes.Buffer
	if err := runWormSeal([]string{"--worm", original, "--key-file", seed}, &ancora); err != nil {
		t.Fatalf("selagem honesta: %v", err)
	}
	anteriorFile := filepath.Join(dir, "anterior.json")
	if err := os.WriteFile(anteriorFile, ancora.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Store 2: MESMA partição, MESMO comprimento, conteúdo DIFERENTE — a reescrita.
	reescrito := filepath.Join(dir, "reescrito.wal")
	s2, err := audit.OpenFileStore(reescrito)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s2.Append(ctx, audit.AuditRecord{
			Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t",
			Capability: "cap:fs.read", Resource: audit.Resource{Value: "doc://FORJADO"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// CONTROLO DO CENÁRIO: a reescrita re-encadeia sozinha (é uma cadeia forjada por inteiro,
	// coerente consigo mesma) e tem o MESMO head. Se falhasse aqui, este teste estaria a
	// exercitar outro vector.
	if _, verr := audit.VerifyStore(ctx, s2); verr != nil {
		t.Fatalf("a cadeia reescrita devia re-encadear sozinha: %v", verr)
	}
	if h, _ := s2.Head(ctx, "run-a"); h != 3 {
		t.Fatalf("head da reescrita = %d, queria 3 (mesmo comprimento)", h)
	}
	_ = s2.Close()

	var out bytes.Buffer
	err = runWormSeal([]string{"--worm", reescrito, "--key-file", seed, "--anterior", anteriorFile}, &out)
	if err == nil {
		t.Fatal("selou sobre uma historia REESCRITA — a ancora daria a autoridade da chave do " +
			"selador a uma cadeia forjada, que e exactamente o vector que a verificacao ancorada " +
			"existe para fechar")
	}
	if !errors.Is(err, ErrWormSealDivergencia) {
		t.Errorf("recusou pela razao errada: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("emitiu checkpoint apesar da recusa: %s", out.String())
	}
}

// TestWormSealRecusaParticaoQueENCOLHEU dá cenário PRÓPRIO ao ramo do comprimento.
//
// COMO APARECEU. Mutei as duas guardas de `exigirContinuidade` uma de cada vez e NENHUMA fez cair
// a bateria. A razão não era redundância no código: a truncatura física do
// `TestWormSealRecusaSelarSobreTruncatura` apaga a partição `run-b` INTEIRA e deixa `run-a` intacta
// em head=3. Ou seja, o único ramo exercitado era o do DESAPARECIMENTO — e como `head` fica a 0,
// `0 < cp.AuditSeq` também é verdade, pelo que cada guarda tapava a ausência da outra.
//
// Duas guardas que só se provam uma à outra não estão provadas. Este teste exercita o caso que
// faltava: a partição SOBREVIVE, com MENOS registos — um prefixo honesto da história ancorada.
func TestWormSealRecusaParticaoQueENCOLHEU(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "worm.wal")
	ctx := context.Background()

	store, err := audit.OpenFileStore(caminho)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ { // UMA só partição: a truncatura tem de a encolher, não de a apagar.
		if _, err := store.Append(ctx, audit.AuditRecord{
			Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Close()

	seed, _ := seedDeTeste(t)
	var primeira bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &primeira); err != nil {
		t.Fatalf("primeira selagem: %v", err)
	}
	anteriorFile := filepath.Join(dir, "anterior.json")
	if err := os.WriteFile(anteriorFile, primeira.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	bs, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caminho, bs[:len(bs)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	// CONTROLO DO CENÁRIO, e é ele que distingue este teste do da truncatura: a partição tem de
	// CONTINUAR LÁ, com head entre 1 e 7. Se tivesse desaparecido, isto seria o outro teste outra
	// vez e o ramo do comprimento voltaria a não ter prova nenhuma.
	s2, oerr := audit.OpenFileStore(caminho)
	if oerr != nil {
		t.Skipf("o store nao abre depois da truncatura (%v)", oerr)
	}
	head, _ := s2.Head(ctx, "run-a")
	_, verr := audit.VerifyStore(ctx, s2)
	_ = s2.Close()
	if verr != nil {
		t.Fatalf("o prefixo NAO re-encadeou (%v) — o vector aqui e o que o re-encadeamento nao ve", verr)
	}
	if head == 0 || head >= 8 {
		t.Fatalf("head=%d — a particao tinha de SOBREVIVER encolhida (1..7); com 0 este teste "+
			"exercitaria o ramo do DESAPARECIMENTO e o do comprimento ficaria outra vez sem cenario", head)
	}

	var out bytes.Buffer
	err = runWormSeal([]string{"--worm", caminho, "--key-file", seed, "--anterior", anteriorFile}, &out)
	if err == nil {
		t.Fatal("selou sobre uma particao ENCOLHIDA")
	}
	// E a razão importa: quem lê o alerta tem de distinguir «perdeu registos» de «foi reescrita».
	// São incidentes diferentes, com respostas diferentes.
	if !errors.Is(err, ErrWormSealRecuo) {
		t.Errorf("diagnostico errado — encolher e RECUO, nao divergencia: %v", err)
	}
}

// TestWormSealRecusaParticaoDESAPARECIDA dá cenário PRÓPRIO ao outro ramo, pelo mesmo motivo.
//
// Aqui a partição ancorada não encolheu: não existe de todo no store que se apresenta para selar.
// É o caso limite do recuo, e sem este teste ele só era atingido de raspão pelo teste da truncatura.
func TestWormSealRecusaParticaoDESAPARECIDA(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	seed, _ := seedDeTeste(t)

	completo := filepath.Join(dir, "completo.wal")
	s1, err := audit.OpenFileStore(completo)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"run-a", "run-b"} {
		if _, err := s1.Append(ctx, audit.AuditRecord{
			Partition: p, RunID: p, StepID: "s", ToolID: "t", Capability: "cap:fs.read",
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s1.Close()

	var primeira bytes.Buffer
	if err := runWormSeal([]string{"--worm", completo, "--key-file", seed}, &primeira); err != nil {
		t.Fatalf("primeira selagem: %v", err)
	}
	anteriorFile := filepath.Join(dir, "anterior.json")
	if err := os.WriteFile(anteriorFile, primeira.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Um store onde `run-b` NUNCA existiu — e `run-a` está impecável.
	parcial := filepath.Join(dir, "parcial.wal")
	s2, err := audit.OpenFileStore(parcial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Append(ctx, audit.AuditRecord{
		Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
	}); err != nil {
		t.Fatal(err)
	}
	if _, verr := audit.VerifyStore(ctx, s2); verr != nil {
		t.Fatalf("o store parcial devia re-encadear: %v", verr)
	}
	_ = s2.Close()

	var out bytes.Buffer
	err = runWormSeal([]string{"--worm", parcial, "--key-file", seed, "--anterior", anteriorFile}, &out)
	if err == nil {
		t.Fatal("selou um store onde uma particao JA ANCORADA nao existe — a ancora nova nao " +
			"mencionaria run-b e a sua desaparicao passaria a ser invisivel")
	}
	if !errors.Is(err, ErrWormSealRecuo) {
		t.Errorf("recusou pela razao errada: %v", err)
	}
	if !strings.Contains(err.Error(), "run-b") {
		t.Errorf("a recusa nao NOMEIA a particao que desapareceu — o operador nao sabe o que procurar: %v", err)
	}
	// E o DIAGNOSTICO tem de dizer «desapareceu», nao «encolheu para 0».
	//
	// Este ramo nao e uma segunda defesa: uma particao ausente da head=0, e `0 < cp.AuditSeq`
	// apanha-a de qualquer maneira — mutei o ramo e a bateria nao caiu enquanto nao exigi esta
	// linha. O que ele acrescenta e a LEITURA CERTA do incidente. «O store so tem 0 registos» faz
	// procurar registos apagados; «a particao desapareceu» faz procurar um ficheiro trocado, e
	// distingue ainda o caso em que o `Head` nem sequer conseguiu responder.
	//
	// Um ramo que so existe pela mensagem tem de ter a mensagem testada, senao e codigo morto a
	// espera de ser limpo por quem nao souber porque la esta.
	if !strings.Contains(err.Error(), "DESAPARECEU") {
		t.Errorf("diagnostico impreciso: uma particao AUSENTE nao e o mesmo incidente que uma "+
			"encolhida, e a resposta do operador difere: %v", err)
	}
}

// TestWormSealLeAnteriorComBOM — o selador tem de conseguir ler o que ele proprio escreveu.
//
// Descoberto a correr `selar-worm.ps1` DUAS vezes: a segunda recusou com «--anterior nao e JSON
// de checkpoints: invalid character 'ï'». O `Set-Content -Encoding utf8` do PowerShell 5.1 poe
// BOM, e o `encoding/json` recusa-o.
//
// Sem esta rede, a selagem DIARIA partia-se no segundo dia — e a mensagem de erro nao aponta para
// codificacao nenhuma.
func TestWormSealLeAnteriorComBOM(t *testing.T) {
	caminho, store := wormDeTeste(t)
	seed, _ := seedDeTeste(t)
	_ = store

	var primeira bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed}, &primeira); err != nil {
		t.Fatalf("primeira selagem: %v", err)
	}
	// O ficheiro tal como o PowerShell 5.1 o escrevia: com BOM.
	comBom := append([]byte{0xEF, 0xBB, 0xBF}, primeira.Bytes()...)
	anteriorFile := filepath.Join(t.TempDir(), "anterior.json")
	if err := os.WriteFile(anteriorFile, comBom, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed, "--anterior", anteriorFile}, &out); err != nil {
		t.Fatalf("o BOM impediu a segunda selagem: %v — a cadencia diaria partia-se no 2o dia", err)
	}
	if out.Len() == 0 {
		t.Error("selou sem emitir checkpoints")
	}
}

// TestWormSealRecusaAnteriorQueELIXO é o controlo: retirar o BOM não pode virar tolerância.
func TestWormSealRecusaAnteriorQueELIXO(t *testing.T) {
	caminho, _ := wormDeTeste(t)
	seed, _ := seedDeTeste(t)
	anteriorFile := filepath.Join(t.TempDir(), "anterior.json")
	// Lixo SEGUIDO de JSON válido: uma normalização gulosa aparava o prefixo e aceitava.
	if err := os.WriteFile(anteriorFile, []byte(`LIXO ANTES [{"Partition":"run-a","AuditSeq":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runWormSeal([]string{"--worm", caminho, "--key-file", seed, "--anterior", anteriorFile}, &out); err == nil {
		t.Fatal("aceitou um --anterior que e LIXO — a normalizacao do BOM virou tolerancia")
	}
}
