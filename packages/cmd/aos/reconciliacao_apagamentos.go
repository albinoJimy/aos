package main

// O APAGAMENTO SOBREVIVE AO RESTAURO (AOS-436).
//
// O DEFEITO, com a causa e não só o sintoma. Um `POST /dsar/erase` destrói a KEK do titular no
// Vault e sela `dsar.key_destroyed` no WORM. O `deploy/server/backup.sh` copia o volume do Vault
// (`aos_vault-data`) — e com ele TODAS as KEKs vivas nesse instante — para um bundle que fica em
// rotação 14 dias no servidor e 30 na máquina do operador. Restaurar um bundle anterior ao
// apagamento repõe a KEK, e o conteúdo do titular volta a decifrar. Havia dois sabores:
//
//	(a) vault-data ANTIGO + WORM ACTUAL — a cadeia sabe do apagamento, mas nada re-verificava a
//	    custódia: `restoreShredPending` trata `dsar.key_destroyed` como confirmado e cala-se;
//	(b) TUDO ANTIGO — a cadeia restaurada é anterior ao apagamento e nem sabe que ele aconteceu.
//
// A DECISÃO DO DONO: «reaplicar no restauro». No arranque, o nó reúne tudo o que sabe estar
// destruído — a cadeia DSAR, o registo de apagamentos próprio ([registoDeApagamentos], que cobre
// (a) e a expiração por TTL) e um registo IMPORTADO do exterior (que cobre (b)) — e pergunta à
// custódia, chave a chave, se alguma voltou. As que voltaram são destruídas DE NOVO e o facto fica
// selado (`dsar.key_reshredded`).
//
// A PERGUNTA NÃO É «A CHAVE EXISTE?», e é aqui que o desenho diverge do enunciado literal («o
// último facto é key_destroyed ⇒ se a KEK existir, destrói»). Um titular apagado pode voltar a
// gerar dados, e o `EnsureKey` re-provisiona legitimamente uma KEK NOVA com o MESMO nome — a nota
// em [vaultKeyVault.marcarShredPorConfirmar] já o avisava. Destruir por existência apagaria dados
// novos e legítimos de um titular que voltou. A pergunta certa é «esta chave é a que foi
// destruída?», e responde-se pela IDADE: a custódia sabe quando cada chave nasceu (no Vault, o
// instante de criação da versão 1). Uma KEK nascida ANTES (ou no mesmo segundo) da destruição
// registada é a destruída que o restauro ressuscitou; uma nascida DEPOIS é uma geração nova.
//
// FAIL-CLOSED, no molde das pendências de AOS-322: uma custódia que não responde, um registo
// ilegível, uma destruição que não se confirma ou um facto que não se sela deixam a reconciliação
// POR PROVAR, e a prontidão da custódia fica VERMELHA ([vaultKeyVault.apagamentosFault]) — o
// `/readyz`, o `aos_ready` e o SLI de disponibilidade seguem-na de uma vez, porque os três já
// consultam a mesma sonda. O nó ARRANCA (recusar o arranque por um Vault momentaneamente em baixo
// dava um crash-loop — a lição do `crash_resume`), mas não se diz pronto; o laço de manutenção da
// custódia re-tenta a cada tick até provar.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

