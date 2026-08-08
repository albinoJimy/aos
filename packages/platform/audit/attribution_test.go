package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// AOS-011 — ATRIBUIÇÃO da decisão no selo, sem partir o que já foi selado
// ---------------------------------------------------------------------------
//
// O log inviolável provava QUE uma acção foi recusada, não POR QUEM nem PORQUÊ: o
// MediationRecord do Reference Monitor traz Code/DeniedBy/Reason e o adaptador deitava-os
// fora. Responder a "que gate negou isto?" obrigava a bissectar o sistema com experiências
// de controlo — o oposto do que um audit existe para fazer.
//
// A dificuldade não é acrescentar campos: é acrescentá-los a um WORM. Os selos antigos não
// se re-escrevem, e o arranque do nó RE-VERIFICA a hash-chain fail-closed — uma mudança
// global no conteúdo canónico invalidaria toda a cadeia já escrita e o nó deixaria de
// arrancar sobre o seu próprio log. Daí a versão POR-REGISTO.

// TestAtribuicao_SeloTrazQuemNegouEPorque: o caminho novo sela a atribuição e ela sobrevive
// à leitura.
func TestAtribuicao_SeloTrazQuemNegouEPorque(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	sealed, err := s.Append(ctx, AuditRecord{
		Partition: "run-x", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionDeny,
		Capability: "cap:fs.read", ToolID: "doc_read",
		Code: "E_SCOPE_DENIED", DeniedBy: "scope", Reason: "capability fora do tecto user∩classe",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if sealed.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("um registo novo tem de ser selado na versao corrente; veio %d", sealed.SchemaVersion)
	}
	recs, err := s.Read(ctx, "run-x", 1, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := recs[0]
	if got.Code != "E_SCOPE_DENIED" || got.DeniedBy != "scope" || got.Reason == "" {
		t.Fatalf("a atribuicao tem de sobreviver ao selo e a leitura: %+v", got)
	}
	if err := Verify(ctx, s, "run-x", 1, 1); err != nil {
		t.Fatalf("a cadeia devia estar integra: %v", err)
	}
}

// TestAtribuicao_EstaCobertaPeloHash: os campos novos entram no conteúdo selado. Se não
// entrassem, seriam editáveis sem deixar rasto — uma atribuição não-verificável é pior do
// que nenhuma, porque parece prova.
func TestAtribuicao_EstaCobertaPeloHash(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	base := AuditRecord{
		Partition: "run-y", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionDeny,
		Code: "E_TAINT_DENIED", DeniedBy: "taint", Reason: "untrusted nao comanda",
	}
	if _, err := s.Append(ctx, base); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs, _ := s.Read(ctx, "run-y", 1, 1)
	sealed := recs[0]

	for _, tc := range []struct {
		nome  string
		mutar func(*AuditRecord)
	}{
		{"code", func(r *AuditRecord) { r.Code = "E_OUTRA_COISA" }},
		{"denied_by", func(r *AuditRecord) { r.DeniedBy = "policy" }},
		{"reason", func(r *AuditRecord) { r.Reason = "razao inventada" }},
	} {
		t.Run(tc.nome, func(t *testing.T) {
			mutado := sealed
			tc.mutar(&mutado)
			if string(ComputeEntryHash(mutado.PrevHash, mutado)) == string(sealed.EntryHash) {
				t.Fatalf("mutar %q tinha de mudar o entry_hash — o campo nao esta selado", tc.nome)
			}
		})
	}
}

// TestAtribuicao_RegistoV2ContinuaAVerificar é a propriedade CRÍTICA de compatibilidade:
// um registo selado ANTES desta capacidade tem de continuar a verificar byte-a-byte. Se
// falhasse, o arranque do nó abortaria fail-closed sobre a sua própria cadeia — um upgrade
// de binário partiria todos os deployments com WORM em disco.
func TestAtribuicao_RegistoV2ContinuaAVerificar(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()

	// Registo escrito "à moda antiga": versão fixada em v2 pelo produtor, sem atribuição.
	if _, err := s.Append(ctx, AuditRecord{
		SchemaVersion: SchemaV2,
		Partition:     "run-legado", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
		Capability: "cap:fs.read", ToolID: "doc_read",
	}); err != nil {
		t.Fatalf("Append v2: %v", err)
	}
	// E a cadeia continua noutro registo, agora na versão corrente.
	if _, err := s.Append(ctx, AuditRecord{
		Partition: "run-legado", Timestamp: time.Unix(2, 0).UTC(), Decision: DecisionDeny,
		Code: "E_EGRESS_DENIED", DeniedBy: "egress", Reason: "destino fora da allowlist",
	}); err != nil {
		t.Fatalf("Append v3: %v", err)
	}

	if err := Verify(ctx, s, "run-legado", 1, 2); err != nil {
		t.Fatalf("uma cadeia com registos de DUAS epocas tem de verificar: %v", err)
	}
	recs, _ := s.Read(ctx, "run-legado", 1, 2)
	if recs[0].SchemaVersion != SchemaV2 || recs[1].SchemaVersion != SchemaV3 {
		t.Fatalf("cada registo guarda a SUA versao: %d e %d", recs[0].SchemaVersion, recs[1].SchemaVersion)
	}
}

// TestAtribuicao_V2NaoSelaOsCamposNovos sela a razão de a versão existir: no formato antigo
// os campos novos NÃO fazem parte do conteúdo canónico. Se fizessem, os bytes de um registo
// v2 mudariam e o selo antigo deixaria de bater certo.
func TestAtribuicao_V2NaoSelaOsCamposNovos(t *testing.T) {
	semAtribuicao := AuditRecord{
		SchemaVersion: SchemaV2, AuditSeq: 1, Partition: "p",
		Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionDeny,
	}
	comAtribuicao := semAtribuicao
	comAtribuicao.Code = "E_QUALQUER"
	comAtribuicao.DeniedBy = "gate"
	comAtribuicao.Reason = "razao"

	if string(canonicalContent(semAtribuicao)) != string(canonicalContent(comAtribuicao)) {
		t.Fatal("em v2 os campos de atribuicao NAO podem entrar no conteudo canonico")
	}
	// E em v3 entram — senão a atribuição não estaria selada de todo.
	a := semAtribuicao
	a.SchemaVersion = SchemaV3
	b := comAtribuicao
	b.SchemaVersion = SchemaV3
	if string(canonicalContent(a)) == string(canonicalContent(b)) {
		t.Fatal("em v3 os campos de atribuicao TEM de entrar no conteudo canonico")
	}
}

// TestAtribuicao_VersaoDesconhecidaEhFailClosedComTipoProprio: um registo de uma época que
// este binário não conhece não é "verificado por omissão" — é recusado, e com um tipo que
// aponta para a causa provável (binário mais antigo do que o log), não para um adulterador
// inexistente.
func TestAtribuicao_VersaoDesconhecidaEhFailClosedComTipoProprio(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.Append(ctx, AuditRecord{
		SchemaVersion: 99, // época do futuro
		Partition:     "run-futuro", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	err := Verify(ctx, s, "run-futuro", 1, 1)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("uma versao desconhecida tem de ser fail-closed; veio %v", err)
	}
	var ve *VerifyError
	if !errors.As(err, &ve) || ve.Type != TamperUnknownSchema {
		t.Fatalf("o tipo devia ser %q para nao mandar procurar um adulterador; veio %+v", TamperUnknownSchema, ve)
	}
}
