package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
)

// AOS-453 — a custódia de KEK que SELA segmentos do backup: a DEK embrulhada DENTRO da custódia
// (porta audit.KeyWrapper, o molde de audit.SealContent/OpenContent), com o formato KEK-crua de
// sempre intacto byte a byte.
//
// O InMemoryKeyWrapper de referência modela a custódia Vault Transit do nó: key-never-leaves
// (EnsureKey → nil, Key → false), com WrapDEK/UnwrapDEK dentro dela e Delete como crypto-shred.
// Partilhado entre dois exportadores, modela uma custódia que SOBREVIVE ao processo — que é
// exactamente o que o Vault é.

// goldenSegmentoKEKCrua é o blob que o sealSegment ANTERIOR ao AOS-453 produzia para a entrada
// abaixo (detRand, InMemoryKeyVault com a mesma fonte, titular aos.backup:eu-west). Capturado do
// código antes da alteração: é o que prova que os `omitempty` novos não mudaram UM byte do formato
// que já está nos destinos.
const goldenSegmentoKEKCrua = `{"key_ref":"aos.audit.pii:aos.backup:eu-west","wrapped_dek":"MpPO/I1e5piBXnDQOfyfgbh27wvToNINhfN1w2t/27hNZyAhC/7adcC20yK9xkQq","dek_nonce":"TE1OT1BRUlNUVVZX","ciphertext":"uXbO9SITpGYhEGpTpzgkHD7vv7muHdqjRF68Pbfh5rznSxhljWY4ow==","nonce":"QEFCQ0RFRkdISUpL"}`

// TestAOS453_FormatoKEKCruaByteAByte fixa a serialização do caminho KEK-crua.
func TestAOS453_FormatoKEKCruaByteAByte(t *testing.T) {
	r := detRand()
	v := audit.NewInMemoryKeyVault(r)
	seg, err := sealSegment(v, "aos.backup:eu-west", []byte(`{"streams":{"run-a":[]}}`), r)
	if err != nil {
		t.Fatalf("sealSegment: %v", err)
	}
	blob, err := marshalSegment(seg)
	if err != nil {
		t.Fatalf("marshalSegment: %v", err)
	}
	if string(blob) != goldenSegmentoKEKCrua {
		t.Fatalf("o formato KEK-crua MUDOU (segmentos ja escritos deixariam de ser os mesmos):\n got  %s\n want %s", blob, goldenSegmentoKEKCrua)
	}
	// E continua a abrir pelo caminho KEK-crua, com o titular.
	enc, err := unmarshalSegment(blob)
	if err != nil {
		t.Fatalf("unmarshalSegment: %v", err)
	}
	pt, err := openSegment(v, "aos.backup:eu-west", enc)
	if err != nil || string(pt) != `{"streams":{"run-a":[]}}` {
		t.Fatalf("o segmento KEK-crua nao abre: pt=%q err=%v", pt, err)
	}
}

// TestAOS453_OEnvelopeTemDiscriminadorExplicitoESemDEKNonce: com uma custódia KeyWrapper o
// segmento leva `wrap:"envelope"` e NÃO leva `dek_nonce` — e o formato KEK-crua nunca leva `wrap`.
func TestAOS453_OEnvelopeTemDiscriminadorExplicitoESemDEKNonce(t *testing.T) {
	w := audit.NewInMemoryKeyWrapper(detRand())
	seg, err := sealSegment(w, "aos.backup:eu-west", []byte("conteudo"), detRand())
	if err != nil {
		t.Fatalf("sealSegment (envelope): %v", err)
	}
	blob, _ := marshalSegment(seg)
	s := string(blob)
	if !strings.Contains(s, `"wrap":"envelope"`) {
		t.Errorf("o segmento de envelope tem de levar o discriminador explicito; blob=%s", s)
	}
	if strings.Contains(s, "dek_nonce") {
		t.Errorf("o segmento de envelope NAO leva dek_nonce (o embrulho e da custodia); blob=%s", s)
	}
	if seg.KeyRef != audit.KeyRefFor("aos.backup:eu-west") {
		t.Errorf("keyRef %q nao e o do titular do backup", seg.KeyRef)
	}
	if strings.Contains(goldenSegmentoKEKCrua, `"wrap"`) {
		t.Fatal("o formato KEK-crua nao pode levar wrap")
	}
	pt, err := openSegment(w, "aos.backup:eu-west", seg)
	if err != nil || string(pt) != "conteudo" {
		t.Fatalf("o envelope nao abre com a mesma custodia: pt=%q err=%v", pt, err)
	}
}

