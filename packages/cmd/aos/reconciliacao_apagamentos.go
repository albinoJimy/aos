package main

// O APAGAMENTO SOBREVIVE AO RESTAURO (AOS-436).
//
// O DEFEITO. Um `POST /dsar/erase` destrói a KEK do titular no Vault e sela `dsar.key_destroyed` no
// WORM. O `deploy/server/backup.sh` copia o volume do Vault — com TODAS as KEKs vivas nesse
// instante — para um bundle em rotação. Restaurar um bundle anterior ao apagamento repõe a KEK, e o
// conteúdo do titular volta a decifrar. Dois sabores: (a) Vault antigo + WORM actual — a cadeia
// sabe do apagamento, mas nada re-verificava a custódia; (b) tudo antigo — a cadeia restaurada nem
// sabe que o apagamento aconteceu.
//
// A DECISÃO DO DONO: «reaplicar no restauro». O nó reúne o que sabe estar destruído — a cadeia DSAR,
// o registo de apagamentos próprio e um registo importado ([registoDeApagamentos]) — e pergunta à
// custódia se alguma dessas KEKs voltou. As que voltaram são destruídas de novo e o facto é selado.
//
// A PERGUNTA É PELA IDADE, não pela existência. Um titular apagado pode voltar a gerar dados e o
// `EnsureKey` re-provisiona legitimamente uma KEK nova com o MESMO nome. Uma KEK nascida ANTES (ou
// no mesmo segundo) da destruição registada é a destruída que um restauro ressuscitou; uma nascida
// DEPOIS é uma geração nova e fica intacta.
//
// O QUE A REVISÃO ADVERSARIAL DO PRIMEIRO DESENHO MEDIU, e o que mudou por isso:
//
//	(1) um registo importado forjado destruía KEKs de titulares VIVOS, e o selo do re-apagamento,
//	    relido da cadeia, eternizava a data forjada ⇒ as linhas passam a ser autenticadas (HMAC),
//	    um instante no futuro é recusado, e o `dsar.key_reshredded` NUNCA é relido como autoridade
//	    de destruição — só o `dsar.key_destroyed` do fluxo DSAR o é;
//	(2) «não pronto» não protegia nada — o nó continuava a servir e a decifrar ⇒ o portão passa a
//	    estar na CUSTÓDIA ([vaultKeyVault.portao]): por provar, nenhuma DEK se embrulha nem
//	    desembrulha;
//	(3) uma fonte opcional que falhava abortava a passagem antes de a cadeia ser reconciliada ⇒
//	    cada fonte é independente, e a cadeia é SEMPRE processada;
//	(4) a re-destruição não consultava o legal hold ⇒ consulta, sob a mesma barreira que o
//	    shredder usa, e uma KEK retida fica BLOQUEADA no portão em vez de destruída;
//	(5) um GET por chave, a abortar no primeiro erro ⇒ um LIST, GETs só para o que existe, e
//	    nenhuma chave deixa de ser processada por causa de outra.
//
// PERIÓDICA, e não só no arranque: a passagem corre em cada tick da manutenção da custódia. Custa
// um LIST e as leituras das duas fontes locais, e fecha o caso de um Vault restaurado com o nó a
// correr.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	audit "github.com/aos-ref/platform/audit"
)

