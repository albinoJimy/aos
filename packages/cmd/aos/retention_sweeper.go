package main

// SCHEDULER INTERNO DE RETENÇÃO (AOS-267, Grupo B) — vive no LOOP DE SERVIÇO, no molde EXACTO do
// varrimento de aprovações (approval_sweeper.go) e do de deadlines (deadline_sweeper.go): um
// ticker que termina com o MESMO `sweepStop` fechado pelo Shutdown.
//
// O PROBLEMA QUE FECHA. O [audit.ExpirationJob] estava composto e correcto, mas tinha UM condutor:
// `POST /dsar/expire`. Ou seja, com `AOS_RETENTION_VERSION`+`AOS_RETENTION_PERIODS` definidas — um
// operador que fez tudo o que o README lhe pediu — NADA expirava enquanto não existisse um cron
// EXTERNO que soubesse assinar com a credencial forte de governação. O risco não é uma feature em
// falta: é *storage limitation* (RGPD Art. 5.º-1-e) silenciosamente não aplicada, com o nó a
// anunciar uma política de retenção que ninguém executava. Um TTL que só corre se alguém se lembrar
// não é um TTL.
//
// O QUE ESTE LAÇO NÃO MUDA (e é deliberado):
//
//   - NÃO reimplementa a expiração. Corre o MESMO [audit.ExpirationJob] que a rota corre — a
//     política, a idempotência, o crypto-shred por-titular e a BARREIRA DE LEGAL HOLD são os do
//     job (ver expiration.go `held`: por-titular, pela partição do registo e, via o índice
//     titular→partição, pelas DEMAIS partições do titular). Um segundo varredor escrito à mão
//     seria um segundo sítio onde o legal hold podia ficar mais fraco.
//   - NÃO substitui `POST /dsar/expire`. A rota continua a servir a passagem SOB DEMANDA de um
//     operador autenticado; as duas vias partilham o MESMO guard de serialização
//     ([NodeService.expireInFlight]) para que nunca corram em simultâneo.
//
// CREDENCIAL EM NOME PRÓPRIO. Um varrimento agendado não tem humano na origem, e havia duas formas
// erradas de o resolver: pedir emprestada a credencial de governação de alguém (uma mentira selada
// na cadeia) ou não selar nada (destruição não-atribuível). Nenhuma serve. O scheduler age sob a
// SUA identidade — [retentionSchedulerNHI], uma NHI do próprio nó, nomeada no selo junto do trust
// anchor do issuer deste deployment — e cada passagem deixa DOIS selos na cadeia WORM de retenção:
//
//   - `retention.sweep.started` ANTES de a passagem correr, e FAIL-CLOSED: se o WORM não aceitar o
//     selo, a passagem NÃO corre. É a disciplina de legalhold.go ("sela primeiro, aplica depois"),
//     e aqui é indispensável porque o `retention.expired` que o job sela por-registo NÃO leva
//     Principal ([audit.BuildRetentionExpiredRecord]): sem este selo, a destruição automática
//     ficaria na cadeia sem NINGUÉM a quem a atribuir.
// RESIDUAL NOMEADO, com eixo (não se over-claim): [retentionSchedulerNHI] é uma IDENTIDADE em
// nome próprio, não uma CREDENCIAL verificada. O selo leva a NHI do nó como atribuição estável e
// distinta de qualquer operador, mas não há token emitido e verificado pela cadeia de identidade
// a suportá-la — o `issuer` copiado de [Config.IssuerID] nomeia o trust anchor do deployment, não
// prova uma autenticação. É atribuição forte o suficiente para a cadeia ser lida sem ambiguidade,
// e menos do que o critério de aceitação diz à letra ("credencial em nome próprio"). O eixo é o
// D4 (autoridade de identidade, EPIC-16/AOS-156): enquanto uma NHI do próprio nó não for
// emissível e verificável, uma "credencial" aqui seria auto-emitida — uma afirmação a fingir de
// prova, que é precisamente o que a disciplina deste ficheiro recusa.
//
//   - `retention.sweep.completed` DEPOIS, com o QUÊ da passagem: varridos/expirados/RETIDOS por
//     legal hold/saltados/não-expirados. Contagens, nunca titulares (a mesma disciplina sem-PII de
//     [expireResponse]).
//
// Os dois selos vão para a MESMA partição dos `retention.expired` ([retentionPartition]) de
// propósito: numa cadeia gapless, o selo de atribuição fica imediatamente ANTES das destruições que
// autorizou e o de desfecho imediatamente a seguir — a ordem é ela própria a prova, e um auditor lê
// "quem, o quê, quando" sem cruzar partições.
//
// PORQUE É QUE SELA MESMO QUANDO NADA EXPIRA. Uma passagem que não expirou nada não é ruído: é a
// evidência de que a aplicação da retenção está VIVA. Sem ela, um scheduler parado há três meses é
// indistinguível de um scheduler que não teve nada a fazer — e é exactamente essa a pergunta a que
// uma auditoria de *storage limitation* precisa de responder.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// DefaultRetentionSweepInterval é a cadência por omissão do scheduler de retenção.
//
// É UMA HORA, e não o minuto dos outros dois varrimentos deste loop, porque o que se mede é
// diferente. Um pendente de aprovação expira em 15 minutos e um deadline de run em minutos — ali a
// latência de observação tem de ser pequena face ao prazo. Um TTL de retenção mede-se em dias ou
// meses (`pii_operational=720h` é o exemplo do README): varrer 60 vezes mais depressa não encurta
// materialmente a janela de conformidade e multiplica por 60 tanto os selos de atribuição na cadeia
// WORM como o custo de cada passagem — que é uma leitura de TODOS os streams do Event Store
// ([eventStoreRecordSource.List]), não uma consulta indexada como a do sweeper de aprovações.
const DefaultRetentionSweepInterval = 1 * time.Hour

