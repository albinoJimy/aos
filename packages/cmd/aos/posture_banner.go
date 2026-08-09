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

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// budgetPostureBanner declara o ORÇAMENTO. É incondicional porque o estado é incondicional: o
// composition-root NÃO importa `control-plane/budget` em lado nenhum — não há reserva, não há
// tecto por-run/por-árvore e nenhuma decisão do Reference Monitor consulta custo. O único
// controlo com aparência de tecto de custo (as velocidades de queima do disjuntor) está
// INLIGÁVEL desde AOS-246: sem [breaker.VelocitySource] cablada, um valor > 0 aborta o arranque
// ([ErrBreakerVelocitySourceUnwired]).
//
// A linha existe para que "o nó não me avisou" deixe de ser verdade: um agente autónomo sem
// tecto de gasto é uma decisão, e uma decisão tem de estar escrita onde o operador a lê.
func budgetPostureBanner() []string {
	return []string{
		"orcamento / tecto de custo (AOS-008): NAO COMPOSTO — nenhum control-plane/budget entra neste no: nao ha reserva, nao ha tecto por-run nem por-arvore, e NENHUMA decisao e negada por custo; um run so para por MaxTurns, pelo disjuntor (no-progress/wall-clock) ou por steer/pause do operador. Os tectos de VELOCIDADE de queima (AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC / AOS_BREAKER_MAX_TOKENS_PER_SEC) NAO sao ligaveis hoje: sem VelocitySource cablada, qualquer valor >0 ABORTA o arranque (ErrBreakerVelocitySourceUnwired, AOS-246). Eixo: AOS-008 / EPIC-20",
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
			"modelo (EPIC-06): MODEL GATEWAY REAL composto (modelgateway.NewProduction) — allowlist regional ASSINADA, keypool, routing de failover, metering/pricing e endurecimento SSRF do gateway. RESIDUAIS DECLARADOS deste wiring: (1) o egress e DELEGADO no http.Client injectado (seam de dev) — o SSRF fail-closed de AOS-223 so volta a valer com BaseURL https + AllowedEgressHosts e SEM HTTPClient; (2) o audit de governacao do gateway (activacao da allowlist + decisoes) e um MemStore PROPRIO do gateway, NAO o WORM do no — nao entra na hash-chain que o no verifica; (3) o adaptador RT->GW NAO propaga custo (translateResponse nao preenche CostMicroUSD) ⇒ o span chat do turno leva custo ZERO, mesmo com o metering do gateway a contar",
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
	return []string{
		fmt.Sprintf("autonomia / escalate (AOS-087/AOS-248): ORACULO LIGADO — %d par(es) agente:dominio provisionado(s) de AOS_AUTONOMY_LEVELS, cada um SELADO na hash-chain WORM como autonomy.level_changed (particao %q, motivo %q, actor %q): uma mudanca de autonomia deixa de poder acontecer sem rasto. E a composicao nivel x classe de risco que rebaixa um permit para ESCALATE — e portanto e esta ligacao que torna o bridge de aprovacao humana (AOS-021) ALCANCAVEL. Par NAO registado ⇒ L0 fail-closed (tudo escala)",
			len(w.sealedPairs), autonomy.DefaultAutonomyPartition, autonomyProvisionReason, autonomyProvisionActor),
	}
}
