package sandbox

// AOS-501 — O CORPUS ADVERSARIAL DENTRO DO ENVELOPE REAL (segundo elo da cadeia de fio).
//
// Com a entrega por referência, o que o nó seguinte de um plano recebe é extraído pelo `aos-orq`
// do ENVELOPE que [encodeResult] escreve. O ADR-038 §5 põe como condição para ligar a entrega que
// o corpus de injecção da suite de segurança corra por esse caminho. A suite não pode importar o
// `aos-orq`, pelo que o caminho é uma cadeia de ficheiros de fio (ver
// `security-tests/aos501_corpus_por_referencia_test.go`); este é o elo da sandbox:
//
//   - LÊ `security-tests/testdata/aos501_corpus_por_referencia/documentos.json` — os documentos
//     adversariais que a suite de segurança deriva do corpus versionado;
//   - lê cada um pelo DRIVER DE REFERÊNCIA, como documento do RootFS base (o caminho que o nó
//     percorre, e que devolve o ficheiro também como artefacto), e codifica o resultado com o
//     codificador real;
//   - ESCREVE (com `AOS494_ACTUALIZAR_FIO=1`) ou EXIGE `testdata/aos501_corpus_envelope/
//     envelopes.json`, que o `aos-orq` consome.
//
// Nenhum envelope é escrito à mão: um documento que o codificador passasse a transportar de
// outra maneira avermelhava este teste e, com ele, os dois elos seguintes.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// aos501DocumentoDoCorpus é um documento do fio da suite de segurança (só o que este elo lê).
type aos501DocumentoDoCorpus struct {
	ID    string `json:"id"`
	Texto string `json:"texto"`
}

// aos501EnvelopeDoCorpus é o envelope de um documento do corpus, tal como o codificador real o
// escreve para a leitura desse documento.
type aos501EnvelopeDoCorpus struct {
	ID       string `json:"id"`
	Envelope string `json:"envelope"`
}

// TestAOS501_CorpusEmEnvelope prende cada envelope do fio ao que o codificador real escreve para
// a leitura do documento correspondente.
func TestAOS501_CorpusEmEnvelope(t *testing.T) {
	cru, err := os.ReadFile(filepath.Join("..", "..", "security-tests", "testdata", "aos501_corpus_por_referencia", "documentos.json"))
	if err != nil {
		t.Fatalf("fio dos documentos do corpus em falta (%v) — gera-o em packages/security-tests com AOS494_ACTUALIZAR_FIO=1", err)
	}
	var fio struct {
		CorpusVersion string                    `json:"corpus_version"`
		Documentos    []aos501DocumentoDoCorpus `json:"documentos"`
	}
	if err := json.Unmarshal(cru, &fio); err != nil || len(fio.Documentos) == 0 {
		t.Fatalf("fio dos documentos ilegivel ou vazio: %v (%d documentos)", err, len(fio.Documentos))
	}

	envelopes := make([]aos501EnvelopeDoCorpus, 0, len(fio.Documentos))
	for _, d := range fio.Documentos {
		if !utf8.ValidString(d.Texto) || d.Texto == "" {
			t.Fatalf("%s: o documento do corpus tem de ser texto UTF-8 nao vazio para viajar em stdout_text", d.ID)
		}
		// A LEITURA REAL: o documento no RootFS base, lido pelo driver de referência pelo ciclo de
		// vida do Launcher.
		res := aos499Leitura(t, []byte(d.Texto))
		if !bytes.Equal(res.Stdout, []byte(d.Texto)) || res.ExitCode != 0 {
			t.Fatalf("%s: a leitura no driver de referencia nao devolveu o documento byte a byte (exit=%d, %d bytes; quero %d)", d.ID, res.ExitCode, len(res.Stdout), len(d.Texto))
		}
		enc, err := encodeResult(res)
		if err != nil {
			t.Fatalf("%s: encodeResult: %v", d.ID, err)
		}
		if bytes.ContainsAny(enc, "\r\n") {
			t.Fatalf("%s: o envelope tem de ser uma so linha; veio %q", d.ID, enc)
		}
		// Ida e volta: o descodificador da sandbox devolve o documento byte a byte.
		dec, err := decodeResult(enc)
		if err != nil || !bytes.Equal(dec.Stdout, []byte(d.Texto)) || dec.ExitCode != 0 {
			t.Fatalf("%s: ida e volta do envelope = (%d bytes, %d), %v", d.ID, len(dec.Stdout), dec.ExitCode, err)
		}
		// E o documento vai no campo de TEXTO (é dele que o `aos-orq` extrai o que entrega).
		var campos struct {
			Texto *string `json:"stdout_text"`
			Saida *int    `json:"exit_code"`
		}
		if err := json.Unmarshal(enc, &campos); err != nil || campos.Texto == nil || *campos.Texto != d.Texto || campos.Saida == nil || *campos.Saida != 0 {
			t.Fatalf("%s: o envelope tem de levar o documento em stdout_text, com exit_code 0: %s (%v)", d.ID, enc, err)
		}
		envelopes = append(envelopes, aos501EnvelopeDoCorpus{ID: d.ID, Envelope: string(enc)})
	}

	agora, err := json.MarshalIndent(struct {
		CorpusVersion string                   `json:"corpus_version"`
		Envelopes     []aos501EnvelopeDoCorpus `json:"envelopes"`
	}{fio.CorpusVersion, envelopes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	agora = append(agora, '\n')
	caminho := filepath.Join("testdata", "aos501_corpus_envelope", "envelopes.json")
	if os.Getenv("AOS494_ACTUALIZAR_FIO") == "1" {
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatalf("criar a pasta do fio: %v", err)
		}
		if err := os.WriteFile(caminho, agora, 0o644); err != nil {
			t.Fatalf("escrever %s: %v", caminho, err)
		}
	}
	quer, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(quer, []byte("\r\n"), []byte("\n")), agora) {
		t.Fatalf("o codificador, ou o corpus, deixou de dar os envelopes que o aos-orq le nos testes dele (%s): regenera a cadeia com AOS494_ACTUALIZAR_FIO=1", caminho)
	}
	t.Logf("corpus %s: %d documentos -> %d envelopes do codificador real", fio.CorpusVersion, len(fio.Documentos), len(envelopes))
}
