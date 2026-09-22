package main

// aos424_migracao_cablagem_test.go — A MIGRAÇÃO DE APROVAÇÕES CORRE, E CORRE ANTES.
//
// # PORQUE É QUE ISTO PRECISA DE UM TESTE PRÓPRIO
//
// «Migrar ANTES de compor» é o argumento de correcção inteiro da migração do stream de
// aprovações: um `used-` por copiar é um grant que se pode consumir duas vezes. E não tinha um
// único sensor — uma revisão adversarial substituiu a chamada por `copiados, merr := 0, nil`
// (migração efectivamente removida) e a suite completa deste pacote ficou VERDE.
//
// É o mesmo modo de falha que já apareceu no AOS-418 (a cablagem removida não avermelhava
// nada) e no AOS-426 (o sensor a medir nomes mortos): o comportamento tem teste, a CABLAGEM
// não. A diferença aqui é o que está em jogo — a propriedade que uma cerimónia humana de
// quatro olhos existe para dar.
//
// # PORQUE É QUE O TESTE LÊ O FICHEIRO
//
// O `Bootstrap` compõe a migração dentro de `if foureyes != nil`, e montar um nó de teste com
// four-eyes composto E um stream legado povoado exigiria um aparato que mede outra coisa (a
// migração em si já tem sete testes no pacote `integration`, incluindo o caminho real de
// `Put`/`Consume`). O que falta é a garantia ESTRUTURAL, e essa lê-se na fonte — a mesma
// técnica do `aos255_budget_scope_test.go`, do guard do AOS-417 e do teste do `ValidNodeID`.

import (
	"os"
	"strings"
	"testing"
)

// A CHAMADA EXISTE E PRECEDE OS TRÊS CONSUMIDORES DO STREAM.
//
// Os três são compostos no mesmo ramo: a store de grants, o registo de pendentes e os registos
// de retoma — este último escreve no MESMO stream. Se algum deles passar a ser composto antes
// da migração, a cerimónia compõe-se sobre uma migração que não correu.
func TestAOS424MigracaoDeAprovacoesCorreAntesDeCompor(t *testing.T) {
	bruto, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(bruto)

	iMigracao := strings.Index(src, "integration.MigrarAprovacoes(")
	if iMigracao < 0 {
		t.Fatal("o composition-root deixou de chamar `integration.MigrarAprovacoes`:\n" +
			"a cerimonia four-eyes passa a compor-se sobre um stream que pode ter factos por\n" +
			"migrar. Um `used-` por copiar e um grant consumivel DUAS vezes — e o uso-unico e a\n" +
			"propriedade que a cerimonia existe para dar. Eixo: AOS-424.")
	}

	// Os consumidores do stream de aprovações, por nome do construtor.
	consumidores := []struct{ nome, construtor string }{
		{"store de grants", "integration.NewEventStoreApprovalStore("},
		{"registo de pendentes", "integration.NewPendingApprovals("},
		{"registos de retoma", "integration.NewResumeRecords("},
	}
	for _, c := range consumidores {
		i := strings.Index(src, c.construtor)
		if i < 0 {
			t.Errorf("nao encontrei %s (`%s`) no composition-root:\n"+
				"ou deixou de ser composto, ou mudou de nome — este teste deixa de saber se a\n"+
				"migracao o precede. Actualize-o; NAO o relaxe.", c.nome, c.construtor)
			continue
		}
		if i < iMigracao {
			t.Errorf("%s (`%s`) e composto ANTES da migracao de aprovacoes:\n"+
				"le o stream antes de os factos legados la estarem. A ordem nao e estetica — e o\n"+
				"argumento de correccao da migracao. Eixo: AOS-424.", c.nome, c.construtor)
		}
	}
}

// A MIGRAÇÃO É FAIL-CLOSED NO ARRANQUE.
//
// Compor a cerimónia sobre uma migração PARCIAL seria servir governação com uma garantia que
// já não vale. O `Bootstrap` tem de abortar, e não seguir com um aviso.
func TestAOS424MigracaoDeAprovacoesEFailClosed(t *testing.T) {
	bruto, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(bruto)

	i := strings.Index(src, "integration.MigrarAprovacoes(")
	if i < 0 {
		t.Skip("a chamada nao existe — o teste acima ja o reporta")
	}
	// A janela a seguir à chamada tem de conter um `return nil, ...` sobre o erro dela.
	janela := src[i:min(i+400, len(src))]
	if !strings.Contains(janela, "return nil, fmt.Errorf") {
		t.Error("a migracao de aprovacoes deixou de ser FAIL-CLOSED no composition-root:\n" +
			"um erro tem de ABORTAR o arranque. Seguir com um aviso compoe a cerimonia sobre uma\n" +
			"migracao parcial, e um `used-` por copiar e um grant consumivel duas vezes.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
