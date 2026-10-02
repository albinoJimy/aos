package main

// AOS-359 — UM COMANDO DE LEITURA DO `aos-orq` APAGAVA BYTES CONFIRMADOS DO WAL.
//
// O AOS-347 provou, ao nível do pacote `eventstore`, que [eventstore.Open] trunca a
// cauda parcial e que [eventstore.OpenReadOnly] não lhe toca. A varredura de chamadores
// que acompanhou esse ticket ficou-se pelo módulo `cmd/aos`: a via de leitura do
// `aos-orq` (`substrato.abrirParaLeitura`, que serve `inspect` e `plans`) continuou em
// [eventstore.Open].
//
// Estes testes medem a via DESTE binário, não a do pacote. É a diferença entre «o
// abridor só-leitura existe» e «o comando de leitura usa-o».

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// walComCaudaRasgada escreve n eventos confirmados e deixa a seguir bytes parciais — o
// estado em que um write foi interrompido, e exactamente aquele em que um operador vai
// inspeccionar. Devolve o caminho e a dimensão do ficheiro com a cauda já lá.
func walComCaudaRasgada(t *testing.T, n int) (string, int64) {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "events.wal")

	escritor, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatalf("preparar WAL: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := escritor.Append(context.Background(), "run-359", eventstore.EventInput{
			Type:    "plan.node.dispatched",
			Payload: json.RawMessage(`{"n":` + string(rune('0'+i)) + `}`),
			RunID:   "run-359",
			StepID:  "passo-" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := escritor.Close(); err != nil {
		t.Fatalf("fechar escritor: %v", err)
	}

	f, err := os.OpenFile(caminho, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("abrir para rasgar a cauda: %v", err)
	}
	// Bytes que não formam um registo: é o que um write interrompido deixa para trás.
	if _, err := f.Write([]byte{0x7b, 0x22, 0x74, 0x79}); err != nil {
		t.Fatalf("escrever cauda parcial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("fechar: %v", err)
	}

	fi, err := os.Stat(caminho)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return caminho, fi.Size()
}

// TestAOS359_AbrirParaLeituraNaoEncolheOWAL é o teste que falha com [eventstore.Open] e
// passa com [eventstore.OpenReadOnly]. Mede bytes no disco, que é o facto que interessa:
// um comando de leitura não pode mudar o ficheiro que foi lá ver.
func TestAOS359_AbrirParaLeituraNaoEncolheOWAL(t *testing.T) {
	caminho, antes := walComCaudaRasgada(t, 3)

	sub := substrato{wal: caminho}
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura sobre um WAL com a cauda rasgada = %v — inspeccionar tem de ser possível precisamente neste estado", err)
	}
	defer func() { _ = fechar() }()

	fi, err := os.Stat(caminho)
	if err != nil {
		t.Fatalf("stat depois de abrir: %v", err)
	}
	if fi.Size() != antes {
		t.Fatalf("o WAL passou de %d para %d bytes ao ser ABERTO PARA LEITURA: um comando de leitura truncou o ficheiro (AOS-359)", antes, fi.Size())
	}
	_ = store
}

// TestAOS359_AbrirParaLeituraContinuaALerOsEventosConfirmados impede a correcção
// trivial-e-errada: um abridor que não toca no ficheiro por não ler nada dele serviria
// a asserção acima e não serviria o operador.
func TestAOS359_AbrirParaLeituraContinuaALerOsEventosConfirmados(t *testing.T) {
	caminho, _ := walComCaudaRasgada(t, 3)

	sub := substrato{wal: caminho}
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura: %v", err)
	}
	defer func() { _ = fechar() }()

	evs, err := store.Read(context.Background(), "run-359", 1)
	if err != nil {
		t.Fatalf("Read do inspector: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("o inspector leu %d eventos, quero os 3 confirmados — a cauda rasgada não é evento de ninguém", len(evs))
	}
}