// TestAOS453_DoisExportadoresComACustodiaDeEnvelopeRetomamERESTAURAM (critério 2): o exportador
// sela com a custódia key-never-leaves, um 2.º processo RETOMA (abre o último segmento pela
// custódia), sela o ciclo 2, e a cadeia dos dois arranques RESTAURA.
func TestAOS453_DoisExportadoresComACustodiaDeEnvelopeRetomamERESTAURAM(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	custodia := audit.NewInMemoryKeyWrapper(nil) // entropia real: o que sobrevive é a CUSTÓDIA

	seed(t, src, "run-a", 3, "segredo-1")
	exp1, err := NewExporter(src, dst, signer, WithKeyVault(custodia))
	if err != nil {
		t.Fatalf("NewExporter (1.º): %v", err)
	}
	if r, err := exp1.Export(ctx); err != nil || r.Cycle != 1 {
		t.Fatalf("ciclo 1: r=%+v err=%v", r, err)
	}

	exp2, err := NewExporter(src, dst, signer, WithKeyVault(custodia))
	if err != nil {
		t.Fatalf("o 2.º processo devia RETOMAR com a mesma custodia de envelope: %v", err)
	}
	if exp2.ResumedFrom() != 1 {
		t.Fatalf("ResumedFrom=%d, quero 1", exp2.ResumedFrom())
	}
	seed(t, src, "run-a", 2, "segredo-2")
	if r, err := exp2.Export(ctx); err != nil || r.Cycle != 2 {
		t.Fatalf("ciclo 2: r=%+v err=%v", r, err)
	}

	// Nenhum segmento no destino tem dek_nonce, e todos são de envelope.
	m, cp, err := mustRestorer(t, dst, custodia, exp2).LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	for _, s := range m.Segments {
		blob, _ := dst.Get(s.Ref)
		if !strings.Contains(string(blob), `"wrap":"envelope"`) || strings.Contains(string(blob), "segredo") {
			t.Fatalf("segmento %q nao e de envelope ou leva plaintext: %s", s.Ref, blob)
		}
	}
	out := freshDest(t, "board-eu", "eu-west")
	ev, err := mustRestorer(t, dst, custodia, exp2).RestoreTo(ctx, m, cp, 0, nil, out)
	if err != nil {
		t.Fatalf("a cadeia de dois arranques sob a custodia de envelope devia RESTAURAR: %v", err)
	}
	if !ev.Verified || ev.EventsRestored != 5 {
		t.Fatalf("restauro incompleto: %+v", ev)
	}
}

// TestAOS453_OCryptoShredDoTitularDoBackupTornaOsSegmentosIrrecuperaveis (critério 4): destruir a
// KEK do titular aos.backup:<região> na custódia ⇒ o restauro ABORTA com ErrRestoreVerify (não é
// adulteração: a chave não existe) sem escrever nada, e a retoma de um 3.º processo é recusada a
// nomear a KEK — a custódia RESPONDE (a sonda de composição passa), a KEK é que já não é a que selou.
func TestAOS453_OCryptoShredDoTitularDoBackupTornaOsSegmentosIrrecuperaveis(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	custodia := audit.NewInMemoryKeyWrapper(nil)

	seed(t, src, "run-a", 2, "x")
	exp, err := NewExporter(src, dst, signer, WithKeyVault(custodia))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}

	custodia.Delete(backupSubjectFor("eu-west")) // crypto-shred do titular do backup

	rst := mustRestorer(t, dst, custodia, exp)
	m, cp, err := rst.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	if _, err := rst.RestoreTo(ctx, m, cp, 0, nil, out); !errors.Is(err, ErrRestoreVerify) {
		t.Fatalf("depois do shred o restauro tinha de falhar com ErrRestoreVerify; got %v", err)
	} else if errors.Is(err, ErrSegmentTampered) {
		t.Fatalf("o shred NAO e adulteracao; got %v", err)
	}
	if streams, _ := out.Streams(); len(streams) != 0 {
		t.Fatalf("o restauro recusado NAO pode escrever nada; streams=%v", streams)
	}

	_, err = NewExporter(src, dst, signer, WithKeyVault(custodia))
	if !errors.Is(err, ErrResumeUnverifiable) || !strings.Contains(err.Error(), "KEK do backup NAO e a que selou") {
		t.Fatalf("a retoma sobre uma KEK destruida tinha de ser recusada a nomear a KEK; got %v", err)
	}
	if errors.Is(err, ErrKEKCustodyUnavailable) {
		t.Fatalf("a custodia RESPONDE: nao e indisponibilidade; got %v", err)
	}
}

