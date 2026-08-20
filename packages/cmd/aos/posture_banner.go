package main

// AOS-248 (achado F14) — AS QUATRO SUPERFÍCIES QUE O BANNER CALAVA.
//
// A disciplina de banner do nó (AOS-203) é uma só regra: POSTURA ANUNCIADA = POSTURA LIGADA.
// Cada subsistema que o operador possa julgar composto declara o estado REALMENTE composto, com
// a causa e — quando exista — o remédio ALCANÇÁVEL a partir do ambiente. O silêncio é um modo de
// falha: sobre estas quatro superfícies, quem lia o banner do nó não tinha como saber que
//
//   - não há orçamento nenhum a limitar o que um run gasta;
//   - não há credential broker: os segredos entram por ficheiro e ficam no processo;
//   - o modelo tanto pode ser o gateway real como um turno fixo de referência;
//   - sem AOS_AUTONOMY_LEVELS **nenhum** `escalate` é emitido — e portanto todo o bridge de
//     aprovação humana está construído mas INALCANÇÁVEL.
//
// A regra que estas funções seguem, e que quem lhes tocar a seguir tem de manter: uma linha que
// diga "ligado" sobre algo que não está composto é PIOR do que o silêncio que substitui — treina
// o operador a confiar numa protecção que não existe. Por isso todas as afirmações abaixo estão
// amarradas ao ESTADO composto (o valor recebido por parâmetro), não à intenção da config, e as
// que são estáticas ("não composto") só o são porque o composition-root inteiro não tem uma única
// linha que as componha.
//
// São funções PURAS (estado → linhas) pelo mesmo motivo que [devModelCredentialBanner] e
// [sovereignReadKillSwitchBanner]: assim os testes cobrem cada estado sem levantar um nó.