const (
	// EventKeyReshredded — uma KEK que a cadeia ou o registo dão por destruída REAPARECEU na
	// custódia (tipicamente por um restauro de backup) e foi destruída de novo pelo nó, com a
	// destruição confirmada. Selado na partição DSAR, em nome próprio do nó. É PROVA, não
	// autoridade: nunca é relido como fonte de destruição (ver [apagamentosDaCadeia]). Declarado
	// junto do emissor (tecnica/13 §3.3).
	EventKeyReshredded = "dsar.key_reshredded"

	// reconciliacaoNHI é a identidade em nome próprio sob a qual o nó re-destrói.
	reconciliacaoNHI = "nhi:aos-node/erasure-reconciler"
	// reconciliacaoToolID nomeia o produtor do selo (sem PII).
	reconciliacaoToolID = "gov.dsar.reconciliation"
	// reconciliacaoRequestID correlaciona os selos desta via.
	reconciliacaoRequestID = "aos436-reconciliacao"
	// kekResourceType rotula o Resource quando o titular NÃO é conhecido — uma KEK que só o
	// registo conhece. O selo nomeia o `id` do registo, nunca o nome do Vault (invertível).
	kekResourceType = "dsar.kek"
	// obReshred é a obrigação que carrega os metadados do re-apagamento (nunca PII).
	obReshred = "dsar.reshred"

	// prazoDaReconciliacaoNoArranque e prazoDaReconciliacaoPeriodica são o orçamento de UMA
	// passagem. Esgotado, o que ficou por verificar é contado e nomeado e a passagem fica por
	// provar — a seguinte continua.
	prazoDaReconciliacaoNoArranque = 30 * time.Second
	prazoDaReconciliacaoPeriodica  = 45 * time.Second
)

// ErrApagamentoPorReconciliar — a reconciliação dos apagamentos com a custódia não ficou provada.
// Enquanto persistir, o portão da custódia recusa embrulhar e desembrulhar DEKs.
var ErrApagamentoPorReconciliar = errors.New("aos: apagamentos DSAR por reconciliar com a custodia da KEK (AOS-436) — o conteudo por-titular fica fechado ate a reconciliacao ficar provada")

// custodiaReconciliavel é a porta que a reconciliação exige da custódia. O [vaultKeyVault]
// implementa-a. O vault in-memory de referência NÃO, e está certo: as suas KEKs morrem com o
// processo, pelo que nenhum restauro as traz de volta.
type custodiaReconciliavel interface {
	// listarKEKs devolve os nomes das KEKs que a custódia TEM.
	listarKEKs(ctx context.Context) ([]string, error)
	// nascimentoDaKEK diz quando a chave nasceu (geração mais antiga que a custódia guarda).
	nascimentoDaKEK(ctx context.Context, nome string) (nascida time.Time, existe bool, err error)
	// destruirKEKPorNome destrói e só devolve nil com a destruição CONFIRMADA.
	destruirKEKPorNome(ctx context.Context, nome string) error
	// registarReconciliacao guarda o desfecho: o erro global (nil = provada) e as KEKs
	// bloqueadas no portão.
	registarReconciliacao(err error, bloqueadas map[string]bloqueioDeKEK)
}

// bloqueioDeKEK é uma KEK ressuscitada que ficou viva. retida ⇒ por legal hold (não tira o nó de
// rotação); senão ⇒ a destruição ou a verificação falhou (tira).
type bloqueioDeKEK struct {
	motivo string
	retida bool
}

// alvoDeReconciliacao é tudo o que o nó sabe sobre UMA chave destruída.
type alvoDeReconciliacao struct {
	id          string    // HMAC do nome, quando há registo — é o que o selo nomeia sem titular
	destruidaEm time.Time // o instante MAIS RECENTE de destruição conhecido
	titular     string    // "" quando só o registo a conhece
	origem      string    // "cadeia", "registo" ou "importado" — a primeira fonte que a trouxe
}

// relatorioDeReconciliacao são as contagens de uma passagem. Nunca titulares.
type relatorioDeReconciliacao struct {
	DaCadeia         int
	DoRegisto        int
	DoImportado      int
	Rejeitadas       int // linhas/factos recusados (MAC, forma, instante no futuro) — nunca destroem
	Acrescentadas    int // entradas que faltavam no registo próprio e foram escritas
	Vivas            int // KEKs que o Vault tem, das conhecidas como destruídas
	Reprovisionadas  int // vivas, nascidas DEPOIS da destruição: titular que voltou
	DestruidasDeNovo int // ressuscitadas e destruídas de novo, com o facto selado
	Retidas          int // ressuscitadas sob legal hold: bloqueadas, não destruídas
	PorVerificar     int // ficaram por verificar (falha da custódia ou orçamento esgotado)
}