// Vocabulário estável dos selos do scheduler (sem PII).
const (
	// retentionSweepStartedEvent — a passagem vai correr, sob esta identidade (o selo de ATRIBUIÇÃO).
	retentionSweepStartedEvent = "retention.sweep.started"
	// retentionSweepCompletedEvent — a passagem correu, com estas contagens (o selo de DESFECHO).
	retentionSweepCompletedEvent = "retention.sweep.completed"

	// capRetentionSweep é o direito exercido: conduzir UMA passagem de expiração por TTL.
	capRetentionSweep = "retention:sweep"

	// retentionSweepToolID nomeia o produtor do selo (sem PII), no molde de [legalHoldToolID].
	retentionSweepToolID = "gov.retention.scheduler"

	// retentionSchedulerNHI é a identidade EM NOME PRÓPRIO sob a qual o varrimento agendado age.
	// É deliberadamente DISTINTA de qualquer principal de operador: quem lê a cadeia distingue,
	// sem ambiguidade, uma expiração conduzida por um humano autenticado (`POST /dsar/expire`, cujo
	// principal vem do gate soberano) de uma expiração conduzida pelo nó a cumprir a política. O nó
	// nunca se apresenta como um operador que não está lá.
	retentionSchedulerNHI = "nhi:aos-node/retention-scheduler"

	// Rótulos das obrigações seladas (metadados de governação, nunca PII).
	obRetentionSweepTrigger = "gov.retention.trigger"
	obRetentionSweepCounts  = "gov.retention.counts"

	// retentionHoldSourceWORM nomeia a PROVENIÊNCIA da barreira de preservação que vigorou na
	// passagem: os holds em vigor foram repostos da cadeia durável no arranque
	// ([restoreLegalHolds]), não apenas construídos em memória. Ver [sealRetentionSweep].
	retentionHoldSourceWORM = "worm:" + legalHoldPartition

	// retentionTriggerScheduler distingue a ORIGEM da passagem no selo. Hoje só o scheduler sela
	// (a rota mantém a superfície de AOS-213 intacta), mas o campo existe para que acrescentar
	// outra origem amanhã não obrigue a reinterpretar os selos já escritos.
	retentionTriggerScheduler = "scheduler"
	// retentionTriggerRota distingue a expiração pedida por um HUMANO pela rota. O selo passa a
	// carregar o principal autenticado em vez da NHI do nó — e é isso que torna as duas passagens
	// comparáveis na cadeia: mesma forma, gatilho e autor diferentes.
	retentionTriggerRota = "rota"
)

