package audit

// aos439_quem_pediu_test.go — O SELO DIZ QUEM PEDIU O RUN E SOB QUE MANDATO CORREU (AOS-439).
//
// SchemaV4 acrescenta ao conteúdo canónico o `requested_by` e o `mandate_id`, no FIM e sob um
// domínio novo. A propriedade crítica é a mesma do v3: o que já foi selado continua a verificar
// byte-a-byte, porque o arranque do nó re-verifica a cadeia fail-closed.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Os bytes de um registo v3 NÃO dependem dos campos novos. Se dependessem, TODO o WORM v3 em
// produção deixava de verificar no arranque do binário novo. (A forma v3 em si está fixada pelos
// testes do AOS-011 em attribution_test.go.)
func TestAOS439RegistoV3TemOsBytesDeSempre(t *testing.T) {
	rec := AuditRecord{
		SchemaVersion: SchemaV3, AuditSeq: 1, Partition: "p", Timestamp: time.Unix(1, 0).UTC(),
		Decision: DecisionDeny, Code: "E_X", DeniedBy: "g", Reason: "r",
		// Os campos novos preenchidos NÃO podem mexer num v3.
		Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1"},
	}
	semNovos := rec
	semNovos.Principal = Principal{NHIID: "agt"}
	if string(canonicalContent(rec)) != string(canonicalContent(semNovos)) {
		t.Fatal("em v3 o requested_by e o mandate_id NAO podem entrar no conteudo canonico")
	}
	// E em v4 entram — senão não estariam selados de todo.
	a, b := rec, semNovos
	a.SchemaVersion, b.SchemaVersion = SchemaV4, SchemaV4
	if string(canonicalContent(a)) == string(canonicalContent(b)) {
		t.Fatal("em v4 o requested_by e o mandate_id TEM de entrar no conteudo canonico")
	}
}

