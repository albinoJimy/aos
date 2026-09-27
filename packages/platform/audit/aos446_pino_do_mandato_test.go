package audit

// aos446_pino_do_mandato_test.go — O SELO DIZ SOB QUE CHAVE O MANDATO FOI VERIFICADO
// (AOS-446 fase 1).
//
// [SchemaV5] acrescenta ao conteúdo canónico a impressão digital do PINO, no FIM e sob um domínio
// novo. As propriedades a provar são as mesmas de cada época anterior, e há uma a mais:
//
//  1. um registo v3 e um registo v4 produzem EXACTAMENTE os bytes de antes deste ticket — o que
//     está selado em produção continua a verificar no arranque, que é fail-closed;
//  2. num v5 a impressão ENTRA no hash: mutá-la depois de selado parte a cadeia;
//  3. abaixo do v5 a impressão nem sequer fica gravada ao lado — atribuição com cara de selo é
//     pior do que atribuição nenhuma;
//  4. uma cadeia MISTA (v3, depois v4, depois v5) verifica: cada registo é lido com as regras da
//     sua própria época.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const pinoDeProva = "SHA256:NptMONesV1zvQt3rvUGXqipxV96EE0ERyKbato1QuqY"

// A EPOCA ANTERIOR NAO SE MEXE. É a propriedade que faz um WORM em produção sobreviver ao deploy.
func TestAOS446V3EV4TemOsBytesDeSempre(t *testing.T) {
	base := AuditRecord{
		AuditSeq: 1, Partition: "p", Timestamp: time.Unix(1, 0).UTC(),
		Decision: DecisionDeny, Code: "E_X", DeniedBy: "g", Reason: "r",
		Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1", MandateSigner: pinoDeProva},
	}
	for _, v := range []uint8{SchemaV2, SchemaV3, SchemaV4} {
		com, sem := base, base
		com.SchemaVersion, sem.SchemaVersion = v, v
		sem.Principal.MandateSigner = ""
		if string(canonicalContent(com)) != string(canonicalContent(sem)) {
			t.Fatalf("em v%d a impressao do pino NAO pode entrar no conteudo canonico", v)
		}
	}
	com, sem := base, base
	com.SchemaVersion, sem.SchemaVersion = SchemaV5, SchemaV5
	sem.Principal.MandateSigner = ""
	if string(canonicalContent(com)) == string(canonicalContent(sem)) {
		t.Fatal("em v5 a impressao do pino TEM de entrar no conteudo canonico")
	}
	// E os domínios das cinco épocas são todos distintos: dois hashes de épocas diferentes nunca
	// podem ser comparados como iguais.
	vistos := map[string]uint8{}
	for _, v := range []uint8{SchemaV2, SchemaV3, SchemaV4, SchemaV5} {
		d := domainFor(v)
		if d == "" {
			t.Fatalf("v%d sem dominio", v)
		}
		if outra, dup := vistos[d]; dup {
			t.Fatalf("v%d e v%d partilham o dominio %q", outra, v, d)
		}
		vistos[d] = v
	}
	if domainFor(6) != "" {
		t.Fatal("uma versao desconhecida nao se adivinha")
	}
}