import (
	"fmt"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// BudgetScopeDeclaration é o ALCANCE da v1 do orçamento — a FRASE, não uma paráfrase dela.
// Aparece nos DOIS estados de [budgetPostureBanner] porque descreve o que o orçamento cobre
// QUANDO composto, e essa é precisamente a coisa que o operador precisa de saber antes de o
// ligar (e não depois).
//
// # A frase MUDOU em AOS-260, e a mudança é o ticket
//
// O texto aprovado em AOS-255 dizia «cobre tool calls em TOKENS; o gasto de inferencia e
// travado por tempo (wall-clock), nao por tecto». Era verdade e era o defeito: o hook de
// orçamento é um [rm.Hook] e a cadeia do Reference Monitor só é atravessada POR TOOL CALL,
// enquanto o turno de modelo era invocado DIRECTAMENTE (`kernel/agent-runtime/loop.go`,
// `rt.model.Call`), fora da cadeia, sem hook e sem reserva — a LINHA DE CUSTO DOMINANTE sem
// admission control (desafio A1, risco 1).
//
// A decisão do dono (D1 = opção B, 2026-08-13) mandou fechá-lo, e AOS-260 fechou-o: há uma
// porta de ADMISSÃO no runtime ([agentruntime.ModelAdmission]) que RESERVA headroom no MESMO
// nó de orçamento por-run imediatamente antes da inferência e o SALDA com o consumo MEDIDO da
// resposta (usage + `CostMicroUSD`, o canal que AOS-259 ligou). Manter a frase velha seria
// exactamente o defeito que este banner existe para evitar: postura anunciada ≠ postura ligada.
//
// A frase nova diz as três coisas que mudaram e a que não mudou:
//
//   - o alcance deixou de ser tool-only (cobre as DUAS coisas que gastam);
//   - o mecanismo é reserva ANTES + saldo pelo REAL (e não uma contagem a posteriori, que é
//     burn-down e declaradamente não decide — AOS-261/AOS-262);
//   - a dimensão de TOKENS decide sempre; a de DÓLARES é medida sempre mas só NEGA quando o
//     operador configura o tecto (`AOS_BUDGET_MAX_COST_MICRO_USD`). Dizer «cobre em dólares»
//     sem essa ressalva seria a promessa a mais.
//
// Escrita em ASCII sem acentos como o resto do banner. A forma acentuada da mesma frase é a que
// está em `deploy/node/README.md` e no relatório de prontidão.
const BudgetScopeDeclaration = "orcamento: cobre tool calls E o turno de modelo em TOKENS — reserva antes da inferencia, saldo pelo consumo medido; o tecto em dolares e opcional e so decide quando configurado"

// budgetPostureBanner declara o ORÇAMENTO em função do que está REALMENTE composto — o
// parâmetro é o estado, nunca a intenção da config, como em [modelPostureBanner] e
// [autonomyPostureBanner].
//
// Os dois estados dizem coisas DIFERENTES e ambos têm de ser verdade:
//
//   - composed == false (o default: `AOS_BUDGET_MAX_TOKENS` por definir): o ponto de injecção
//     "budget" da cadeia ficou com o [referencemonitor.BudgetStub] neutro — não há reserva, não
//     há tecto por-run/por-árvore e nenhuma decisão do Reference Monitor consulta custo. Nem as
//     tool calls estão cobertas. O único controlo com aparência de tecto de custo (as
//     velocidades de queima do disjuntor) está INLIGÁVEL desde AOS-246: sem
//     [breaker.VelocitySource] cablada, um valor > 0 aborta o arranque
//     ([ErrBreakerVelocitySourceUnwired]);
//   - composed == true (LIGADO em AOS-256/AOS-257, quando AOS_BUDGET_MAX_TOKENS está
//     definida): há tecto por-run, e o que ele cobre é EXACTAMENTE
//     [BudgetScopeDeclaration] — nem mais.
//
// Porque é que o ramo COMPOSTO já existe antes de haver wiring: é o inverso de uma promessa a
// mais. O defeito que AOS-255 remove não é a ausência de orçamento — é o operador ler
// "orçamento LIGADO" e inferir que o gasto do agente tem tecto, quando a linha dominante ficou
// de fora. Fixar o texto ANTES do wiring é o que impede que a linha seja escrita, no calor do
// ticket que liga o hook, como se cobrisse tudo. AOS-256/AOS-257 fizeram exactamente o que
// aqui estava previsto: o ARGUMENTO passou a derivar do estado real (`runBudget != nil` no
// composition-root) e a linha ganhou o nome da env do tecto — a declaração de alcance NÃO foi
// reescrita.
//
// A linha existe para que "o nó não me avisou" deixe de ser verdade: um agente autónomo sem
// tecto de gasto é uma decisão, e uma decisão tem de estar escrita onde o operador a lê.
func budgetPostureBanner(composed bool) []string {
	if composed {
		return []string{
			"orcamento / tecto de custo (AOS-008): COMPOSTO e a cobrir as DUAS coisas que gastam — " + BudgetScopeDeclaration + ". Ou seja: as TOOL CALLS sao admitidas pelo hook de orcamento na cadeia do Reference Monitor (AOS-256/257) e o TURNO DE MODELO passou a ser admitido pela porta de admissao do runtime (AOS-260, agentruntime.ModelAdmission), que RESERVA com o prompt ja materializado IMEDIATAMENTE antes de rt.model.Call e SALDA a seguir com o consumo MEDIDO da resposta (usage + cost_micro_usd, o canal de AOS-259) — a provisao nunca fica como contabilidade. ESGOTAMENTO = DEGRADACAO DECLARADA, nunca deny-loop cego: o loop NAO retenta; com o prompt de exaustao armado (AOS-263) o run SUSPENDE-SE em waiting_on_human a espera da decisao humana assinada (continue/abort) e fica retomavel, e sem ele para com razao propria e sela timed_out com o rotulo budget_exhausted (nunca failed — um tecto atingido nao e falha recuperavel por compensacao). REPLAY NAO RE-RESERVA: um turno REPRODUZIDO da captura na retoma e admitido SEM reserva (dedup por run_id:step_id, o mesmo par que o Event Store e o step-ledger ja usam), pelo que retomar um run nao o cobra duas vezes pelo mesmo turno. A dimensao que DECIDE por omissao e TOKENS; a de DOLARES e debitada e legivel em cada run mas so NEGA se AOS_BUDGET_MAX_COST_MICRO_USD estiver definida, e a projecao de custo do turno seguinte sai da TARIFA MEDIDA do proprio run (no primeiro turno de cada incarnacao ainda nao ha medicao, e a decisao e so por tokens). O TECTO EM DOLARES NAO E ACEITE SEM FONTE DE PRECO: definir AOS_BUDGET_MAX_COST_MICRO_USD num no cujo par (modelo, regiao) nao tem preco na tabela em vigor — ou que corre com o modelo de REFERENCIA, cujo custo e uma constante fabricada — ABORTA o arranque (ErrBudgetCostNoPriceSource), em vez de servir um tecto que compararia sempre contra ZERO e nunca negaria. Quando esse tecto esta em vigor, o burn-down (AOS-262) le as DUAS dimensoes e a pergunta humana de exaustao (AOS-263) reporta as DUAS e NOMEIA a que bloqueou o run, no campo reason de pending_exhaustion. O tecto e POR-RUN (AOS_BUDGET_MAX_TOKENS tokens por run, registado como no proprio da arvore no arranque de cada run e libertado no fim), nunca por-mandato/delegacao: dois runs concorrentes tem tectos independentes. ATENCAO — o tecto e tambem POR-INCARNACAO e NAO E CUMULATIVO ENTRE ELAS: a arvore de orcamento vive EM MEMORIA e o no do run e criado no arranque de CADA hospedagem e removido no fim, pelo que cada RE-HOSPEDAGEM do mesmo run_id (retoma apos escalada/aprovacao humana em POST /runs/{id}/resume, ou apos restart do processo) recebe um no NOVO com o tecto INTEIRO. Um run em ciclo de escalada/retoma pode portanto gastar ate N x AOS_BUDGET_MAX_TOKENS, N = numero de incarnacoes. ASSIMETRIA DECLARADA: o burn-down/aviso (AOS-261/AOS-262) le o LEDGER DURAVEL chaveado por run_id e por isso E cumulativo entre incarnacoes — o AVISO ve o total, o ENFORCEMENT recomeca. Fechar a assimetria exige estado de orcamento DURAVEL por run, que este no nao tem (eixo por abrir). A ESTIMATIVA da TOOL CALL e a da CALL MATERIALIZADA (AOS-258, integration.TokenOnlyEstimator — o DefaultEstimator placeholder do control-plane/budget NAO e usado): conta os ARGUMENTOS na forma FINAL do efeito (pos-reescrita) MAIS o envelope que tambem ocupa contexto (tool_id, capability, resource), por aproximacao de tokens sem dependencias — piso de ~1 token por 4 bytes, elevado nos payloads estruturados (JSON/URLs/base64), 1 token por rune nao-ASCII. A ESTIMATIVA do TURNO DE MODELO e o PROMPT MATERIALIZADO pela MESMA contagem MAIS uma PROVISAO DE OUTPUT fixa, porque o output e desconhecido antes da chamada — provisao que o saldo substitui pelo consumo real no MESMO turno, pelo que sobre-provisionar custa admissao momentanea e nunca orcamento. NAO se conta o RESULTADO da tool (so mensuravel DEPOIS do efeito). Complementos que continuam a valer: o wall-clock do disjuntor (AOS_BREAKER_MAX_WALL_CLOCK), o no-progress e MaxTurns",
		}
	}
	return []string{
		"orcamento / tecto de custo (AOS-008): NAO COMPOSTO — AOS_BUDGET_MAX_TOKENS nao esta definida, logo o ponto de injeccao \"budget\" da cadeia ficou com o hook NEUTRO de referencia E a porta de admissao do turno de modelo (AOS-260) nao foi ligada: nao ha reserva, nao ha tecto por-run nem por-arvore, e NENHUMA decisao e negada por custo — nem tool call nem inferencia; um run so para por MaxTurns, pelo disjuntor (no-progress/wall-clock) ou por steer/pause do operador. ALCANCE DECLARADO da v1 para quando FOR composto (define AOS_BUDGET_MAX_TOKENS para o ligar; AOS_BUDGET_MAX_COST_MICRO_USD acrescenta-lhe o tecto em dolares, que sem ela e medido mas nao nega) — " + BudgetScopeDeclaration + ". Os tectos de VELOCIDADE de queima (AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC / AOS_BREAKER_MAX_TOKENS_PER_SEC) NAO sao ligaveis hoje: sem VelocitySource cablada, qualquer valor >0 ABORTA o arranque (ErrBreakerVelocitySourceUnwired, AOS-246). Eixo: AOS-008 / EPIC-20",
	}
}

// credentialBrokerPostureBanner declara a AUSÊNCIA do Credential Broker (AOS-070, ADR-006).
// Também incondicional: `platform/broker` não é importado pelo nó. O que o nó faz em vez disso
// — ler segredos de FICHEIROS MONTADOS no arranque e retê-los em memória do processo enquanto
// viver — é legítimo e é o padrão documentado, mas NÃO é o invariante do ADR-006 e não deve ser
// confundido com ele: sem broker não há troca token→credencial server-side, não há TTL curto,
// não há revogação por lease e não há injecção no ponto de execução.
//
// As variáveis são nomeadas para que o operador saiba EXACTAMENTE que material está no processo,
// e a lista é EXAUSTIVA sobre os caminhos de material privado que o nó lê do disco — porque é
// dela que sai a lista do que há a rodar e a proteger. Uma lista apresentada como completa e que
// omite material é pior do que não a dar: faz sub-dimensionar a rotação com a confiança de quem
// leu tudo (achado F-A3 da auditoria da W0, que omitia as três CHAVES PRIVADAS abaixo).
//
// Duas famílias, com riscos distintos, por isso declaradas em separado:
//
//   - CREDENCIAIS DOWNSTREAM (bearers/tokens que o nó APRESENTA a terceiros): AOS_MODEL_API_KEY_PATH,
//     AOS_OTLP_BEARER_TOKEN_PATH, AOS_DSAR_VAULT_TOKEN_PATH, AOS_ATTESTATION_VERIFIER_TOKEN_PATH;
//   - CHAVES PRIVADAS do próprio nó: AOS_ISSUER_KEY_PATH (`main.go` — a chave ed25519 de ASSINATURA
//     da autoridade co-localizada, o segredo de maior valor que o nó detém: quem a tem cunha
//     tokens), AOS_TLS_KEY_PATH (`main.go` — chave do ingresso) e AOS_OTLP_CLIENT_KEY_PATH
//     (`otlpexporter.go` — chave de cliente mTLS para o MESMO colector cujo bearer está na lista
//     de cima). Todas lidas de ficheiro e retidas em memória do processo, exactamente como as
//     credenciais — e nenhuma delas é gerida por broker.
//
// Os VALORES nunca aparecem no banner — nem aqui nem em [devModelCredentialBanner].
func credentialBrokerPostureBanner() []string {
	return []string{
		"credential broker (AOS-070/EPIC-07, ADR-006): AUSENTE — este no NAO compoe o platform/broker. As credenciais downstream entram por FICHEIRO MONTADO (AOS_MODEL_API_KEY_PATH, AOS_OTLP_BEARER_TOKEN_PATH, AOS_DSAR_VAULT_TOKEN_PATH, AOS_ATTESTATION_VERIFIER_TOKEN_PATH), sao lidas UMA VEZ no arranque e ficam em MEMORIA do processo enquanto o no viver: sem troca token-scoped server-side, sem TTL curto, sem revogacao por lease e sem injeccao no ponto de execucao. Pelo MESMO caminho (ficheiro montado, lido no arranque, retido em memoria) entram tambem as CHAVES PRIVADAS do proprio no: AOS_ISSUER_KEY_PATH (chave ed25519 de ASSINATURA da autoridade co-localizada — quem a obtiver cunha tokens deste emissor), AOS_TLS_KEY_PATH (chave do ingresso TLS) e AOS_OTLP_CLIENT_KEY_PATH (chave de cliente mTLS do colector). Esta lista e a superficie COMPLETA de material privado que o processo detem — e o que ha a rodar e a proteger. O invariante do ADR-006 (\"o agente apresenta identidade, NUNCA segredo\") NAO e imposto por este no — e responsabilidade de quem monta os ficheiros. Nenhum VALOR e impresso, aqui ou em qualquer linha. Eixo: EPIC-07",
	}
}

// modelPostureBanner declara QUAL modelo está composto, a partir do valor REALMENTE recebido em
// [Config.Model] (não de AOS_MODEL_ENDPOINT: um modelo injectado por config nunca passa pelo
// ambiente). Três estados, porque o nó tem três:
//
//   - nil ⇒ [referenceModel]: um turno final fixo, sem tool calls, com um custo CONSTANTE. Nada
//     sai do nó. É o default e é fácil confundi-lo com "o gateway está a responder";
//   - [modelgateway.ModelClientAdapter] ⇒ o Model Gateway REAL (EPIC-06). A linha declara o que
//     ele traz E os residuais que ele NÃO fecha neste wiring — senão "gateway REAL" leria-se
//     como "postura de produção", que é precisamente a promessa a mais que AOS-248 proíbe;
//   - qualquer outra coisa ⇒ injectado por config (testes/wiring avançado): o nó não sabe o que
//     é e NÃO finge saber.
//
// Os residuais declarados no ramo do gateway estão todos verificados em [newGatewayModelClient]:
// o `HTTPClient` injectado DELEGA a validação de egress (seam de dev), o `Audit` do gateway é um
// [audit.NewMemStore] PRÓPRIO (não o WORM do nó) e [modelgateway] `translateResponse` não
// preenche `CostMicroUSD` — o custo por-turno que chega ao span `chat` é zero.
func modelPostureBanner(m agentruntime.ModelClient) []string {
	switch m.(type) {
	case nil:
		return []string{
			"modelo (EPIC-06): MODELO DE REFERENCIA (referenceModel) — NAO ha model gateway composto: cada turno devolve um texto fixo e FINAL, sem tool calls, com um custo CONSTANTE de 1500 micro-USD que NAO e uma medicao; NENHUMA chamada sai deste no. Defina AOS_MODEL_ENDPOINT + AOS_MODEL_NAME para compor o Model Gateway REAL",
		}
	case *modelgateway.ModelClientAdapter:
		return []string{
			"modelo (EPIC-06): MODEL GATEWAY REAL composto (modelgateway.NewProduction) — IDENTIDADE REAL do GW LIGADA (AOS-278, CUTOVER DURO): o estagio authn REAL substituiu o antigo stub allow-all; cada turno EXIGE o token NHI do RUN (Goal.Credential) verificado (EdDSA + janela + revogacao + raiz humana ADR-003) pelo MESMO verifier das tool calls, e o principal e RESOLVIDO do token, NUNCA forjado — sem credencial no ctx o pedido e NEGADO ATRIBUIVELMENTE; o token TEM de selar model:invoke no escopo. allowlist regional ASSINADA, keypool, routing de failover, metering/pricing e endurecimento SSRF do gateway. RESIDUAIS DECLARADOS deste wiring: (1) o egress e DELEGADO no http.Client injectado (seam de dev) — o SSRF fail-closed de AOS-223 so volta a valer com BaseURL https + AllowedEgressHosts e SEM HTTPClient; (2) o audit de governacao do gateway (activacao da allowlist + decisoes) e um MemStore PROPRIO do gateway, NAO o WORM do no — nao entra na hash-chain que o no verifica. O RESIDUAL (3) FECHOU (AOS-259): o adaptador RT->GW JA propaga o custo — translateResponse preenche CostMicroUSD a partir de port.Usage.CostMicroUSD, que o metering do gateway escreve na resposta normalizada, e dai o numero flui para o span chat, para o acumulado do run e para o campo cost_micro_usd do evento DURAVEL turn.recorded. Se o custo e ZERO ou DERIVADO depende de a tabela de precos cobrir o par (modelo, regiao) deste no — o que a linha de postura do canal de custo declara explicitamente",
		}
	default:
		return []string{
			"modelo (EPIC-06): ModelClient INJECTADO por Config.Model (nem gateway nem referencia) — o no nao conhece a sua origem, custo, egress nem soberania e NAO os atesta; a postura e a de quem o injectou",
		}
	}
}

// autonomyPostureBanner declara o ORÁCULO DE AUTONOMIA — a superfície mais cara de calar, porque
// a sua ausência é uma postura de segurança INVISÍVEL: sem oráculo, o veredicto `escalate` nunca
// é emitido, e portanto todo o bridge de aprovação humana (AOS-021) fica construído e
// INALCANÇÁVEL por muitos aprovadores que AOS_APPROVERS_FILE pine.
//
// w é a cablagem REALMENTE composta ([Config.Autonomy]): nil ⇒ não ligado. A afirmação "cada um
// SELADO" deriva de [autonomyWiring.sealedPairs] — os pares cujo SetLevel APLICOU E SELOU — e não
// de `specs`, que é só o que foi DECLARADO (achado F-A6 da auditoria da W0). A diferença importa:
// hoje o binário está certo porque [Bootstrap] provisiona antes de imprimir, mas uma reordenação
// do boot faria a versão antiga anunciar selos inexistentes com o teste na mesma verde. Daí o
// TERCEIRO ramo: cablagem construída e ainda não provisionada declara-se como tal, em vez de
// mentir ou de calar — é a regra da linha 19 aplicada a si própria.
//
// O ramo NÃO-LIGADO nomeia AS DUAS variáveis porque uma sozinha não chega: o oráculo entra pela
// opção [pdp.WithAutonomyOracle] de [pdp.Open], e [loadPolicyBundleFromEnv] só chega a LER
// AOS_AUTONOMY_LEVELS quando AOS_POLICY_BUNDLE_DIR está definida. Sem bundle, a variável é
// ignorada em silêncio — ATÉ se estiver malformada, porque nem chega a ser parseada. Um operador
// que a defina sozinha fica convencido de que ligou o gate humano.
func autonomyPostureBanner(w *autonomyWiring) []string {
	if w == nil {
		return []string{
			"autonomia / escalate (AOS-087): ORACULO NAO LIGADO — NENHUM veredicto `escalate` e emitido por este no, pelo que o bridge de aprovacao humana (AOS-021) fica INALCANCAVEL por muitos aprovadores que AOS_APPROVERS_FILE pine: Cedar so exprime permit/deny, e e a composicao nivel-de-autonomia x classe-de-risco que rebaixa um permit para escalate. Para o ligar sao precisas AS DUAS variaveis: AOS_AUTONOMY_LEVELS=\"agente:dominio=Ln\" E AOS_POLICY_BUNDLE_DIR (o oraculo e opcao do PDP carregado) — sozinha, AOS_AUTONOMY_LEVELS e IGNORADA em silencio, ate malformada",
		}
	}
	if len(w.sealedPairs) == 0 {
		return []string{
			fmt.Sprintf("autonomia / escalate (AOS-087/AOS-248): ORACULO CONSTRUIDO MAS NAO PROVISIONADO — ha %d entrada(s) declarada(s) em AOS_AUTONOMY_LEVELS mas NENHUM nivel foi aplicado nem selado (autonomyWiring.provision ainda nao correu): o registo responde L0 a TODO o par, o fail-closed do oraculo, pelo que TUDO escala e nenhum run avanca sem aval humano. No arranque normal esta linha NAO deve aparecer — se aparece, a ordem do composition-root inverteu-se (o provisionamento tem de correr DEPOIS do WORM e ANTES do banner)",
				len(w.specs)),
		}
	}
	// DUAS AFIRMAÇÕES QUE DEIXARAM DE SER VERDADE, e foi este trabalho que as quebrou:
	//
	//  · "par NAO registado ⇒ L0" — falso desde que existe AOS_AUTONOMY_DEFAULT. O piso é
	//    declarável, e anunciar L0 quando alguém declarou L3 diria ao operador que o sistema é
	//    MAIS supervisionado do que é.
	//  · a contagem é a DO ARRANQUE. Com POST /autonomy os níveis mudam em runtime, e esta linha
	//    envelhece no instante seguinte. Um banner que afirma o presente e descreve o passado é
	//    pior do que um que não afirma nada — e esta é, por confissão do próprio ficheiro, uma
	//    superfície com histórico de mentir.
	return []string{
		fmt.Sprintf("autonomia / escalate (AOS-087/AOS-248): ORACULO LIGADO — %d par(es) alvo:dominio provisionado(s) NO ARRANQUE de AOS_AUTONOMY_LEVELS, cada um SELADO na hash-chain WORM como autonomy.level_changed (particao %q, motivo %q, actor %q): uma mudanca de autonomia deixa de poder acontecer sem rasto. O alvo pode ser uma INSTANCIA (agt-1:fs) ou uma CLASSE (class:agent-worker:fs), com resolucao em CASCATA: instancia -> classe -> piso -> L0. Par sem instancia NEM classe registadas ⇒ %s (piso%s). E a composicao nivel x classe de risco que rebaixa um permit para ESCALATE — e portanto e esta ligacao que torna o bridge de aprovacao humana (AOS-021) ALCANCAVEL. ATENCAO: esta linha e o ESTADO NO ARRANQUE; POST /autonomy muda niveis em runtime (assinado e selado) e GET /autonomy e a FONTE DE VERDADE do que vigora agora",
			len(w.sealedPairs), autonomy.DefaultAutonomyPartition, autonomyProvisionReason, autonomyProvisionActor,
			w.piso.String(), pisoOrigem(w.piso)),
	}
}

// pisoOrigem distingue o piso HERDADO do DECLARADO. A diferença não é cosmética: "L0 por
// omissão" e "L0 porque alguém o escreveu" são posturas idênticas com responsáveis diferentes,
// e um banner que as confunde apaga precisamente a informação que o piso declarável introduziu.
func pisoOrigem(p autonomy.Level) string {
	if p == autonomy.L0 {
		return " por omissao, AOS_AUTONOMY_DEFAULT ausente"
	}
	return " DECLARADO em AOS_AUTONOMY_DEFAULT"
}

// burndownPostureBanner declara o BURN-DOWN e o AVISO DE EXAUSTÃO (AOS-261/AOS-262) em
// função do que está REALMENTE composto — o parâmetro é o estado do observador entregue ao
// loop, nunca a intenção da config (mesma disciplina de [budgetPostureBanner]).
//
// A linha existe porque esta é uma superfície com um histórico específico de mentir: até
// AOS-261, `Evaluate` recebia os spans por parâmetro e NADA no nó os produzia ou retinha
// (NoopTracer por omissão, SpanTracer dispara-e-esquece), pelo que qualquer leitura devolvia
// «0% consumido» — verde, e falso. O que se declara aqui tem de ser, portanto,
// EXACTAMENTE o que a fonte sabe e o que o aviso faz:
//
//   - a FONTE é o LEDGER DE TURNOS (`turn.recorded`), não spans. Conta os TURNOS DE MODELO
//     e só eles: o ledger não pesa tool calls, logo o burn-down é um LIMITE INFERIOR do
//     consumo do run — dispara TARDE, nunca cedo por engano;
//   - a dimensão que decide POR OMISSÃO é TOKENS. A frase antiga — «a de dólares está a zero
//     enquanto o canal de custo não estiver ligado ponta a ponta (AOS-259)» — deixou de ser
//     verdade em dobro: o canal está ligado (o `cost_micro_usd` de cada `turn.recorded` traz o
//     custo derivado) e a dimensão passou a ter TECTO PRÓPRIO quando o operador define
//     `AOS_BUDGET_MAX_COST_MICRO_USD`. Aí é o MÁXIMO das duas fracções que dispara o aviso, e
//     era exactamente aqui que a divergência postura-declarada/postura-composta se instalava:
//     um run avisado pelos dólares lia no banner que os dólares não tinham tecto;
//   - o aviso, EM SI, NÃO DECIDE NADA. Não pede escolha e não estende o tecto — não apresenta
//     extend/summarize_stop/abort. Quem apresenta opções é o PROMPT de AOS-263, e só as que
//     TÊM executor: o `abort` passou a ter (a decisão assinada da parte 3), `extend` e
//     `summarize_stop` continuam a não ter (o tecto não tem mutador — decisão do dono (iii);
//     o loop não tem caminho de resumo). Quem pára um run continua a ser o disjuntor (com
//     veredicto durável) ou o operador. O que o aviso PASSOU a poder fazer (AOS-263) é
//     SUSPENDER o run à espera de decisão humana — e suspender não é parar: o run fica
//     retomável. Se ESTE nó tem essa suspensão armada é a linha de
//     [exhaustionPromptPostureBanner] que o diz, para que as duas afirmações não vivam numa só
//     frase que só é verdadeira em metade dos nós;
//   - ONDE O AVISO SE VÊ. A linha nomeia as duas superfícies e diz qual delas depende de
//     config: o LOG do nó (sempre) e o span (só com `AOS_OTLP_ENDPOINT`). Anunciar só o
//     span era a mentira exacta que esta função existe para evitar — na configuração por
//     omissão o tracer é o NoopTracer e o operador não tinha ONDE olhar.
func burndownPostureBanner(composed bool, threshold float64) []string {
	if composed {
		return []string{
			fmt.Sprintf("burn-down / aviso de exaustao (AOS-261/AOS-262): COMPOSTO — o loop le, na fronteira de fim-de-turno, o consumo ACUMULADO do run no LEDGER DE TURNOS (eventos turn.recorded do event store, deduplicados por run_id:step_id: uma re-emissao NAO infla a contagem e a retoma NAO a zera, porque a chave e o run_id e nao o trace_id) e compara-o com o tecto por-run de AOS_BUDGET_MAX_TOKENS. Ao atingir %.2f da fraccao consumida avisa UMA VEZ POR RUN (latch por-incarnacao do processo; um restart re-avisa uma vez). ONDE SE VE O AVISO: uma linha [aos] AVISO DE BURN-DOWN NESTE LOG, que e o canal que existe sempre; o span aos.control.budget_warning SO tem destino com AOS_OTLP_ENDPOINT definida — sem ela o tracer do no e o NoopTracer e o span nao vai a lado nenhum. ALCANCE: conta os TURNOS DE MODELO e SO eles — o ledger nao pesa tool calls, pelo que a leitura e um LIMITE INFERIOR do consumo (o aviso dispara TARDE, nunca cedo por engano); a dimensao que decide por omissao e TOKENS (o tecto por-run e em tokens), e a de DOLARES ja e alimentada pelo canal de custo de AOS-259 — o campo cost_micro_usd de cada turn.recorded traz o custo DERIVADO pela tabela de precos quando ela cobre o par (modelo, regiao) do no (ver a linha de postura do canal de custo). A dimensao de DOLARES PASSA A TER TECTO PROPRIO quando AOS_BUDGET_MAX_COST_MICRO_USD esta definida: nesse caso o burn-down le as DUAS dimensoes e a fraccao e o MAXIMO entre elas, pelo que o aviso dispara pela que primeiro se aproximar do seu tecto — um run com um modelo caro e avisado pelos dolares muito antes de os tokens la chegarem, e a linha do aviso nomeia a grandeza. Sem essa env a dimensao $ e MEDIDA e legivel mas nao tem denominador, e nao contribui para a fraccao (dividir por um tecto que ninguem configurou daria um numero sem significado). O aviso, EM SI, NAO DECIDE NADA: nao pede escolha e NAO apresenta extend/summarize_stop/abort — quem apresenta opcoes e o PROMPT de AOS-263 (ver a linha seguinte), e mesmo esse so apresenta as que TEM executor: o abort passou a ter (decisao AUTENTICADA e selada, AOS-263 parte 3), extend e summarize_stop continuam a NAO ter (o budget.Budget nao tem mutador de tecto — decisao do dono; e o loop nao tem caminho de resumo). Quem para um run e o disjuntor (veredicto duravel) ou o operador; o que este aviso PODE accionar, quando o prompt de exaustao de AOS-263 esta ARMADO, e a SUSPENSAO do run em waiting_on_human a espera de decisao humana — suspender nao e parar: o run fica RETOMAVEL ate alguem decidir. FAIL-CLOSED: a leitura devolve ERRO e o run aborta — nunca 0%% — quando nao ha fonte, quando o ledger existe mas somou ZERO tokens (o provider nao ecoou usage, ErrBurndownNoUsage) ou quando o payload e ilegivel. Indisponibilidade TRANSITORIA do substrato (sem quorum, ctx do run a cair) NAO mata o run: a leitura adia-se para a fronteira seguinte e so passa a fatal ao fim de %d fronteiras consecutivas", threshold, maxLeiturasTransitoriasToleradas),
		}
	}
	return []string{
		"burn-down / aviso de exaustao (AOS-261/AOS-262): NAO COMPOSTO — AOS_BUDGET_MAX_TOKENS nao esta definida, logo NAO HA TECTO e portanto nao ha denominador: qualquer fraccao seria 0 para sempre e nenhum aviso poderia disparar. O loop nao consulta observador nenhum (o gancho de fim-de-turno fica inerte) e NADA neste no avisa que um run se aproxima de um limite de gasto — o que trava um run e MaxTurns, o disjuntor (no-progress/wall-clock) ou o steer do operador. Defina AOS_BUDGET_MAX_TOKENS para ligar o tecto e, com ele, o burn-down; AOS_PROGRESS_THRESHOLD ajusta o limiar do aviso (default ~0.80) e ABORTA o arranque se for definida SEM o tecto (ErrProgressBudgetUnwired). Eixo: AOS-261/AOS-262 / EPIC-20",
	}
}

// wormAnchorPostureBanner declara a VERIFICAÇÃO ANCORADA DO WORM (AOS-268/AOS-072) em função
// do que está REALMENTE composto — o argumento é o ESTADO da verificação no arranque (passou/não
// corre), nunca a intenção da config (mesma disciplina de [budgetPostureBanner] e
// [exhaustionPromptPostureBanner]).
//
// É uma LINHA PRÓPRIA, complementar à de AOS-221, porque as duas verificações fecham vectores
// DIFERENTES e um nó pode ter uma sem a outra: AOS-221 re-encadeia a hash-chain SEM chave
// (detecta mutação, remoção-interna, inserção, encadeamento-quebrado) mas, por não ter raiz de
// confiança FORA do store, NÃO apanha a TRUNCATURA DO TAIL (remover os registos mais recentes: a
// cadeia truncada re-encadeia como íntegra) nem a REESCRITA DESDE A GÉNESE através de um restart
// (uma cadeia forjada por inteiro re-encadeia consigo mesma). AOS-268 fecha esses dois com uma
// âncora ASSINADA out-of-band + um piso de frescura persistido INDEPENDENTEMENTE do store.
//
// Duas dobras, ambas verdadeiras:
//
//   - composed == true (as três envs presentes e [audit.VerifyFromCheckpointAtHead] PASSOU no
//     arranque): a assinatura do checkpoint validou contra o trust-anchor de AOS_WORM_TRUST_ANCHOR,
//     o AuditSeq da âncora é >= ao piso de AOS_WORM_EXPECTED_HEADS_FILE (não é rollback) e a âncora
//     corresponde ao registo real desse audit_seq. LIMITE HONESTO: o piso só prova até ao
//     audit_seq SELADO — registos legítimos apendidos DEPOIS do último checkpoint e ainda não
//     selados podem ser truncados sem esta verificação os apanhar; encolher a janela exige
//     SELAGEM PERIÓDICA (chave privada, out-of-process) — DEFERIDA (DEF-268);
//   - composed == false (default: nenhuma das três envs, ou a superfície ausente): a verificação
//     ancorada NÃO corre e o nó fica com o que AOS-221 dá — que NÃO fecha a truncatura do tail
//     nem a reescrita da génese. Declara-se o vector aberto e as três envs que o fecham, para que
//     "não fecha a truncatura" nunca seja um silêncio.
//
// A regra da linha 19 (uma afirmação amarrada ao ESTADO, nunca à intenção) vale aqui em dobro: o
// ramo composto SÓ é impresso quando a verificação PASSOU de facto — uma verificação FALHADA
// abortou o arranque antes de chegar ao banner, pelo que nunca há uma linha "ancorado" sobre um
// WORM que não ancorou.
// wormAnchorPostureBanner declara a COBERTURA, e nao so o estado.
//
// `ancoradas` de `total` particoes. Dizer apenas «ANCORADA» seria repetir o erro que este projecto
// ja pagou: um banner verdadeiro sobre o mecanismo (a verificacao correu) e enganador sobre o
// efeito (quanto e que ela cobre). A cobertura NUNCA e completa por desenho — as particoes nascem
// por run — pelo que o numero e a unica forma honesta de o dizer.
func wormAnchorPostureBanner(ancoradas, total int) []string {
	if ancoradas > 0 {
		naoAncoradas := total - ancoradas
		if naoAncoradas < 0 {
			naoAncoradas = 0
		}
		return []string{
			fmt.Sprintf("verificacao ancorada do WORM (AOS-268/AOS-072): ANCORADA em %d de %d particao(oes) — as restantes %d NAO estao ancoradas e ficam so com a re-verificacao de hash-chain (AOS-221), que NAO fecha a truncatura do tail nem a reescrita desde a genese. A cobertura nunca e completa POR DESENHO: as particoes nascem POR RUN, pelo que o run seguinte cria uma particao que nenhuma selagem anterior cobre — selar mais vezes (`aos-issuer worm-seal`) encolhe a janela, nao a fecha. ", ancoradas, total, naoAncoradas) +
				"Sobre as ANCORADAS: no arranque, audit.VerifyFromCheckpointAtHead validou a ANCORA ASSINADA do WORM (JSON de AOS_WORM_CHECKPOINT_FILE) contra o TRUST-ANCHOR out-of-band (AOS_WORM_TRUST_ANCHOR, pubkey ed25519) e contra o PISO DE FRESCURA persistido independentemente do store (AOS_WORM_EXPECTED_HEADS_FILE): a assinatura verificou, o AuditSeq da ancora e >= ao piso (nao e rollback de checkpoint — um checkpoint LEGITIMO mas anterior, reapresentado para mascarar truncatura do tail, teria dado ErrCheckpointStale) e a ancora corresponde ao registo real desse audit_seq (uma REESCRITA DESDE A GENESE diverge do EntryHash assinado ⇒ ErrCheckpointAnchor; um head caido abaixo da ancora ⇒ ErrRangeBeyondHead). Isto FECHA os dois vectores que AOS-221 (re-encadeamento sem chave) nao apanha: a TRUNCATURA DO TAIL e a reescrita da genese via restart. FAIL-CLOSED: qualquer uma destas falhas ABORTA o arranque — o no nao serve um WORM que nao ancorou. LIMITE HONESTO: o piso so prova ate ao audit_seq SELADO; registos legitimos apendidos DEPOIS do ultimo checkpoint e ainda nao selados podem ser truncados sem esta verificacao os apanhar — encolher essa janela exige SELAGEM PERIODICA de novos checkpoints (chave PRIVADA, out-of-process, molde AOS-156), DEFERIDA em DEF-268: este no CONSOME a ancora selada, nao a produz",
		}
	}
	return []string{
		"verificacao ancorada do WORM (AOS-268/AOS-072): NAO ANCORADA — as envs da ancora nao estao definidas, logo a verificacao ancorada NAO corre e o no fica so com a re-verificacao de hash-chain de AOS-221 (audit.VerifyStore no restart). Essa via SEM CHAVE detecta mutacao, remocao-interna, insercao e encadeamento-quebrado, mas NAO apanha a TRUNCATURA DO TAIL (remover os registos mais recentes: a cadeia truncada re-encadeia como integra) nem a REESCRITA DESDE A GENESE via restart (uma cadeia forjada por inteiro re-encadeia consigo mesma) — os dois vectores precisam de uma raiz de confianca FORA do store. Para os fechar, componha AS TRES juntas: AOS_WORM_TRUST_ANCHOR (pubkey ed25519 hex do selador, out-of-band), AOS_WORM_CHECKPOINT_FILE (JSON da ancora assinada selada out-of-process) e AOS_WORM_EXPECTED_HEADS_FILE (ficheiro JSON com o mapa particao->audit_seq com o piso POR PARTICAO, persistido INDEPENDENTEMENTE do store E do ficheiro de checkpoints — nao derivado do checkpoint, senao o piso seria um no-op e o rollback passaria; juntos no mesmo ficheiro, quem o trocasse trocava os dois). Definir ALGUMAS-mas-nao-todas ABORTA o arranque (ErrWormAnchorIncomplete). As ancoras produzem-se com `aos-issuer worm-seal --worm <copia-do-backup> --key-file <selador>` (a chave PRIVADA nunca vai para o servidor) e os pisos com o mesmo comando e `--heads`. A selagem PERIODICA (quem a corre, e com que cadencia) continua a ser decisao operacional: cada selagem encolhe a janela nao-ancorada, que nunca fecha porque as particoes nascem por run. Eixo: AOS-268/AOS-072 / EPIC-20",
	}
}

// exhaustionPromptPostureBanner declara o PROMPT DE EXAUSTÃO (AOS-263) em função do que está
// REALMENTE composto — o argumento é o prompt entregue ao observador de burn-down, nunca a
// intenção da config (mesma disciplina de [burndownPostureBanner]).
//
// É uma LINHA PRÓPRIA, e não mais uma frase na do aviso, por uma razão de verdade: o aviso e
// o prompt têm condições de composição DIFERENTES (o aviso precisa de tecto; o prompt precisa
// da maquinaria HITL do four-eyes E da rota de decisão composta) e um nó pode perfeitamente
// ter um sem o outro. Numa só linha, metade dos nós leria uma afirmação falsa.
//
// O que a linha tem de dizer, e porquê:
//
//   - O QUE ACONTECE ao cruzar o limiar (suspensão em `waiting_on_human`), para que ninguém
//     leia o aviso de AOS-262 e conclua que o run continua sempre;
//   - ONDE SE VÊ a pergunta (`GET /runs/{id}`, campo `pending_exhaustion`) — um prompt sem
//     superfície de leitura seria a mesma classe de defeito que AOS-262 fechou no aviso;
//   - QUE OPÇÕES têm executor (`continue` e `abort`, ambas na rota assinada de AOS-263 parte
//     3), COMO se decide (a assinatura ed25519 do operador cobre a decisão e a pergunta — não
//     é o mesmo sinal que o `pause`) e que a RETOMA está BARRADA até haver decisão: é a metade
//     que faltava para o prompt não ser contornável por quem já tinha credencial do run;
//   - QUE `extend` e `summarize_stop` NÃO são oferecidos, com a razão de cada um: o tecto não
//     tem mutador e os limites são configuração fora do log de eventos; o loop não tem caminho
//     de resumo. Um operador que leia o banner não pode ficar à espera de uma opção que não vai
//     encontrar — nem concluir que ela foi esquecida;
//   - QUE A DELIBERAÇÃO NÃO MATA O RUN pelo relógio (a suspensão repõe o `enteredAt` da
//     máquina de estados e o deadline de AOS-252 só conta tempo em `running`);
//   - O QUE ACONTECE SE NINGUÉM DECIDIR (o TTL de pendentes expira a pergunta, o run continua
//     retomável) e o que acontece se a SUSPENSÃO FALHAR (o run aborta — nunca segue em
//     silêncio, porque o sinal que o alimenta é uma-vez-por-run e um erro engolido deixaria o
//     run a queimar orçamento com o operador à espera de um prompt que nunca chegaria).
func exhaustionPromptPostureBanner(armed bool, ttl time.Duration) []string {
	if armed {
		return []string{
			fmt.Sprintf("prompt de exaustao de orcamento (AOS-263): ARMADO — quando o aviso de burn-down dispara, o run SUSPENDE em waiting_on_human (a MESMA aresta duravel da escalada de AOS-021, no MESMO runGate) e um pendente DURAVEL de tipo `exhaustion` e selado, amarrado a (run, limiar, tokens consumidos, tecto) e SEM preview — nao ha efeito exibido para assinar, a pergunta e sobre o run. ONDE SE VE: GET /runs/{id}, campo pending_exhaustion (a mesma via por onde as aprovacoes pendentes ja aparecem). AS DUAS OPCOES ENTRAM PELA MESMA ROTA E COM A MESMA AUTORIDADE — %s: (a) %s — o run fica AUTORIZADO a prosseguir e so entao a re-hospedagem %s (credencial fresca, turnos ja vividos reproduzidos da captura) e aceite; (b) %s — o run PARA de forma duravel. Enquanto a pergunta estiver POR RESPONDER, %s RECUSA com 409: a retoma e a EXECUCAO de um continue decidido, nunca a decisao — senao a resposta arriscada (deixar queimar orcamento acima do limiar) seria a unica que dispensava assinatura de operador e registo de quem a tomou. AUTENTICACAO: mesma admissao do /approve e do /pause (token-bucket de controlo + mTLS quando composto) e o MESMO Ed25519Authenticator de AOS_OPERATORS, com nonce DURAVEL de uso-unico e frescura; a assinatura cobre a DECISAO e a PERGUNTA (kind proprio + payload canonico), pelo que um pause capturado NAO se converte num abort nem um continue num abort. O abort so decide um run SUSPENSO e materializa waiting_on_human->killed (TERMINAL, o vocabulario de AOS-252; a mesma aresta que ADR-013 ja usa no timeout humano, com razao propria); um run que voltou a correr NAO se mata a meio de um turno — a rota recusa e nomeia a PAUSA GRACIOSA %s, que o para na fronteira de fim-de-turno e o deixa em paused, RETOMAVEL. CADA decisao (continue ou abort) escreve SELO PROPRIO na hash-chain WORM (particao %s) com o PRINCIPAL VERIFICADO, o run, o passo, o montante consumido e a razao — e o selo e PRE-CONDICAO do efeito: se nao selar, nada acontece. NAO se oferece extend: o budget.Budget nao tem mutador de tecto e os limites sao configuracao declarativa FORA do log de eventos (nao reconstruiveis por Rebuild) — levantar o tecto exige evento proprio e ADR (eixo DEF-220 no registo de deferimentos). NAO se oferece summarize_stop: nao tem executor (o loop nao tem caminho de resumo) — seria uma paragem comum com um nome que promete mais. A DELIBERACAO NAO MATA O RUN: a suspensao repoe o enteredAt da maquina de estados e o deadline de wall-clock de AOS-252 so conta tempo em `running`. SEM DECISAO: o varrimento de pendentes expira a pergunta ao fim de %s e o run volta a ser RETOMAVEL (expirar NAO autoriza nada, e uma pergunta RESPONDIDA sai da lista por DECISAO, nao por expiracao). SE A SUSPENSAO FALHAR (registo ou transicao duravel): o erro SOBE e o run ABORTA como falhado — nao ha fail-open, porque o sinal que alimenta este prompt e emitido uma vez por run e engoli-lo deixaria o run a queimar orcamento com o operador a espera de uma pergunta que nunca apareceria",
				exhaustionDecisionRoute,
				exhaustionOptionContinue, exhaustionResumeRoute,
				exhaustionOptionAbort,
				exhaustionResumeRoute,
				exhaustionGracefulPauseRoute, exhaustionDecisionPartition, ttl),
		}
	}
	return []string{
		"prompt de exaustao de orcamento (AOS-263): NAO ARMADO — falta a este no pelo menos uma das pecas sem as quais a pergunta seria uma armadilha: o registo duravel de pendentes e o registo de retoma (four-eyes, AOS_APPROVERS_FILE — sem eles nao ha a quem perguntar nem como re-hospedar o run) ou a ROTA DE DECISAO composta (pelo menos um operador pinado em AOS_OPERATORS e WORM — sem eles ninguem poderia responder, nem a resposta poderia ser selada). O comportamento ao cruzar o limiar e o de AOS-262, palavra por palavra: avisa UMA VEZ no log (e no span com OTLP) e o run CONTINUA ate ao tecto, ao MaxTurns, ao disjuntor ou ao steer do operador. Para o armar, componha AS DUAS metades: o four-eyes (AOS_APPROVERS_FILE) e os operadores do canal de controlo (AOS_OPERATORS). Eixo: AOS-263 / EPIC-20",
	}
}