// soKeyVault esconde a porta de envelope: expõe SÓ audit.KeyVault de uma custódia key-never-leaves.
// É a custódia que não entrega a KEK nem sabe embrulhar — a do nó antes do AOS-453, vista pelo backup.
type soKeyVault struct{ audit.KeyVault }

// TestAOS453_UmaCustodiaQueNaoEntregaAKEKNemEmbrulhaERecusadaNaComposicao (critério 3): na
// CONSTRUÇÃO, com erro nomeado — e não ciclo a ciclo com «invalid key size 0».
func TestAOS453_UmaCustodiaQueNaoEntregaAKEKNemEmbrulhaERecusadaNaComposicao(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	_, err := NewExporter(src, dst, newSigner(t), WithKeyVault(soKeyVault{audit.NewInMemoryKeyWrapper(nil)}))
	if !errors.Is(err, ErrKEKCustodyUnsupported) {
		t.Fatalf("uma custodia sem KEK crua e sem envelope tinha de ser recusada na composicao com ErrKEKCustodyUnsupported; got %v", err)
	}
}

// custodiaEmBaixo é uma custódia de envelope que não responde (Vault em baixo, token morto).
type custodiaEmBaixo struct {
	*audit.InMemoryKeyWrapper
	baixo bool
}

var errCustodiaEmBaixo = errors.New("teste: custodia em baixo")

func (c *custodiaEmBaixo) WrapDEK(s string, d []byte) ([]byte, string, error) {
	if c.baixo {
		return nil, "", errCustodiaEmBaixo
	}
	return c.InMemoryKeyWrapper.WrapDEK(s, d)
}

func (c *custodiaEmBaixo) UnwrapDEK(r string, w []byte) ([]byte, bool) {
	if c.baixo {
		return nil, false
	}
	return c.InMemoryKeyWrapper.UnwrapDEK(r, w)
}

// TestAOS453_UmaCustodiaEmBaixoNaoSeLeComoKEKErrada: sobre uma cadeia existente, a custódia que
// não responde é ErrKEKCustodyUnavailable ANTES da retoma — e não ErrResumeUnverifiable («KEK
// errada, use um destino novo»), que mandaria o operador abandonar um backup bom.
func TestAOS453_UmaCustodiaEmBaixoNaoSeLeComoKEKErrada(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	signer := newSigner(t)
	c := &custodiaEmBaixo{InMemoryKeyWrapper: audit.NewInMemoryKeyWrapper(nil)}

	seed(t, src, "run-a", 1, "x")
	exp, err := NewExporter(src, dst, signer, WithKeyVault(c))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}
	c.baixo = true
	_, err = NewExporter(src, dst, signer, WithKeyVault(c))
	if !errors.Is(err, ErrKEKCustodyUnavailable) || !errors.Is(err, errCustodiaEmBaixo) {
		t.Fatalf("custodia em baixo tinha de ser ErrKEKCustodyUnavailable (com a causa); got %v", err)
	}
	if errors.Is(err, ErrResumeUnverifiable) {
		t.Fatalf("custodia em baixo NAO e «KEK errada»; got %v", err)
	}
	// E volta a funcionar quando a custódia volta: a cadeia era boa.
	c.baixo = false
	if _, err := NewExporter(src, dst, signer, WithKeyVault(c)); err != nil {
		t.Fatalf("com a custodia de volta a retoma tinha de passar: %v", err)
	}
}