// reconciliadorDeApagamentos junta as fontes, interroga a custódia e re-destrói o que voltou.
type reconciliadorDeApagamentos struct {
	worm      audit.Store
	particao  string
	custodia  custodiaReconciliavel
	registo   *registoDeApagamentos // nil ⇒ sem registo próprio (declarado no banner)
	importado string                // "" ⇒ nenhum registo importado
	holds     *audit.LegalHold      // a barreira de destruição (a mesma do shredder)
	retido    func(titular string) bool
	agora     func() time.Time
	log       func(string, ...any)

	mu           sync.Mutex
	ultimoEstado string // o último desfecho anunciado — o log só fala quando muda
}

// reconciliar corre UMA passagem e regista o desfecho na custódia. Serializado.
func (r *reconciliadorDeApagamentos) reconciliar(ctx context.Context) (relatorioDeReconciliacao, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rel, bloqueadas, err := r.passagem(ctx)
	r.custodia.registarReconciliacao(err, bloqueadas)
	for _, a := range r.registo.tirarAvisos() {
		r.log("apagamentos DSAR (AOS-436): %s", a)
	}
	return rel, err
}

// reconciliarPeriodicamente é a via do laço de manutenção da custódia. Só escreve no log quando o
// desfecho MUDA — a mesma linha a cada minuto deixava de ser lida (a lição do aviso de opacidade).
func (r *reconciliadorDeApagamentos) reconciliarPeriodicamente(log func(string, ...any)) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), prazoDaReconciliacaoPeriodica)
	defer cancel()
	_ = r.registo.descarregar() // a passagem mede o que ficar pendente
	rel, err := r.reconciliar(ctx)
	estado := "PROVADA"
	if err != nil {
		estado = "POR PROVAR: " + err.Error()
	}
	r.mu.Lock()
	mudou := estado != r.ultimoEstado || rel.DestruidasDeNovo > 0
	r.ultimoEstado = estado
	r.mu.Unlock()
	if !mudou {
		return
	}
	if err != nil {
		log("apagamentos DSAR (AOS-436): reconciliacao POR PROVAR — o conteudo por-titular fica FECHADO no portao da custodia e o /readyz VERMELHO; re-tenta no proximo tick: %v", err)
		return
	}
	log("apagamentos DSAR (AOS-436): reconciliacao PROVADA — %s", rel.resumo())
}

