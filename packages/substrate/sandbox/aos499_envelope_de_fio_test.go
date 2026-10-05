package sandbox

// AOS-499 — O ENVELOPE DA SANDBOX COMO FICHEIRO DE FIO.
//
// O resultado de uma tool que corre na sandbox é o ENVELOPE que [encodeResult] escreve
// (`stdout_text`, ou `stdout` em base64, e `exit_code`), e não o documento cru. É esse envelope que
// o kernel designa como origem da saída de um run (ADR-038), que o nó devolve em `output`
// (AOS-498) e que o `aos-orq` desembrulha para medir (AOS-499).
//
// A revisão adversarial de 2026-10-05 (achado I1) mostrou que todos os testes da medição usavam
// tools que devolviam bytes crus, e que com o envelope real três das séries não mediam o que
// diziam. Estes ficheiros prendem a FORMA do envelope ao codificador real, byte a byte:
//
//   - este teste chama [encodeResult] e exige o ficheiro (ou reescreve-o, com
//     `AOS494_ACTUALIZAR_FIO=1` — o interruptor de todos os ficheiros de fio destes tickets);
//   - o teste do nó (`cmd/aos`, `TestAOS498_Sandbox_…`) semeia a sandbox com o documento de um
//     deles e exige que o `GET /runs/{id}` devolva exactamente os bytes do ficheiro;
//   - o teste do `aos-orq` (`TestAOS499_Envelope_…`) mede sobre eles — sem importar este pacote
//     e sem escrever o JSON à mão.
//
// Uma linha por ficheiro: a forma não depende de fins de linha.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// aos499Notas é a forma de um documento de produção: linhas, números, acentos, aspas e os três
// caracteres que o escape de HTML do JSON mexeria.
const aos499Notas = "# Notas da reunião de 15/08/2026\n\n- [ ] tarefa 12: rever o orçamento de \"2026\" até sexta\n- [x] tarefa 7: fechar o contrato <gVisor> & runsc\n- [ ] ligar ao fornecedor (ref. 4471) antes das 17h30\n\nTotal aprovado: 1250 EUR em 3 parcelas.\n"

// aos499Envelopes são os casos do fio: o que a tool escreveu no stdout e com que código saiu.
//
// `leitura` ⇒ o resultado não é montado à mão: é o que o DRIVER DE REFERÊNCIA devolve ao ler o
// documento `notes` do RootFS base, pelo ciclo de vida do [Launcher] — o mesmo caminho que o nó
// percorre. É aí que se vê o que um teste com `newResult` não mostra: a leitura devolve também o
// ficheiro como ARTEFACTO, e o envelope leva o documento duas vezes (em `stdout_text` e, em
// base64, em `artifacts`).
var aos499Envelopes = []struct {
	nome    string
	stdout  []byte
	exit    int
	leitura bool
}{
	{nome: "leitura-notas", stdout: []byte(aos499Notas), leitura: true},
	{nome: "leitura-em-falta", exit: 1, leitura: true},
	// O mesmo documento sem artefactos: só o stdout.
	{nome: "notas", stdout: []byte(aos499Notas)},
	// Uma só linha, sem números.
	{nome: "uma-linha", stdout: []byte("conteudo do documento notes")},
	// Um documento vazio NÃO é um resultado vazio: são os bytes do envelope.
	{nome: "vazio"},
	// A tool correu e saiu com erro: é uma chamada efectiva, e o envelope é designável.
	{nome: "saida-3", stdout: []byte("cat: notes: No such file or directory\n"), exit: 3},
	// Um stdout que não é UTF-8 viaja em base64, no outro campo.
	{nome: "binario", stdout: []byte{0xff, 0xfe, 0x00, 'a', 0x80}},
	// A mesma linha repetida: os tamanhos com que a revisão mostrou que comparar o texto final com
	// o ENVELOPE classificava como resumo uma transcrição byte a byte.
	{nome: "linhas-1", stdout: aos499Linhas(1)},
	{nome: "linhas-2", stdout: aos499Linhas(2)},
	{nome: "linhas-5", stdout: aos499Linhas(5)},
	{nome: "linhas-10", stdout: aos499Linhas(10)},
	{nome: "linhas-20", stdout: aos499Linhas(20)},
	{nome: "linhas-100", stdout: aos499Linhas(100)},
}

