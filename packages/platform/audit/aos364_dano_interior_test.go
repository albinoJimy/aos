package audit

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// corromperTrailerCRCdoFrame corrompe os 4 bytes do trailer de CRC do frame no índice `idx`
// (0-based) do WAL, iterando os offsets pelo comprimento de cada frame anterior. Preserva o
// framing (comprimento e payload intactos): o frame continua FISICAMENTE COMPLETO, só o CRC
// deixa de fechar. É a mecânica exacta da medição de analises/13 §2.1 (O-11).
func corromperTrailerCRCdoFrame(t *testing.T, path string, idx int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	off := 0
	for j := 0; ; j++ {
		if off+4 > len(data) {
			t.Fatalf("frame %d nao existe: WAL tem %d frames", idx, j)
		}
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		trailerOff := off + 4 + n
		if trailerOff+4 > len(data) {
			t.Fatalf("frame %d truncado no ficheiro (off=%d n=%d)", j, off, n)
		}
		if j == idx {
			// Vira todos os 4 bytes do trailer; o CRC deixa de casar mas os bytes continuam lá.
			for k := 0; k < 4; k++ {
				data[trailerOff+k] ^= 0xFF
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write wal: %v", err)
			}
			return
		}
		off = trailerOff + 4
	}
}

// TestAOS364_DanoInterior_RecusaSemTruncar reproduz a medição de O-11: um WAL de 6 registos,
// corromper o CRC do registo de índice 2 (interior), e provar que OpenFileStore (a) RECUSA com
// erro que desembrulha para ErrWORMDanoInterior, (b) NÃO altera o tamanho do ficheiro, e (c) o
// erro nomeia partição, audit_seq e offset (observabilidade da CA5). Falha-antes: sem a
// distinção cauda/interior, o Open truncava para ~1176 bytes e devolvia nil.
func TestAOS364_DanoInterior_RecusaSemTruncar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 6; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	fiAntes, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat antes: %v", err)
	}
	corromperTrailerCRCdoFrame(t, path, 2) // registo interior, framing intacto

	// (a) recusa fail-closed. Guarda-se o erro do Open numa variável PRÓPRIA — reutilizar `err`
	// para o os.Stat seguinte sobrescrevê-lo-ia (o Stat tem sucesso ⇒ err=nil).
	_, openErr := OpenFileStore(path)
	if openErr == nil {
		t.Fatal("dano interior devia RECUSAR o Open, veio nil (o defeito de O-11: truncava em silêncio)")
	}
	if !errors.Is(openErr, ErrWORMDanoInterior) {
		t.Fatalf("erro devia desembrulhar para ErrWORMDanoInterior, veio: %v", openErr)
	}

	// (b) o ficheiro NÃO foi tocado — nenhuma amputação
	fiDepois, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat depois: %v", err)
	}
	if fiDepois.Size() != fiAntes.Size() {
		t.Fatalf("o ficheiro foi AMPUTADO num dano recusado: size antes=%d depois=%d", fiAntes.Size(), fiDepois.Size())
	}

	// (c) observabilidade: o erro nomeia partição, audit_seq e offset (só o trailer foi
	// corrompido, logo o payload ainda desserializa e dá partição/seq).
	var die *DanoInteriorError
	if !errors.As(openErr, &die) {
		t.Fatalf("erro devia ser *DanoInteriorError, veio: %T", openErr)
	}
	if !die.HasSeq || die.Partition != "p" {
		t.Errorf("erro devia nomear a partição %q e o audit_seq (só o trailer foi corrompido): %+v", "p", die)
	}
	if die.Offset <= 0 {
		t.Errorf("erro devia nomear um offset > 0 (o frame de índice 2 não começa em 0): %+v", die)
	}
	// audit_seq do índice 2 (base 1) é 3.
	if die.HasSeq && die.AuditSeq != 3 {
		t.Errorf("audit_seq do registo danificado (índice 2) devia ser 3, veio %d", die.AuditSeq)
	}
}