// / memStoreV4 é um MemStore que escreve v4 — o que o nó compõe com AOS_AUDIT_WRITE_V4=1.
func memStoreV4(t *testing.T) *MemStore {
	t.Helper()
	s, err := NewMemStore().ComVersaoDeEscrita(SchemaV4)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// POR OMISSÃO ESCREVE-SE v3 (expand/contract, revisão do AOS-439): um binário anterior continua
// a verificar tudo o que este escreve, e o rollback continua possível. Num v3 o requested_by e o
// mandate_id NÃO ficam no registo — nem selados nem ao lado, que seria atribuição com cara de selo.
func TestAOS439PorOmissaoEscreveV3SemOsCamposNovos(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if _, err := s.Append(ctx, AuditRecord{
		Partition: "run-f~n1", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
		Principal: Principal{NHIID: "agt-drenador", RequestedBy: "sub-bob", MandateID: "m-1"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs, _ := s.Read(ctx, "run-f~n1", 1, 1)
	if recs[0].SchemaVersion != SchemaV3 {
		t.Fatalf("por omissao sela-se v3, veio %d", recs[0].SchemaVersion)
	}
	if recs[0].Principal.RequestedBy != "" || recs[0].Principal.MandateID != "" {
		t.Fatalf("num v3 os campos novos nao podem ficar no registo (nao estao selados): %+v", recs[0].Principal)
	}
	// E a forma no FICHEIRO é a de um binário anterior: sem as chaves novas.
	b, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "RequestedBy") || strings.Contains(string(b), "MandateID") {
		t.Fatalf("um registo v3 serializa as chaves novas: %s", b)
	}
}

// Uma versão que esta release não escreve é recusada — nos dois stores.
func TestAOS439VersaoDeEscritaInvalidaERecusada(t *testing.T) {
	for _, v := range []uint8{0, SchemaV2, 6} { // o 5 passou a ser escrevivel no AOS-446 fase 1
		if _, err := NewMemStore().ComVersaoDeEscrita(v); !errors.Is(err, ErrVersaoDeEscrita) {
			t.Errorf("MemStore v%d: tinha de recusar, veio %v", v, err)
		}
		if _, err := OpenFileStore(filepath.Join(t.TempDir(), "w.wal"), ComVersaoDeEscrita(v)); !errors.Is(err, ErrVersaoDeEscrita) {
			t.Errorf("FileStore v%d: tinha de recusar, veio %v", v, err)
		}
	}
}

// Com o v4 ligado, os dois campos ENTRAM no hash: mutá-los depois de selado muda o entry_hash.
func TestAOS439QuemPediuEstaSeladoEmV4(t *testing.T) {
	ctx := context.Background()
	s := memStoreV4(t)
	if _, err := s.Append(ctx, AuditRecord{
		Partition: "run-f~n1", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
		Capability: "cap:doc.read", ToolID: "doc_read",
		Principal: Principal{NHIID: "agt-drenador", RequestedBy: "sub-bob", MandateID: "m-1"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs, err := s.Read(ctx, "run-f~n1", 1, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	selado := recs[0]
	if selado.SchemaVersion != SchemaV4 {
		t.Fatalf("com o v4 ligado sela-se v4, veio %d", selado.SchemaVersion)
	}
	if selado.Principal.RequestedBy != "sub-bob" || selado.Principal.MandateID != "m-1" {
		t.Fatalf("os campos tem de sobreviver ao selo e a leitura: %+v", selado.Principal)
	}
	for _, c := range []struct {
		nome  string
		mutar func(*AuditRecord)
	}{
		{"requested_by", func(r *AuditRecord) { r.Principal.RequestedBy = "sub-intruso" }},
		{"mandate_id", func(r *AuditRecord) { r.Principal.MandateID = "m-outro" }},
	} {
		m := selado
		c.mutar(&m)
		if string(ComputeEntryHash(m.PrevHash, m)) == string(selado.EntryHash) {
			t.Errorf("mutar %s nao mudou o entry_hash — o campo nao esta selado", c.nome)
		}
	}
	if err := Verify(ctx, s, "run-f~n1", 1, 1); err != nil {
		t.Fatalf("a cadeia v4 tem de verificar: %v", err)
	}
}

// A CADEIA MISTA, EM DISCO: o WORM de produção depois do deploy (v3 por omissão) e depois de o
// operador ligar o v4 (reabre o mesmo ficheiro com a opção) — v2 antigo, v3, v4 na mesma
// partição. Tem de verificar no Open (que re-encadeia a partir da génese) e pelo Verify.
func TestAOS439CadeiaMistaEmDiscoVerifica(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	rec := func(seg int64, v uint8) AuditRecord {
		return AuditRecord{SchemaVersion: v, Partition: "run-epocas", Timestamp: time.Unix(seg, 0).UTC(),
			Decision: DecisionAllow, Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1"}}
	}
	antes, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range []uint8{SchemaV2, 0} {
		if _, err := antes.Append(ctx, rec(int64(i+1), v)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := antes.Close(); err != nil {
		t.Fatal(err)
	}
	depois, err := OpenFileStore(caminho, ComVersaoDeEscrita(SchemaV4))
	if err != nil {
		t.Fatalf("reabrir com o v4 ligado: %v", err)
	}
	defer func() { _ = depois.Close() }()
	if _, err := depois.Append(ctx, rec(3, 0)); err != nil {
		t.Fatalf("Append v4: %v", err)
	}
	recs, _ := depois.Read(ctx, "run-epocas", 1, 3)
	if recs[0].SchemaVersion != SchemaV2 || recs[1].SchemaVersion != SchemaV3 || recs[2].SchemaVersion != SchemaV4 {
		t.Fatalf("epocas: %d %d %d", recs[0].SchemaVersion, recs[1].SchemaVersion, recs[2].SchemaVersion)
	}
	if err := Verify(ctx, depois, "run-epocas", 1, 3); err != nil {
		t.Fatalf("uma cadeia v2+v3+v4 tem de verificar: %v", err)
	}
	if err := depois.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStoreReadOnly(caminho); err != nil {
		t.Fatalf("o re-encadeamento no Open tem de aceitar a cadeia mista: %v", err)
	}
}

// O adaptador do RM leva os dois campos da mediação para o selo — com o v4 ligado.
func TestAOS439AdaptadorDoRMSelaQuemPediu(t *testing.T) {
	ctx := context.Background()
	s := memStoreV4(t)
	a := NewMediationSink(s)
	if _, err := a.RecordMediation(ctx, referencemonitor.MediationRecord{
		RunID: "run-f~n1", StepID: "s1", Effect: referencemonitor.EffectPermit, ToolID: "doc_read",
		Principal: referencemonitor.Principal{NHIID: "agt-drenador", RequestedBy: "sub-bob", MandateID: "m-1"},
	}); err != nil {
		t.Fatalf("RecordMediation: %v", err)
	}
	recs, err := s.Read(ctx, "run-f~n1", 1, 1)
	if err != nil || len(recs) != 1 {
		t.Fatalf("Read: %v %d", err, len(recs))
	}
	if recs[0].Principal.RequestedBy != "sub-bob" || recs[0].Principal.MandateID != "m-1" {
		t.Fatalf("o selo da decisao tem de levar requested_by e mandate_id: %+v", recs[0].Principal)
	}
}

// UM PRODUTOR NÃO CONTORNA O EXPAND/CONTRACT (2.ª ronda da revisão). Um registo preposto em v4
// num store que escreve v3 é recusado — nos dois stores, sem consumir audit_seq —; prepor uma
// versão abaixo continua aceite, e o v4 preposto passa num store que escreve v4.
func TestAOS439VersaoPrepostaAcimaDaEscritaERecusada(t *testing.T) {
	ctx := context.Background()
	v4 := func(p string) AuditRecord {
		return AuditRecord{SchemaVersion: SchemaV4, Partition: p, Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow}
	}
	fs, err := OpenFileStore(filepath.Join(t.TempDir(), "w.wal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	for nome, s := range map[string]Store{"MemStore": NewMemStore(), "FileStore": fs} {
		if _, err := s.Append(ctx, v4("p")); !errors.Is(err, ErrVersaoAcimaDaEscrita) {
			t.Errorf("%s em v3: um v4 preposto tinha de ser recusado, veio %v", nome, err)
		}
		if h, _ := s.Head(ctx, "p"); h != 0 {
			t.Errorf("%s: a recusa consumiu audit_seq (head=%d)", nome, h)
		}
		abaixo := v4("p")
		abaixo.SchemaVersion = SchemaV2
		if _, err := s.Append(ctx, abaixo); err != nil {
			t.Errorf("%s: prepor uma versao ABAIXO continua aceite: %v", nome, err)
		}
	}
	if _, err := memStoreV4(t).Append(ctx, v4("p")); err != nil {
		t.Fatalf("num store em v4 o v4 preposto passa: %v", err)
	}
}