const (
	// EventKeyReshredded — uma KEK que a cadeia ou o registo dão por destruída REAPARECEU na
	// custódia (tipicamente por um restauro de backup) e foi destruída de novo pelo nó, com a
	// destruição confirmada. Selado na partição DSAR, em nome próprio do nó, com o nome
	// não-reversível da chave, o instante da destruição original e o nascimento da chave
	// ressuscitada. Declarado junto do emissor (tecnica/13 §3.3).
	EventKeyReshredded = "dsar.key_reshredded"

	// reconciliacaoNHI é a identidade em nome próprio sob a qual o nó re-destrói. Distinta de
	// qualquer operador, no molde de [retentionSchedulerNHI]: quem lê a cadeia distingue um
	// apagamento ordenado por um humano de um re-apagamento que o nó fez sozinho para manter um
	// facto que já estava selado.
	reconciliacaoNHI = "nhi:aos-node/erasure-reconciler"
	// reconciliacaoToolID nomeia o produtor do selo (sem PII).
	reconciliacaoToolID = "gov.dsar.reconciliation"
	// reconciliacaoRequestID correlaciona os selos desta via.
	reconciliacaoRequestID = "aos436-reconciliacao"
	// kekResourceType rotula o Resource quando o titular NÃO é conhecido — uma entrada que só
	// existe no registo importado. O registo nunca teve o titular, e o selo não o inventa.
	kekResourceType = "dsar.kek"
	// obReshred é a obrigação que carrega os metadados do re-apagamento (nunca PII).
	obReshred = "dsar.reshred"

	// prazoDaReconciliacaoNoArranque limita o que o arranque espera pela custódia. Esgotado, a
	// reconciliação fica por provar (prontidão vermelha) e o laço de manutenção retoma-a.
	prazoDaReconciliacaoNoArranque = 30 * time.Second
)

// ErrApagamentoPorReconciliar — a reconciliação dos apagamentos com a custódia não ficou provada.
// Enquanto persistir, uma KEK destruída pode ter voltado e o conteúdo do titular pode decifrar.
var ErrApagamentoPorReconciliar = errors.New("aos: apagamentos DSAR por reconciliar com a custodia da KEK (AOS-436) — uma KEK destruida pode ter voltado com um restauro e o conteudo do titular voltar a decifrar")

// custodiaReconciliavel é a porta que a reconciliação exige da custódia. O [vaultKeyVault]
// implementa-a. O vault in-memory de referência NÃO, e está certo: as suas KEKs morrem com o
// processo, pelo que nenhum restauro as traz de volta — não há nada a reconciliar.
type custodiaReconciliavel interface {
	// nascimentoDaKEK diz se a chave com este nome existe e, se existir, quando NASCEU (o
	// instante de criação da geração mais antiga que a custódia ainda guarda).
	nascimentoDaKEK(ctx context.Context, nome string) (nascida time.Time, existe bool, err error)
	// destruirKEKPorNome destrói a chave e só devolve nil com a destruição CONFIRMADA. É por
	// NOME porque o registo nunca teve o titular.
	destruirKEKPorNome(ctx context.Context, nome string) error
	// registarReconciliacao guarda o desfecho da última passagem: nil prova-a; não-nil põe a
	// prontidão da custódia vermelha.
	registarReconciliacao(err error)
}

// alvoDeReconciliacao é tudo o que o nó sabe sobre UMA chave destruída.
type alvoDeReconciliacao struct {
	destruidaEm time.Time // o instante MAIS RECENTE de destruição conhecido
	titular     string    // "" quando só o registo a conhece
	origem      string    // "cadeia", "registo" ou "importado" — a primeira fonte que a trouxe
}

// relatorioDeReconciliacao são as contagens de uma passagem. Nunca titulares.
type relatorioDeReconciliacao struct {
	Conhecidas       int // chaves que o nó sabe destruídas (união das três fontes)
	DaCadeia         int
	DoRegisto        int
	DoImportado      int
	Acrescentadas    int // entradas que faltavam no registo próprio e foram escritas
	Ausentes         int // continuam destruídas — o caso normal
	Reprovisionadas  int // existem, mas nasceram DEPOIS da destruição: titular que voltou
	DestruidasDeNovo int // ressuscitadas e destruídas de novo, com o facto selado
}

// reconciliadorDeApagamentos junta as três fontes, interroga a custódia e re-destrói o que voltou.
type reconciliadorDeApagamentos struct {
	worm      audit.Store
	particao  string
	custodia  custodiaReconciliavel
	registo   *registoDeApagamentos // nil ⇒ sem registo próprio (declarado no banner)
	importado string                // "" ⇒ nenhum registo importado
	agora     func() time.Time

	mu          sync.Mutex
	ultimaFalha error
}

