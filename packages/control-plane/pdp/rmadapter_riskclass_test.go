package pdp

import (
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestAdaptadorTransportaAClasseDeRisco fecha um defeito que não produzia sintoma nenhum: o
// adaptador RM→PDP mapeava Taint, Reversibility e Sensitivity, e DEIXAVA CAIR a RiskClass.
//
// Porque é que ninguém dava por isso: `riskClassFromString("")` resolve para ClassDanger
// (fail-closed). Ou seja, a classe em falta não dava erro nem deny visível — dava OVERSIGHT A
// MAIS. E oversight a mais parece prudência, não parece defeito.
//
// O que custava, e é grave: com toda a acção tratada como `danger`, a taxonomia L0–L5 COLAPSA em
// dois estados. A L3 ("safe corre, gray agrupa, danger confirma") comporta-se exactamente como a
// L1. A L4 ("corre por omissão, só escala em danger") escala tudo. Só a L5 deixa alguma coisa
// correr. Um mecanismo graduado, documentado como graduado, com dois estados na prática.
func TestAdaptadorTransportaAClasseDeRisco(t *testing.T) {
	for _, classe := range []string{"safe", "gray", "danger"} {
		call := &rm.Call{Capability: "cap:fs.read"}
		call.Context.RiskClass = classe

		in := inputFromCall(call)
		if in.Context.RiskClass != classe {
			t.Errorf("classe %q não atravessou o adaptador: in.Context.RiskClass = %q",
				classe, in.Context.RiskClass)
		}
	}
}

// TestClasseAusenteContinuaADarDanger é o CONTROLO do fail-closed, e prova que a correcção não o
// enfraqueceu: uma acção que chega SEM classe continua a ser tratada como o pior caso.
//
// Sem este caso, alguém poderia "corrigir" o defeito acima fazendo o vazio resolver para `safe` —
// o que faria os números baterem e abriria exactamente o buraco que o fail-closed fecha.
func TestClasseAusenteContinuaADarDanger(t *testing.T) {
	if got := riskClassFromString(""); got != risk.ClassDanger {
		t.Fatalf("classe vazia = %v, quero ClassDanger (fail-closed)", got)
	}
	if got := riskClassFromString("desconhecida"); got != risk.ClassDanger {
		t.Fatalf("classe desconhecida = %v, quero ClassDanger (fail-closed)", got)
	}

	call := &rm.Call{Capability: "cap:fs.read"} // sem RiskClass
	if in := inputFromCall(call); in.Context.RiskClass != "" {
		t.Fatalf("uma call sem classe não deve ganhar uma: %q", in.Context.RiskClass)
	}
}