// ErrBadRetentionSweepInterval — AOS_RETENTION_SWEEP_INTERVAL está definida mas não é uma duração
// Go > 0. FAIL-CLOSED de config, no molde de [ErrBadIngressLimits]/[ErrBadRetention]: o nó recusa
// arrancar em vez de degradar para o default.
//
// E, ao contrário do varrimento de aprovações, NENHUM valor DESLIGA o scheduler — nem "0". Desligar
// a expiração periódica com a política de retenção definida é precisamente o estado que este
// ticket veio eliminar: o nó anunciaria uma retenção que nada aplicava. Quem não quer expiração
// não define a política (`AOS_RETENTION_VERSION`/`AOS_RETENTION_PERIODS` vazias ⇒ nada expira, e o
// scheduler nem arranca).
var ErrBadRetentionSweepInterval = errors.New("aos: AOS_RETENTION_SWEEP_INTERVAL invalida (esperada uma duracao Go > 0, ex.: \"1h\") — e a cadencia com que a expiracao por TTL corre sozinha; NENHUM valor a desliga (0 nao desliga: com a politica de retencao definida, um scheduler desligado e a violacao de storage-limitation que AOS-267 fecha). Deixe a variavel POR DEFINIR para manter o default")

// WithRetentionSweepInterval sobrepõe a cadência do scheduler de retenção. <= 0 DESLIGA o laço —
// alcançável apenas EM PROCESSO (os testes conduzem a passagem à mão via
// [NodeService.SweepRetentionNow]); pelo ambiente não há valor que o desligue, ver
// [ErrBadRetentionSweepInterval]. Arma a flag Set, sem a qual o "explicitamente 0" seria
// indistinguível de "não configurado" e o default ganharia.
func WithRetentionSweepInterval(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.retentionSweepInterval = d
		c.retentionSweepIntervalSet = true
	}
}

// retentionSweepIntervalFromEnv resolve a cadência a partir do ambiente. Vazia ⇒
// [DefaultRetentionSweepInterval]; malformada/≤0 ⇒ [ErrBadRetentionSweepInterval] (aborta o
// arranque). Devolve SEMPRE a duração EM VIGOR, para que o valor anunciado e o valor ligado sejam
// o mesmo por construção (a disciplina de [ingressLimitsFromEnv]).
func retentionSweepIntervalFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_RETENTION_SWEEP_INTERVAL"))
	if raw == "" {
		return DefaultRetentionSweepInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: AOS_RETENTION_SWEEP_INTERVAL=%q", ErrBadRetentionSweepInterval, raw)
	}
	return d, nil
}