// TestAOS364_CaudaRasgada_ContinuaATruncar é o CONTROLO NEGATIVO da CA3: um WAL cuja cauda foi
// genuinamente rasgada (últimos bytes cortados a meio de um registo — bytes em FALTA) continua
// a abrir com err==nil, a truncar o tail parcial, e a servir os registos íntegros. A
// recuperação de crash NÃO regride.
func TestAOS364_CaudaRasgada_ContinuaATruncar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 4; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Corta os últimos 5 bytes: o último registo perde o trailer + 1 byte de payload ⇒ short
	// read ⇒ cauda rasgada (bytes em FALTA, não frame completo corrompido).
	rasgado := full[:len(full)-5]
	if err := os.WriteFile(path, rasgado, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s2, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("cauda rasgada devia ABRIR (crash-safety), veio erro: %v", err)
	}
	defer s2.Close()

	// (CA4) o primeiro audit_seq pós-abertura é head+1 da cadeia PRESERVADA (3 registos
	// íntegros sobreviveram; o 4º foi truncado).
	if h, _ := s2.Head(ctx, "p"); h != 3 {
		t.Fatalf("head devia ser 3 (3 registos íntegros preservados, o 4º rasgado truncado), veio %d", h)
	}
	rec, err := s2.Append(ctx, sampleRecord("p", DecisionAllow))
	if err != nil {
		t.Fatalf("append pós-recuperação: %v", err)
	}
	if rec.AuditSeq != 4 {
		t.Fatalf("o primeiro audit_seq pós-recuperação devia ser head+1=4 (sem reemitir), veio %d", rec.AuditSeq)
	}
}

// TestAOS364_DanoInterior_RecusaAntesDeQualquerEscrita cobre a CA6: no dano interior, a recusa
// acontece ANTES de qualquer escrita ao ficheiro — o vector que tornava verifyReplayedChain
// inalcançável (o os.Truncate corria 38 linhas antes) deixa de o ser, porque o Open nem chega a
// abrir o ficheiro para escrita. Prova-se pela imutabilidade byte-a-byte do ficheiro.
func TestAOS364_DanoInterior_RecusaAntesDeQualquerEscrita(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 4; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	antes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read antes: %v", err)
	}
	corromperTrailerCRCdoFrame(t, path, 1) // interior
	// Re-lê o estado corrompido (o helper alterou o disco) para comparar byte-a-byte depois do Open.
	corrompido, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corrompido: %v", err)
	}

	if _, err := OpenFileStore(path); !errors.Is(err, ErrWORMDanoInterior) {
		t.Fatalf("dano interior devia recusar com ErrWORMDanoInterior, veio: %v", err)
	}

	depois, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read depois: %v", err)
	}
	if len(depois) != len(corrompido) {
		t.Fatalf("o Open alterou o tamanho do ficheiro (escreveu): %d -> %d", len(corrompido), len(depois))
	}
	for i := range depois {
		if depois[i] != corrompido[i] {
			t.Fatalf("o Open alterou o byte %d — houve escrita apesar do dano recusado", i)
		}
	}
	_ = antes // lido antes da corrupção; a asserção é sobre o estado corrompido→pós-Open
}

// TestAOS364_ComprimentoMalformado_Recusa: um comprimento lixo (> max) num frame INTERIOR, com
// registos íntegros a seguir (orfaos>0), recusa — não trunca. A classificação é walStopIncomplete
// e a decisão vem da ressincronização.
func TestAOS364_ComprimentoMalformado_Recusa(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Salta o 1º frame e escreve um comprimento gigante (> auditMaxRecordBytes) no header do 2º.
	n0 := int(binary.BigEndian.Uint32(data[0:4]))
	off1 := 4 + n0 + 4
	binary.BigEndian.PutUint32(data[off1:off1+4], 0xFFFFFFFF)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sizeAntes := int64(len(data))

	if _, err := OpenFileStore(path); !errors.Is(err, ErrWORMDanoInterior) {
		t.Fatalf("comprimento malformado devia recusar com ErrWORMDanoInterior, veio: %v", err)
	}
	if fi, _ := os.Stat(path); fi != nil && fi.Size() != sizeAntes {
		t.Fatalf("o ficheiro não pode ser alterado: size antes=%d depois=%d", sizeAntes, fi.Size())
	}
}

