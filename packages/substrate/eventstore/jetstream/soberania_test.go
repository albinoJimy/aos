package jetstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// soberania_test.go — o AC5 do AOS-100: «as réplicas NUNCA cruzam a fronteira regional
// de soberania» (ADR-011).
//
// O cluster de medição declara `server_tags: ["region:eu-west"]` nos três nós. Sem isso
// nada disto é verificável — e é por isso que a etiqueta do servidor faz parte da
// reprodução, não do ambiente.

const regiaoDoCluster = "eu-west"

func abrirComOpcoes(t *testing.T, addr string, opts ...jetstream.Option) (*jetstream.Store, error) {
	t.Helper()
	st, err := jetstream.Abrir(addr, opts...)
	if st != nil {
		t.Cleanup(func() {
			_ = st.ApagarStream()
			_ = st.Close()
		})
	}
	return st, err
}

func opcoesBase(t *testing.T, prefixo string) []jetstream.Option {
	t.Helper()
	return []jetstream.Option{
		jetstream.ComNomeDeStream(prefixo + sufixo(t)),
		jetstream.ComPrazo(prazo),
		jetstream.ComReplicas(3),
	}
}

// TestSoberania_StreamFicaConfinadoARegiaoDoBoard — o caminho feliz, e o que ele prova:
// a fronteira não é uma anotação nossa, é uma restrição ARMAZENADA no servidor, que é
// quem coloca as réplicas.
func TestSoberania_StreamFicaConfinadoARegiaoDoBoard(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "SOBOK_"),
		jetstream.ComBoardDeSoberania("board-ue", regiaoDoCluster))...)
	if err != nil {
		t.Fatalf("abrir com fronteira %q: %v", regiaoDoCluster, err)
	}
	if st.Regiao() != regiaoDoCluster {
		t.Fatalf("Regiao() = %q, quer %q", st.Regiao(), regiaoDoCluster)
	}
	if st.BoardDeSoberania() != "board-ue" {
		t.Fatalf("BoardDeSoberania() = %q, quer board-ue", st.BoardDeSoberania())
	}
	// E o store funciona — uma fronteira que impedisse escrever não seria soberania,
	// seria indisponibilidade.
	if _, err := st.Append(context.Background(), "run-soberano",
		eventstore.EventInput{Type: "soberania.ok", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append num store com fronteira: %v", err)
	}
}

// TestSoberania_RegiaoQueOClusterNaoServeERecusada — pedir uma fronteira que nenhum par
// do cluster satisfaz tem de ABORTAR. É o fail-closed do SERVIDOR, e é o mais forte que
// existe: não depende de nós verificarmos coisa nenhuma.
func TestSoberania_RegiaoQueOClusterNaoServeERecusada(t *testing.T) {
	addr := servidor(t)
	_, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "SOBFORA_"),
		jetstream.ComRegiao("us-east-que-ninguem-serve"))...)
	if err == nil {
		t.Fatal("um stream foi criado numa região que nenhum servidor do cluster anuncia — " +
			"a fronteira de soberania não está a ser imposta")
	}
	if !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("erro = %v; quer E_SOVEREIGNTY_VIOLATION — o chamador tem de distinguir "+
			"«não posso colocar dentro da fronteira» de um erro qualquer de criação", err)
	}
	t.Logf("recusa: %v", err)
}

// TestSoberania_LigarAStreamSemColocacaoERecusado é o teste que importa mais, e mede a
// falha SILENCIOSA.
//
// Um stream criado SEM fronteira não tem colocação: as réplicas podem estar em qualquer
// par. Se um nó com fronteira declarada se ligasse a ele sem verificar, julgar-se-ia
// soberano — e tudo funcionaria. As escritas passariam, as leituras passariam, e os
// dados estariam onde não podiam estar.
//
// É por isto que a fronteira é verificada contra a configuração ARMAZENADA, e não contra
// a que pedimos.
func TestSoberania_LigarAStreamSemColocacaoERecusado(t *testing.T) {
	addr := servidor(t)
	nome := "SOBSEM_" + sufixo(t)

	// (1) alguém cria o stream SEM fronteira.
	semFronteira, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome), jetstream.ComPrazo(prazo), jetstream.ComReplicas(3))
	if err != nil {
		t.Fatalf("criar stream sem fronteira: %v", err)
	}
	t.Cleanup(func() {
		_ = semFronteira.ApagarStream()
		_ = semFronteira.Close()
	})

	// (2) um nó COM fronteira liga-se ao mesmo stream. Tem de ser RECUSADO.
	comFronteira, err := jetstream.Abrir(addr,
		jetstream.ComNomeDeStream(nome), jetstream.ComPrazo(prazo),
		jetstream.SemCriarStream(), jetstream.ComRegiao(regiaoDoCluster))
	if comFronteira != nil {
		_ = comFronteira.Close()
	}
	if err == nil {
		t.Fatal("um nó com fronteira declarada ligou-se a um stream SEM colocação e não deu por isso — " +
			"julgar-se soberano sobre dados que podem estar em qualquer par é a falha mais silenciosa possível")
	}
	if !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("erro = %v; quer E_SOVEREIGNTY_VIOLATION", err)
	}
	t.Logf("recusa: %v", err)
}