func memStoreV5(t *testing.T) *MemStore {
	t.Helper()
	s, err := NewMemStore().ComVersaoDeEscrita(SchemaV5)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Abaixo do v5 a impressão não fica no registo — nem selada nem ao lado.
func TestAOS446AbaixoDeV5AImpressaoNaoFicaGravada(t *testing.T) {
	ctx := context.Background()
	for _, escrita := range []uint8{SchemaV3, SchemaV4} {
		s, err := NewMemStore().ComVersaoDeEscrita(escrita)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(ctx, AuditRecord{
			Partition: "run-1", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
			Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1", MandateSigner: pinoDeProva},
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		recs, _ := s.Read(ctx, "run-1", 1, 1)
		if recs[0].SchemaVersion != escrita {
			t.Fatalf("esperado v%d, veio v%d", escrita, recs[0].SchemaVersion)
		}
		if recs[0].Principal.MandateSigner != "" {
			t.Fatalf("num v%d a impressao nao pode ficar no registo: %q", escrita, recs[0].Principal.MandateSigner)
		}
		b, err := json.Marshal(recs[0])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "MandateSigner") {
			t.Fatalf("um registo v%d serializa a chave nova: %s", escrita, b)
		}
	}
}

// Com o v5 ligado, a impressão ENTRA no hash: mutá-la depois de selado muda o entry_hash.
func TestAOS446PinoEstaSeladoEmV5(t *testing.T) {
	ctx := context.Background()
	s := memStoreV5(t)
	selado, err := s.Append(ctx, AuditRecord{
		Partition: "run-1", Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionAllow,
		Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1", MandateSigner: pinoDeProva},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if selado.SchemaVersion != SchemaV5 || selado.Principal.MandateSigner != pinoDeProva {
		t.Fatalf("o registo tinha de sair v5 com a impressao: %+v", selado)
	}
	// A MUTACAO DA GUARDA: trocar o pino (o que um root que troca `AOS_MANDATE_SIGNERS` produz)
	// tem de partir o entry_hash. Sem esta linha, o campo estaria no registo sem estar no selo.
	trocado := selado
	trocado.Principal.MandateSigner = "SHA256:UmaChaveQueNaoEstavaPinadaAntesAAAAAAAAAAAA"
	if string(ComputeEntryHash(selado.PrevHash, trocado)) == string(selado.EntryHash) {
		t.Fatal("trocar o pino NAO mudou o entry_hash — a impressao nao esta selada")
	}
	// E o mesmo registo com o MESMO pino re-encadeia.
	if string(ComputeEntryHash(selado.PrevHash, selado)) != string(selado.EntryHash) {
		t.Fatal("o registo nao re-encadeia contra si proprio")
	}
}

// Um produtor NÃO preposta uma época acima da que o store escreve — senão o expand/contract não
// protegia rollback nenhum.
func TestAOS446V5PrepostoNumStoreV4ERecusado(t *testing.T) {
	ctx := context.Background()
	s, err := NewMemStore().ComVersaoDeEscrita(SchemaV4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, AuditRecord{
		SchemaVersion: SchemaV5, Partition: "run-1", Timestamp: time.Unix(1, 0).UTC(),
		Decision: DecisionAllow, Principal: Principal{NHIID: "agt", MandateSigner: pinoDeProva},
	}); err == nil {
		t.Fatal("um v5 preposto num store v4 tinha de ser recusado")
	}
}

// UMA CADEIA MISTA VERIFICA. É o que acontece em produção: registos v3 de antes do deploy, v4 se
// o operador tiver ligado a época intermédia, e v5 depois. Cada um lê-se com as regras da SUA
// época — é isso que a [AuditRecord.SchemaVersion] existe para permitir.
func TestAOS446CadeiaMistaV3V4V5Verifica(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for i, v := range []uint8{SchemaV3, SchemaV3, SchemaV4, SchemaV5, SchemaV5} {
		if _, err := s.ComVersaoDeEscrita(v); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(ctx, AuditRecord{
			Partition: "run-misto", Timestamp: time.Unix(int64(i+1), 0).UTC(), Decision: DecisionAllow,
			Principal: Principal{NHIID: "agt", RequestedBy: "sub-bob", MandateID: "m-1", MandateSigner: pinoDeProva},
		}); err != nil {
			t.Fatalf("Append v%d: %v", v, err)
		}
	}
	rel, err := VerifyStore(ctx, s)
	if err != nil {
		t.Fatalf("a cadeia mista tinha de verificar: %v", err)
	}
	if rel != 1 { // VerifyStore conta PARTICOES verificadas, e so ha uma
		t.Fatalf("esperada 1 particao verificada, vieram %d", rel)
	}
	recs, _ := s.Read(ctx, "run-misto", 1, 5)
	if recs[0].Principal.MandateSigner != "" || recs[2].Principal.MandateSigner != "" {
		t.Fatal("os registos v3/v4 nao podem trazer a impressao")
	}
	if recs[3].Principal.MandateSigner != pinoDeProva || recs[4].Principal.MandateSigner != pinoDeProva {
		t.Fatal("os registos v5 tem de trazer a impressao")
	}
	// CONTROLO NEGATIVO: mutar a impressão num registo v5 parte a verificação da cadeia.
	recs[3].Principal.MandateSigner = "SHA256:outra"
	if string(ComputeEntryHash(recs[3].PrevHash, recs[3])) == string(recs[3].EntryHash) {
		t.Fatal("mutar a impressao de um v5 no meio da cadeia nao a partiu")
	}
}