// retentionSchedulerArmed decide se o scheduler ARRANCA, e é a conjunção de tudo o que uma
// destruição automática e não-supervisionada exige. FAIL-CLOSED em cada perna — na dúvida o
// scheduler fica DORMENTE e a expiração continua a ser conduzível pela rota:
//
//  1. cadência > 0 e nó composto;
//  2. [audit.ExpirationJob] composto (sem ele não há o que conduzir);
//  3. POLÍTICA DE RETENÇÃO ARMADA — versão rotulada E pelo menos uma classe com período. Sem
//     política nada expiraria, e um laço que sela a sua atribuição de hora a hora para não fazer
//     nada seria ruído na cadeia. É também o antecedente literal do critério de aceitação;
//  4. LEGAL HOLD COMPOSTO. O job só faz valer a preservação se lhe tiverem passado o
//     [audit.LegalHold] (com holds nil, `ExpirationJob.held` devolve sempre false). Uma expiração
//     SOB DEMANDA sem barreira de hold ainda tem um humano autenticado a assumi-la; um varredor
//     automático que apague material sob preservação é um incidente sem ninguém no caminho. Sem a
//     barreira composta, o scheduler NÃO arranca;
//     4-bis. LEGAL HOLD RE-HIDRATADO ([Node.holdsRestored]). A perna (4) prova que a barreira
//     EXISTE; esta prova que o seu CONTEÚDO sobreviveu ao restart. Um [audit.LegalHold]
//     construído vazio a cada arranque é não-nulo e derrota `ExpirationJob.held` em silêncio —
//     `holds` não é nil, está VAZIO — enquanto a cadeia WORM continua a AFIRMAR a preservação.
//     A guarda tinha a ameaça certa e a prova errada; ver governance_restore.go;
//  5. WORM composto — sem cadeia não há selo de atribuição, e a regra deste laço é que nada se
//     destrói sem que o "quem" fique selado primeiro.
func retentionSchedulerArmed(node *Node, interval time.Duration) bool {
	if interval <= 0 || node == nil {
		return false
	}
	if node.ExpirationJob == nil || node.DSARHolds == nil || node.WORM == nil {
		return false
	}
	if !node.holdsRestored {
		return false
	}
	return retentionPolicyArmed(node.Retention)
}

// retentionPolicyArmed indica se a política de retenção tem de facto TTL a aplicar: uma versão que
// a rotula E pelo menos uma classe com período definido (>0). Uma config vazia (o default do nó)
// devolve false — nada expira, o comportamento de sempre.
func retentionPolicyArmed(rc audit.RetentionConfig) bool {
	if strings.TrimSpace(rc.Version()) == "" {
		return false
	}
	for _, class := range []audit.DataClass{
		audit.ClassDiagnostic, audit.ClassTrajectory, audit.ClassAudit, audit.ClassPIIOperational,
	} {
		if _, ok := rc.Period(class); ok {
			return true
		}
	}
	return false
}

// sweepRetention é o laço periódico. Termina quando stop fecha (shutdown do serviço) OU quando uma
// passagem detecta a hash-chain do WORM comprometida — ver [NodeService.sweepRetentionOnce].
func (s *NodeService) sweepRetention(stop <-chan struct{}) {
	if !retentionSchedulerArmed(s.node, s.retentionSweepInterval) {
		return
	}
	t := time.NewTicker(s.retentionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if !s.sweepRetentionOnce(context.Background()) {
				// Integridade da cadeia comprometida: o laço PÁRA. Continuar a destruir sobre um
				// log de auditoria que já não se verifica é o oposto de fail-closed — cada
				// passagem seguinte tornaria o incidente menos investigável. A expiração fica
				// conduzível pela rota (que responde 500 pelo mesmo motivo, AOS-221), e o
				// operador tem o incidente no log com o eixo nomeado.
				return
			}
		}
	}
}