// passagem é o corpo de [reconciliar]. Chamado sob r.mu. Devolve o relatório, as KEKs bloqueadas
// uma a uma e o erro GLOBAL (as fontes e a custódia).
func (r *reconciliadorDeApagamentos) passagem(ctx context.Context) (relatorioDeReconciliacao, map[string]bloqueioDeKEK, error) {
	var rel relatorioDeReconciliacao
	var falhas []string
	bloqueadas := make(map[string]bloqueioDeKEK)
	agora := r.agora()

	// (1) A CADEIA DSAR — sempre, seja o que for que aconteça às outras fontes.
	porNome, titularDe, rejCadeia, err := apagamentosDaCadeia(ctx, r.worm, r.particao, agora)
	if err != nil {
		falhas = append(falhas, "cadeia DSAR: "+err.Error())
	}
	falhas = append(falhas, rejCadeia...)
	rel.Rejeitadas += len(rejCadeia)
	rel.DaCadeia = len(porNome)
	for nome, a := range porNome {
		a.origem = "cadeia"
		porNome[nome] = a
	}

	// (2) O REGISTO PRÓPRIO e (3) O IMPORTADO — cada um por si. Uma fonte que falha é NOMEADA e
	// deixa a passagem por provar; as outras continuam a contar.
	porId := make(map[string]*alvoDeReconciliacao)
	var proprio map[string]time.Time
	lerFonte := func(caminho string, ausenteOK bool, origem string) map[string]time.Time {
		lida, lerr := r.registo.ler(caminho, ausenteOK, agora)
		if lerr != nil {
			dica := ""
			if origem == "importado" && r.registo.foiCriadaAgora() {
				dica = " — a chave do registo foi CRIADA neste arranque: o bundle restaurado e anterior a ela; copie `" +
					sufixoDaChaveDoRegisto + "` do bundle mais recente para o volume"
			}
			falhas = append(falhas, origem+": "+lerr.Error()+dica)
			return nil
		}
		if lida.fragmento {
			r.log("apagamentos DSAR (AOS-436): %s termina num fragmento sem fim de linha (escrita interrompida) — ignorado nesta leitura", caminho)
		}
		for _, rej := range lida.rejeitadas {
			if origem == "importado" && r.registo.foiCriadaAgora() {
				rej += " (a chave do registo foi CRIADA neste arranque — o bundle e anterior a ela)"
			}
			falhas = append(falhas, origem+": "+rej)
		}
		rel.Rejeitadas += len(lida.rejeitadas)
		for id, quando := range lida.validas {
			a, ok := porId[id]
			if !ok {
				porId[id] = &alvoDeReconciliacao{id: id, destruidaEm: quando, origem: origem}
				continue
			}
			if quando.After(a.destruidaEm) {
				a.destruidaEm = quando
			}
		}
		return lida.validas
	}
	if r.registo != nil {
		proprio = lerFonte(r.registo.caminho, true, "registo")
		rel.DoRegisto = len(proprio)
		if r.importado != "" {
			rel.DoImportado = len(lerFonte(r.importado, false, "importado"))
		}
	} else if r.importado != "" {
		falhas = append(falhas, "importado: AOS_DSAR_ERASURE_REGISTER_IMPORT sem AOS_DSAR_ERASURE_REGISTER — sem a chave do registo proprio nao ha como autenticar o importado")
	}

	if len(porNome) == 0 && len(porId) == 0 {
		return rel, bloqueadas, juntarFalhas(falhas) // nada conhecido como destruído: nada a pedir ao Vault
	}

	// (4) O QUE A CUSTÓDIA TEM. Um LIST; sem ele nada se verifica.
	vivas, lerr := r.custodia.listarKEKs(ctx)
	if lerr != nil {
		falhas = append(falhas, "custodia: "+lerr.Error())
		rel.PorVerificar = len(porNome) + len(porId)
		return rel, bloqueadas, juntarFalhas(falhas)
	}

	// (5) DO id AO NOME, pelas chaves que existem; e o registo próprio passa a superconjunto.
	if r.registo != nil {
		nomesCadeia := nomesOrdenados(porNome)
		idCadeia, ierr := r.registo.idsDe(nomesCadeia)
		idVivas, verr := r.registo.idsDe(vivas)
		if ierr != nil || verr != nil {
			falhas = append(falhas, "registo: a chave do registo nao esta legivel — o registo nao foi cruzado com a custodia")
		} else {
			for id, nome := range idCadeia {
				porNome[nome].id = id
			}
			for id, a := range porId {
				nome, viva := idVivas[id]
				if !viva {
					nome, viva = idCadeia[id]
				}
				if !viva {
					continue // a KEK não existe no Vault: continua destruída, nada a fazer
				}
				t, ok := porNome[nome]
				if !ok {
					porNome[nome] = &alvoDeReconciliacao{id: id, destruidaEm: a.destruidaEm, titular: titularDe[nome], origem: a.origem}
					continue
				}
				if a.destruidaEm.After(t.destruidaEm) {
					t.destruidaEm = a.destruidaEm
				}
			}
			var faltam []entradaDeApagamento
			for _, nome := range nomesOrdenados(porNome) {
				a := porNome[nome]
				if q, ok := proprio[a.id]; a.id != "" && (!ok || a.destruidaEm.After(q)) {
					faltam = append(faltam, entradaDeApagamento{id: a.id, destruidaEm: a.destruidaEm})
				}
			}
			for id, a := range porId {
				if q, ok := proprio[id]; !ok || a.destruidaEm.After(q) {
					if _, jaVai := idCadeia[id]; !jaVai {
						faltam = append(faltam, entradaDeApagamento{id: id, destruidaEm: a.destruidaEm})
					}
				}
			}
			if werr := r.registo.acrescentar(faltam...); werr != nil {
				falhas = append(falhas, "registo: "+werr.Error())
			} else {
				rel.Acrescentadas = len(faltam)
			}
		}
	}

	// (6) CHAVE A CHAVE — só as que existem. Nenhuma deixa de ser processada por causa de outra.
	existe := make(map[string]bool, len(vivas))
	for _, n := range vivas {
		existe[n] = true
	}
	alvos := make([]string, 0)
	for _, nome := range nomesOrdenados(porNome) {
		if existe[nome] {
			alvos = append(alvos, nome)
		}
	}
	rel.Vivas = len(alvos)
	for i, nome := range alvos {
		if ctx.Err() != nil {
			rel.PorVerificar += len(alvos) - i
			falhas = append(falhas, fmt.Sprintf("custodia: orcamento da passagem esgotado com %d de %d KEK(s) vivas por verificar", len(alvos)-i, len(alvos)))
			for _, n := range alvos[i:] {
				bloqueadas[n] = bloqueioDeKEK{motivo: "por verificar (orcamento esgotado)"}
			}
			break
		}
		a := porNome[nome]
		nascida, viva, nerr := r.custodia.nascimentoDaKEK(ctx, nome)
		switch {
		case nerr != nil:
			rel.PorVerificar++
			bloqueadas[nome] = bloqueioDeKEK{motivo: "idade por verificar: " + nerr.Error()}
			continue
		case !viva:
			continue // morreu entre o LIST e o GET
		case a.destruidaEm.IsZero():
			bloqueadas[nome] = bloqueioDeKEK{motivo: "a chave existe e o instante da destruicao e desconhecido"}
			continue
		case nascida.After(a.destruidaEm.Truncate(time.Second)):
			rel.Reprovisionadas++
			continue
		}
		titular := a.titular
		if titular == "" {
			titular = titularDe[nome]
		}
		if motivo, retida := r.reDestruir(ctx, nome, titular); motivo != "" {
			bloqueadas[nome] = bloqueioDeKEK{motivo: motivo, retida: retida}
			if retida {
				rel.Retidas++
			}
			continue
		}
		if serr := r.selarReshred(ctx, a, titular, nascida); serr != nil {
			falhas = append(falhas, "selo do re-apagamento NAO escrito (a KEK morreu; o facto perde-se): "+serr.Error())
		}
		rel.DestruidasDeNovo++
	}
	return rel, bloqueadas, juntarFalhas(falhas)
}