// TestAOS453_OSegmentoTemDeSerDoTitularPedido: subject-binding nos dois formatos, e as causas certas.
func TestAOS453_OSegmentoTemDeSerDoTitularPedido(t *testing.T) {
	w := audit.NewInMemoryKeyWrapper(nil)
	seg, err := sealSegment(w, backupSubjectFor("eu-west"), []byte("p"), cryptoRand)
	if err != nil {
		t.Fatalf("sealSegment: %v", err)
	}
	if _, err := openSegment(w, backupSubjectFor("us-east"), seg); !errors.Is(err, ErrRestoreVerify) {
		t.Fatalf("segmento de outro titular (envelope) tinha de ser ErrRestoreVerify; got %v", err)
	}
	v := audit.NewInMemoryKeyVault(nil)
	raw, err := sealSegment(v, backupSubjectFor("eu-west"), []byte("p"), cryptoRand)
	if err != nil {
		t.Fatalf("sealSegment (KEK-crua): %v", err)
	}
	if _, err := openSegment(v, backupSubjectFor("us-east"), raw); !errors.Is(err, ErrRestoreVerify) {
		t.Fatalf("segmento de outro titular (KEK-crua) tinha de ser ErrRestoreVerify; got %v", err)
	}

	// Formato desconhecido ⇒ recusado, não tentado.
	desconhecido := seg
	desconhecido.Wrap = "hsm-v9"
	if _, err := openSegment(w, backupSubjectFor("eu-west"), desconhecido); !errors.Is(err, ErrRestoreVerify) {
		t.Fatalf("formato de embrulho desconhecido tinha de ser ErrRestoreVerify; got %v", err)
	}
	// Envelope com uma custódia que não desembrulha (trocada por uma KEK-crua) ⇒ ErrRestoreVerify.
	if _, err := openSegment(v, backupSubjectFor("eu-west"), seg); !errors.Is(err, ErrRestoreVerify) {
		t.Fatalf("envelope aberto por custodia sem KeyWrapper tinha de ser ErrRestoreVerify; got %v", err)
	}
	// Conteúdo adulterado com a KEK presente ⇒ ErrSegmentTampered (a DEK abre, o GCM do conteúdo não).
	adulterado := seg
	adulterado.Ciphertext = append([]byte(nil), seg.Ciphertext...)
	adulterado.Ciphertext[0] ^= 0xff
	if _, err := openSegment(w, backupSubjectFor("eu-west"), adulterado); !errors.Is(err, ErrSegmentTampered) {
		t.Fatalf("conteudo adulterado tinha de ser ErrSegmentTampered; got %v", err)
	}
}

// trocaNoSegundoGet serve o blob verdadeiro na 1.ª leitura de `alvo` (a do VerifyManifest) e outro
// blob AUTÊNTICO — de outro segmento, sob a mesma KEK — nas seguintes: quem tem escrita no destino
// entre a verificação e a abertura.
type trocaNoSegundoGet struct {
	ImmutableStore
	alvo, troca string
	lidas       int
}

func (s *trocaNoSegundoGet) Get(ref string) ([]byte, error) {
	if ref == s.alvo {
		s.lidas++
		if s.lidas > 1 {
			return s.ImmutableStore.Get(s.troca)
		}
	}
	return s.ImmutableStore.Get(ref)
}

// TestAOS453_ORestauroReconfereOHashDoBlobQueAbre (revisão de segurança, TOCTOU): o RestoreTo volta a
// ler cada segmento depois do VerifyManifest; um blob trocado entretanto — mesmo autêntico e sob a
// mesma KEK (outra época, outro ciclo) — é ErrSegmentTampered, e nada é escrito.
func TestAOS453_ORestauroReconfereOHashDoBlobQueAbre(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("eu-west")
	custodia := audit.NewInMemoryKeyWrapper(nil)
	exp, err := NewExporter(src, dst, newSigner(t), WithKeyVault(custodia))
	if err != nil {
		t.Fatal(err)
	}
	seed(t, src, "run-a", 2, "a")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatal(err)
	}
	seed(t, src, "run-a", 2, "b")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatal(err)
	}
	m := exp.Manifest()
	troca := &trocaNoSegundoGet{ImmutableStore: dst, alvo: m.Segments[0].Ref, troca: m.Segments[1].Ref}
	rst, err := NewRestorer(troca, custodia, exp.Public())
	if err != nil {
		t.Fatal(err)
	}
	out := freshDest(t, "board-eu", "eu-west")
	if _, err := rst.RestoreTo(ctx, m, exp.Checkpoint(), 0, nil, out); !errors.Is(err, ErrSegmentTampered) {
		t.Fatalf("um blob trocado depois da verificacao tinha de ser ErrSegmentTampered; got %v", err)
	}
	if troca.lidas < 2 {
		t.Fatalf("o teste nao exercitou a 2.ª leitura (lidas=%d)", troca.lidas)
	}
	if streams, _ := out.Streams(); len(streams) != 0 {
		t.Fatalf("nada pode ser escrito; streams=%v", streams)
	}
}

// mustRestorer constrói o restaurador sobre dst com a custódia dada e a chave pública do exportador.
func mustRestorer(t *testing.T, dst ImmutableStore, vault audit.KeyVault, exp *Exporter) *Restorer {
	t.Helper()
	r, err := NewRestorer(dst, vault, exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	return r
}