// reconciliar corre UMA passagem e regista o desfecho na custódia. Serializado: o arranque e o laço
// de manutenção nunca correm duas passagens ao mesmo tempo.
func (r *reconciliadorDeApagamentos) reconciliar(ctx context.Context) (relatorioDeReconciliacao, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, err := r.passagem(ctx)
	r.ultimaFalha = err
	r.custodia.registarReconciliacao(err)
	return rel, err
}

// porProvar diz se a última passagem falhou, ou se o registo próprio tem entradas por escrever.
func (r *reconciliadorDeApagamentos) porProvar() bool {
	r.mu.Lock()
	falhou := r.ultimaFalha != nil
	r.mu.Unlock()
	return falhou || r.registo.pendentes() > 0
}

// retentarSeFalhou é a via do laço de manutenção da custódia: só volta a interrogar a custódia
// quando a última passagem ficou por provar (uma passagem bem-sucedida não se repete a cada
// minuto — seriam N leituras ao Vault por tick para não aprender nada).
func (r *reconciliadorDeApagamentos) retentarSeFalhou(ctx context.Context, log func(string, ...any)) {
	if r == nil || !r.porProvar() {
		return
	}
	if err := r.registo.descarregar(); err != nil {
		log("apagamentos DSAR (AOS-436): o registo de apagamentos continua por escrever — o /readyz fica VERMELHO: %v", err)
	}
	rel, err := r.reconciliar(ctx)
	if err != nil {
		log("apagamentos DSAR (AOS-436): a reconciliacao com a custodia CONTINUA POR PROVAR (re-tenta no proximo tick; o /readyz fica VERMELHO): %v", err)
		return
	}
	log("apagamentos DSAR (AOS-436): reconciliacao PROVADA na re-tentativa — %s", rel.resumo())
}

// descarregar tenta escrever as entradas pendentes de uma falha anterior.
func (r *registoDeApagamentos) descarregar() error {
	if r == nil {
		return nil
	}
	return r.acrescentar()
}