// reDestruir destrói a KEK ressuscitada SOB A BARREIRA do legal hold — a mesma que o shredder toma
// ([audit.LegalHold.BeginDestruction]): um hold colocado a meio não chega tarde. Devolve o motivo
// do bloqueio ("" = destruída e confirmada) e se o bloqueio é por hold.
//
// O HOLD É CONSULTADO. Uma KEK que um restauro trouxe de volta cifra dados apagados antes; mas um
// hold sobre o titular é uma ordem de preservação, e destruir o que ele cobre é destruir prova. A
// KEK fica BLOQUEADA no portão — nada se decifra com ela, nada se escreve sob ela — e o que decide
// é um humano, pelo levantamento do hold.
func (r *reconciliadorDeApagamentos) reDestruir(ctx context.Context, nome, titular string) (string, bool) {
	defer r.holds.BeginDestruction()()
	if titular != "" && r.retido != nil && r.retido(titular) {
		return "ressuscitada e sob LEGAL HOLD — nao destruida, bloqueada no portao ate o hold ser levantado", true
	}
	if err := r.custodia.destruirKEKPorNome(ctx, nome); err != nil {
		return "ressuscitada e a re-destruicao NAO foi confirmada: " + err.Error(), false
	}
	return "", false
}

// selarReshred sela `dsar.key_reshredded`. Sem PII e sem nome do Vault: o titular só aparece quando
// a cadeia já o tinha; senão, o `id` do registo.
func (r *reconciliadorDeApagamentos) selarReshred(ctx context.Context, a *alvoDeReconciliacao, titular string, nascida time.Time) error {
	res := audit.Resource{Type: kekResourceType, Value: a.id}
	if titular != "" {
		res = audit.Resource{Type: subjectResourceType, Value: titular}
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
				"destruida_em": a.destruidaEm.UTC().Format(time.RFC3339),
				"nascida_em":   nascida.UTC().Format(time.RFC3339),
				"origem":       a.origem,
			},
		}},
	})
	return err
}

