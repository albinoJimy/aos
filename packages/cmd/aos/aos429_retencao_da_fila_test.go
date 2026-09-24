package main

// aos429_retencao_da_fila_test.go — AOS-429.
//
// Três propriedades, e cada uma existe porque a sua ausência tinha uma consequência medida:
//
//  1. o objectivo — texto livre de uma pessoa — está CIFRADO em repouso quando há titular;
//  2. a marca de água dá EXACTAMENTE a mesma fila que ler o histórico inteiro;
//  3. o pedido entra no varredor de retenção, e SÓ quando há o que crypto-shredar.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// ── (1) A CIFRA ────────────────────────────────────────────────────────────────────────────

// TestAOS429ObjetivoNaoVaiEmClaroParaOLog é a asserção central do ticket.
//
// Não pergunta «o campo está preenchido?» — pergunta o que um adversário faria: **procura o
// texto do objectivo nos BYTES do facto gravado**. Um teste que só olhasse para os campos
// passaria se alguém deixasse o objectivo num campo novo, ou no `Detalhe`, ou numa mensagem.
func TestAOS429ObjetivoNaoVaiEmClaroParaOLog(t *testing.T) {
	const objectivo = "compilar-a-lista-de-clientes-em-atraso"
	const titular = "human:alice"

	vault := audit.NewInMemoryKeyVault(nil)
	node := &Node{DSARVault: vault}

	p := planRequestPayload{
		Versao:    planRequestSubmittedVersao,
		RunID:     "run-429-a",
		Objective: objectivo,
		Principal: titular,
	}
	selado, err := selarObjetivo(node, p)
	if err != nil {
		t.Fatalf("selar: %v", err)
	}

	bruto, err := json.Marshal(selado)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(bruto), objectivo) {
		t.Fatalf("o OBJECTIVO aparece em claro nos bytes do facto:\n%s\n"+
			"e daqui vai para o WAL e para os backups, fora do alcance do crypto-shredding", bruto)
	}
	// E o campo em claro tem de estar VAZIO — texto ao lado do ciphertext tornaria a cifra
	// decorativa, que é a forma mais cara de parecer seguro.
	if selado.Objective != "" {
		t.Errorf("Objective devia ficar vazio quando ha ciphertext, veio %q", selado.Objective)
	}
	if len(selado.ObjetivoSelado) == 0 {
		t.Fatal("com titular e custodia, o objectivo tinha de ficar selado")
	}

	// E ABRE, porque uma cifra que não decifra não serve o consumidor.
	claro, err := abrirObjetivo(node, selado)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	if claro != objectivo {
		t.Errorf("round-trip: got %q, quer %q", claro, objectivo)
	}
}

// TestAOS429AChaveDestruidaTornaOPedidoIlegivel prova o Art. 17 sobre a fila.
//
// É o teste que distingue «cifrado» de «cifrado E alcançável pelo apagamento»: se destruir a KEK
// do titular não tornasse o objectivo ilegível, a cifra seria só ofuscação.
func TestAOS429AChaveDestruidaTornaOPedidoIlegivel(t *testing.T) {
	const titular = "human:bob"
	vault := audit.NewInMemoryKeyVault(nil)
	node := &Node{DSARVault: vault}

	selado, err := selarObjetivo(node, planRequestPayload{
		RunID: "run-429-b", Objective: "apagar-isto-depois", Principal: titular,
	})
	if err != nil {
		t.Fatalf("selar: %v", err)
	}
	if _, err := abrirObjetivo(node, selado); err != nil {
		t.Fatalf("antes do erase tinha de abrir: %v", err)
	}

	// O MESMO `Delete` que o `/dsar/erase` chama.
	vault.Delete(titular)

	if claro, err := abrirObjetivo(node, selado); err == nil {
		t.Fatalf("depois de destruir a KEK do titular o objectivo continuou legivel: %q\n"+
			"entao o crypto-shredding nao alcanca a fila, e o Art. 17 nao se cumpre aqui", claro)
	}
}

