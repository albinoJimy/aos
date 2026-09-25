package main

// retoma_do_plano.go — UMA RETOMA CORRE PELO DOCUMENTO VALIDADO, NUNCA POR UMA DECOMPOSIÇÃO NOVA
// (AOS-442).
//
// # O DEFEITO, MEDIDO EM PRODUÇÃO
//
// O `consume` corria SEMPRE `serve --goal`. Numa retoma — uma geração nova depois de uma falha
// transitória, com o plano já aprovado — o modelo decompunha outra vez, saía outro organigrama, e o
// gate recusava-o: o `plan.validated` é de primeira escrita e a decisão aprova UM hash (AOS-412).
// Saída 7, terminal. Uma falha passageira tornava-se definitiva, e pagava uma decomposição ao
// modelo (`plan-e2e-437-1790336067`; resíduo 1 do AOS-438).
//
// # ONDE VIVE O DOCUMENTO VALIDADO, E PORQUE NÃO É NO LOG
//
// O log só leva o HASH do documento (ADR-005: o `PlanDocument` é conteúdo untrusted escrito por um
// modelo). O documento cru vive num ficheiro — o mesmo `--plan-out` que o `decide` já usava para o
// pendente —, e o `serve` passa a escrevê-lo, de forma atómica, sempre que o plano é validado, ANTES
// de apensar os factos (ver `gatearPlano`). O `consume` dá a cada pedido um ficheiro seu, numa pasta
// do volume do `aos-orq` ([pastaDosPlanos]), e apaga-o quando o pedido fecha.
//
// # A REGRA: O LOG DECIDE, O FICHEIRO SÓ É USADO SE FOR O QUE O LOG ANCORA
//
// O `consume` lê o log do run ANTES de correr o `serve` ([escolherOrigem]):
//
//   - sem `plan.validated`: decompõe (`--goal`). Um documento que exista para o run NÃO é usado —
//     não há âncora contra a qual o confrontar — e é substituído pelo que a decomposição validar;
//   - validado e decidido: `--plan-doc`, e o gate decide (aprovado corre, recusado fecha com 7);
//   - validado, sem decisão, dentro do prazo, e o plano exige humano: continua à espera — reporta-se
//     `aguarda_humano` SEM correr o `serve` (sem posse, sem modelo);
//   - validado, sem decisão, e o plano NÃO exige humano: ficou a meio de uma auto-aprovação (uma
//     falha entre o `plan.validated` e a decisão). `--plan-doc`, e o gate auto-aprova;
//   - validado, sem decisão, fora do prazo: 7, sem `serve`;
//   - validado e sem documento: 7, sem `serve` — uma decomposição nova seria recusada pelo gate.
//
// Um documento que não descodifique, ou cujo hash não seja o do `plan.validated`, é recusado com o
// código 10 — terminal, porque apresentá-lo outra vez dá sempre o mesmo.
//
// # O QUE ISTO NÃO FAZ: GOVERNAR
//
// O `serve --plan-doc` passa pela MESMA validação estrutural e pelo MESMO gate que o `--goal`, e
// exige que o documento seja o do `plan.validated` do run (ver `materializar`). Esta escolha só diz
// POR ONDE se entra no gate — nunca o salta.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/runlifecycle"
)

// origemDoPlano diz por onde o `serve` de um pedido obtém o documento do plano.
type origemDoPlano struct {
	// documento é o ficheiro do documento DESTE pedido: lido com `--plan-doc`, ou escrito com
	// `--plan-out` quando o `serve` decompõe. Vem preenchido mesmo quando não há `serve` a correr,
	// para o `consume` o poder apagar quando o pedido fecha.
	documento string
	// porDocumento ⇒ `--plan-doc documento`; senão `--goal` com `--plan-out documento`.
	porDocumento bool
	// jaValidado ⇒ o log do run já tinha `plan.validated`: um `aguarda_humano` daqui é uma
	// RE-VERIFICAÇÃO, e não gasta o `--max` da drenagem.
	jaValidado bool
	// decomposeFixture é o fixture NÃO-PRODUÇÃO do decompositor, passado ao `--goal`.
	decomposeFixture string
}