// apagamentosDaCadeia extrai da cadeia DSAR as chaves destruídas e o instante MAIS RECENTE de cada
// destruição, e o mapa nome→titular de TODOS os titulares que a cadeia (DSAR e legal hold) nomeia —
// é por ele que uma KEK conhecida só pelo registo chega ao titular, e o legal hold é consultado.
//
// SÓ `dsar.key_destroyed` é autoridade: é o facto que o fluxo DSAR sela depois de a custódia
// confirmar a destruição pedida por um humano. O `dsar.key_reshredded` é prova do que a
// reconciliação fez, e relê-lo como autoridade foi o que deixou um instante envenenado ficar no
// WORM imutável a condenar a KEK nova do titular a cada arranque.
//
// Um facto datado para lá de `agora+folga` é REJEITADO (nomeado) — um relógio adiantado no instante
// do apagamento não pode condenar as gerações seguintes.
func apagamentosDaCadeia(ctx context.Context, store audit.Store, particao string, agora time.Time) (map[string]*alvoDeReconciliacao, map[string]string, []string, error) {
	porNome := make(map[string]*alvoDeReconciliacao)
	titularDe := make(map[string]string)
	if store == nil {
		return porNome, titularDe, nil, nil
	}
	lerParticao := func(p string) ([]audit.AuditRecord, error) {
		head, err := store.Head(ctx, p)
		if err != nil || head == 0 {
			return nil, err
		}
		return store.Read(ctx, p, 1, head)
	}
	// Os titulares com legal hold, para o mapa nome→titular. Uma falha aqui não impede a
	// reconciliação da cadeia DSAR; fica nomeada.
	var rejeitadas []string
	if recs, err := lerParticao(legalHoldPartition); err != nil {
		rejeitadas = append(rejeitadas, "cadeia de legal hold ilegivel — as KEKs conhecidas so pelo registo nao chegam ao titular: "+err.Error())
	} else {
		for _, rec := range recs {
			if s, _ := legalHoldTargetOf(rec); s != "" {
				titularDe[nomeDaKEK(s)] = s
			}
		}
	}
	recs, err := lerParticao(particao)
	if err != nil {
		return porNome, titularDe, rejeitadas, fmt.Errorf("leitura da cadeia %q: %v", particao, err)
	}
	for _, rec := range recs {
		if rec.Resource.Type != subjectResourceType || rec.Resource.Value == "" {
			continue
		}
		titular := rec.Resource.Value
		nome := nomeDaKEK(titular)
		titularDe[nome] = titular
		if rec.Capability != dsar.EventKeyDestroyed {
			continue
		}
		if rec.Timestamp.After(agora.Add(folgaDoFuturo)) {
			rejeitadas = append(rejeitadas, fmt.Sprintf("cadeia DSAR: key_destroyed audit_seq=%d datado no FUTURO (%s) — rejeitado, NAO destroi nada", rec.AuditSeq, rec.Timestamp.UTC().Format(time.RFC3339)))
			continue
		}
		a, ok := porNome[nome]
		if !ok {
			porNome[nome] = &alvoDeReconciliacao{destruidaEm: rec.Timestamp.UTC(), titular: titular}
			continue
		}
		if rec.Timestamp.After(a.destruidaEm) {
			a.destruidaEm = rec.Timestamp.UTC()
		}
	}
	return porNome, titularDe, rejeitadas, nil
}