// TestAOS429SemTitularFicaEmClaroENaoEExpiravel amarra as duas metades da postura do nó de
// referência — e que elas são COERENTES entre si.
//
// Sem gate soberano não há principal, logo não há KEK, logo o objectivo fica em claro. Se o
// `subjectOf` devolvesse o principal na mesma, o varredor contaria uma expiração que não expira
// nada: um verde que mede o vazio.
func TestAOS429SemTitularFicaEmClaroENaoEExpiravel(t *testing.T) {
	node := &Node{DSARVault: audit.NewInMemoryKeyVault(nil)}

	semTitular, err := selarObjetivo(node, planRequestPayload{RunID: "run-429-c", Objective: "sem-titular"})
	if err != nil {
		t.Fatalf("selar sem titular nao devia falhar: %v", err)
	}
	if !objetivoDeclaradoEmClaro(semTitular) {
		t.Error("sem titular o objectivo tinha de ficar em claro — nao ha KEK sob a qual selar")
	}

	bruto, _ := json.Marshal(semTitular)
	ev := eventstore.Event{Type: EventTypePlanRequestSubmitted, Payload: bruto}
	if s := subjectOf(ev); s != "" {
		t.Errorf("subjectOf devolveu %q para um pedido EM CLARO — o varredor contaria uma "+
			"expiracao que nao torna nada ilegivel", s)
	}
}

// TestAOS429PedidoSeladoEExpiravelPeloVarredor é a outra metade: com ciphertext, o pedido ENTRA.
func TestAOS429PedidoSeladoEExpiravelPeloVarredor(t *testing.T) {
	const titular = "human:carol"
	node := &Node{DSARVault: audit.NewInMemoryKeyVault(nil)}

	selado, err := selarObjetivo(node, planRequestPayload{
		RunID: "run-429-d", Objective: "objectivo", Principal: titular,
	})
	if err != nil {
		t.Fatalf("selar: %v", err)
	}
	bruto, _ := json.Marshal(selado)
	ev := eventstore.Event{Type: EventTypePlanRequestSubmitted, Payload: bruto}

	if s := subjectOf(ev); s != titular {
		t.Errorf("subjectOf = %q, quer %q — sem isto a fila NUNCA entra na lista de expiraveis "+
			"e o objectivo fica no log para sempre", s, titular)
	}
}

// ── (2) A MARCA DE ÁGUA ────────────────────────────────────────────────────────────────────