// sweepRetentionOnce conduz UMA passagem completa: selo de ATRIBUIÇÃO → passagem do job → selo de
// DESFECHO → verificação pós-shred da cadeia. Devolve false SÓ quando a hash-chain do WORM deixou
// de validar (o laço pára); todos os outros erros são best-effort e re-tentados no tick seguinte.
//
// Exportada-por-teste através de [NodeService.SweepRetentionNow].
func (s *NodeService) sweepRetentionOnce(ctx context.Context) bool {
	// EXCLUSÃO ENTRE RÉPLICAS (AOS-283), e é a PRIMEIRA porta porque é a única que fala do
	// mundo fora deste processo. Sem ela, o guard `expireInFlight` abaixo — um `atomic.Bool`
	// POR PROCESSO — dava a impressão de serializar as passagens quando só serializava as
	// DESTA réplica: medido, duas réplicas sobre o mesmo substrato selavam DOIS
	// `retention.expired` para o MESMO facto, poluindo a cadeia gapless que a idempotência
	// do job existe para proteger. Ver posse_de_laco.go.
	//
	// Devolve TRUE quando não é líder: não há incidente nenhum — há outra réplica a fazer
	// este trabalho. O laço continua vivo e volta a tentar a posse no tick seguinte, que é
	// o que lhe permite assumir quando a líder morrer (TTL) ou anunciar a largada.
	if !s.posseRetencao.assumir(ctx, s.sweepStop) {
		return true
	}
	// SERIALIZAÇÃO partilhada com POST /dsar/expire (ver a nota em [apiHandler.handleExpire]): o
	// Run do job faz Seen(key)…Add(key) sem atomicidade check-then-act, pelo que duas passagens
	// concorrentes poderiam selar DOIS `retention.expired` para o mesmo facto. O guard é UM só,
	// no [NodeService], para que rota e scheduler não possam sobrepor-se — um guard por-via seria
	// dois guards e nenhuma exclusão entre eles. Não conseguir o guard NÃO é erro: a rota está a
	// correr uma passagem agora, e ela faz o mesmo trabalho.
	if !s.expireInFlight.CompareAndSwap(false, true) {
		s.log("scheduler de retencao (AOS-267): passagem SALTADA — ja ha uma expiracao em curso (POST /dsar/expire ou tick anterior); re-tenta no proximo tick")
		return true
	}
	defer s.expireInFlight.Store(false)

	at := time.Now().UTC()
	// O id correlaciona os DOIS selos da MESMA passagem na cadeia. Deriva do instante de arranque
	// (não há aleatoriedade no caminho de auditoria); repetições sob um relógio de teste congelado
	// são inócuas — o id correlaciona, não identifica univocamente.
	sweepID := "retsweep-" + at.Format(time.RFC3339Nano)

	// (1) ATRIBUIÇÃO, FAIL-CLOSED. Sem o "quem" selado, a passagem NÃO corre — nada se destrói
	// automaticamente sem ficar atribuído a esta identidade na cadeia tamper-evident.
	if err := s.sealRetentionSweep(ctx, retentionSweepStartedEvent, sweepID, at, nil); err != nil {
		s.log("scheduler de retencao (AOS-267): selo de ATRIBUICAO recusado pelo WORM — a passagem NAO corre (fail-closed: nada se destroi sem quem selado); re-tenta no proximo tick: %v", err)
		return true
	}

	// (2) A PASSAGEM. É o job composto no nó: mesma política, mesma idempotência, mesmo
	// crypto-shred por-titular e MESMA barreira de legal hold da rota. Um erro parcial (ex.: uma
	// selagem de `retention.expired` falhada) é agregado pelo job, que processa os restantes
	// registos na mesma; o desfecho é selado a seguir de qualquer forma.
	report, runErr := s.node.ExpirationJob.Run(ctx)

	// (3) DESFECHO (o "o quê"): contagens, nunca titulares.
	if err := s.sealRetentionSweep(ctx, retentionSweepCompletedEvent, sweepID, time.Now().UTC(), &report); err != nil {
		// Nada fica por auditar: CADA destruição foi selada pelo job ANTES de acontecer. O que se
		// perde é o resumo da passagem — registado aqui de forma ruidosa.
		s.log("scheduler de retencao (AOS-267): selo de DESFECHO recusado pelo WORM (as destruicoes desta passagem ficaram seladas uma a uma pelo job; falta o resumo): %v", err)
	}
	if runErr != nil {
		s.log("scheduler de retencao (AOS-267): passagem com erro parcial (os restantes registos foram processados; re-tenta no proximo tick): %v", runErr)
	}
	// DESTRUIÇÃO POR CONFIRMAR — o eixo tem de ter CAUSA no log. Sem isto, uma política Transit
	// sem `deletion_allowed` fazia a primeira passagem marcar a pendência e o nó ficava UNREADY
	// para sempre com um sinal indistinguível do de um token a expirar, sem causa em lado nenhum.
	// A contagem não nomeia titulares (os nomes de chave são sha256 do keyRef).
	if pend, ok := shredPendingOf(s.node.DSARVault); ok && pend > 0 {
		s.log("scheduler de retencao (AOS-267): ATENCAO — %d destruicao(oes) de KEK POR CONFIRMAR na custodia. A expiracao esta SELADA na cadeia mas a chave pode continuar viva e o conteudo recuperavel; o /readyz fica VERMELHO ate uma destruicao confirmada. Causas tipicas: politica Transit sem deletion_allowed, replicacao, ou token sem autoridade para destruir", pend)
	}
	s.log("scheduler de retencao (AOS-267): passagem concluida sob %q (politica %q) — varridos=%d expirados=%d RETIDOS_por_legal_hold=%d saltados=%d nao_expirados=%d; expiracao = crypto-shred da KEK POR-TITULAR (apagamento real)",
		retentionSchedulerNHI, s.node.Retention.Version(),
		report.Scanned, report.Expired, report.Held, report.Skipped, report.NotExpired)

	// (4) VERIFICAÇÃO PÓS-SHRED (AOS-221), em PARIDADE com POST /dsar/expire: o crypto-shred
	// destrói a CHAVE, não os registos selados — a hash-chain TEM de continuar a validar. Um WORM
	// injectado opaco (sem [audit.PartitionLister]) não é verificável pelo nó e NÃO é falha da
	// expiração.
	if verr := s.node.VerifyWORM(ctx); verr != nil && !errors.Is(verr, audit.ErrPartitionsUnavailable) {
		s.log("scheduler de retencao (AOS-267): INCIDENTE DE INTEGRIDADE — a hash-chain do WORM NAO valida apos a passagem; o scheduler PARA (nao se destroi mais sobre um log de auditoria que ja nao se verifica). Investigue o WORM antes de religar o no: %v", verr)
		// A paragem e DEFINITIVA e ate agora so existia nesta linha de log. Marca-se para que
		// `/metrics` a possa dizer: sem isto, um no que deixou de expirar dados fora do TTL e
		// indistinguivel de um no que expira bem, ate alguem ler o log ate ao fim.
		// POR PROVAR, e fica declarado em vez de contado como coberto: nenhum teste exercita
		// ESTA linha pelo caminho real. Para o fazer seria preciso o `VerifyWORM` falhar DEPOIS
		// de uma passagem, e nem o MemStore nem o FileStore se deixam adulterar em memória — a
		// verificação lê a vista já carregada. Uma mutação que remova esta linha NÃO faz cair a
		// bateria; o que está provado é que a métrica LÊ o campo, não que o varredor o escreve.
		s.varredorParado.Store(true)
		return false
	}
	// PASSAGEM CONCLUIDA. O contador e o instante alimentam `aos_retention_*` — a resposta a
	// «a expiracao por TTL esta a correr?», que ate agora nao existia em runtime.
	s.varrimentosTotal.Add(1)
	s.ultimoVarrimentoUnix.Store(time.Now().Unix())
	return true
}