func juntarFalhas(falhas []string) error {
	if len(falhas) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d falha(s): %s", ErrApagamentoPorReconciliar, len(falhas), strings.Join(falhas, "; "))
}

func (rel relatorioDeReconciliacao) resumo() string {
	return fmt.Sprintf("fontes: cadeia %d, registo proprio %d, importado %d (%d rejeitada(s), %d acrescentada(s) ao registo); %d KEK(s) conhecidas como destruidas existem no Vault: %d geracoes NOVAS (intactas), %d RESSUSCITADAS destruidas DE NOVO (dsar.key_reshredded selado), %d sob LEGAL HOLD (bloqueadas), %d por verificar",
		rel.DaCadeia, rel.DoRegisto, rel.DoImportado, rel.Rejeitadas, rel.Acrescentadas, rel.Vivas, rel.Reprovisionadas, rel.DestruidasDeNovo, rel.Retidas, rel.PorVerificar)
}

// comporReconciliacaoDeApagamentos liga o registo à custódia, corre a reconciliação do arranque e
// declara a postura. Devolve nil quando a custódia não é reconciliável.
func comporReconciliacaoDeApagamentos(ctx context.Context, cfg Config, worm audit.Store, particao string, vault audit.KeyVault,
	holds *audit.LegalHold, retido func(string) bool, log func(string, ...any)) *reconciliadorDeApagamentos {
	cust, ok := vault.(custodiaReconciliavel)
	if !ok {
		_, referencia := vault.(*audit.InMemoryKeyVault)
		switch {
		case !referencia:
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
	} else {
		log("apagamentos DSAR (AOS-436): SEM registo de apagamentos (AOS_DSAR_ERASURE_REGISTER vazio) — a reconciliacao usa SO a cadeia DSAR: cobre um Vault restaurado com o WORM actual, NAO cobre restaurar um bundle anterior a um apagamento")
	}
	r := &reconciliadorDeApagamentos{
		worm: worm, particao: particao, custodia: cust, registo: reg, importado: cfg.DSARErasureRegisterImport,
		holds: holds, retido: retido, agora: time.Now, log: log,
	}
	rctx, cancel := context.WithTimeout(ctx, prazoDaReconciliacaoNoArranque)
	defer cancel()
	rel, err := r.reconciliar(rctx)
	if err != nil {
		r.ultimoEstado = "POR PROVAR: " + err.Error()
		log("apagamentos DSAR (AOS-436): reconciliacao do arranque POR PROVAR — o conteudo por-titular fica FECHADO no portao da custodia (nada se decifra nem se escreve sob KEK) e o /readyz VERMELHO; a manutencao da custodia re-tenta a cada tick: %v", err)
		return r
	}
	r.ultimoEstado = "PROVADA"
	log("apagamentos DSAR (AOS-436): reconciliacao do arranque PROVADA — %s", rel.resumo())
	if rel.DestruidasDeNovo > 0 {
		log("apagamentos DSAR (AOS-436): ⚠️ %d KEK(s) destruida(s) REAPARECERAM na custodia — tipicamente um restauro de backup anterior ao apagamento — e foram destruidas DE NOVO; o facto ficou selado como %s na particao %q", rel.DestruidasDeNovo, EventKeyReshredded, particao)
	}
	if rel.Retidas > 0 {
		log("apagamentos DSAR (AOS-436): ⚠️ %d KEK(s) ressuscitada(s) sob LEGAL HOLD — NAO destruidas; bloqueadas no portao da custodia ate o hold ser levantado", rel.Retidas)
	}
	return r
}