// TestSoberania_FronteiraSemRegiaoERecusadaAntesDeLigar — região ausente é região
// DESCONHECIDA, e desconhecida NEGA (a mesma postura do PDP em AOS-094). A recusa é
// anterior a haver socket: uma config auto-contraditória não merece uma ligação aberta
// para descobrir isso.
func TestSoberania_FronteiraSemRegiaoERecusadaAntesDeLigar(t *testing.T) {
	// Endereço deliberadamente inalcançável: se a recusa dependesse de ligar, este
	// teste falharia com um erro de rede em vez do sentinela.
	_, err := jetstream.Abrir("127.0.0.1:1", jetstream.ComRegiao("   "))
	if !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("erro = %v; quer E_SOVEREIGNTY_VIOLATION antes de qualquer ligação", err)
	}
}

// TestSoberania_RegiaoENormalizadaComoNoStoreDeReferencia — duas normalizações
// diferentes fariam "EU-West" e "eu-west" ser a mesma região num substrato e regiões
// diferentes no outro. A fronteira não pode depender do substrato.
func TestSoberania_RegiaoENormalizadaComoNoStoreDeReferencia(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "SOBNORM_"),
		jetstream.ComRegiao("  EU-West  "))...)
	if err != nil {
		t.Fatalf("abrir com região não-normalizada: %v", err)
	}
	if st.Regiao() != regiaoDoCluster {
		t.Fatalf("Regiao() = %q, quer %q — a normalização diverge do store de referência", st.Regiao(), regiaoDoCluster)
	}
}

// TestSoberania_SemFronteiraFicaDormente — sem fronteira declarada o comportamento é o
// de antes. Retro-compatível, como no store de referência: quem não declara soberania
// não passa a precisar dela.
func TestSoberania_SemFronteiraFicaDormente(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "SOBDORM_")...)
	if err != nil {
		t.Fatalf("abrir sem fronteira: %v", err)
	}
	if st.Regiao() != "" || st.BoardDeSoberania() != "" {
		t.Fatalf("sem fronteira declarada, Regiao()=%q BoardDeSoberania()=%q — deviam estar vazios",
			st.Regiao(), st.BoardDeSoberania())
	}
	if _, err := st.Append(context.Background(), "run-sem-fronteira",
		eventstore.EventInput{Type: "soberania.dormente", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append sem fronteira: %v", err)
	}
}

// TestSoberania_ReplicasNaoCaemForaDaRegiao é a forma FORTE do AC5, e a que faltava.
//
// Os testes acima provam que a restrição é PEDIDA, ARMAZENADA e VERIFICADA, e que a sua
// ausência aborta. Nenhum deles prova que as réplicas ficam DE FACTO dentro da fronteira —
// e essa é a promessa do ADR-011: «as réplicas e os backups NUNCA cruzam a fronteira».
//
// Aqui o cluster tem nós em DUAS regiões. Cria-se um stream confinado a uma delas e
// pergunta-se ao servidor ONDE ele ficou. Se algum par da outra região aparecer, a
// fronteira é decorativa.
//
//	AOS_NODES_OUTRA_REGIAO=aos-medicao-es-3
func TestSoberania_ReplicasNaoCaemForaDaRegiao(t *testing.T) {
	addr := servidor(t)
	fora := os.Getenv("AOS_NODES_OUTRA_REGIAO")
	if fora == "" {
		t.Skip("define AOS_NODES_OUTRA_REGIAO (nós do cluster que estão FORA da região do board) — " +
			"sem um cluster multi-região, esta propriedade não é observável")
	}
	proibidos := map[string]bool{}
	for _, n := range strings.Split(fora, ",") {
		if n = strings.TrimSpace(n); n != "" {
			proibidos[n] = true
		}
	}

	st, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "SOBMULTI_"),
		jetstream.ComBoardDeSoberania("board-ue", regiaoDoCluster))...)
	if err != nil {
		t.Fatalf("abrir com fronteira %q: %v", regiaoDoCluster, err)
	}

	col, err := st.ColocacaoEfectiva()
	if err != nil {
		t.Fatalf("ler a colocação efectiva: %v", err)
	}
	todos := col.Todos()
	if len(todos) == 0 {
		t.Fatal("o servidor não reporta pares para o stream — sem isso não se pode afirmar onde ele está")
	}
	for _, par := range todos {
		if proibidos[par] {
			t.Fatalf("o stream está alojado em %q, que está FORA da fronteira do board — "+
				"a colocação é decorativa. Pares: %v (líder=%q)", par, todos, col.Lider)
		}
	}
	t.Logf("fronteira %q CUMPRIDA: o stream está em %v (líder=%q), e nenhum dos %d pares proibidos aparece",
		regiaoDoCluster, todos, col.Lider, len(proibidos))
}