// TestAOS364_OrfaosDecideNaoAPosicao prova, ao nível do classificador, o que separa a recusa da
// truncagem — e que é a RESSINCRONIZAÇÃO (orfaos) que decide, não a posição do leitor. Cobre os
// três eixos: dano de CRC interior (walStopComplete), comprimento inflado interior
// (walStopIncomplete + orfaos>0) e cauda rasgada genuína (walStopIncomplete + orfaos==0). Este
// teste avermelha se a decisão voltar a depender do tipo de falha na posição corrente (o defeito
// crítico que a revisão adversarial reproduziu).
func TestAOS364_OrfaosDecideNaoAPosicao(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 5; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	// (1) CRC interior ⇒ walStopComplete (recusa sem sequer consultar orfaos).
	pathCRC := path + ".crc"
	copiar(t, path, pathCRC)
	corromperTrailerCRCdoFrame(t, pathCRC, 2)
	_, _, stopCRC, _, err := replayAuditWAL(pathCRC)
	if err != nil {
		t.Fatalf("replay crc: %v", err)
	}
	if stopCRC.kind != walStopComplete {
		t.Fatalf("CRC interior devia ser walStopComplete, veio kind=%d", stopCRC.kind)
	}

	// (2) COMPRIMENTO inflado interior ⇒ walStopIncomplete com orfaos > 0 (há frames a seguir).
	pathLen := path + ".len"
	copiar(t, path, pathLen)
	inflarComprimentoDoFrame(t, pathLen, 2)
	_, _, stopLen, orfaosLen, err := replayAuditWAL(pathLen)
	if err != nil {
		t.Fatalf("replay len: %v", err)
	}
	if stopLen.kind != walStopIncomplete {
		t.Fatalf("comprimento inflado devia ser walStopIncomplete, veio kind=%d", stopLen.kind)
	}
	if orfaosLen == 0 {
		t.Fatalf("comprimento inflado num frame interior devia ter orfaos>0 (ressincronização acha os frames seguintes), veio 0 — o defeito crítico")
	}

	// (3) CAUDA RASGADA genuína ⇒ walStopIncomplete com orfaos == 0 (nada íntegro a seguir).
	pathTail := path + ".tail"
	copiar(t, path, pathTail)
	full, _ := os.ReadFile(pathTail)
	_ = os.WriteFile(pathTail, full[:len(full)-5], 0o600)
	_, _, stopTail, orfaosTail, err := replayAuditWAL(pathTail)
	if err != nil {
		t.Fatalf("replay tail: %v", err)
	}
	if stopTail.kind != walStopIncomplete {
		t.Fatalf("cauda rasgada devia ser walStopIncomplete, veio kind=%d", stopTail.kind)
	}
	if orfaosTail != 0 {
		t.Fatalf("cauda rasgada NÃO tem frames íntegros a seguir: orfaos devia ser 0, veio %d", orfaosTail)
	}

	// (4) WAL íntegro ⇒ walStopClean.
	_, _, stopClean, _, err := replayAuditWAL(path)
	if err != nil {
		t.Fatalf("replay clean: %v", err)
	}
	if stopClean.kind != walStopClean {
		t.Fatalf("WAL íntegro devia ser walStopClean, veio kind=%d", stopClean.kind)
	}
}

func copiar(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("copiar read: %v", err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("copiar write: %v", err)
	}
}

// inflarComprimentoDoFrame corrompe os 4 bytes de COMPRIMENTO do frame no índice `idx`, somando
// 65536 (byte alto). O framing dos restantes fica intacto — os frames seguintes continuam
// fisicamente presentes DEPOIS —, mas o leitor sequencial passa a consumir o número errado de
// bytes. É o vector do achado crítico da revisão (comprimento, não CRC).
func inflarComprimentoDoFrame(t *testing.T, path string, idx int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	off := 0
	for j := 0; j < idx; j++ {
		if off+4 > len(data) {
			t.Fatalf("frame %d nao existe", idx)
		}
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		off += 4 + n + 4
	}
	if off+2 > len(data) {
		t.Fatalf("frame %d sem header completo", idx)
	}
	data[off+1] ^= 0x01 // comprimento += 65536, o resto do ficheiro intacto
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}
}

// TestAOS364_ZerosNaCauda_NaoEFalsoPositivo é o CONTROLO do achado MÉDIO da revisão: uma cauda de
// ZEROS após o último registo íntegro (o artefacto de crash mais comum em FS com delayed
// allocation) NÃO pode ser recusada como dano — isso impediria o nó de arrancar após um crash
// normal. Com orfaos==0 (nada íntegro a seguir aos zeros), é cauda rasgada ⇒ trunca e abre.
func TestAOS364_ZerosNaCauda_NaoEFalsoPositivo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Acrescenta 200 bytes de zeros (o header seguinte lê n==0 ⇒ comprimento lixo; nada íntegro a seguir).
	comZeros := append(full, make([]byte, 200)...)
	if err := os.WriteFile(path, comZeros, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s2, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("zeros na cauda (crash normal) devia ABRIR truncando, veio recusa: %v", err)
	}
	defer s2.Close()
	if h, _ := s2.Head(ctx, "p"); h != 3 {
		t.Fatalf("os 3 registos íntegros deviam sobreviver, head=%d", h)
	}
	// E os zeros foram truncados (o ficheiro voltou ao tamanho dos 3 registos íntegros).
	if fi, _ := os.Stat(path); fi != nil && fi.Size() != int64(len(full)) {
		t.Fatalf("a cauda de zeros devia ser truncada: size=%d, esperado %d", fi.Size(), len(full))
	}
}
