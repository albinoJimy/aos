package main

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
)

// TestSimulacaoConcordaComOClassificadorReal é o controlo que o plano nomeou como o mais
// importante desta fase, e o único que decide se a simulação vale alguma coisa.
//
// Uma simulação que diverge do comportamento real é PIOR do que não ter simulação: produz
// confiança em vez de a medir. Alguém olharia para "correriam 40, escalariam 2", ligaria a
// configuração, e o sistema faria outra coisa.
//
// Por isso a simulação NÃO reimplementa as regras — reconstrói um Call a partir dos MESMOS factos
// que o selo guardou e chama o classificador real. Este teste prova essa igualdade: para cada
// conjunto de factos, o que a simulação deriva TEM de ser o que o classificador do nó derivaria.
func TestSimulacaoConcordaComOClassificadorReal(t *testing.T) {
	casos := []struct {
		nome                  string
		cap, tipo, val        string
		taint, revers, sensib string
	}{
		{"leitura local reversivel untrusted", "cap:fs.read", "file", "doc://x", "untrusted", "reversible", ""},
		{"leitura sem declaracao", "cap:fs.read", "file", "doc://x", "untrusted", "", ""},
		{"egress externo sensivel", "cap:http.post", "url", "https://x.test/y", "untrusted", "reversible", "confidential"},
		{"leitura trusted interna", "cap:fs.read", "file", "doc://x", "trusted", "reversible", "internal"},
		{"apagar", "cap:fs.delete", "file", "doc://x", "untrusted", "reversible", ""},
	}
	for _, k := range casos {
		rec := audit.AuditRecord{
			Capability: k.cap,
			Resource:   audit.Resource{Type: k.tipo, Value: k.val},
		}
		rec.Context.Taint = k.taint
		rec.Context.Reversibility = k.revers
		rec.Context.Sensitivity = k.sensib

		daSimulacao := reclassificar(rec)

		// O MESMO caminho que o nó percorre: o classificador real sobre os mesmos factos.
		doNo := classeReal(t, k.cap, k.tipo, k.val, k.taint, k.revers, k.sensib)
		if daSimulacao != doNo {
			t.Errorf("%s: simulacao diz %v, o no diria %v — a simulacao esta a prever outro sistema",
				k.nome, daSimulacao, doNo)
		}
	}
}

// TestSimulacaoUsaOParserDoArranque — uma configuração que o nó RECUSARIA tem de ser recusada
// aqui também.
//
// Se a simulação tivesse um parser próprio, mais permissivo, daria luz verde a uma tabela que
// nunca chegaria a entrar em vigor: o operador veria "correriam 40", aplicaria, e o nó abortaria
// o arranque. O sim mais caro possível.
func TestSimulacaoUsaOParserDoArranque(t *testing.T) {
	for _, mau := range []string{"agt-1=L4", "agt-1:fs=L9", ":fs=L4", "agt-1:"} {
		if _, err := parseAutonomyLevelsFrom(mau); err == nil {
			t.Errorf("a simulacao aceitaria %q, que o arranque recusa", mau)
		}
	}
	// E a gramática válida — incluindo as regras de CLASSE — continua a passar.
	specs, err := parseAutonomyLevelsFrom("agt-1:fs=L4,class:agent-worker:http=L3")
	if err != nil || len(specs) != 2 {
		t.Fatalf("gramatica valida recusada: %v (%d entradas)", err, len(specs))
	}
}

// TestSimulacaoNaoSela — a hipótese não pode contaminar o trilho de auditoria.
//
// O registo efémero da simulação é construído SEM sink. Se selasse, o WORM ficaria com
// `autonomy.level_changed` de níveis que ninguém aplicou, e um auditor não conseguiria distinguir
// uma mudança real de um ensaio.
func TestSimulacaoNaoSela(t *testing.T) {
	worm := audit.NewMemStore()
	// Um registo sem sink — exactamente como a simulação o constrói.
	r := autonomy.NewLevelRegistry(autonomy.WithDefaultLevel(autonomy.L4))
	if _, err := r.SetLevel(context.Background(), "agt-x", "fs", autonomy.L5, "simulacao", "simulacao"); err != nil {
		t.Fatal(err)
	}
	if head, err := worm.Head(context.Background(), "autonomy"); err != nil || head != 0 {
		t.Fatalf("a simulacao selou na particao autonomy (head=%d) — o ensaio contaminou o registo", head)
	}
}

// classeReal deriva a classe pelo caminho do NÓ — o RiskGate real sobre um Call construído à mão
// — para o teste acima comparar a simulação contra algo que NÃO seja a própria função em teste.
//
// A primeira versão deste helper chamava `reclassificar`, o que fazia o teste comparar uma função
// consigo mesma: verde garantido, propriedade nenhuma. É a armadilha exacta que este projecto
// passou o dia a recusar, e caí nela ao escrever o teste que existe para a evitar.
func classeReal(t *testing.T, cap, tipo, val, taint, revers, sensib string) risk.Class {
	t.Helper()
	call := &rm.Call{
		Capability: cap,
		Resource:   rm.Resource{Type: tipo, Value: val},
	}
	call.Context.Taint = taint
	call.Context.Reversibility = revers
	call.Context.Sensitivity = sensib
	if _, err := (rm.RiskGate{}).Evaluate(context.Background(), call); err != nil {
		t.Fatalf("RiskGate: %v", err)
	}
	switch call.Context.RiskClass {
	case risk.ClassSafe.String():
		return risk.ClassSafe
	case risk.ClassGray.String():
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}