// pastaDosPlanos resolve onde o `consume` guarda os documentos. Por omissão, `planos/` ao lado do
// WAL — no mesmo volume, que é o que tem de sobreviver entre drenagens.
//
// Sobre o substrato REPLICADO não há WAL de onde a derivar, e exige-se. E tem de ser PARTILHADA
// entre as réplicas: uma pasta local a cada uma faria a re-oferta, noutra réplica, ver um plano
// validado sem documento — e fechá-lo com 7. Não se verifica aqui (resíduo do AOS-442).
func pastaDosPlanos(explicita string, sub substrato) (string, error) {
	if explicita != "" {
		return explicita, nil
	}
	if sub.wal != "" {
		return filepath.Join(filepath.Dir(sub.wal), "planos"), nil
	}
	return "", errors.New("consume sobre --nats exige --plan-dir (PARTILHADA entre as replicas): sem sitio para o " +
		"documento de cada plano, uma retoma teria de decompor de novo — e o gate recusaria o organigrama novo (AOS-442)")
}

// caminhoDoDocumento é o ficheiro do documento de um pedido: o SHA-256 do `run_id`, em hex.
//
// O `run_id` vem do nó e pode ter `/` (representável por decisão do AOS-424), `:` e qualquer
// comprimento que o Event Store aceite. Escapá-lo deixava um `run_id` longo passar os 255 bytes de
// um nome de ficheiro (`ENAMETOOLONG`). O resumo tem sempre 64 caracteres, não tem separadores, e
// dois `run_id` distintos não partilham ficheiro.
func caminhoDoDocumento(pasta, runID string) (string, error) {
	if runID == "" {
		return "", errors.New("documento do plano: run_id vazio")
	}
	soma := sha256.Sum256([]byte(runID))
	return filepath.Join(pasta, hex.EncodeToString(soma[:])+".plan.json"), nil
}

// retratoDoPedido é o que o `consume` sabe de um pedido antes de decidir por onde o correr.
type retratoDoPedido struct {
	haDocumento bool
	validado    bool
	decidido    bool
	expirado    bool
}

// escolherOrigem é a regra — ver o cabeçalho deste ficheiro. `exigeHumano` só é chamada no caso
// validado-sem-decisão-dentro-do-prazo, e é a única que lê o documento e o snapshot.
//
// Os erros devolvidos SÃO o desfecho do pedido, sem `serve`: [errPlanoPendente] (6, continua à
// espera), [errDecisaoRecusada] (7) ou [errDocumentoDoPlanoRecusado] (10).
func escolherOrigem(documento string, r retratoDoPedido, fixture string, exigeHumano func() (bool, error)) (origemDoPlano, error) {
	o := origemDoPlano{documento: documento, jaValidado: r.validado}
	switch {
	case !r.validado:
		o.decomposeFixture = fixture
		return o, nil
	case !r.haDocumento:
		return o, fmt.Errorf("%w: o plano deste run ja foi validado e o seu documento nao esta em %s — "+
			"uma decomposicao nova produziria outro organigrama, que o gate recusaria; retome a mao com "+
			"`aos-orq serve --plan-doc <documento>` se o tiver (AOS-442)", errDecisaoRecusada, documento)
	case r.decidido:
		o.porDocumento = true
		return o, nil
	case r.expirado:
		return o, fmt.Errorf("%w: o prazo do pendente expirou (%s) sem decisao — um plano novo exige um run novo",
			errDecisaoRecusada, ttlPendentePorOmissao)
	}
	humano, err := exigeHumano()
	if err != nil {
		return o, err
	}
	if humano {
		return o, fmt.Errorf("%w: sem decisao no log (re-verificado sem correr o serve)", errPlanoPendente)
	}
	// Sem nós de risco e sem decisão: a auto-aprovação ficou a meio. O gate acaba-a.
	o.porDocumento = true
	return o, nil
}