// sealRetentionSweep sela UM dos dois registos da passagem na cadeia [retentionPartition], SEM PII.
// report nil ⇒ o selo de ATRIBUIÇÃO (ainda não há contagens); report != nil ⇒ o de DESFECHO.
//
// O que fica selado, e porquê cada campo: o PRINCIPAL é a identidade em nome próprio do scheduler
// (o "quem" que faltava — o `retention.expired` do job não leva Principal); o `issuer` nomeia o
// trust anchor de identidade DESTE deployment (qual nó, não só qual papel); a `cadence` torna
// auditável a periodicidade realmente em vigor; `legal_hold=enforced` regista que a passagem correu
// com a barreira de preservação composta — o scheduler não arranca sem ela ([retentionSchedulerArmed]).
func (s *NodeService) sealRetentionSweep(ctx context.Context, event, sweepID string, at time.Time, report *audit.ExpirationReport) error {
	return s.selarPassagemDeRetencao(ctx, event, sweepID, at, report, retentionTriggerScheduler, retentionSchedulerNHI)
}

// selarPassagemDeRetencao e o selo de atribuicao, com o GATILHO e o PRINCIPAL parametrizados.
//
// Existia so para o varredor automatico, com os dois valores fixos. A rota `POST /dsar/expire`
// — que destroi KEKs de TODOS os titulares fora do TTL — nao selava atribuicao NENHUMA: o
// caminho SEM humano registava quem, o caminho COM humano nao registava ninguem.
//
// Uma so forma de registo para os dois gatilhos, porque um auditor tem de os poder comparar.
func (s *NodeService) selarPassagemDeRetencao(ctx context.Context, event, sweepID string, at time.Time, report *audit.ExpirationReport, gatilho, principal string) error {
	version := s.node.Retention.Version()
	params := map[string]string{
		"trigger":        gatilho,
		"cadence":        s.retentionSweepInterval.String(),
		"config_version": version,
		"issuer":         s.node.IssuerID,
		"legal_hold":     "enforced",
		// De ONDE veio a barreira que vigorou. `enforced` era, até à remediação da W6, um
		// literal incondicional — e um literal não é evidência: depois de um restart sem
		// re-hidratação, a cadeia tamper-evident registaria "a barreira estava em vigor"
		// precisamente na passagem que destruiu material retido. O antecedente é agora
		// VERIFICADO em [retentionSchedulerArmed] (perna 4-bis), não presumido aqui.
		"legal_hold_source": retentionHoldSourceWORM,
	}
	obligations := []audit.Obligation{{
		Type:   obRetentionSweepTrigger,
		Fields: []string{gatilho},
		Params: params,
	}}
	if report != nil {
		counts := map[string]string{
			"scanned":     strconv.Itoa(report.Scanned),
			"expired":     strconv.Itoa(report.Expired),
			"held":        strconv.Itoa(report.Held),
			"skipped":     strconv.Itoa(report.Skipped),
			"not_expired": strconv.Itoa(report.NotExpired),
		}
		// DESTRUIÇÕES POR CONFIRMAR (remediação da W6). `expired` conta o que ficou SELADO como
		// expirado — e a selagem precede o sink por desenho fail-closed (nada se destrói sem o seu
		// evento auditável). Quando a custódia sabe dizer que a chave pode ter sobrevivido
		// ([shredPendingReporter], hoje o Vault Transit), a cadeia regista-o AO LADO da contagem,
		// em vez de deixar `expired` a afirmar sozinho uma irrecuperabilidade por provar. Custódias
		// que não sabem responder não ganham um campo inventado — o parâmetro simplesmente não sai.
		if pend, ok := shredPendingOf(s.node.DSARVault); ok {
			counts["shred_unconfirmed"] = strconv.Itoa(pend)
		}
		obligations = append(obligations, audit.Obligation{
			Type:   obRetentionSweepCounts,
			Fields: []string{event},
			Params: counts,
		})
	}
	rec := audit.AuditRecord{
		Partition:     retentionPartition,
		Timestamp:     at,
		Decision:      audit.DecisionAllow,
		Principal:     audit.Principal{NHIID: principal},
		Capability:    capRetentionSweep,
		PolicyVersion: version,
		RequestID:     sweepID,
		ToolID:        retentionSweepToolID,
		Resource:      audit.Resource{Type: event, Value: sweepID},
		Obligations:   obligations,
	}
	_, err := s.node.WORM.Append(ctx, rec)
	return err
}