// TestAOS359_AbrirParaLeituraRecusaEscrever fixa a propriedade que substitui a
// convenção documentada: a via de leitura deste binário não tem por onde escrever, quer
// alguém lhe chame um comando de leitura ou não.
func TestAOS359_AbrirParaLeituraRecusaEscrever(t *testing.T) {
	caminho, _ := walComCaudaRasgada(t, 1)

	sub := substrato{wal: caminho}
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura: %v", err)
	}
	defer func() { _ = fechar() }()

	if _, err := store.Append(context.Background(), "run-359", eventstore.EventInput{
		Type:    "plan.node.dispatched",
		Payload: json.RawMessage(`{"intruso":true}`),
		RunID:   "run-359",
		StepID:  "passo-intruso",
	}); !errors.Is(err, eventstore.ErrReadOnly) {
		// A asserção é pelo erro NOMEADO, não por «falhou»: um Append que passe a falhar
		// por validação, por store fechado ou por contexto deixaria este teste verde com
		// a propriedade perdida.
		t.Fatalf("o Append pela via de LEITURA deu %v, quero ErrReadOnly — a via de inspecção ganhou uma cabeça de escrita (AOS-347/AOS-359)", err)
	}
}

// TestAOS359_AbrirParaLeituraNaoApagaEventoConfirmado é a afirmação do ticket na sua
// forma forte: não «o ficheiro encolheu», mas «um evento que o Append confirmou
// desapareceu por causa de um comando de LEITURA». Corrompe-se um byte DENTRO do último
// registo confirmado — o replay pára ali, `contaOrfaos` não acha registo íntegro depois,
// a guarda fail-closed não dispara, e o abridor de escrita trunca por cima dele.
func TestAOS359_AbrirParaLeituraNaoApagaEventoConfirmado(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "events.wal")

	escritor, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatalf("preparar WAL: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := escritor.Append(context.Background(), "run-359", eventstore.EventInput{
			Type:    "plan.node.dispatched",
			Payload: json.RawMessage(`{"n":` + string(rune('0'+i)) + `}`),
			RunID:   "run-359",
			StepID:  "passo-" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := escritor.Close(); err != nil {
		t.Fatalf("fechar escritor: %v", err)
	}

	bruto, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	antes := int64(len(bruto))
	// Um byte trocado dentro do ÚLTIMO registo, que o Append já tinha confirmado.
	bruto[len(bruto)-12] ^= 0xff
	if err := os.WriteFile(caminho, bruto, 0o600); err != nil {
		t.Fatalf("regravar WAL: %v", err)
	}

	sub := substrato{wal: caminho}
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura: %v", err)
	}
	defer func() { _ = fechar() }()

	fi, err := os.Stat(caminho)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != antes {
		t.Fatalf("o WAL passou de %d para %d bytes: o comando de LEITURA apagou %d bytes de um registo que o Append confirmou (AOS-359)", antes, fi.Size(), antes-fi.Size())
	}
	_ = store
}

// TestAOS359_InspeccaoComEscritorVivoNaoEnvenenaOEscritor é o cenário que o critério de
// aceitação nomeia: o escritor está VIVO, como está quando alguém inspecciona a meio de
// um incidente. O abridor de escrita ganhava aqui uma SEGUNDA cabeça de append, que
// atribui seqs em colisão com os do escritor — e o arranque seguinte recusava o WAL
// inteiro com E_RESTORE_ORDER. Não chega o ficheiro não encolher: tem de continuar
// replayável.
func TestAOS359_InspeccaoComEscritorVivoNaoEnvenenaOEscritor(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "events.wal")

	escritor, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatalf("preparar WAL: %v", err)
	}
	apensar := func(store eventstore.EventStore, n int) {
		t.Helper()
		if _, err := store.Append(context.Background(), "run-359", eventstore.EventInput{
			Type:    "plan.node.dispatched",
			Payload: json.RawMessage(`{"n":` + string(rune('0'+n)) + `}`),
			RunID:   "run-359",
			StepID:  "passo-" + string(rune('0'+n)),
		}); err != nil {
			t.Fatalf("append %d: %v", n, err)
		}
	}
	apensar(escritor, 0)
	apensar(escritor, 1)

	// O inspector olha para o MESMO WAL, com o escritor vivo.
	sub := substrato{wal: caminho}
	inspector, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura com o escritor vivo = %v — inspeccionar durante um incidente é o caso de uso", err)
	}
	evs, err := inspector.Read(context.Background(), "run-359", 1)
	if err != nil {
		t.Fatalf("Read do inspector: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("inspector leu %d eventos, quero 2", len(evs))
	}

	// O gesto que envenenava: o inspector escreve. Com [eventstore.OpenReadOnly] isto
	// devolve erro e não chega ao ficheiro; com [eventstore.Open] tinha SUCESSO, com um
	// seq atribuído pela segunda cabeça — em colisão com o do escritor vivo. O erro é
	// deliberadamente ignorado aqui: o que este teste mede não é a recusa (isso é o
	// TestAOS359_AbrirParaLeituraRecusaEscrever), é o ESTADO EM QUE O FICHEIRO FICA.
	_, _ = inspector.Append(context.Background(), "run-359", eventstore.EventInput{
		Type:    "plan.node.dispatched",
		Payload: json.RawMessage(`{"inspector":true}`),
		RunID:   "run-359",
		StepID:  "passo-do-inspector",
	})
	if err := fechar(); err != nil {
		t.Fatalf("fechar inspector: %v", err)
	}

	// O escritor continua a escrever DEPOIS da inspecção.
	apensar(escritor, 2)
	if err := escritor.Close(); err != nil {
		t.Fatalf("fechar escritor: %v", err)
	}

	// E o arranque seguinte tem de reconstruir os três — é aqui que a cabeça concorrente
	// se manifestava, com E_RESTORE_ORDER.
	reaberto, err := eventstore.OpenReadOnly(caminho)
	if err != nil {
		t.Fatalf("reabrir o WAL depois da inspecção = %v — a inspecção envenenou o escritor (AOS-359)", err)
	}
	defer func() { _ = reaberto.Close() }()
	finais, err := reaberto.Read(context.Background(), "run-359", 1)
	if err != nil {
		t.Fatalf("Read depois de reabrir: %v", err)
	}
	if len(finais) != 3 {
		t.Fatalf("depois da inspecção o WAL tem %d eventos, quero 3 — a inspecção comeu ou duplicou eventos do escritor", len(finais))
	}
}

