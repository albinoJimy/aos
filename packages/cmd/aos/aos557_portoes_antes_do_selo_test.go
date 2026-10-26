package main

// AOS-557 — OS PORTÕES FAIL-CLOSED DO RUN CORREM ANTES DO SELO DAS ENTRADAS POR HANDLE.
//
// O que estes testes trancam é uma ORDEM, e ordem é exactamente o tipo de propriedade que um teste
// verde sobre o cenário errado não apanha. Donde a forma de cada um:
//
//   - [TestAOS557_ContratoImpossivelNaoSelaHandleNenhum] — o critério 2, pelo caminho REAL da
//     hospedagem (`Submit` → `hostRun`), num nó com o espaço de handles composto e um run na
//     1.8.0 com entradas. E com o CONTROLO que o torna não-vacuoso: o mesmo run com um contrato
//     possível SELA. Sem esse controlo, a asserção «zero handles» passaria num nó que não sela
//     nada — que é a maneira mais fácil de este teste mentir.
//   - [TestAOS557_AMesmaRecusaQueOSecuredRuntime] — o critério 5: não há dois guardas. Mede-se
//     contra o `SecuredRuntime.Run` REAL, que congela o tool set e o corta pela lista-branca, e é
//     por isso que ele vê a divergência de CAUSA que o corte de [ofertaDoNo] fecha. A primeira
//     versão deste ficheiro tinha aqui dois testes que comparavam [agentruntime.RefuseAtStartup]
//     consigo própria — tautológicos, não podiam ficar vermelhos. Achado da revisão adversarial.
//     A premissa «só o nome da tool importa» é amarrada no kernel, onde o `Run` vê specs com
//     versão e digest e a `RefuseAtStartup` vê specs só com nome.
//   - [TestAOS557_OfertaDoNoEADoCongelamento] — a equivalência de conjuntos em que a fidelidade da
//     conferência assenta: a oferta que o nó lê é a mesma que o congelamento do run vai ver.
//   - [TestAOS557_RetomaNaoEConferida] — a REGRESSÃO que o âmbito estreito evita: um run retomado
//     traz o tool set do snapshot durável, e conferi-lo contra o catálogo vivo recusaria um run que
//     o `Run` admite.
//   - [TestAOS557_HaEntradasPorSelarImplicaQueOSeloCunha] — amarra o predicado do âmbito ao selo.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// aos557Entradas é uma entrada do plano com o digest certo — a forma que o nó aceita.
func aos557Entradas() []agentruntime.PlanInput {
	const conteudo = "o relatorio do trimestre, por handle"
	soma := sha256.Sum256([]byte(conteudo))
	return []agentruntime.PlanInput{{
		From: "no-produtor", Output: "relatorio",
		Digest:  "sha256:" + hex.EncodeToString(soma[:]),
		Content: []byte(conteudo),
	}}
}

// aos557FicheirosNoEspaco conta os ficheiros de conteúdo que existem no espaço de handles. É a
// metade da asserção que o stream não dá: um `handle.created` pode não ter sido escrito e os bytes
// estarem no disco (é a ordem de [armazemDeHandles.selar] — grava, e só depois declara).
func aos557FicheirosNoEspaco(t *testing.T, pasta string) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(pasta, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("percorrer o espaço de handles %q: %v", pasta, err)
	}
	return n
}

// aos557HandlesDeclarados conta os `handle.created` que o stream do run declara.
//
// UM STREAM QUE NAO EXISTE E ZERO HANDLES; QUALQUER OUTRO ERRO FALHA O TESTE. A distincao nao e
// zelo: devolver zero a um erro de leitura punha a assercao-chave deste ficheiro («zero handles»)
// verde numa regressao que tornasse o stream ilegivel — o molde exacto do teste a passar pela
// razao errada.
func aos557HandlesDeclarados(t *testing.T, n *aos486No, runID string) int {
	t.Helper()
	eventos, err := n.node.EventStore.Read(context.Background(), runID, 1)
	switch {
	case errors.Is(err, eventstore.ErrStreamNotFound):
		return 0
	case err != nil:
		t.Fatalf("ler o stream do run %q: %v", runID, err)
	}
	declarados, _ := handlesDoRun(eventos)
	return len(declarados)
}