// passagem é o corpo de [reconciliar]. Chamado sob r.mu.
func (r *reconciliadorDeApagamentos) passagem(ctx context.Context) (relatorioDeReconciliacao, error) {
	var rel relatorioDeReconciliacao
	alvos := make(map[string]*alvoDeReconciliacao)
	junta := func(nome string, quando time.Time, titular, origem string) {
		a, ok := alvos[nome]
		if !ok {
			alvos[nome] = &alvoDeReconciliacao{destruidaEm: quando, titular: titular, origem: origem}
			return
		}
		if quando.After(a.destruidaEm) {
			a.destruidaEm = quando
		}
		if a.titular == "" {
			a.titular = titular
		}
	}

	// (1) A CADEIA DSAR.
	daCadeia, err := apagamentosDaCadeia(ctx, r.worm, r.particao)
	if err != nil {
		return rel, err
	}
	for nome, a := range daCadeia {
		junta(nome, a.destruidaEm, a.titular, "cadeia")
	}
	rel.DaCadeia = len(daCadeia)

	// (2) O REGISTO PRÓPRIO — pode ainda não existir.
	var proprio map[string]time.Time
	if r.registo != nil {
		proprio, err = lerRegistoDeApagamentos(r.registo.caminho, true)
		if err != nil {
			return rel, err
		}
		for nome, quando := range proprio {
			junta(nome, quando, "", "registo")
		}
		rel.DoRegisto = len(proprio)
	}

	// (3) O REGISTO IMPORTADO — TEM de existir: foi pedido.
	if r.importado != "" {
		imp, ierr := lerRegistoDeApagamentos(r.importado, false)
		if ierr != nil {
			return rel, ierr
		}
		for nome, quando := range imp {
			junta(nome, quando, "", "importado")
		}
		rel.DoImportado = len(imp)
	}
	rel.Conhecidas = len(alvos)

	// (4) O REGISTO PRÓPRIO PASSA A SER SUPERCONJUNTO. O que a cadeia e o importado sabem e ele
	// não sabia é acrescentado AGORA, antes de interrogar a custódia — assim o próximo backup já
	// leva tudo, mesmo que a verificação abaixo falhe. É também assim que os apagamentos
	// anteriores a AOS-436 entram no registo: a cadeia é a sua fonte.
	if r.registo != nil {
		var faltam []entradaDeApagamento
		for _, nome := range nomesOrdenados(alvos) {
			a := alvos[nome]
			if a.destruidaEm.IsZero() {
				continue // sem instante não há linha válida; a verificação abaixo denuncia-o
			}
			if q, ok := proprio[nome]; !ok || a.destruidaEm.After(q) {
				faltam = append(faltam, entradaDeApagamento{nome: nome, destruidaEm: a.destruidaEm})
			}
		}
		if err := r.registo.acrescentar(faltam...); err != nil {
			return rel, err
		}
		rel.Acrescentadas = len(faltam)
	}

	// (5) A CUSTÓDIA, chave a chave.
	var falhas []string
	for i, nome := range nomesOrdenados(alvos) {
		a := alvos[nome]
		nascida, existe, nerr := r.custodia.nascimentoDaKEK(ctx, nome)
		if nerr != nil {
			// A custódia não respondeu: aborta a passagem em vez de somar N timeouts ao arranque.
			return rel, fmt.Errorf("%w: a custodia nao respondeu sobre %s (%d de %d chave(s) por verificar): %v",
				ErrApagamentoPorReconciliar, nome, len(alvos)-i, len(alvos), nerr)
		}
		if !existe {
			rel.Ausentes++
			continue
		}
		if a.destruidaEm.IsZero() {
			// Sem instante de destruição não há como distinguir a chave ressuscitada de uma
			// geração nova. Não se destrói às cegas — fica por provar, com o nome.
			falhas = append(falhas, nome+": a chave existe e o instante da destruicao e desconhecido")
			continue
		}
		// O Vault data a criação ao SEGUNDO. Nascida num segundo POSTERIOR ao da destruição ⇒
		// geração nova. O MESMO segundo conta como a chave destruída: a ambiguidade resolve-se
		// pelo lado do apagamento (declarado nos resíduos do AOS-436).
		if nascida.After(a.destruidaEm.Truncate(time.Second)) {
			rel.Reprovisionadas++
			continue
		}
		if derr := r.custodia.destruirKEKPorNome(ctx, nome); derr != nil {
			falhas = append(falhas, nome+": a re-destruicao nao foi confirmada: "+derr.Error())
			continue
		}
		if serr := r.selarReshred(ctx, nome, a, nascida); serr != nil {
			// A chave MORREU (confirmado), mas o facto não ficou na cadeia. Fica por provar AGORA,
			// com o nome no log: na próxima passagem a chave já não existe e a passagem prova-se,
			// pelo que o facto em falta só se vê aqui (resíduo declarado do AOS-436).
			falhas = append(falhas, nome+": destruida de novo, mas o facto NAO foi selado: "+serr.Error())
			continue
		}
		rel.DestruidasDeNovo++
	}
	if len(falhas) > 0 {
		return rel, fmt.Errorf("%w: %d chave(s) por provar: %v", ErrApagamentoPorReconciliar, len(falhas), falhas)
	}
	return rel, nil
}

