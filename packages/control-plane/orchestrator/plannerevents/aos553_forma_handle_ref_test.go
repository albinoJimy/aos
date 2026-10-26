package plannerevents

// aos553_forma_handle_ref_test.go — A FORMA `handle_ref` DO EVENTO DE PUBLICAÇÃO (AOS-553, ADR-045).
//
// O payload de uma aresta atravessada é a REFERÊNCIA que o kernel designou, não conteúdo. Tem forma
// própria porque quem audita o log do plano tem de poder distinguir uma aresta atravessada de uma
// entrega de conteúdo — a cadeia de confiança de quem a lê depois é outra.

import (
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

func TestAOS553_FormaHandleRefNoVocabulario(t *testing.T) {
	if !PayloadExtractionHandleRef.valid() {
		t.Fatal("handle_ref tem de ser uma forma de extraccao valida")
	}
	if PayloadExtractionHandleRef == PayloadExtractionRaw || PayloadExtractionHandleRef == PayloadExtractionSandboxStdoutText {
		t.Fatal("a forma nova tem de ser distinta das duas que ja existiam")
	}
	if PayloadExtraction("inventada").valid() {
		t.Fatal("o vocabulario tem de continuar fechado")
	}
}

// TestAOS553_ADigestDaReferenciaTemDeSerODaAncora — a invariante que separa as formas que entregam
// os bytes designados INTEIROS (`raw` e `handle_ref`) da que deriva um sub-conjunto
// (`sandbox_stdout_text`). Uma `handle_ref` cujo digest divergisse da âncora era uma referência que
// o consumidor não conseguiria conferir contra o produtor.
func TestAOS553_ADigestDaReferenciaTemDeSerODaAncora(t *testing.T) {
	const (
		ancora = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		outro  = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	contrato := plan.Output{Name: "documento", FromTool: "doc_read"}
	fonte := func(forma PayloadExtraction) *PayloadSource {
		return &PayloadSource{StepID: "step-000001-tool-1", AnchorDigest: ancora, AnchorBytes: 42, Extraction: forma}
	}
	// O caso bom: o digest do payload é o da âncora.
	if _, err := normalizeSource(contrato, PayloadRecordRef{Digest: ancora}, fonte(PayloadExtractionHandleRef)); err != nil {
		t.Fatalf("handle_ref com o digest da ancora: %v", err)
	}
	// E o controlo negativo, para as DUAS formas.
	for _, forma := range []PayloadExtraction{PayloadExtractionHandleRef, PayloadExtractionRaw} {
		_, err := normalizeSource(contrato, PayloadRecordRef{Digest: outro}, fonte(forma))
		if !errors.Is(err, ErrInvalidPayloadRef) {
			t.Errorf("%s com um digest diferente do da ancora tinha de ser recusada; veio %v", forma, err)
		}
		if err != nil && !strings.Contains(err.Error(), string(forma)) {
			t.Errorf("%s: a mensagem tem de nomear a forma; veio %v", forma, err)
		}
	}
	// E a forma que DERIVA um sub-conjunto continua a poder ter outro digest.
	if _, err := normalizeSource(contrato, PayloadRecordRef{Digest: outro}, fonte(PayloadExtractionSandboxStdoutText)); err != nil {
		t.Errorf("sandbox_stdout_text deriva um sub-conjunto e o seu digest difere por desenho: %v", err)
	}
}