// aos557No compõe o nó com o espaço de handles, a execução durável e a 1.8.0 habilitada, e
// oferece UMA tool (`arquivo`). Devolve o nó, a pasta do espaço de handles e o CATÁLOGO com que o
// nó foi composto — o mesmo objecto, não uma reconstrução, para a equivalência de ofertas se
// medir contra ele.
func aos557No(t *testing.T) (*aos486No, string, toolset.Catalog) {
	t.Helper()
	ordem, extra, specs, caps := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra
	t.Cleanup(func() {
		aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486SpecsExtra, aos486CapsExtra = ordem, extra, specs, caps
	})
	aos486OrdemDoFicheiro = []string{"arquivo"}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`

	pasta := t.TempDir()
	var cat toolset.Catalog
	n := aos486Compor(t, func(cfg *Config) {
		cfg.HandleDir, cfg.HandleMaxBytes = pasta, 1<<30
		cfg.ConteudoPorHandle = true
		cat = cfg.Catalog
	})
	if n.node.planoHandles == nil {
		t.Fatal("o plano de handles tinha de ficar composto (sem ele não há selo a anteceder)")
	}
	if !n.node.conteudoPorHandle {
		t.Fatal("o nó tinha de habilitar o layout por handle")
	}
	if cat == nil {
		t.Fatal("o nó tinha de ser composto com um catálogo")
	}
	return n, pasta, cat
}

// aos557Submeter submete um run na 1.8.0 com entradas e o contrato dado, e espera o desfecho.
func aos557Submeter(t *testing.T, n *aos486No, runID string, contrato, listaBranca []string) RunOutcome {
	t.Helper()
	if err := n.svc.Submit(context.Background(), agentruntime.Goal{
		RunID:              runID,
		Principal:          referencemonitor.Principal{NHIID: durAgent},
		Subject:            "titular-557",
		Credential:         n.tok,
		Objective:          "Le o relatorio do plano",
		MaxTurns:           4,
		AllowedTools:       listaBranca,
		CompletionRequires: contrato,
		Inputs:             aos557Entradas(),
		// É o PEDIDO que põe o run na 1.8.0 ([layoutDoRunNovo]); é por aqui que a hospedagem
		// chega ao selo das entradas.
		ConteudoPorHandle: true,
	}); err != nil {
		t.Fatalf("Submit(%s): %v", runID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	oc, ok, err := n.svc.Wait(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}
	return oc
}

// O CRITÉRIO 2. Um run com um contrato que este nó não pode cumprir, num layout por handle e com
// entradas, é recusado SEM deixar um `handle.created` no stream nem um byte no espaço.
//
// E o CONTROLO, que é o que torna a asserção uma medição: o MESMO run com um contrato possível
// passa a conferência, chega ao selo e SELA. Sem ele, «zero handles» é o que um nó que não sela
// nada também responde.
func TestAOS557_ContratoImpossivelNaoSelaHandleNenhum(t *testing.T) {
	n, pasta, _ := aos557No(t)
	var pedidos atomic.Int64
	n.upstream.mu.Lock()
	n.upstream.responde = func(_ bool, _ string) []byte {
		pedidos.Add(1)
		return aos551Final()
	}
	n.upstream.mu.Unlock()

	// (a) CONTRATO IMPOSSÍVEL — a tool está na lista-branca do run (logo a porta HTTP aceitaria o
	// corpo: ela não vê a oferta do nó, e di-lo em [validarContratoDeConclusao]) mas o nó não a
	// oferece.
	const runMau = "run-557-contrato-impossivel"
	oc := aos557Submeter(t, n, runMau, []string{"tool_fantasma"}, []string{"arquivo", "tool_fantasma"})
	if !errors.Is(oc.Err, agentruntime.ErrImpossibleCompletionContract) {
		t.Fatalf("o run tinha de ser recusado pelo contrato impossível; err=%v\n%s", oc.Err, n.log.String())
	}
	if h := aos557HandlesDeclarados(t, n, runMau); h != 0 {
		t.Fatalf("o run recusado declarou %d handle(s) no stream — a conferência correu DEPOIS do selo", h)
	}
	if f := aos557FicheirosNoEspaco(t, pasta); f != 0 {
		t.Fatalf("o run recusado deixou %d ficheiro(s) no espaço de handles — os bytes ficaram sem run que os use", f)
	}
	// E NÃO FOI RECUSADO A CAMINHO DO MODELO: o provider não viu um único pedido.
	if p := pedidos.Load(); p != 0 {
		t.Fatalf("o run recusado chegou ao modelo %d vez(es)", p)
	}

	// (b) O CONTROLO. O mesmo run, com um contrato que o nó PODE cumprir, sela as entradas.
	const runBom = "run-557-contrato-possivel"
	ocBom := aos557Submeter(t, n, runBom, []string{"arquivo"}, []string{"arquivo"})
	if errors.Is(ocBom.Err, agentruntime.ErrImpossibleCompletionContract) {
		t.Fatalf("a conferência recusou um contrato que o nó oferece — é uma REGRESSÃO, não um aperto: %v\n%s", ocBom.Err, n.log.String())
	}
	if h := aos557HandlesDeclarados(t, n, runBom); h != 1 {
		t.Fatalf("o run admitido declarou %d handle(s), esperava 1 — sem selo este teste não mede ordem nenhuma\n%s", h, n.log.String())
	}
	if f := aos557FicheirosNoEspaco(t, pasta); f == 0 {
		t.Fatal("o run admitido não deixou byte nenhum no espaço — o controlo é vácuo")
	}
}

// O CRITERIO 5, MEDIDO CONTRA O CAMINHO REAL: a conferencia de cima e o portao do kernel dao a
// MESMA recusa, com a MESMA causa, para o mesmo run.
//
// # Porque e que isto se mede contra o `SecuredRuntime.Run` e nao contra a `RefuseAtStartup`
//
// A primeira versao deste teste comparava `RefuseAtStartup` com `RefuseAtStartup` — a mesma
// funcao, com entradas que um projector de nomes tornava bit-identicas. Era tautologico: uma
// segunda copia da regra no `Run` deixava-o verde. Achado da revisao adversarial deste ticket.
//
// Aqui o lado de baixo e o `SecuredRuntime.Run` REAL: ele congela o tool set do catalogo e corta-o
// pela lista-branca ([integration.ApplyFrozenToGoal]) antes de o portao do kernel decidir. E esse
// corte que o [ofertaDoNo] replica, e e so contra o caminho real que se ve se ele o replica bem.
//
// # Porque e que o Goal vai SELADO
//
// Dentro do `Run` a semente do tail corre ANTES do portao do contrato e, num layout por handle,
// exige a referencia de cada entrada. Um Goal sem referencias morria la e nunca chegaria ao portao
// que este teste quer comparar — o que e, aliais, a razao pela qual a semente do tail nao pode
// subir.
func TestAOS557_AMesmaRecusaQueOSecuredRuntime(t *testing.T) {
	n, _, _ := aos557No(t)
	ctx := withModelCredential(context.Background(), n.tok)
	n.upstream.mu.Lock()
	n.upstream.responde = func(_ bool, _ string) []byte { return aos551Final() }
	n.upstream.mu.Unlock()

	nomes, oerr := n.node.Runtime.OfferedToolNames(ctx)
	if oerr != nil {
		t.Fatalf("OfferedToolNames: %v", oerr)
	}

	for nome, c := range map[string]struct {
		contrato    []string
		origem      string
		listaBranca []string
		sentinel    error
	}{
		// A tool nao existe no catalogo deste no, e a lista-branca admite-a: e o caso que a porta
		// HTTP nao ve.
		"contrato fora da oferta": {[]string{"tool_fantasma"}, "", []string{"arquivo", "tool_fantasma"}, agentruntime.ErrImpossibleCompletionContract},
		// A tool EXISTE no catalogo e esta FORA da lista-branca: e aqui que o corte do
		// [ofertaDoNo] decide a causa. Sem ele, a conferencia dizia «fora da lista-branca» e o
		// `Run` «nao esta no tool set» — mesma recusa, causas diferentes.
		"contrato fora da lista": {[]string{"arquivo"}, "", []string{"outra_qualquer"}, agentruntime.ErrImpossibleCompletionContract},
		"origem fora da oferta":  {nil, "tool_fantasma", []string{"arquivo", "tool_fantasma"}, agentruntime.ErrImpossibleOutputSource},
		"origem fora da lista":   {nil, "arquivo", []string{"outra_qualquer"}, agentruntime.ErrImpossibleOutputSource},
	} {
		runID := "run-557-mesma-recusa-" + strings.ReplaceAll(nome, " ", "-")
		goal := agentruntime.Goal{
			RunID:              runID,
			Principal:          referencemonitor.Principal{NHIID: durAgent},
			Subject:            "titular-557",
			Credential:         n.tok,
			Objective:          "Le o relatorio do plano",
			MaxTurns:           4,
			AssemblyVersion:    agentruntime.AssemblyVersion180,
			CompletionMode:     agentruntime.CompletionObserve,
			CompletionRequires: c.contrato,
			AllowedTools:       c.listaBranca,
			Inputs:             aos557Entradas(),
		}
		if c.origem != "" {
			goal.OutputFromTool, goal.OutputSourceBinding = c.origem, agentruntime.OutputSourceMeasure
		}

		// A DECISAO DE CIMA, com a oferta que o no le e o corte que ele aplica.
		deCima := agentruntime.RefuseAtStartup(goal, ofertaDoNo(nomes, goal.AllowedTools))

		// E A DO CAMINHO REAL, com as entradas JA seladas (ver o cabecalho).
		selado, serr := selarEntradasDoPlano(ctx, n.node.planoHandles, goal)
		if serr != nil {
			t.Fatalf("%s: selar as entradas: %v", nome, serr)
		}
		_, _, doRun := n.node.Runtime.Run(ctx, selado, nil)

		switch {
		case deCima == nil || doRun == nil:
			t.Errorf("%s: cima=%v, Run=%v — os dois tinham de recusar", nome, deCima, doRun)
		case !errors.Is(doRun, c.sentinel):
			t.Errorf("%s: o Run recusou por %v, esperava %v", nome, doRun, c.sentinel)
		case doRun.Error() != deCima.Error():
			t.Errorf("%s: causas DIFERENTES para a mesma recusa:\n  cima: %v\n  Run:  %v", nome, deCima, doRun)
		}
	}

	// A NAO-VACUIDADE: um run cujo contrato e origem o no PODE cumprir e admitido pelos dois. Sem
	// este caso, tudo acima passaria com dois lados que recusassem sempre.
	bom := agentruntime.Goal{
		RunID:               "run-557-mesma-recusa-admitido",
		Principal:           referencemonitor.Principal{NHIID: durAgent},
		Subject:             "titular-557",
		Credential:          n.tok,
		Objective:           "Le o relatorio do plano",
		MaxTurns:            4,
		AssemblyVersion:     agentruntime.AssemblyVersion180,
		CompletionMode:      agentruntime.CompletionObserve,
		CompletionRequires:  []string{"arquivo"},
		OutputFromTool:      "arquivo",
		OutputSourceBinding: agentruntime.OutputSourceMeasure,
		AllowedTools:        []string{"arquivo"},
		Inputs:              aos557Entradas(),
	}
	if err := agentruntime.RefuseAtStartup(bom, ofertaDoNo(nomes, bom.AllowedTools)); err != nil {
		t.Fatalf("a conferencia recusou um run que o no pode cumprir: %v", err)
	}
	seladoBom, serr := selarEntradasDoPlano(ctx, n.node.planoHandles, bom)
	if serr != nil {
		t.Fatalf("selar as entradas do run admitido: %v", serr)
	}
	if _, _, err := n.node.Runtime.Run(ctx, seladoBom, nil); recusadoNoArranque(err) {
		t.Fatalf("o Run recusou NO ARRANQUE um run que o no pode cumprir: %v", err)
	}
}

// A OFERTA QUE O NÓ LÊ É A MESMA QUE O CONGELAMENTO DO RUN VAI VER — a equivalência de conjuntos
// em que a fidelidade da conferência assenta, medida contra a função de produção que o `Run` usa
// ([toolset.FreezeToolSet]) e não contra uma reconstrução deste teste.
//
// Se o congelamento passar a filtrar entradas activas — descartar uma em vez de abortar —, a
// oferta de cima deixa de ser fiel e isto fica vermelho.
func TestAOS557_OfertaDoNoEADoCongelamento(t *testing.T) {
	n, _, cat := aos557No(t)
	ctx := context.Background()

	oferta, err := n.node.Runtime.OfferedToolNames(ctx)
	if err != nil {
		t.Fatalf("OfferedToolNames: %v", err)
	}
	if len(oferta) == 0 {
		t.Fatal("a oferta saiu vazia — este nó regista um catálogo assinado e o teste seria vácuo")
	}

	congelado, ferr := toolset.FreezeToolSet(ctx, cat, "run-557-oferta", nil)
	if ferr != nil {
		t.Fatalf("FreezeToolSet: %v", ferr)
	}
	doCongelamento := congelado.IDs()
	if len(oferta) != len(doCongelamento) {
		t.Fatalf("a oferta tem %d nomes e o congelamento %d: %v vs %v", len(oferta), len(doCongelamento), oferta, doCongelamento)
	}
	naOferta := map[string]bool{}
	for _, nome := range oferta {
		naOferta[nome] = true
	}
	for _, nome := range doCongelamento {
		if !naOferta[nome] {
			t.Fatalf("o congelamento oferece %q e a conferência de cima não — recusaria um run que o Run admite", nome)
		}
	}
}

// A REGRESSÃO QUE O ÂMBITO ESTREITO EVITA, e é por isso que ela é testada e não só comentada.
//
// Um run RETOMADO entra com as entradas JÁ seladas e com o tool set do snapshot DURÁVEL, que ganha
// de propósito ao catálogo vivo (AOS-050/AOS-155). Conferi-lo contra o catálogo vivo recusaria um
// run que o `Run` admite — um operador que tire uma tool do manifesto e reinicie o nó mataria os
// runs retomados que a usam. A conferência não corre nesse caminho, e nada lá há a evitar: o selo é
// um no-op num Goal cujas entradas já trazem referência.
func TestAOS557_RetomaNaoEConferida(t *testing.T) {
	n, pasta, _ := aos557No(t)
	ctx := context.Background()

	// O Goal de um run retomado: a 1.8.0, entradas COM referência, e um contrato que o catálogo
	// vivo deste nó não cumpre.
	entradas := aos557Entradas()
	entradas[0].Handle = agentruntime.HandleRefPrefix + agentruntime.MintHandleToken("run-557-retoma", entradas[0].Digest, 1)
	goal := agentruntime.Goal{
		RunID:              "run-557-retoma",
		Principal:          referencemonitor.Principal{NHIID: durAgent},
		Subject:            "titular-557",
		AssemblyVersion:    agentruntime.AssemblyVersion180,
		CompletionMode:     agentruntime.CompletionObserve,
		CompletionRequires: []string{"tool_fantasma"},
		AllowedTools:       []string{"tool_fantasma"},
		Inputs:             entradas,
	}
	if haEntradasPorSelar(goal) {
		t.Fatal("um Goal cujas entradas já trazem referência não tem nada por selar")
	}
	if err := n.svc.recusaAntesDoSelo(ctx, &runState{}, goal); err != nil {
		t.Fatalf("a conferencia recusou um run RETOMADO, cujo tool set vem do snapshot duravel: %v", err)
	}
	if f := aos557FicheirosNoEspaco(t, pasta); f != 0 {
		t.Fatalf("a conferência escreveu %d ficheiro(s) — ela não tem efeito nenhum", f)
	}

	// E a metade que prova que a conferência não está simplesmente morta: o MESMO Goal sem a
	// referência (um run NOVO) é recusado.
	novo := goal
	novo.Inputs = aos557Entradas()
	if !haEntradasPorSelar(novo) {
		t.Fatal("um Goal na 1.8.0 com entradas sem referência tem de ter algo por selar")
	}
	if err := n.svc.recusaAntesDoSelo(ctx, &runState{}, novo); !errors.Is(err, agentruntime.ErrImpossibleCompletionContract) {
		t.Fatalf("o run NOVO com o mesmo contrato tinha de ser recusado; err=%v", err)
	}

	// E A RE-HOSPEDAGEM QUE A FORMA DO GOAL NAO DISTINGUE (achado ALTO da revisao adversarial).
	//
	// Um `POST /runs` com um `run_id` que JA EXISTE e esta em curso e admitido como retoma por
	// outro nome ([NodeService.submitObservando], caso «(2-ter-ter)», AOS-520), e o Goal dele e o
	// DO CORPO: entradas SEM referencia, logo indistinguivel de um arranque pelo predicado. Mas o
	// tool set desse run vem do snapshot DURAVEL, que ganha ao catalogo vivo — conferi-lo aqui
	// recusaria um run que o `Run` admite. Quem o distingue e a bandeira, e e por isso que ela
	// entra na decisao.
	//
	// E O MESMO Goal da linha de cima, que acabou de ser recusado: so a bandeira muda. Sem isso o
	// teste nao mediria a bandeira, mediria o Goal.
	if err := n.svc.recusaAntesDoSelo(ctx, &runState{existente: true}, novo); err != nil {
		t.Fatalf("a conferencia recusou uma RE-HOSPEDAGEM (rs.existente), cujo tool set vem do snapshot duravel: %v", err)
	}
	// E um `rs` nil nao decide nada — o guarda defensivo.
	if err := n.svc.recusaAntesDoSelo(ctx, nil, novo); err != nil {
		t.Fatalf("sem estado de run a conferencia tinha de se abster: %v", err)
	}
}

// O PREDICADO DO ÂMBITO AMARRADO AO SELO: sempre que [haEntradasPorSelar] diz sim, o selo CUNHA um
// handle. O recíproco NÃO se afirma — o selo tem recusas próprias que esta pergunta não cobre, e é
// deliberado (ver o comentário da função).
func TestAOS557_HaEntradasPorSelarImplicaQueOSeloCunha(t *testing.T) {
	plano, _, es, _ := aos550Plano(t)
	ctx := context.Background()

	for nome, c := range map[string]struct {
		id       string
		versao   string
		inputs   []agentruntime.PlanInput
		porSelar bool
	}{
		"na 1.8.0, com entrada nova": {"nova", agentruntime.AssemblyVersion180, aos557Entradas(), true},
		"na 1.8.0, sem entradas":     {"vazio", agentruntime.AssemblyVersion180, nil, false},
		"layout de sempre, entradas": {"sempre", agentruntime.AssemblyVersion, aos557Entradas(), false},
		"versao desconhecida":        {"desconhecida", "9.9.9", aos557Entradas(), false},
	} {
		// O id do run não leva a versão do layout: um ponto no nome não forma stream (o mesmo
		// defeito de forma do AOS-426), e o selo recusaria por `registo_indisponivel` — uma
		// recusa do substrato a passar por uma do predicado.
		goal := agentruntime.Goal{
			RunID:           "run-557-predicado-" + c.id,
			Principal:       referencemonitor.Principal{NHIID: "nhi:a"},
			Subject:         "titular-557",
			AssemblyVersion: c.versao,
			Inputs:          c.inputs,
		}
		if got := haEntradasPorSelar(goal); got != c.porSelar {
			t.Errorf("%s: haEntradasPorSelar=%v, quero %v", nome, got, c.porSelar)
			continue
		}
		if !c.porSelar {
			continue
		}
		selado, err := selarEntradasDoPlano(ctx, plano, goal)
		if err != nil {
			t.Errorf("%s: o selo recusou: %v", nome, err)
			continue
		}
		if selado.Inputs[0].Handle == "" {
			t.Errorf("%s: o predicado disse que o selo cunha e ele não cunhou", nome)
		}
		eventos, rerr := es.Read(ctx, goal.RunID, 1)
		if rerr != nil {
			t.Errorf("%s: o selo não declarou nada no stream: %v", nome, rerr)
			continue
		}
		if declarados, _ := handlesDoRun(eventos); len(declarados) != 1 {
			t.Errorf("%s: o stream declara %d handles, esperava 1", nome, len(declarados))
		}
	}
}

// AS PRE-CONDICOES DO GOAL TAMBEM CORREM ANTES DO SELO, e o caso que o exige esta medido: um Goal
// com `Subject` preenchido e `Principal.NHIID` VAZIO tem titular nao vazio ([agentruntime.Goal.Titular]
// cai no `Subject`), pelo que a quarentena do espaco de handles o aceita e o selo CUNHA — e so
// depois o `Run` recusava com [agentruntime.ErrNoPrincipal]. Residuo da mesma classe que este
// ticket fecha, alcancavel por um `Submit` programatico (o `Submit` so confere o id do run).
//
// Achado da SEGUNDA passagem da revisao adversarial: a primeira versao deste ticket deixava esta
// metade do `rt.validate` em baixo, com a justificacao de que era inalcancavel — e a justificacao
// estava apoiada no guarda errado.
func TestAOS557_PreCondicoesDoGoalCorremAntesDoSelo(t *testing.T) {
	n, pasta, _ := aos557No(t)
	ctx := context.Background()

	semPrincipal := agentruntime.Goal{
		RunID:           "run-557-sem-principal",
		Subject:         "titular-557", // titular NAO vazio: a quarentena aceita
		AssemblyVersion: agentruntime.AssemblyVersion180,
		CompletionMode:  agentruntime.CompletionObserve,
		Inputs:          aos557Entradas(),
	}
	if semPrincipal.Titular() == "" {
		t.Fatal("o Goal tinha de ter titular — e o titular que faz a quarentena aceitar e o selo cunhar")
	}
	if !haEntradasPorSelar(semPrincipal) {
		t.Fatal("o Goal tinha de ter entradas por selar")
	}
	if err := n.svc.recusaAntesDoSelo(ctx, &runState{}, semPrincipal); !errors.Is(err, agentruntime.ErrNoPrincipal) {
		t.Fatalf("um Goal sem principal tinha de ser recusado ANTES do selo; err=%v", err)
	}
	if f := aos557FicheirosNoEspaco(t, pasta); f != 0 {
		t.Fatalf("a conferencia deixou %d ficheiro(s) no espaco — ela nao tem efeito nenhum", f)
	}

	// NAO-VACUIDADE: o MESMO Goal com principal passa a conferencia (este no tem catalogo e o Goal
	// nao declara contrato nem origem), e o selo cunha. Sem isto, a recusa acima passaria num no
	// que recusasse tudo.
	comPrincipal := semPrincipal
	comPrincipal.RunID = "run-557-com-principal"
	comPrincipal.Principal = referencemonitor.Principal{NHIID: durAgent}
	if err := n.svc.recusaAntesDoSelo(ctx, &runState{}, comPrincipal); err != nil {
		t.Fatalf("o mesmo Goal COM principal tinha de passar: %v", err)
	}
	selado, serr := selarEntradasDoPlano(ctx, n.node.planoHandles, comPrincipal)
	if serr != nil || selado.Inputs[0].Handle == "" {
		t.Fatalf("o selo tinha de cunhar para o Goal admitido: err=%v handle=%q", serr, selado.Inputs[0].Handle)
	}
}