// TestAOS429MarcaDeAguaNaoMudaOResultado é o teste que torna o corte SEGURO.
//
// Não verifica que a marca «parece razoável»: constrói um log, projecta-o inteiro, corta-o na
// marca, projecta o resto, e exige que as duas filas sejam IGUAIS. Se alguma vez o corte saltar
// um pedido vivo, este teste diz qual.
func TestAOS429MarcaDeAguaNaoMudaOResultado(t *testing.T) {
	agora := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	// Um log com terminados à cabeça, um pendente pelo meio, e mais terminados a seguir — que é
	// o caso em que um corte ingénuo (cortar no MAIOR terminado) perderia o pendente.
	var log []eventstore.Event
	seq := uint64(0)
	acrescentar := func(ev eventstore.Event) {
		seq++
		ev.Seq = seq
		ev.Ts = agora.Add(-time.Hour).Format(time.RFC3339Nano)
		log = append(log, ev)
	}
	submeter := func(run string) {
		p, _ := json.Marshal(planRequestPayload{RunID: run, Objective: "obj-" + run})
		acrescentar(eventstore.Event{Type: EventTypePlanRequestSubmitted, StepID: prefixoPedido + run, Payload: p})
	}
	terminar := func(run string, ger int) {
		d, _ := json.Marshal(desfechoPayload{RunID: run, Classe: DesfechoTerminal})
		acrescentar(eventstore.Event{
			Type:    EventTypePlanRequestOutcome,
			StepID:  prefixoDesfecho + strconv.Itoa(ger) + "-" + run,
			Payload: d,
		})
	}

	submeter("t1")
	submeter("t2")
	submeter("vivo")
	submeter("t3")
	terminar("t1", 1)
	terminar("t2", 1)
	terminar("t3", 1)

	inteira, marca := projectarFilaComMarca(log, agora)

	// A marca tem de ser o PREFIXO CONTÍGUO: t1 e t2 terminaram, o `vivo` não — logo a marca
	// pára no `seq` do t2 (2), e NÃO salta para o t3 (4).
	if marca != 2 {
		t.Fatalf("marca = %d, quer 2 (prefixo contiguo de terminados)\n"+
			"cortar no maior terminado (4) deixaria de ler o `submitted` do pedido VIVO", marca)
	}

	// E agora a prova que interessa: cortar na marca dá a MESMA fila.
	var cortado []eventstore.Event
	for _, ev := range log {
		if ev.Seq > marca {
			cortado = append(cortado, ev)
		}
	}
	depois, _ := projectarFilaComMarca(cortado, agora)

	if len(inteira) != len(depois) {
		t.Fatalf("a fila mudou com o corte: inteira=%d depois=%d", len(inteira), len(depois))
	}
	for i := range inteira {
		if inteira[i].RunID != depois[i].RunID || inteira[i].Geracao != depois[i].Geracao {
			t.Errorf("posicao %d: inteira=%+v depois=%+v", i, inteira[i], depois[i])
		}
	}
	if len(inteira) != 1 || inteira[0].RunID != "vivo" {
		t.Errorf("a fila devia ter so o pedido vivo, veio %+v", inteira)
	}
}

// TestAOS429MarcaNuncaRecua: duas projecções concorrentes que terminem fora de ordem não podem
// fazer a marca descer — e, sobretudo, um valor menor não a pode fazer saltar para a frente.
func TestAOS429MarcaNuncaRecua(t *testing.T) {
	var m marcaDeAgua
	m.avancar(10)
	m.avancar(3)
	if got := m.desde(); got != 11 {
		t.Errorf("desde() = %d, quer 11 — uma marca que recua so custa uma leitura a mais, "+
			"mas o CAS existe para que nunca avance de mais", got)
	}
}

// ── (3) NÃO SE APAGA NADA, E ESTÁ ESCRITO PORQUÊ ───────────────────────────────────────────

// TestAOS429OContratoDoEventStoreNaoTemRemocao é um guard de FONTE, e é a prova do critério
// «a razão de não sair está escrita».
//
// Se alguém acrescentar `Delete`/`Truncate`/`Purge` ao contrato, este teste fica vermelho — e
// aí a decisão do AOS-429 (não encolher o log) tem de ser reavaliada, em vez de continuar
// escrita num comentário que deixou de ser verdade.
func TestAOS429OContratoDoEventStoreNaoTemRemocao(t *testing.T) {
	fonte := lerFonteDeTeste(t, "eventstore_port.go")
	for _, proibido := range []string{"Delete(", "Truncate(", "Purge(", "Compact("} {
		if strings.Contains(fonte, proibido) {
			t.Errorf("o contrato do Event Store passou a ter %q\n"+
				"o AOS-429 assenta em NAO haver remocao (um log encadeado por hash de que se "+
				"removem entradas deixa de ser tamper-evident). Reavalia a decisao, nao o teste.", proibido)
		}
	}
}

// lerFonteDeTeste lê um ficheiro do próprio pacote. Um guard de fonte é a única forma de fixar
// uma AUSÊNCIA: não há como afirmar em runtime que um método não existe numa interface. É o
// mesmo idioma de `aos255_budget_scope_test.go` e companhia.
func lerFonteDeTeste(t *testing.T, nome string) string {
	t.Helper()
	b, err := os.ReadFile(nome)
	if err != nil {
		t.Fatalf("ler %s: %v", nome, err)
	}
	return string(b)
}