// selarReshred sela `dsar.key_reshredded`. Sem PII: o titular só aparece quando a cadeia já o
// tinha (é o mesmo pseudónimo que o `dsar.key_destroyed` original selou).
func (r *reconciliadorDeApagamentos) selarReshred(ctx context.Context, nome string, a *alvoDeReconciliacao, nascida time.Time) error {
	res := audit.Resource{Type: kekResourceType, Value: nome}
	if a.titular != "" {
		res = audit.Resource{Type: subjectResourceType, Value: a.titular}
	}
	_, err := r.worm.Append(ctx, audit.AuditRecord{
		Principal:  audit.Principal{NHIID: reconciliacaoNHI},
		Partition:  r.particao,
		Timestamp:  r.agora().UTC(),
		Decision:   audit.DecisionAllow,
		Capability: EventKeyReshredded,
		RequestID:  reconciliacaoRequestID,
		ToolID:     reconciliacaoToolID,
		Resource:   res,
		Obligations: []audit.Obligation{{
			Type: obReshred,
			Params: map[string]string{
				"kek":          nome,
				"destruida_em": a.destruidaEm.UTC().Format(time.RFC3339),
				"nascida_em":   nascida.UTC().Format(time.RFC3339),
				"origem":       a.origem,
			},
		}},
	})
	return err
}

// apagamentosDaCadeia extrai da cadeia DSAR as chaves destruídas e o instante MAIS RECENTE de cada
// destruição. Contam os dois factos que afirmam uma destruição confirmada:
//
//	dsar.key_destroyed   — o apagamento original (instante = o do selo);
//	dsar.key_reshredded  — um re-apagamento anterior (instante = o da destruição ORIGINAL, que o
//	                       selo carrega; o instante do re-apagamento não é o que interessa).
//
// NÃO se usa «o último facto ganha». Uma destruição que aconteceu continua a ter acontecido, seja
// o que for que a cadeia diga depois (um segundo pedido bloqueado por legal hold, uma segunda
// destruição por confirmar): a KEK dessa época não pode voltar. O que distingue a chave dessa época
// de uma geração nova é a idade, não a ordem dos factos.
func apagamentosDaCadeia(ctx context.Context, store audit.Store, particao string) (map[string]*alvoDeReconciliacao, error) {
	out := make(map[string]*alvoDeReconciliacao)
	if store == nil {
		return out, nil
	}
	head, err := store.Head(ctx, particao)
	if err != nil {
		return nil, fmt.Errorf("%w: head da cadeia %q: %v", ErrApagamentoPorReconciliar, particao, err)
	}
	if head == 0 {
		return out, nil
	}
	recs, err := store.Read(ctx, particao, 1, head)
	if err != nil {
		return nil, fmt.Errorf("%w: leitura da cadeia %q: %v", ErrApagamentoPorReconciliar, particao, err)
	}
	for _, rec := range recs {
		var nome, titular string
		var quando time.Time
		switch rec.Capability {
		case dsar.EventKeyDestroyed:
			if rec.Resource.Type != subjectResourceType || rec.Resource.Value == "" {
				continue
			}
			titular, nome, quando = rec.Resource.Value, nomeDeApagamento(rec.Resource.Value), rec.Timestamp
		case EventKeyReshredded:
			for _, ob := range rec.Obligations {
				if ob.Type != obReshred {
					continue
				}
				nome = ob.Params["kek"]
				quando, _ = time.Parse(time.RFC3339, ob.Params["destruida_em"])
			}
			if !reNomeApagamento.MatchString(nome) {
				continue
			}
			if rec.Resource.Type == subjectResourceType {
				titular = rec.Resource.Value
			}
		default:
			continue
		}
		a, ok := out[nome]
		if !ok {
			out[nome] = &alvoDeReconciliacao{destruidaEm: quando.UTC(), titular: titular}
			continue
		}
		if quando.After(a.destruidaEm) {
			a.destruidaEm = quando.UTC()
		}
		if a.titular == "" {
			a.titular = titular
		}
	}
	return out, nil
}

func (rel relatorioDeReconciliacao) resumo() string {
	return fmt.Sprintf("%d chave(s) conhecida(s) destruida(s) (cadeia %d, registo proprio %d, importado %d; %d acrescentada(s) ao registo): %d continuam destruidas, %d sao geracoes NOVAS (titular que voltou — intactas), %d RESSUSCITADAS e destruidas DE NOVO (dsar.key_reshredded selado)",
		rel.Conhecidas, rel.DaCadeia, rel.DoRegisto, rel.DoImportado, rel.Acrescentadas, rel.Ausentes, rel.Reprovisionadas, rel.DestruidasDeNovo)
}

