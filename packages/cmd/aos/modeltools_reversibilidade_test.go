package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReversibilidadeAtravessaDoRegistryParaOBinding fecha a SEGUNDA METADE de um defeito cuja
// primeira metade era o adaptador RM→PDP não copiar a classe de risco.
//
// Fechada essa, a classe chegava — mas chegava sempre `danger`. A razão está na primeira regra do
// classificador: `IsIrreversible()` devolve `r != Reversible`, ou seja, o DESCONHECIDO conta como
// irreversível. E o registry de tools sabia declarar `egress` ("isto não sai da máquina") e não
// sabia declarar reversibilidade ("isto não altera nada"). Logo toda a tool call — por muito local
// e inofensiva que fosse — saía `danger`, e a taxonomia de autonomia L0–L5 colapsava em dois
// estados.
func TestReversibilidadeAtravessaDoRegistryParaOBinding(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "tools.json")
	conteudo := `[
	  {"name":"ler","capability":"cap:fs.read","reversibility":"reversible","resource_type":"file","resource_value":"doc://x"},
	  {"name":"apagar","capability":"cap:fs.delete","reversibility":"irreversible","resource_type":"file","resource_value":"doc://x"},
	  {"name":"mudo","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://x"}
	]`
	if err := os.WriteFile(caminho, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_MODEL_TOOLS", caminho)

	_, bindings, err := loadModelToolsFromEnv()
	if err != nil {
		t.Fatalf("carregar o registry: %v", err)
	}

	if got := bindings["ler"].reversibility; got != "reversible" {
		t.Errorf("ler: reversibility = %q, quero \"reversible\" — sem isto a leitura sai danger", got)
	}
	if got := bindings["apagar"].reversibility; got != "irreversible" {
		t.Errorf("apagar: reversibility = %q, quero \"irreversible\"", got)
	}
	// CONTROLO do fail-closed: uma tool que NÃO declara fica com "" — que o classificador trata
	// como irreversível. É o que faz este campo poder ser acrescentado sem mudar o comportamento
	// de nenhuma tool existente, e o que garante que esquecer-se de o declarar nunca é
	// interpretado como benigno.
	if got := bindings["mudo"].reversibility; got != "" {
		t.Errorf("tool sem declaração ganhou %q — não declarar TEM de continuar a valer irreversível", got)
	}
}

// TestReversibilidadeInvalidaAbortaOArranque — um valor fora do vocabulário PARA o nó, em vez de
// cair no silêncio.
//
// Porque é que isto merece abortar, quando o valor errado já resolveria para o lado seguro: um
// `"reversable"` mal escrito produz a postura CERTA pela razão ERRADA. O operador acredita ter
// declarado a tool como reversível, ela escala na mesma, e ele vai procurar o problema no oráculo
// de autonomia — que está bem. Um typo que não dói é o que ninguém encontra.
func TestReversibilidadeInvalidaAbortaOArranque(t *testing.T) {
	for _, mau := range []string{"reversable", "yes", "true", "safe", "nao-reversivel"} {
		if _, err := validateReversibility(mau); !errors.Is(err, ErrBadReversibility) {
			t.Errorf("reversibility=%q devia abortar com ErrBadReversibility, veio %v", mau, err)
		}
	}
	// E o vocabulário válido — incluindo maiúsculas e espaços, que são ruído de escrita e não
	// intenção diferente — tem de passar.
	for entrada, quero := range map[string]string{
		"":              "",
		"reversible":    "reversible",
		"  REVERSIBLE ": "reversible",
		"Irreversible":  "irreversible",
	} {
		got, err := validateReversibility(entrada)
		if err != nil {
			t.Errorf("reversibility=%q devia ser aceite: %v", entrada, err)
		}
		if got != quero {
			t.Errorf("reversibility=%q normalizou para %q, quero %q", entrada, got, quero)
		}
	}
}