// ── (4) A EXPIRAÇÃO, PONTA-A-PONTA, COM RELÓGIO INJECTADO ──────────────────────────────────

// TestAOS429AFilaExpiraPeloVarredorComposto é o critério que amarra tudo o resto.
//
// Os testes acima provam as peças: que o objectivo é selado, que destruir a KEK o torna
// ilegível, que o `subjectOf` o reconhece. Nenhum deles prova que o VARREDOR COMPOSTO NO NÓ
// chega lá — e essa é a diferença entre um mecanismo que existe e um que corre.
//
// Submete um pedido pela rota real, corre o `ExpirationJob` do nó, e exige que o objectivo
// deixe de abrir.
//
// # O RELÓGIO
//
// Injectado, como o critério exige, pelo `cfg.RetentionClock` de `newRetentionNode` — um
// instante em 2100, pelo que a idade ultrapassa sempre o TTL. Não se usa `testkit.ManualClock`:
// `packages/cmd/aos` não importa o `testkit` (só tem o `replace`, sem `require`), e acrescentar
// uma dependência de módulo para obter uma constante de tempo seria pagar caro por nada quando
// o pacote já tem o idioma estabelecido em `aos213_legalhold_expiration_test.go`. O que o
// critério pede — que o relógio seja INJECTADO e não `time.Now()` — está cumprido.
func TestAOS429AFilaExpiraPeloVarredorComposto(t *testing.T) {
	ctx := context.Background()
	node := newRetentionNode(t)
	_, h := newAPI(t, node)

	rec := postReq(h, "/plans", planRequest{RunID: "run-429-expira", Objective: "objectivo-a-expirar"}, euReaderHeaders())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /plans devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}

	// Lê o facto tal como ficou gravado.
	evs, err := node.EventStore.Read(ctx, planRequestStream, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("devia haver 1 facto: err=%v n=%d", err, len(evs))
	}
	p := planRequestPayloadDeTeste(t, evs[0].Payload)
	if len(p.ObjetivoSelado) == 0 {
		t.Fatal("num no com gate soberano o objectivo tinha de ficar selado")
	}

	// NÃO-VÁCUO: antes da passagem, abre.
	if _, err := abrirObjetivo(node, p); err != nil {
		t.Fatalf("antes da expiracao o objectivo tinha de abrir: %v", err)
	}

	// A PASSAGEM REAL do varredor composto no nó.
	rel, err := node.ExpirationJob.Run(ctx)
	if err != nil {
		t.Fatalf("ExpirationJob.Run: %v", err)
	}
	if rel.Expired < 1 {
		t.Fatalf("o varredor nao expirou nada (%+v)\n"+
			"sem o `subjectOf` a reconhecer `planrequest.submitted`, a fila nunca entra na lista", rel)
	}

	// O OBJECTIVO DEIXOU DE SER LEGÍVEL. É este o apagamento — por ilegibilidade, não por
	// remoção: o evento continua no log, e o `TestAOS429OContratoDoEventStoreNaoTemRemocao`
	// explica porque é que tem de continuar.
	if claro, err := abrirObjetivo(node, p); err == nil {
		t.Fatalf("depois da passagem do varredor o objectivo continuou legivel: %q", claro)
	}

	// E O EVENTO CONTINUA LÁ, intacto. Não é um detalhe: se desaparecesse, a cadeia de hash
	// deixaria de fechar e o log deixaria de ser tamper-evident.
	depois, err := node.EventStore.Read(ctx, planRequestStream, 1)
	if err != nil || len(depois) != 1 {
		t.Fatalf("o evento tinha de continuar no log: err=%v n=%d", err, len(depois))
	}
}