// origemDoPedido aplica [escolherOrigem] a um pedido reclamado.
func origemDoPedido(sub substrato, pasta, runID, fixture, snapshot string, agora time.Time) (origemDoPlano, error) {
	doc, err := caminhoDoDocumento(pasta, runID)
	if err != nil {
		return origemDoPlano{}, err
	}
	var r retratoDoPedido
	_, err = os.Stat(doc)
	switch {
	case err == nil:
		r.haDocumento = true
	case !errors.Is(err, fs.ErrNotExist):
		// Existe e não se consegue ver: nem o `serve --plan-doc` o leria. É FAIL-CLOSED e terminal
		// — decompor por cima de um documento que talvez exista era o defeito que isto fecha, e
		// retentar um ficheiro que não se vê é o laço à cabeça da fila.
		return origemDoPlano{documento: doc}, fmt.Errorf("%w: %q inacessivel: %v", errDocumentoDoPlanoRecusado, doc, err)
	}
	planoID := runID + "-plan" // a mesma convenção do `serve` sem `--plan` e do `decide`
	estado, err := lerEstadoDoPlano(sub, planoID)
	if err != nil {
		if !r.haDocumento {
			// Sem documento, a leitura só serviria para não pagar ao modelo uma recusa certa. Se
			// não se consegue ler, o `serve --goal` corre e o gate — que lê o mesmo log, sob a
			// posse do run — decide. Dito em voz alta, porque é ele que fica a arbitrar.
			fmt.Fprintf(os.Stderr, "aos-orq: estado do plano de %s ilegivel antes do serve (%v); "+
				"segue por --goal e o gate decide\n", runID, err)
			return origemDoPlano{documento: doc, decomposeFixture: fixture}, nil
		}
		return origemDoPlano{documento: doc}, fmt.Errorf("estado do plano de %s: %w", runID, err)
	}
	r.validado = estado.Validated()
	r.decidido = estado.Decision() != ""
	r.expirado = pendenteExpirado(estado, agora)
	return escolherOrigem(doc, r, fixture, func() (bool, error) {
		return documentoExigeHumano(doc, snapshot, runID, estado)
	})
}

// documentoExigeHumano lê o documento guardado, exige que seja o do `plan.validated`, e diz se o
// plano exige decisão humana sob o snapshot — pelo mesmo predicado do gate ([exigeDecisaoHumana]).
func documentoExigeHumano(doc, snapshot, runID string, estado *runlifecycle.PlanDecisionSnapshot) (bool, error) {
	raw, err := os.ReadFile(doc)
	if err != nil {
		return false, fmt.Errorf("%w: %q ilegivel: %v", errDocumentoDoPlanoRecusado, doc, err)
	}
	d, err := plan.Decode(raw)
	if err != nil {
		return false, fmt.Errorf("%w: %q nao descodifica: %v", errDocumentoDoPlanoRecusado, doc, err)
	}
	if h := hashDoPlano(d); h != estado.PlanHash() {
		return false, fmt.Errorf("%w: %q tem hash %s e o plano validado tem %s", errDocumentoDoPlanoRecusado, doc, h, estado.PlanHash())
	}
	// Um snapshot que não se carrega é configuração, não o documento: transitório.
	snap, err := carregarSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	return exigeDecisaoHumana(d, snap, runID), nil
}

// lerEstadoDoPlano lê o log do `aos-orq` e devolve o retrato da decisão do plano. Abre para LEITURA
// (sem posse, sem truncar) e fecha antes de o `serve` abrir para escrita.
func lerEstadoDoPlano(sub substrato, planoID string) (*runlifecycle.PlanDecisionSnapshot, error) {
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		return nil, err
	}
	defer func() { _ = fechar() }()
	return lerDecisaoDoPlano(context.Background(), store, planoID)
}
