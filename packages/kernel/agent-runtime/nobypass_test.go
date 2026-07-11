package agentruntime

import (
	"reflect"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/archlint"
)

// TestNoToolFuncField prova ESTRUTURALMENTE (por reflexão) que o Runtime NÃO
// detém qualquer campo executável de tool (referencemonitor.ToolFunc): o único
// caminho de execução é rm.Mediate. Um campo ToolFunc seria um caminho directo —
// a sua ausência é a prova de que a API do RT não expõe bypass.
func TestNoToolFuncField(t *testing.T) {
	toolFuncType := reflect.TypeOf(referencemonitor.ToolFunc(nil))
	monitorPtr := reflect.TypeOf((*referencemonitor.Monitor)(nil))

	rtType := reflect.TypeOf(Runtime{})
	foundMonitor := false
	for i := 0; i < rtType.NumField(); i++ {
		f := rtType.Field(i)
		if f.Type == toolFuncType {
			t.Fatalf("Runtime.%s é uma ToolFunc — caminho directo de execução (bypass)", f.Name)
		}
		// Um campo função com a assinatura de ToolFunc também seria suspeito.
		if f.Type.Kind() == reflect.Func && f.Type.ConvertibleTo(toolFuncType) && f.Type != toolFuncType {
			t.Fatalf("Runtime.%s tem assinatura convertível para ToolFunc", f.Name)
		}
		if f.Type == monitorPtr {
			foundMonitor = true
		}
	}
	if !foundMonitor {
		t.Fatalf("Runtime não detém um *referencemonitor.Monitor — despacho não passa pelo RM")
	}
}

// TestNoExportedToolFuncSurface confirma que nenhum método exportado do Runtime
// aceita ou devolve uma ToolFunc (não há forma de injectar/extrair um executor
// de tool pela API pública).
func TestNoExportedToolFuncSurface(t *testing.T) {
	toolFuncType := reflect.TypeOf(referencemonitor.ToolFunc(nil))
	rtPtr := reflect.TypeOf(&Runtime{})
	for i := 0; i < rtPtr.NumMethod(); i++ {
		m := rtPtr.Method(i)
		ft := m.Func.Type()
		for a := 0; a < ft.NumIn(); a++ {
			if ft.In(a) == toolFuncType {
				t.Fatalf("método exportado %s aceita ToolFunc", m.Name)
			}
		}
		for r := 0; r < ft.NumOut(); r++ {
			if ft.Out(r) == toolFuncType {
				t.Fatalf("método exportado %s devolve ToolFunc", m.Name)
			}
		}
	}
}

// TestArchlintNoBypass reutiliza o analisador de arquitectura do RM (AOS-003)
// sobre o código-fonte do Agent Runtime: falha se houver invocação directa de
// ToolFunc ou chamada a um dispatcher reservado fora do RM. É a segunda camada
// (heurística sintáctica) sobre a garantia estrutural do teste acima.
func TestArchlintNoBypass(t *testing.T) {
	violations, err := archlint.AnalyzeDir(".")
	if err != nil {
		t.Fatalf("AnalyzeDir: %v", err)
	}
	if len(violations) != 0 {
		for _, v := range violations {
			t.Errorf("violação no-bypass: %s", v)
		}
		t.FailNow()
	}
}
