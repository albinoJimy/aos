package audit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// aos284_posse_test.go — AC1 (escritor exclusivo por partição) e AC3 (a recusa é
// observável), provados contra o cenário que a medição confirmou.

// posseFixa é uma [PosseDeParticao] de teste: detém exactamente as partições da lista.
// Um lease durável real responde à mesma pergunta com mais passos.
type posseFixa struct {
	minhas map[string]bool
	erro   error
}

func (p posseFixa) Detem(_ context.Context, particao string) (bool, error) {
	if p.erro != nil {
		return false, p.erro
	}
	return p.minhas[particao], nil
}

// TestAOS284_AC1_ADisciplinaDePosseIMPEDEOForkQueAMedicaoConfirmou.
//
// É o teste que fecha o ciclo: o MESMO cenário de aos284_dois_escritores_test.go — dois
// FileStore, o mesmo ficheiro, a mesma partição — mas agora com posse armada. B é recusado,
// a cadeia não bifurca, e o WAL REABRE.
//
// O contraste é o ponto. Sem posse, o mesmo cenário deixava o WORM inabrível e o nó sem
// arrancar; a diferença entre as duas situações é uma porta de um método.
func TestAOS284_AC1_ADisciplinaDePosseImpedeOFork(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	const particao = "gov.read/run-posse"

	a, err := OpenFileStore(caminho, ComPosseDeParticao(posseFixa{minhas: map[string]bool{particao: true}}))
	if err != nil {
		t.Fatalf("abrir A: %v", err)
	}
	// B abre o MESMO ficheiro mas NÃO detém a partição — é a réplica que perdeu a eleição.
	b, err := OpenFileStore(caminho, ComPosseDeParticao(posseFixa{minhas: map[string]bool{}}))
	if err != nil {
		t.Fatalf("abrir B: %v", err)
	}

	if _, err := a.Append(ctx, registoDeTeste(particao, "base")); err != nil {
		t.Fatalf("A é dono e devia escrever: %v", err)
	}
	if _, err := a.Append(ctx, registoDeTeste(particao, "escrito-por-A")); err != nil {
		t.Fatalf("segundo append de A: %v", err)
	}

	_, erroDeB := b.Append(ctx, registoDeTeste(particao, "escrito-por-B"))
	if !errors.Is(erroDeB, ErrParticaoAlheia) {
		t.Fatalf("B não detém a partição e devia ser RECUSADO; veio %v", erroDeB)
	}
	_ = a.Close()
	_ = b.Close()

	// O WAL REABRE — e é isto que distingue esta correcção de um simples erro devolvido.
	// Sem a disciplina, este mesmo cenário deixava o ficheiro inabrível.
	depois, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("com posse armada o WAL tinha de reabrir sem bifurcação: %v", err)
	}
	defer func() { _ = depois.Close() }()

	head, err := depois.Head(ctx, particao)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 2 {
		t.Fatalf("a partição devia ter exactamente os 2 registos de A; tem head=%d", head)
	}
}

// TestAOS284_AC3_ARecusaEOBSERVAVELEDizONDE.
//
// «Recusada» não chega: um erro devolvido a um chamador que o engula é indistinguível de
// nada ter acontecido. O contador diz que houve recusa E em que partição — que é a
// diferença entre um alarme e um diagnóstico.
func TestAOS284_AC3_ARecusaEObservavelEDizOnde(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")

	s, err := OpenFileStore(caminho, ComPosseDeParticao(posseFixa{minhas: map[string]bool{"minha": true}}))
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer func() { _ = s.Close() }()

	if total, _ := s.RecusasDePosse(); total != 0 {
		t.Fatalf("um store novo não tem recusas; tem %d", total)
	}
	if _, err := s.Append(ctx, registoDeTeste("minha", "autorizada")); err != nil {
		t.Fatalf("a partição própria devia ser aceite: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, registoDeTeste("alheia", "recusada")); !errors.Is(err, ErrParticaoAlheia) {
			t.Fatalf("escrita %d em partição alheia devia ser recusada; veio %v", i, err)
		}
	}
	if _, err := s.Append(ctx, registoDeTeste("outra-alheia", "recusada")); !errors.Is(err, ErrParticaoAlheia) {
		t.Fatalf("segunda partição alheia devia ser recusada; veio %v", err)
	}

	total, porParticao := s.RecusasDePosse()
	if total != 4 {
		t.Fatalf("total de recusas = %d, quer 4", total)
	}
	if porParticao["alheia"] != 3 || porParticao["outra-alheia"] != 1 {
		t.Fatalf("repartição errada: %v", porParticao)
	}
	if _, existe := porParticao["minha"]; existe {
		t.Fatalf("a partição própria não devia aparecer nas recusas: %v", porParticao)
	}

	// O instantâneo é uma CÓPIA: mutá-lo não corrompe o estado do store.
	porParticao["alheia"] = 999
	if _, outra := s.RecusasDePosse(); outra["alheia"] != 3 {
		t.Fatalf("RecusasDePosse devolveu o mapa interno (mutável pelo chamador)")
	}

	// E a partição alheia não ficou com registo nenhum — recusada, não aplicada.
	if head, err := s.Head(ctx, "alheia"); err != nil || head != 0 {
		t.Fatalf("a partição recusada devia estar vazia; head=%d err=%v", head, err)
	}
}

// TestAOS284_AC3_NaoSABERSeDetemERecusaTambem — fail-closed sobre a incerteza.
//
// Se a porta de posse falhar (lease store inalcançável, por exemplo), a resposta não é
// «escreve na mesma»: não conseguir estabelecer a posse não autoriza a escrever. É a mesma
// disciplina da guarda 1c do nó e da recusa do backup sobre substrato replicado.
func TestAOS284_AC3_NaoSaberSeDetemRecusaTambem(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	indisponivel := errors.New("lease store inalcançável")

	s, err := OpenFileStore(caminho, ComPosseDeParticao(posseFixa{erro: indisponivel}))
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer func() { _ = s.Close() }()

	_, errA := s.Append(ctx, registoDeTeste("qualquer", "sob incerteza"))
	if !errors.Is(errA, ErrParticaoAlheia) {
		t.Fatalf("incerteza devia recusar como falta de posse; veio %v", errA)
	}
	// E a CAUSA sobrevive: quem diagnostica precisa de saber que foi o lease store que
	// caiu, e não que a réplica perdeu a posse. São remédios diferentes.
	if !errors.Is(errA, indisponivel) {
		t.Fatalf("o erro devia carregar a causa original: %v", errA)
	}
	if total, _ := s.RecusasDePosse(); total != 1 {
		t.Fatalf("a recusa por incerteza também tem de ser contada; total=%d", total)
	}
}

// TestAOS284_SemPosteArmadaNadaMuda — a garantia de não-regressão.
//
// Sem a opção, o FileStore comporta-se exactamente como antes. É o que permite acrescentar
// a disciplina sem tocar em nenhum dos chamadores actuais — e é o que este teste fixa, para
// que um default «esperto» no futuro não passe despercebido.
func TestAOS284_SemPosseArmadaNadaMuda(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")

	s, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, p := range []string{"a", "b", "c"} {
		if _, err := s.Append(ctx, registoDeTeste(p, "sem posse")); err != nil {
			t.Fatalf("sem porta armada, a escrita em %q devia passar: %v", p, err)
		}
	}
	if total, _ := s.RecusasDePosse(); total != 0 {
		t.Fatalf("sem porta armada não pode haver recusas; total=%d", total)
	}
}