// comporReconciliacaoDeApagamentos liga o registo à custódia, corre a reconciliação do arranque e
// declara a postura. Devolve nil quando a custódia não é reconciliável.
//
// Chamado pelo [Bootstrap] ANTES de o nó servir: é aqui que um restauro de backup anterior deixa de
// poder pôr a servir conteúdo de um titular apagado em silêncio.
func comporReconciliacaoDeApagamentos(ctx context.Context, cfg Config, worm audit.Store, particao string, vault audit.KeyVault, log func(string, ...any)) *reconciliadorDeApagamentos {
	cust, ok := vault.(custodiaReconciliavel)
	if !ok {
		_, referencia := vault.(*audit.InMemoryKeyVault)
		switch {
		case !referencia:
			// A custódia que sobrevive ao restart é precisamente a que um restauro pode fazer
			// recuar — e esta não sabe responder à pergunta da idade. Declara-se sempre.
			log("apagamentos DSAR (AOS-436): a custodia da KEK injectada (%T) NAO implementa a reconciliacao — um restauro da custodia pode RESSUSCITAR KEKs destruidas sem o no o detectar; o registo de apagamentos NAO e escrito nem lido", vault)
		case cfg.DSARErasureRegister != "" || cfg.DSARErasureRegisterImport != "":
			log("apagamentos DSAR (AOS-436): AOS_DSAR_ERASURE_REGISTER/AOS_DSAR_ERASURE_REGISTER_IMPORT definidos com o vault in-memory de referencia — nada a reconciliar (as KEKs morrem com o processo, nenhum restauro as traz de volta); o registo NAO e escrito nem lido")
		}
		return nil
	}
	var reg *registoDeApagamentos
	if cfg.DSARErasureRegister != "" {
		reg = novoRegistoDeApagamentos(cfg.DSARErasureRegister)
		if l, ok := vault.(interface{ ligarRegistoDeApagamentos(*registoDeApagamentos) }); ok {
			l.ligarRegistoDeApagamentos(reg)
		}
	}
	r := &reconciliadorDeApagamentos{
		worm:      worm,
		particao:  particao,
		custodia:  cust,
		registo:   reg,
		importado: cfg.DSARErasureRegisterImport,
		agora:     time.Now,
	}
	if reg == nil {
		log("apagamentos DSAR (AOS-436): SEM registo de apagamentos (AOS_DSAR_ERASURE_REGISTER vazio) — a reconciliacao do arranque usa SO a cadeia DSAR: cobre um restauro do Vault com o WORM actual, NAO cobre um restauro de TUDO antigo (a cadeia restaurada nao sabe dos apagamentos posteriores ao backup)")
	}
	rctx, cancel := context.WithTimeout(ctx, prazoDaReconciliacaoNoArranque)
	defer cancel()
	rel, err := r.reconciliar(rctx)
	if err != nil {
		log("apagamentos DSAR (AOS-436): reconciliacao do arranque POR PROVAR — o /readyz fica VERMELHO ate uma passagem provada (o laco de manutencao da custodia re-tenta a cada tick); nenhum conteudo de um titular apagado e servido por um no que se declara pronto: %v", err)
		return r
	}
	if rel.Conhecidas > 0 || r.importado != "" {
		log("apagamentos DSAR (AOS-436): reconciliacao do arranque PROVADA — %s", rel.resumo())
	}
	if rel.DestruidasDeNovo > 0 {
		log("apagamentos DSAR (AOS-436): ⚠️ %d KEK(s) destruida(s) REAPARECERAM na custodia — tipicamente um restauro de backup anterior ao apagamento — e foram destruidas DE NOVO; o facto ficou selado como %s na particao %q", rel.DestruidasDeNovo, EventKeyReshredded, particao)
	}
	return r
}