// TestAOS359_InspeccaoComEscritorVivoNemEncolheNemParaOEscritor é a CONJUNÇÃO que o
// critério de aceitação pede e que os testes acima partiam em dois: escritor VIVO **e**
// ficheiro a encolher. É o cenário que o Contexto da epic lidera e que estava afirmado
// em prosa e medido por coisa nenhuma — a inspecção trunca por baixo de um escritor vivo,
// o tamanho em memória fica à frente do ficheiro, e o WAL deixa de aceitar escritas.
func TestAOS359_InspeccaoComEscritorVivoNemEncolheNemParaOEscritor(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "events.wal")

	escritor, err := eventstore.Open(caminho)
	if err != nil {
		t.Fatalf("preparar WAL: %v", err)
	}
	defer func() { _ = escritor.Close() }()
	for i := 0; i < 3; i++ {
		if _, err := escritor.Append(context.Background(), "run-359", eventstore.EventInput{
			Type:    "plan.node.dispatched",
			Payload: json.RawMessage(`{"n":` + string(rune('0'+i)) + `}`),
			RunID:   "run-359",
			StepID:  "passo-" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Corrupção DENTRO do último registo confirmado, com o escritor ainda vivo — o estado
	// que um fsync interrompido deixa e em que alguém vai inspeccionar.
	bruto, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler WAL: %v", err)
	}
	antes := int64(len(bruto))
	bruto[len(bruto)-12] ^= 0xff
	if err := os.WriteFile(caminho, bruto, 0o600); err != nil {
		t.Fatalf("regravar WAL: %v", err)
	}

	sub := substrato{wal: caminho}
	inspector, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		t.Fatalf("abrirParaLeitura: %v", err)
	}
	_ = inspector
	if err := fechar(); err != nil {
		t.Fatalf("fechar inspector: %v", err)
	}

	fi, err := os.Stat(caminho)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != antes {
		t.Fatalf("com o escritor VIVO, o WAL passou de %d para %d bytes por causa de um comando de leitura (AOS-359)", antes, fi.Size())
	}

	// E a metade que a dimensão do ficheiro não mede: o escritor continua a escrever.
	if _, err := escritor.Append(context.Background(), "run-359", eventstore.EventInput{
		Type:    "plan.node.dispatched",
		Payload: json.RawMessage(`{"depois":true}`),
		RunID:   "run-359",
		StepID:  "passo-depois-da-inspeccao",
	}); err != nil {
		t.Fatalf("o escritor vivo deixou de aceitar escritas depois da inspecção: %v", err)
	}
}