// aos499Linhas devolve um documento de `n` linhas iguais, com um número, aspas e acentos.
func aos499Linhas(n int) []byte {
	return bytes.Repeat([]byte("- [ ] tarefa número 12: rever o orçamento de \"2026\" até sexta\n"), n)
}

// aos499Leitura corre a leitura de `notes` no driver de referência, com o documento dado no
// RootFS base (nil ⇒ o documento não existe na microVM).
func aos499Leitura(t *testing.T, documento []byte) ExecResult {
	t.Helper()
	base := map[string][]byte{}
	if documento != nil {
		base["notes"] = documento
	}
	snap, err := NewSnapshot("img/aos499-fio", base)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	driver, err := NewDriver(DriverFake)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	launcher, err := NewLauncher(driver, WithEventSink(NewEventStoreSink(newStore(t))), WithSnapshot(snap))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	res, err := launcher.run(context.Background(), ExecRequest{RunID: "run-aos499-fio", StepID: "step-000001-tool-1",
		Call: ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"}})
	if err != nil {
		t.Fatalf("launcher.run: %v", err)
	}
	return res
}

// TestAOS499_EnvelopeDeFio prende cada ficheiro de `testdata/aos499_envelope/` ao que o
// codificador real escreve, e confirma que o descodificador o lê de volta.
func TestAOS499_EnvelopeDeFio(t *testing.T) {
	actualizar := os.Getenv("AOS494_ACTUALIZAR_FIO") == "1"
	pasta := filepath.Join("testdata", "aos499_envelope")
	conhecidos := map[string]bool{}
	for _, c := range aos499Envelopes {
		conhecidos[c.nome+".json"] = true
		res := newResult(c.stdout, nil, c.exit)
		if c.leitura {
			res = aos499Leitura(t, c.stdout)
			if !bytes.Equal(res.Stdout, c.stdout) || res.ExitCode != c.exit {
				t.Fatalf("%s: a leitura no driver de referencia devolveu (%q, %d); o caso diz (%q, %d)", c.nome, res.Stdout, res.ExitCode, c.stdout, c.exit)
			}
		}
		enc, err := encodeResult(res)
		if err != nil {
			t.Fatalf("%s: encodeResult: %v", c.nome, err)
		}
		if bytes.ContainsAny(enc, "\r\n") {
			t.Fatalf("%s: o envelope tem de ser uma so linha; veio %q", c.nome, enc)
		}
		caminho := filepath.Join(pasta, c.nome+".json")
		if actualizar {
			if err := os.MkdirAll(pasta, 0o755); err != nil {
				t.Fatalf("criar a pasta do fio: %v", err)
			}
			if err := os.WriteFile(caminho, append(append([]byte{}, enc...), '\n'), 0o644); err != nil {
				t.Fatalf("escrever %s: %v", caminho, err)
			}
		}
		quer, err := os.ReadFile(caminho)
		if err != nil {
			t.Fatalf("ficheiro do fio em falta (%v) — gera-o com AOS494_ACTUALIZAR_FIO=1", err)
		}
		if !bytes.Equal(bytes.TrimSuffix(quer, []byte("\n")), enc) {
			t.Fatalf("o codificador deixou de escrever o envelope que o no e o aos-orq leem nos testes deles (%s):\n  fio:   %s\n  agora: %s", caminho, quer, enc)
		}
		dec, err := decodeResult(enc)
		if err != nil || !bytes.Equal(dec.Stdout, c.stdout) || dec.ExitCode != c.exit || len(dec.Artifacts) != len(res.Artifacts) {
			t.Fatalf("%s: ida e volta = (%q, %d), %v; quero (%q, %d)", c.nome, dec.Stdout, dec.ExitCode, err, c.stdout, c.exit)
		}
	}
	// A pasta não tem ficheiros que este teste não gere: um envelope escrito à mão não é fio.
	entradas, err := os.ReadDir(pasta)
	if err != nil {
		t.Fatalf("ler %s: %v", pasta, err)
	}
	for _, e := range entradas {
		if !conhecidos[e.Name()] {
			t.Errorf("%s nao e gerado por este teste: um envelope escrito a mao nao prova a forma do codificador", e.Name())
		}
	}
}