// SweepRetentionNow conduz UMA passagem imediatamente. Existe para os testes a conduzirem de forma
// determinista, sem esperar pelo ticker (molde de [NodeService.SweepApprovalsNow]). Devolve false
// se a cadeia do WORM deixou de validar — a mesma condição que pára o laço.
func (s *NodeService) SweepRetentionNow(ctx context.Context) bool {
	if !retentionSchedulerArmed(s.node, s.retentionSweepInterval) {
		return true
	}
	return s.sweepRetentionOnce(ctx)
}

// retentionSchedulerBanner declara a postura do scheduler a partir do estado REALMENTE composto —
// nunca da intenção da config (AOS-248: postura anunciada = postura ligada). É emitido por
// [NewNodeService], que é quem sabe se o laço arrancou.
func retentionSchedulerBanner(node *Node, interval time.Duration) string {
	if retentionSchedulerArmed(node, interval) {
		return fmt.Sprintf("scheduler de retencao (AOS-267): LIGADO — a expiracao por TTL corre SOZINHA de %s em %s (AOS_RETENTION_SWEEP_INTERVAL), sem cron externo; politica %q; cada passagem sela retention.sweep.started ANTES (fail-closed: sem o selo de atribuicao a passagem nao corre) e retention.sweep.completed DEPOIS, sob a identidade EM NOME PROPRIO %q na particao %q; RESPEITA o legal hold (a barreira e a do ExpirationJob — por-titular, por-particao e pelas demais particoes do titular) e partilha com POST /dsar/expire o guard que impede passagens concorrentes",
			interval, interval, node.Retention.Version(), retentionSchedulerNHI, retentionPartition)
	}
	// DORMENTE — a razão importa mais do que o facto: cada uma tem um remédio diferente.
	switch {
	case node == nil || node.ExpirationJob == nil:
		return "scheduler de retencao (AOS-267): DORMENTE — o ExpirationJob nao esta composto; a expiracao por TTL nao corre por via nenhuma"
	case node.DSARHolds == nil:
		return "scheduler de retencao (AOS-267): DORMENTE por FAIL-CLOSED — o legal hold NAO esta composto, e um varredor AUTOMATICO sem barreira de preservacao poderia apagar material sob hold sem ninguem no caminho; a expiracao fica conduzivel so por POST /dsar/expire (que tem um operador autenticado a assumi-la)"
	case !node.holdsRestored:
		return "scheduler de retencao (AOS-267): DORMENTE por FAIL-CLOSED — o legal hold NAO foi RE-HIDRATADO do substrato duravel (a cadeia governance.legalhold ou o Event Store nao foram lidos no arranque). Um conjunto de holds VAZIO nao e prova de que nao ha preservacoes: a cadeia pode continuar a afirma-las e o varredor AUTOMATICO destruiria material retido. A expiracao fica conduzivel so por POST /dsar/expire; investigue o erro de re-hidratacao no log de arranque"
	case node.WORM == nil:
		return "scheduler de retencao (AOS-267): DORMENTE por FAIL-CLOSED — sem WORM composto nao ha onde selar QUEM conduziu o varrimento, e nada se destroi automaticamente sem atribuicao selada"
	case !retentionPolicyArmed(node.Retention):
		return "scheduler de retencao (AOS-267): DORMENTE — SEM politica de retencao (AOS_RETENTION_VERSION + AOS_RETENTION_PERIODS vazias): nada tem TTL a aplicar, logo nao ha o que varrer periodicamente. Defina a politica para que a expiracao passe a correr sozinha"
	default:
		return "scheduler de retencao (AOS-267): DESLIGADO em processo (cadencia <= 0) — a expiracao so corre por POST /dsar/expire"
	}
}
