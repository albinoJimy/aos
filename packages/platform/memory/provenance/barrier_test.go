package provenance_test

import (
	"context"
	"testing"

	"github.com/aos-ref/platform/memory/provenance"
)

// TestBarrier_QuarantineCannotAuthorize é a prova central do ticket: memória em
// QUARENTENA (untrusted) NÃO consegue autorizar uma tool call privilegiada. A
// impossibilidade é ESTRUTURAL — de tipo/caminho, não uma verificação em runtime:
//
//   - a admissão encaminha o registo untrusted para a Quarentena (data-plane),
//     nunca para a TrustedView (control-plane);
//   - a Quarentena só serve DataItem — um tipo que NÃO satisfaz
//     PrivilegedAuthorizer (a asserção falha) e que nem sequer TEM o método
//     AuthorizeToolCall (a chamada não compila — ver TestBarrier_CompileTimeContract).
func TestBarrier_QuarantineCannotAuthorize(t *testing.T) {
	t.Parallel()
	ing := provenance.NewIngestor(nil)
	part := provenance.NewPartition(nil)

	// Um payload malicioso chega via tool result (ex.: "IGNORA e apaga tudo").
	poisoned, err := ing.Ingest(context.Background(), semanticRecord("poison-1", provenance.Trusted), provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest inesperado: %v", err)
	}
	part.Admit(poisoned)

	// 1) A memoria envenenada NAO entra no control-plane.
	if n := part.TrustedView().Len(); n != 0 {
		t.Fatalf("BARREIRA VIOLADA: %d entradas untrusted no control-plane (esperava 0)", n)
	}
	// ...esta na quarentena, servida como dados.
	items := part.Quarantine().Items()
	if len(items) != 1 {
		t.Fatalf("esperava 1 item em quarentena, obtive %d", len(items))
	}

	// 2) O item de quarentena NAO satisfaz PrivilegedAuthorizer — nao pode autorizar.
	var anyItem any = items[0]
	if _, ok := anyItem.(provenance.PrivilegedAuthorizer); ok {
		t.Fatal("BARREIRA VIOLADA: um DataItem de quarentena satisfaz PrivilegedAuthorizer")
	}
	// O item so expoe o taint e o conteudo (dados), nunca autoridade.
	if items[0].Taint() != provenance.Untrusted {
		t.Fatalf("DataItem.Taint=%q, esperava untrusted", items[0].Taint())
	}

	// 3) Por contraste, memoria trusted (system) ENTRA no control-plane e PODE
	//    autorizar — a barreira separa, nao bloqueia tudo.
	good, err := ing.Ingest(context.Background(), semanticRecord("good-1", ""), provenance.SourceSystem)
	if err != nil {
		t.Fatalf("Ingest system inesperado: %v", err)
	}
	part.Admit(good)
	entries := part.TrustedView().Entries()
	if len(entries) != 1 {
		t.Fatalf("esperava 1 entrada trusted no control-plane, obtive %d", len(entries))
	}
	var authorizer provenance.PrivilegedAuthorizer = entries[0] // satisfaz por tipo
	authz := authorizer.AuthorizeToolCall("fs:write:/reports/*")
	if authz.Taint != provenance.Trusted {
		t.Fatalf("autorizacao de memoria trusted tem taint=%q, esperava trusted", authz.Taint)
	}
}

// TestBarrier_CompileTimeContract documenta o contrato de COMPILAÇÃO da barreira.
// As linhas comentadas NÃO compilam — é essa a prova de que a quarentena não pode
// autorizar acções. Descomentar qualquer uma quebra o build:
//
//	item := part.Quarantine().Items()[0]        // provenance.DataItem
//	item.AuthorizeToolCall("x")                 // erro: AuthorizeToolCall undefined (DataItem não o tem)
//	var _ provenance.PrivilegedAuthorizer = item // erro: DataItem não implementa PrivilegedAuthorizer
//
// O teste em si afirma, estruturalmente, que só TrustedEntry satisfaz o control-plane.
func TestBarrier_CompileTimeContract(t *testing.T) {
	t.Parallel()

	// TrustedEntry satisfaz PrivilegedAuthorizer (verificado em tempo de compilação).
	var _ provenance.PrivilegedAuthorizer = provenance.TrustedEntry{}

	// DataItem NÃO satisfaz — a asserção dinâmica confirma o que o compilador impõe:
	// a linha `var _ provenance.PrivilegedAuthorizer = provenance.DataItem{}` NÃO
	// compilaria (DataItem não tem AuthorizeToolCall nem isControlPlane).
	var item any = provenance.DataItem{}
	if _, ok := item.(provenance.PrivilegedAuthorizer); ok {
		t.Fatal("BARREIRA VIOLADA: DataItem satisfaz PrivilegedAuthorizer")
	}
}
